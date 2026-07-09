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

const rewriteSystemPrompt = `你是一个查询重写助手。根据对话历史，将用户的追问重写为独立的、完整的检索查询。

规则：
1. 如果用户问题是独立完整的句子，直接返回原问题原文，一字不改。
2. 只有当用户使用指代词（如"它"、"这个"、"那个"、"上次的"）且对话历史中有明确指代对象时，才根据历史补全。
3. 没有对话历史时，直接返回原问题原文。
4. 只返回重写结果，不要添加任何解释、前缀、引号或标点。`

// Rewrite rewrites the user's question based on conversation history.
func (r *LLMRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	// No history → no rewrite needed, return original question
	if len(history) == 0 {
		return &RewriteResult{RewrittenQuestion: question}, nil
	}

	historyStr := truncateHistory(history, r.maxHistoryMsgs, r.maxHistoryChars)
	if historyStr == "" {
		return &RewriteResult{RewrittenQuestion: question}, nil
	}

	messages := []chat.Message{
		{Role: chat.RoleSystem, Content: rewriteSystemPrompt},
		{Role: chat.RoleUser, Content: "对话历史：\n" + historyStr},
		{Role: chat.RoleUser, Content: "当前追问：" + question + "\n\n请将以上追问重写为独立完整的问题："},
	}

	req := chat.Request{Messages: messages}

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

	// Safety: if rewritten is much shorter than original, keep original
	if len([]rune(rewritten)) < len([]rune(question))/2 {
		slog.Warn("llm rewriter: rewritten too short, keeping original",
			"original", question, "rewritten", rewritten)
		return &RewriteResult{RewrittenQuestion: question}, nil
	}

	slog.Info("llm rewriter: success", "from", question, "to", rewritten)
	return &RewriteResult{RewrittenQuestion: rewritten}, nil
}

type rewriteCallback struct {
	builder *strings.Builder
}

func (c *rewriteCallback) OnContent(content string) {
	c.builder.WriteString(content)
}

func (c *rewriteCallback) OnThinking(content string) {}

func (c *rewriteCallback) OnComplete() {}

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
	for _, msg := range history {
		line := fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
		if sb.Len()+len(line) > maxChars {
			break
		}
		sb.WriteString(line)
	}
	return sb.String()
}
