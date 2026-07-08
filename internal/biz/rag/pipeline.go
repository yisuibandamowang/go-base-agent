package rag

import (
	"log/slog"

	"go-base-agent/internal/infra/chat"
)

// Pipeline orchestrates the RAG chat flow: prompt → LLM → SSE events.
// Aligns with Java StreamChatPipeline (minimal subset for 2B-3).
type Pipeline struct {
	llm    chat.LLMService
	prompt PromptBuilder
}

// NewPipeline creates a new RAG pipeline.
func NewPipeline(llm chat.LLMService, prompt PromptBuilder) *Pipeline {
	return &Pipeline{llm: llm, prompt: prompt}
}

// StreamChat implements Service.StreamChat.
func (p *Pipeline) StreamChat(question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	var thinkingVal *bool
	if deepThinking {
		v := true
		thinkingVal = &v
	}

	req := p.prompt.Build(PromptContext{Question: question})
	req.Thinking = thinkingVal

	cb := &pipelineCallback{sender: sender, buf: make([]byte, 0, 1024)}
	handle, err := p.llm.StreamChat(nil, req, cb)
	if err != nil {
		slog.Error("rag pipeline: stream chat failed", "err", err)
		cb.OnError(err)
		return
	}
	_ = handle
}

// StopTask implements Service.StopTask.
func (p *Pipeline) StopTask(taskID string) {
	slog.Info("rag pipeline: stop task", "taskId", taskID)
}

// pipelineCallback converts LLM StreamCallback events to SSE events.
type pipelineCallback struct {
	sender *SSESender
	buf    []byte
}

func (c *pipelineCallback) OnContent(content string) {
	c.sender.SendMessage(MsgTypeResponse, content)
}

func (c *pipelineCallback) OnThinking(content string) {
	c.sender.SendMessage(MsgTypeThink, content)
}

func (c *pipelineCallback) OnComplete() {
	c.sender.SendFinish("", "")
	c.sender.SendDone()
	c.sender.Close()
}

func (c *pipelineCallback) OnError(err error) {
	slog.Error("rag pipeline: llm error", "err", err)
	c.sender.SendFinish("", "")
	c.sender.SendDone()
	c.sender.Close()
}
