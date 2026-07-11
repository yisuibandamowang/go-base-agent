package rag

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go-base-agent/internal/infra/chat"
)

// Pipeline orchestrates the RAG chat flow.
// Aligns with Java StreamChatPipeline.
type Pipeline struct {
	llm      chat.LLMService
	prompt   PromptBuilder
	rewrite  QueryRewriter
	retrieve Retriever
	memory   MemoryService
}

// NewPipeline creates a new RAG pipeline.
func NewPipeline(llm chat.LLMService, prompt PromptBuilder, rewrite QueryRewriter, retrieve Retriever, memory MemoryService) *Pipeline {
	return &Pipeline{llm: llm, prompt: prompt, rewrite: rewrite, retrieve: retrieve, memory: memory}
}

// StreamChat implements Service.StreamChat.
func (p *Pipeline) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	// Timeout context: the entire pipeline should complete within 120s
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// Start heartbeat to keep SSE connection alive during LLM processing
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				if sender.IsClosed() {
					return
				}
				_ = sender.SendMessage("heartbeat", "")
			}
		}
	}()
	defer close(heartbeatDone)

	result, err := p.rewrite.Rewrite(ctx, question, nil)
	q := question
	if err != nil {
		slog.Warn("rag: rewrite failed", "err", err)
	} else if result != nil && result.RewrittenQuestion != "" {
		slog.Info("rag: query rewritten", "from", question, "to", result.RewrittenQuestion)
		q = result.RewrittenQuestion
	}

	history, _ := p.memory.LoadHistory(ctx, conversationID)

	chunks, err := p.retrieve.Retrieve(ctx, q, 10)
	var kbCtx string
	if err != nil {
		slog.Warn("rag: retrieve failed", "err", err)
	} else if len(chunks) > 0 {
		slog.Info("rag: chunks retrieved", "count", len(chunks))
		for _, c := range chunks {
			kbCtx += c.Text + "\n"
		}
	} else {
		slog.Warn("rag: no chunks found for question", "question", runeLimit(q, 50))
	}

	var thinkingVal *bool
	if deepThinking {
		v := true
		thinkingVal = &v
	}

	req := p.prompt.Build(PromptContext{
		Question:  q,
		History:   history,
		KbContext: kbCtx,
	})
	req.Thinking = thinkingVal

	if err := p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
	}

	cb := &pipelineCallback{
		ctx:            ctx,
		conversationID: conversationID,
		memory:         p.memory,
		sender:         sender,
	}
	if ctx.Err() != nil {
		slog.Info("rag pipeline: cancelled before llm call", "err", ctx.Err())
		sender.SendFinish("", "")
		sender.SendDone()
		sender.Close()
		return
	}
	handle, err := p.llm.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: stream chat failed", "err", err)
		cb.OnError(err)
		return
	}
	slog.Info("rag pipeline: llm stream started")
	handle.Wait()
}

// StopTask implements Service.StopTask.
func (p *Pipeline) StopTask(taskID string) {
	slog.Info("rag pipeline: stop task", "taskId", taskID)
}

// pipelineCallback converts LLM StreamCallback events to SSE events.
type pipelineCallback struct {
	ctx            context.Context
	conversationID string
	memory         MemoryService
	sender         *SSESender
	buf            []byte
	answer         strings.Builder
}

func (c *pipelineCallback) OnContent(content string) {
	slog.Info("rag pipeline: llm content chunk", "len", len(content))
	c.answer.WriteString(content)
	c.sender.SendMessage(MsgTypeResponse, content)
}

func (c *pipelineCallback) OnThinking(content string) {
	slog.Info("rag pipeline: llm thinking chunk", "len", len(content))
	c.sender.SendMessage(MsgTypeThink, content)
}

func (c *pipelineCallback) OnComplete() {
	slog.Info("rag pipeline: llm stream complete")
	if c.memory != nil {
		if answer := c.answer.String(); answer != "" {
			if err := c.memory.SaveMessage(c.ctx, c.conversationID, chat.NewAssistantMessage(answer)); err != nil {
				slog.Warn("rag memory: save assistant message failed", "conversationId", c.conversationID, "err", err)
			}
		}
	}
	c.sender.SendFinish("", "")
	c.sender.SendDone()
	c.sender.Close()
}

func (c *pipelineCallback) OnError(err error) {
	slog.Error("rag pipeline: llm error", "err", err)
	// Ignore send errors — client may have already disconnected
	_ = c.sender.SendFinish("", "")
	_ = c.sender.SendDone()
	c.sender.Close()
}

func runeLimit(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
