package rag

import (
	"context"
	"time"

	"go-base-agent/internal/infra/chat"
)

// ConversationMessage represents a row in t_conversation_message.
// Aligns with Java ConversationMessage entity.
type ConversationMessage struct {
	ID               string
	ConversationID   string
	UserID           string
	Role             string
	Content          string
	ThinkingContent  string
	ThinkingDuration int
	CreatedAt        time.Time
}

// Conversation represents a row in t_conversation.
type Conversation struct {
	ID     string
	UserID string
	Title  string
}

// MemoryStore provides persistence for conversation messages.
// Aligns with Java ConversationMemoryStore.
type MemoryStore interface {
	LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error)
	AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error)
	LoadConversation(ctx context.Context, conversationID string) (*Conversation, error)
	UpdateTitle(ctx context.Context, conversationID, title string) error
}

// DefaultMemoryService implements MemoryService with history loading and truncation.
// Aligns with Java DefaultConversationMemoryService.
type DefaultMemoryService struct {
	store            MemoryStore
	historyKeepTurns int
}

// NewDefaultMemoryService creates a new memory service.
func NewDefaultMemoryService(store MemoryStore, historyKeepTurns int) *DefaultMemoryService {
	return &DefaultMemoryService{
		store:            store,
		historyKeepTurns: historyKeepTurns,
	}
}

// LoadHistory loads conversation history with truncation.
func (s *DefaultMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	history, err := s.store.LoadHistory(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return nil, nil
	}

	limit := s.historyKeepTurns * 2
	if len(history) > limit {
		if history[0].Role == chat.RoleSystem {
			recent := history[1:]
			if len(recent) > limit {
				recent = recent[len(recent)-limit:]
			}
			history = append([]chat.Message{history[0]}, recent...)
			return history, nil
		}
		history = history[len(history)-limit:]
	}
	return history, nil
}

// SaveMessage appends a message to the conversation.
func (s *DefaultMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	_, err := s.store.AppendMessage(ctx, conversationID, msg)
	return err
}

// LoadConversation loads the conversation metadata for title resolution.
func (s *DefaultMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return s.store.LoadConversation(ctx, conversationID)
}
