package rag

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go-base-agent/internal/framework/ratelimit"
	"go-base-agent/internal/infra/chat"
)

const queuedChatRejectMessage = "系统繁忙，请稍后再试"

// QueueLimiter is the subset of the fair queue limiter used by chat requests.
type QueueLimiter interface {
	Acquire(ctx context.Context, req ratelimit.AcquireRequest) error
}

// QueuedChatMemory exposes the memory operations required by the reject flow.
type QueuedChatMemory interface {
	LoadConversation(ctx context.Context, conversationID string) (*Conversation, error)
	AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error)
}

// QueuedChatService wraps a chat service with queue-based concurrency control.
type QueuedChatService struct {
	inner   Service
	limiter QueueLimiter
	memory  QueuedChatMemory
	maxWait time.Duration
}

// NewQueuedChatService creates a queue-guarded chat service.
func NewQueuedChatService(inner Service, limiter QueueLimiter, memory QueuedChatMemory, maxWait time.Duration) *QueuedChatService {
	return &QueuedChatService{
		inner:   inner,
		limiter: limiter,
		memory:  memory,
		maxWait: maxWait,
	}
}

// StreamChat enqueues the request before delegating to the real chat pipeline.
func (s *QueuedChatService) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	if s == nil || s.inner == nil {
		return
	}
	if s.limiter == nil || s.maxWait <= 0 {
		s.inner.StreamChat(ctx, question, conversationID, taskID, deepThinking, sender)
		return
	}

	err := s.limiter.Acquire(ctx, ratelimit.AcquireRequest{
		MaxWait: s.maxWait,
		OnAcquire: func() {
			s.inner.StreamChat(ctx, question, conversationID, taskID, deepThinking, sender)
		},
		OnTimeout: func() {
			s.handleTimeout(ctx, question, conversationID, sender)
		},
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("rag chat queue acquire failed", "conversationId", conversationID, "taskId", taskID, "err", err)
	}
}

// StopTask forwards cancellation to the underlying chat service.
func (s *QueuedChatService) StopTask(taskID string) {
	if s == nil || s.inner == nil {
		return
	}
	s.inner.StopTask(taskID)
}

func (s *QueuedChatService) handleTimeout(ctx context.Context, question, conversationID string, sender *SSESender) {
	payload := s.buildRejectedPayload(ctx, question, conversationID)
	if sender == nil {
		return
	}
	if payload != nil {
		_ = sender.SendReject()
		_ = sender.SendFinish(payload.MessageID, payload.Title)
	}
	_ = sender.SendDone()
	sender.Close()
}

func (s *QueuedChatService) buildRejectedPayload(ctx context.Context, question, conversationID string) *CompletionPayload {
	if s.memory == nil || strings.TrimSpace(question) == "" {
		return nil
	}
	if _, err := s.saveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
		slog.Warn("rag queue: save rejected user message failed", "conversationId", conversationID, "err", err)
		return nil
	}
	messageID, err := s.saveMessage(ctx, conversationID, chat.NewAssistantMessage(queuedChatRejectMessage))
	if err != nil {
		slog.Warn("rag queue: save rejected assistant message failed", "conversationId", conversationID, "err", err)
		return nil
	}
	title := s.resolveRejectedTitle(ctx, conversationID, question)
	return &CompletionPayload{
		MessageID: normalizeCompletionMessageID(messageID),
		Title:     title,
	}
}

func (s *QueuedChatService) saveMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	if s.memory == nil {
		return "", nil
	}
	return s.memory.AppendMessage(ctx, conversationID, msg)
}

func (s *QueuedChatService) resolveRejectedTitle(ctx context.Context, conversationID, question string) string {
	const fallbackTitle = "新对话"
	if s.memory == nil {
		return fallbackTitle
	}
	conv, err := s.memory.LoadConversation(ctx, conversationID)
	if err == nil && conv != nil {
		if title := strings.TrimSpace(conv.Title); title != "" {
			return title
		}
	}
	title := strings.TrimSpace(question)
	if title == "" {
		return fallbackTitle
	}
	return trimTitleRunes(title, 30)
}

func normalizeCompletionMessageID(messageID string) string {
	if strings.TrimSpace(messageID) == "" {
		return "null"
	}
	return messageID
}

func trimTitleRunes(title string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 30
	}
	runes := []rune(strings.TrimSpace(title))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) > maxChars {
		runes = runes[:maxChars]
	}
	return string(runes)
}
