package rag

import (
	"context"
	"encoding/json"
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
	enabled         bool
}

// NewLLMRewriter creates an LLMRewriter.
func NewLLMRewriter(llm chat.LLMService, maxHistoryMsgs, maxHistoryChars int, enabled bool) *LLMRewriter {
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
		enabled:         enabled,
	}
}

const rewriteSystemPrompt = `你是一个查询重写与多问句拆分助手。根据对话历史，将用户问题重写为独立、完整的检索查询，并拆分出可独立检索的子问题。

规则：
1. 如果用户问题是独立完整的句子，rewrite 返回原问题原文。
2. 只有当用户使用指代词（如"它"、"这个"、"那个"、"上次的"）且对话历史中有明确指代对象时，才根据历史补全。
3. 如果用户同时问多个问题，将 sub_questions 拆成多个独立检索问题。
4. 只返回严格 JSON 对象，不要添加解释、前缀或 Markdown 代码块。
返回格式：{"rewrite":"...","sub_questions":["..."]}`

// Rewrite rewrites the user's question based on conversation history.
func (r *LLMRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	if r == nil || !r.enabled || r.llm == nil {
		return &RewriteResult{
			RewrittenQuestion: question,
			SubQuestions:      ruleBasedSplitQuestions(question),
		}, nil
	}

	historyStr := truncateHistory(history, r.maxHistoryMsgs, r.maxHistoryChars)

	messages := []chat.Message{
		{Role: chat.RoleSystem, Content: rewriteSystemPrompt},
	}
	if historyStr != "" {
		messages = append(messages, chat.Message{Role: chat.RoleUser, Content: "对话历史：\n" + historyStr})
	}
	messages = append(messages, chat.Message{Role: chat.RoleUser, Content: "当前问题：" + question + "\n\n请返回 JSON："})

	falseVal := false
	req := chat.Request{Messages: messages, Thinking: &falseVal}

	var builder strings.Builder
	handle, err := r.llm.StreamChat(ctx, req, &rewriteCallback{builder: &builder})
	if err != nil {
		slog.Warn("llm rewriter: stream chat failed", "err", err)
		return &RewriteResult{RewrittenQuestion: question}, nil
	}
	if handle != nil {
		handle.Wait()
	}

	rewritten, subQuestions := parseRewriteAndSplitResponse(builder.String(), question)
	if rewritten == "" || rewritten == question {
		return &RewriteResult{RewrittenQuestion: question, SubQuestions: subQuestions}, nil
	}

	slog.Info("llm rewriter: success", "from", question, "to", rewritten)
	return &RewriteResult{RewrittenQuestion: rewritten, SubQuestions: subQuestions}, nil
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

func parseRewriteAndSplitResponse(raw, fallback string) (string, []string) {
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	if cleaned == "" {
		return fallback, ruleBasedSplitQuestions(fallback)
	}

	var obj struct {
		Rewrite      string   `json:"rewrite"`
		SubQuestions []string `json:"sub_questions"`
	}
	if err := json.Unmarshal([]byte(cleaned), &obj); err == nil && strings.TrimSpace(obj.Rewrite) != "" {
		rewrite := strings.TrimSpace(obj.Rewrite)
		subs := normalizeSubQuestions(obj.SubQuestions, rewrite)
		return rewrite, subs
	}

	rewrite := strings.TrimSpace(cleaned)
	return rewrite, ruleBasedSplitQuestions(rewrite)
}

func normalizeSubQuestions(values []string, fallback string) []string {
	seen := make(map[string]bool, len(values)+1)
	result := make([]string, 0, len(values)+1)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	if len(result) == 0 && strings.TrimSpace(fallback) != "" {
		result = append(result, strings.TrimSpace(fallback))
	}
	return result
}

func ruleBasedSplitQuestions(question string) []string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return nil
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		switch r {
		case '?', '？', '。', ';', '；', '\n':
			return true
		default:
			return false
		}
	})
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		result = append(result, part)
	}
	if len(result) == 0 {
		return []string{trimmed}
	}
	return result
}
