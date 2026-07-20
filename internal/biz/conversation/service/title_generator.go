package service

import (
	"context"
	"log/slog"
	"strings"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
)

const conversationTitlePromptFile = "conversation_title.txt"

// LLMTitleGenerator 使用大模型生成会话标题。
type LLMTitleGenerator struct {
	llm      chat.LLMService
	loader   *rag.PromptLoader
	maxChars int
}

// NewLLMTitleGenerator 创建会话标题生成器。
func NewLLMTitleGenerator(llm chat.LLMService, externalPromptDir string, maxChars int) *LLMTitleGenerator {
	if maxChars <= 0 {
		maxChars = 30
	}
	return &LLMTitleGenerator{
		llm:      llm,
		loader:   rag.NewPromptLoader(externalPromptDir),
		maxChars: maxChars,
	}
}

// Generate 生成会话标题。
func (g *LLMTitleGenerator) Generate(ctx context.Context, question string) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "新对话", nil
	}
	maxChars := 30
	if g != nil && g.maxChars > 0 {
		maxChars = g.maxChars
	}
	if g == nil || g.llm == nil || g.loader == nil {
		return trimConversationTitle(question, maxChars), nil
	}

	prompt, err := g.loader.Render(conversationTitlePromptFile, map[string]any{
		"TitleMaxChars": maxChars,
		"Question":      question,
	})
	if err != nil {
		slog.Warn("render conversation title prompt failed", "err", err)
		return "新对话", nil
	}

	temperature := 0.7
	topP := 0.3
	thinking := false
	title, err := g.llm.Chat(ctx, chat.Request{
		Messages:    []chat.Message{chat.NewUserMessage(prompt)},
		Temperature: &temperature,
		TopP:        &topP,
		Thinking:    &thinking,
	})
	if err != nil {
		slog.Warn("conversation title llm failed", "err", err)
		return "新对话", nil
	}
	title = sanitizeConversationTitle(title, maxChars)
	if title == "" {
		return "新对话", nil
	}
	return title, nil
}

func sanitizeConversationTitle(title string, maxChars int) string {
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'`")
	title = strings.ReplaceAll(title, "\r\n", " ")
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	title = strings.TrimSpace(title)
	return trimConversationTitle(title, maxChars)
}

func trimConversationTitle(title string, maxChars int) string {
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
