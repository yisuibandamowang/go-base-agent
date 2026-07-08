package rag

import "go-base-agent/internal/infra/chat"

// PromptContext holds all inputs needed to build a chat prompt.
// Aligns with Java PromptContext (minimal subset for 2B-4).
type PromptContext struct {
	Question   string
	History    []chat.Message
	KbContext  string
	McpContext string
}

// PromptBuilder builds a chat.Request from a PromptContext.
type PromptBuilder interface {
	Build(ctx PromptContext) chat.Request
}

// DefaultPromptBuilder constructs a simple system + user prompt.
type DefaultPromptBuilder struct {
	SystemPrompt string
}

// NewDefaultPromptBuilder creates a builder with the default system prompt.
func NewDefaultPromptBuilder() *DefaultPromptBuilder {
	return &DefaultPromptBuilder{
		SystemPrompt: "你是一个有帮助的AI助手。",
	}
}

// Build constructs a chat.Request with system prompt, optional history, and user question.
func (b *DefaultPromptBuilder) Build(ctx PromptContext) chat.Request {
	messages := make([]chat.Message, 0, len(ctx.History)+2)

	if b.SystemPrompt != "" {
		messages = append(messages, chat.NewSystemMessage(b.SystemPrompt))
	}

	messages = append(messages, ctx.History...)

	content := ctx.Question
	if ctx.KbContext != "" {
		content = "参考以下知识库内容回答问题：\n\n" + ctx.KbContext + "\n\n用户问题：" + content
	}

	messages = append(messages, chat.NewUserMessage(content))
	return chat.Request{Messages: messages}
}
