package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// LLMMcpToolSelector selects relevant MCP tools with an LLM.
type LLMMcpToolSelector struct {
	llm chat.LLMService
}

// NewLLMMcpToolSelector creates an LLM-backed MCP tool selector.
func NewLLMMcpToolSelector(llm chat.LLMService) *LLMMcpToolSelector {
	return &LLMMcpToolSelector{llm: llm}
}

// SelectTools returns the names of tools that should be called for the question.
func (s *LLMMcpToolSelector) SelectTools(ctx context.Context, question string, tools []ToolDefinition) ([]string, error) {
	if s == nil || s.llm == nil || len(tools) == 0 {
		return nil, nil
	}
	if len(tools) == 1 {
		return []string{tools[0].Name}, nil
	}

	req := chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage("你是一个MCP工具选择器。根据用户问题从候选工具中选择最相关的工具名。只返回严格 JSON 字符串数组，例如 [\"tool_a\"]；如果没有相关工具返回 []。"),
			chat.NewUserMessage(buildMcpToolSelectionPrompt(question, tools)),
		},
		Temperature: floatPtr(0.1),
		TopP:        floatPtr(0.3),
	}

	raw, err := s.llm.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("select mcp tools: %w", err)
	}
	selected, err := parseSelectedToolNames(raw)
	if err != nil {
		return nil, fmt.Errorf("parse selected mcp tools: %w", err)
	}
	return filterKnownToolNames(selected, tools), nil
}

func buildMcpToolSelectionPrompt(question string, tools []ToolDefinition) string {
	var b strings.Builder
	b.WriteString("用户问题：")
	b.WriteString(question)
	b.WriteString("\n候选工具：\n")
	for _, tool := range tools {
		b.WriteString("- ")
		b.WriteString(tool.Name)
		if strings.TrimSpace(tool.Description) != "" {
			b.WriteString(": ")
			b.WriteString(tool.Description)
		}
		if len(tool.Parameters) > 0 {
			b.WriteString(" 参数: ")
			for i, param := range tool.Parameters {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(param.Name)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("只返回需要调用的工具名 JSON 数组。")
	return b.String()
}

func parseSelectedToolNames(raw string) ([]string, error) {
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	if cleaned == "" {
		return nil, nil
	}

	var names []string
	if err := json.Unmarshal([]byte(cleaned), &names); err == nil {
		return cleanToolNames(names), nil
	}

	var obj struct {
		SelectedTools []string `json:"selected_tools"`
		Tools         []string `json:"tools"`
		ToolNames     []string `json:"tool_names"`
	}
	if err := json.Unmarshal([]byte(cleaned), &obj); err == nil {
		switch {
		case len(obj.SelectedTools) > 0:
			return cleanToolNames(obj.SelectedTools), nil
		case len(obj.Tools) > 0:
			return cleanToolNames(obj.Tools), nil
		default:
			return cleanToolNames(obj.ToolNames), nil
		}
	}

	start := strings.Index(cleaned, "[")
	end := strings.LastIndex(cleaned, "]")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), &names); err == nil {
			return cleanToolNames(names), nil
		}
	}
	return nil, fmt.Errorf("response is not a JSON array")
}

func cleanToolNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	cleaned := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		cleaned = append(cleaned, name)
	}
	return cleaned
}

func filterKnownToolNames(names []string, tools []ToolDefinition) []string {
	if len(names) == 0 {
		return nil
	}
	known := make(map[string]bool, len(tools))
	for _, tool := range tools {
		known[tool.Name] = true
	}
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if known[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered
}
