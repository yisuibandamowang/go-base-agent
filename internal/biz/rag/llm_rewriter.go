package rag

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// LLMRewriter implements QueryRewriter by asking the LLM to rewrite
// the user's question for better retrieval.
// Replaces NoopRewriter.
type LLMRewriter struct {
	llm             chat.LLMService
	maxHistoryMsgs  int
	maxHistoryChars int
}

// NewLLMRewriter creates an LLMRewriter.
func NewLLMRewriter(llm chat.LLMService, maxHistoryMsgs, maxHistoryChars int) *LLMRewriter {
	if maxHistoryMsgs <= 0 {
		maxHistoryMsgs = 4
	}
	if maxHistoryChars <= 0 {
		maxHistoryChars = 500
	}
	return &LLMRewriter{
		llm:             llm,
		maxHistoryMsgs:  maxHistoryMsgs,
		maxHistoryChars: maxHistoryChars,
	}
}

const rewriteSystemPrompt = `你是一个查询重写助手。你的任务是根据对话历史，将用户的问题重写为一个更完整、更清晰、更适合知识库检索的查询语句。

规则：
1. 如果用户问题已经完整清晰，直接返回原问题。
2. 如果用户问题指代不明确（如"它是什么"、"上次那个"），请根据对话历史补全指代对象。
3. 保持原问题的语言风格和意图不变。
4. 只返回重写后的问题，不要添加任何解释、引号或前缀。`

// Rewrite rewrites the user's question based on conversation history.
func (r *LLMRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	messages := []chat.Message{
		{Role: chat.RoleSystem, Content: rewriteSystemPrompt},
	}

	// Append truncated history
	historyStr := truncateHistory(history, r.maxHistoryMsgs, r.maxHistoryChars)
	if historyStr != "" {
		messages = append(messages, chat.Message{
			Role:    chat.RoleUser,
			Content: "对话历史：\n" + historyStr,
		})
	}

	messages = append(messages, chat.Message{
		Role:    chat.RoleUser,
		Content: "用户当前问题：" + question + "\n\n请重写这个问题：",
	})

	req := chat.Request{
		Messages: messages,
	}

	var builder strings.Builder
	_, err := r.llm.StreamChat(ctx, req, &rewriteCallback{builder: &builder})
	if err != nil {
		slog.Warn("llm rewriter: stream chat failed", "err", err)
		return &RewriteResult{RewrittenQuestion: question}, nil
	}

	rewritten := strings.TrimSpace(builder.String())
	if rewritten == "" || rewritten == question {
		return &RewriteResult{RewrittenQuestion: question}, nil
	}

	return &RewriteResult{
		RewrittenQuestion: rewritten,
	}, nil
}

type rewriteCallback struct {
	builder *strings.Builder
}

func (c *rewriteCallback) OnContent(content string) {
	c.builder.WriteString(content)
}

func (c *rewriteCallback) OnThinking(content string) {
	// skip thinking content for rewrite
}

func (c *rewriteCallback) OnComplete() {
	// no-op
}

func (c *rewriteCallback) OnError(err error) {
	slog.Warn("llm rewriter: callback error", "err", err)
}

func truncateHistory(history []chat.Message, maxMsgs, maxChars int) string {
	if len(history) == 0 {
		return ""
	}
	if len(history) > maxMsgs {
		history = history[len(history)-maxMsgs:]
	}
	var sb strings.Builder
	for i, msg := range history {
		line := fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
		if sb.Len()+len(line) > maxChars {
			break
		}
		sb.WriteString(line)
		_ = i
	}
	return sb.String()
}
