package rag

import (
	"strconv"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// PromptContext holds all inputs needed to build a chat prompt.
// Aligns with Java PromptContext (minimal subset for 2B-4).
type PromptContext struct {
	Question     string
	SubQuestions []string
	History      []chat.Message
	KbContext    string
	McpContext   string
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

	messages = append(messages, chat.NewUserMessage(buildPromptUserContent(ctx)))
	maxTokens := 1024
	return chat.Request{Messages: messages, MaxTokens: &maxTokens}
}

func buildPromptUserContent(ctx PromptContext) string {
	evidence := buildPromptEvidence(ctx.McpContext, ctx.KbContext)
	question := buildPromptQuestion(ctx.Question, ctx.SubQuestions)
	if evidence == "" {
		return ctx.Question
	}

	instruction := "只能依据以下知识库内容回答用户问题；如果知识库内容不足以回答，请直接说明知识库中没有相关信息，不要使用模型自身知识补充。"
	if strings.TrimSpace(ctx.McpContext) != "" && strings.TrimSpace(ctx.KbContext) != "" {
		instruction = "请结合以下MCP工具结果和知识库内容回答用户问题；如果工具结果与知识库内容冲突，请优先说明冲突并给出可追溯依据。"
	} else if strings.TrimSpace(ctx.McpContext) != "" {
		instruction = "请结合以下MCP工具结果回答用户问题；如果工具结果不足以回答，请直接说明工具结果中没有相关信息。"
	}

	if question == "" {
		return instruction + "\n\n" + evidence
	}
	return instruction + "\n\n" + evidence + "\n\n" + question
}

func buildPromptEvidence(mcpContext, kbContext string) string {
	sections := make([]string, 0, 2)
	if mcp := strings.TrimSpace(mcpContext); mcp != "" {
		sections = append(sections, "<tool-data>\n"+mcp+"\n</tool-data>")
	}
	if kb := strings.TrimSpace(kbContext); kb != "" {
		sections = append(sections, "<documents>\n"+kb+"\n</documents>")
	}
	return strings.Join(sections, "\n\n")
}

func buildPromptQuestion(question string, subQuestions []string) string {
	normalized := normalizePromptSubQuestions(subQuestions)
	if len(normalized) > 1 {
		numbered := make([]string, 0, len(normalized))
		for i, item := range normalized {
			numbered = append(numbered, strconv.Itoa(i+1)+". "+item)
		}
		return "<questions>\n" + strings.Join(numbered, "\n") + "\n</questions>"
	}
	if strings.TrimSpace(question) == "" {
		return ""
	}
	return "<question>" + question + "</question>"
}

func normalizePromptSubQuestions(subQuestions []string) []string {
	normalized := make([]string, 0, len(subQuestions))
	for _, question := range subQuestions {
		question = strings.TrimSpace(question)
		if question != "" {
			normalized = append(normalized, question)
		}
	}
	return normalized
}
