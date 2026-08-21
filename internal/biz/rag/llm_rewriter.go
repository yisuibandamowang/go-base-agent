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

const rewriteSystemPrompt = `你是查询改写助手，只负责为 RAG 检索改写查询和判断是否拆分，不回答用户问题。

严格返回 JSON，不要额外文字：
{"rewrite":"改写后的查询","should_split":false,"sub_questions":["子问题"]}

核心规则：
1. 只做完成检索所必需的最小改写，保留专有名词、关键限制、业务场景及“怎么做”“为什么”“有什么区别”“流程是什么”等问题意图。
2. 可以删除礼貌用语、回答格式指令和无关身份描述，但不得添加用户原问题或历史用户问题中不存在的条件、事实或假设。
3. 不得回答用户问题，不得将历史 Assistant 回答中的答案、结论、知识或描述写入 rewrite。
4. 当前问题已完整，或只是问候、致谢、评价反馈等非查询轮次时，保持原样，不要从历史补出问题。

多轮上下文规则：
- 指代消解：当前问题出现“它”“这个”“该系统”“上面的”等指代词时，只结合历史用户问题还原明确实体。
- 省略续问：当前问题为“X呢”“那X呢”“X怎么样”“换成X呢”等表达时，保留当前的新主体，继承上一轮用户问题中省略的问题意图，不得继承 Assistant 的答案内容。例如上一轮用户问“OA系统数据安全怎么做的？”，当前问“保险系统呢”，应改写为“保险系统数据安全怎么做”。
- 非查询轮次：当前问题只是问候、致谢或评价上一轮回答时，原样返回。

拆分规则：
- 仅在多个明确问句、显式列举、明确要求分别询问，或分号/换行分隔多个独立问题时拆分。
- 抽象对比、笼统询问、省略续问和不确定场景均不拆分。
- 不拆分时 sub_questions 只包含 rewrite；拆分时每个子问题必须可独立检索，并尽量保持用户原文表述。`

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
	handle, err := chat.StreamChatWithTier(ctx, r.llm, req, &rewriteCallback{builder: &builder}, "fast")
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
