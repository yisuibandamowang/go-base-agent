package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
)

const conversationSummaryPromptFile = "conversation_summary.txt"

// LLMSummaryGenerator 使用大模型生成会话摘要。
type LLMSummaryGenerator struct {
	llm    chat.LLMService
	loader *rag.PromptLoader
}

// NewLLMSummaryGenerator 创建会话摘要生成器。
func NewLLMSummaryGenerator(llm chat.LLMService, externalPromptDir string) *LLMSummaryGenerator {
	return &LLMSummaryGenerator{
		llm:    llm,
		loader: rag.NewPromptLoader(externalPromptDir),
	}
}

// Generate 生成会话摘要。
func (g *LLMSummaryGenerator) Generate(ctx context.Context, history []chat.Message, previousSummary string, maxChars int) (string, error) {
	if len(history) == 0 {
		return trimSummaryText(previousSummary, maxChars), nil
	}
	if g == nil {
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}

	prompt, err := g.loader.Render(conversationSummaryPromptFile, map[string]any{
		"SummaryMaxChars": maxChars,
		"PreviousSummary": previousSummary,
		"History":         renderConversationHistory(history),
	})
	if err != nil {
		slog.Warn("render conversation summary prompt failed", "err", err)
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}
	if g.llm == nil {
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}

	summary, err := g.llm.Chat(ctx, chat.Request{
		Messages: []chat.Message{chat.NewUserMessage(prompt)},
	})
	if err != nil {
		slog.Warn("conversation summary llm failed", "err", err)
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}
	summary = trimSummaryText(strings.TrimSpace(summary), maxChars)
	if summary == "" {
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}
	return summary, nil
}

func renderConversationHistory(history []chat.Message) string {
	var b strings.Builder
	for _, msg := range history {
		content := strings.TrimSpace(strings.ReplaceAll(msg.Content, "\n", " "))
		if content == "" {
			continue
		}
		b.WriteString(string(msg.Role))
		b.WriteString("：")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return b.String()
}

func fallbackConversationSummary(history []chat.Message, previousSummary string, maxChars int) string {
	var parts []string
	if trimmed := strings.TrimSpace(previousSummary); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, msg := range history {
		content := strings.TrimSpace(strings.ReplaceAll(msg.Content, "\n", " "))
		if content == "" {
			continue
		}
		label := "用户"
		if msg.Role == chat.RoleAssistant {
			label = "助手"
		}
		parts = append(parts, fmt.Sprintf("%s：%s", label, content))
	}
	return trimSummaryText(strings.Join(parts, "；"), maxChars)
}

func trimSummaryText(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars])
}
