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

// DefaultPromptBuilder constructs prompts using a template loader.
type DefaultPromptBuilder struct {
	loader     *PromptLoader
	systemFile string // e.g. "default_system.txt"
}

// NewDefaultPromptBuilder creates a builder using embedded prompt templates.
func NewDefaultPromptBuilder() *DefaultPromptBuilder {
	return NewPromptBuilder("", "default_system.txt")
}

// NewPromptBuilder creates a builder with an optional external template directory.
// If externalDir is empty, embedded prompts are used.
func NewPromptBuilder(externalDir, systemFile string) *DefaultPromptBuilder {
	return &DefaultPromptBuilder{
		loader:     NewPromptLoader(externalDir),
		systemFile: systemFile,
	}
}

// Build constructs a chat.Request from the prompt context.
func (b *DefaultPromptBuilder) Build(ctx PromptContext) chat.Request {
	messages := make([]chat.Message, 0, len(ctx.History)+2)

	sysPrompt, err := b.loader.Render(b.systemFile, nil)
	if err != nil {
		sysPrompt = "你是一个有帮助的AI助手。"
	}
	if sysPrompt != "" {
		messages = append(messages, chat.NewSystemMessage(sysPrompt))
	}

	messages = append(messages, ctx.History...)

	content := ctx.Question
	if ctx.KbContext != "" {
		content = "参考以下知识库内容回答问题：\n\n" + ctx.KbContext + "\n\n用户问题：" + content
	}

	messages = append(messages, chat.NewUserMessage(content))
	return chat.Request{Messages: messages}
}
