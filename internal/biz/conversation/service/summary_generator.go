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
	prompt rag.RuntimePromptResolver
}

// NewLLMSummaryGenerator 创建会话摘要生成器。
func NewLLMSummaryGenerator(llm chat.LLMService, externalPromptDir string, promptResolver ...rag.RuntimePromptResolver) *LLMSummaryGenerator {
	var resolver rag.RuntimePromptResolver
	if len(promptResolver) > 0 {
		resolver = promptResolver[0]
	}
	return &LLMSummaryGenerator{
		llm:    llm,
		loader: rag.NewPromptLoader(externalPromptDir),
		prompt: resolver,
	}
}

// Generate 生成会话摘要。消息编排对齐 Java summarizeMessages：system 提示词 →
// 历史摘要（assistant 角色，仅用于合并去重）→ 待压缩对话 → 合并指令（user），
// 参数固定 temperature 0.3 / topP 0.9 / 关闭思考。
func (g *LLMSummaryGenerator) Generate(ctx context.Context, history []chat.Message, previousSummary string, maxChars int) (string, error) {
	if len(history) == 0 {
		return trimSummaryText(previousSummary, maxChars), nil
	}
	if g == nil {
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}

	prompt, err := g.renderPrompt(maxChars)
	if err != nil {
		slog.Warn("render conversation summary prompt failed", "err", err)
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}
	if g.llm == nil {
		return trimSummaryText(fallbackConversationSummary(history, previousSummary, maxChars), maxChars), nil
	}

	messages := make([]chat.Message, 0, len(history)+3)
	messages = append(messages, chat.NewSystemMessage(prompt))
	if trimmed := strings.TrimSpace(previousSummary); trimmed != "" {
		messages = append(messages, chat.Message{
			Role: chat.RoleAssistant,
			Content: "历史摘要（仅用于合并去重，不得作为事实新增来源；若与本轮对话冲突，以本轮对话为准）：\n" +
				trimmed,
		})
	}
	messages = append(messages, history...)
	messages = append(messages, chat.NewUserMessage(fmt.Sprintf(
		"合并以上对话与历史摘要，去重后输出更新摘要。要求：严格≤%d字符；仅一行。", maxChars)))

	temperature := 0.3
	topP := 0.9
	thinking := false
	summary, err := chat.ChatWithTier(ctx, g.llm, chat.Request{
		Messages:    messages,
		Temperature: &temperature,
		TopP:        &topP,
		Thinking:    &thinking,
	}, "fast")
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

func (g *LLMSummaryGenerator) renderPrompt(maxChars int) (string, error) {
	if g.prompt != nil {
		if rendered, err := g.prompt.Render("CONVERSATION_SUMMARY", map[string]any{
			"SummaryMaxChars": maxChars,
		}); err == nil && strings.TrimSpace(rendered) != "" {
			return rendered, nil
		}
	}
	return g.loader.Render(conversationSummaryPromptFile, map[string]any{
		"SummaryMaxChars": maxChars,
	})
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
