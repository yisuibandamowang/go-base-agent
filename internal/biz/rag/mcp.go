package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// ToolParam describes a parameter of an MCP tool.
type ToolParam struct {
	Name         string
	Type         string
	Description  string
	Required     bool
	DefaultValue interface{}
	Enum         []string
}

// ToolDefinition describes an MCP tool's schema.
// Aligns with Java io.modelcontextprotocol.spec.Tool.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []ToolParam
}

// McpToolExecutor executes an MCP tool with parameters.
// Aligns with Java McpToolExecutor.
type McpToolExecutor interface {
	GetToolDefinition() ToolDefinition
	Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error)
}

// McpToolRegistry manages registered MCP tool executors.
// Aligns with Java McpToolRegistry.
type McpToolRegistry interface {
	Register(executor McpToolExecutor)
	Unregister(toolName string)
	GetExecutor(toolName string) (McpToolExecutor, bool)
	ListAllTools() []ToolDefinition
	ListAllExecutors() []McpToolExecutor
	Size() int
}

// DefaultMcpToolRegistry implements McpToolRegistry with a map.
// Aligns with Java DefaultMcpToolRegistry.
type DefaultMcpToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]McpToolExecutor
}

// NewMcpToolRegistry creates a new registry.
func NewMcpToolRegistry() *DefaultMcpToolRegistry {
	return &DefaultMcpToolRegistry{tools: make(map[string]McpToolExecutor)}
}

func (r *DefaultMcpToolRegistry) Register(executor McpToolExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[executor.GetToolDefinition().Name] = executor
}

func (r *DefaultMcpToolRegistry) Unregister(toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, toolName)
}

func (r *DefaultMcpToolRegistry) GetExecutor(toolName string) (McpToolExecutor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[toolName]
	return e, ok
}

func (r *DefaultMcpToolRegistry) ListAllTools() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]ToolDefinition, 0, len(r.tools))
	for _, e := range r.tools {
		tools = append(tools, e.GetToolDefinition())
	}
	return tools
}

func (r *DefaultMcpToolRegistry) ListAllExecutors() []McpToolExecutor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	execs := make([]McpToolExecutor, 0, len(r.tools))
	for _, e := range r.tools {
		execs = append(execs, e)
	}
	return execs
}

func (r *DefaultMcpToolRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// McpParameterExtractor extracts call parameters from user questions.
// Aligns with Java McpParameterExtractor.
type McpParameterExtractor interface {
	ExtractParameters(ctx context.Context, question string, tool ToolDefinition) (map[string]interface{}, error)
}

// McpToolSelector selects relevant tools for a user question.
type McpToolSelector interface {
	SelectTools(ctx context.Context, question string, tools []ToolDefinition) ([]string, error)
}

// McpContextProvider builds MCP execution context for chat prompts.
type McpContextProvider interface {
	BuildContext(ctx context.Context, question string) (string, error)
}

// DefaultMcpContextProvider executes registered MCP tools and formats their results.
type DefaultMcpContextProvider struct {
	registry  McpToolRegistry
	extractor McpParameterExtractor
	selector  McpToolSelector
}

// NewDefaultMcpContextProvider creates a provider backed by a tool registry.
func NewDefaultMcpContextProvider(registry McpToolRegistry, extractor McpParameterExtractor, selectors ...McpToolSelector) *DefaultMcpContextProvider {
	var selector McpToolSelector
	if len(selectors) > 0 {
		selector = selectors[0]
	}
	return &DefaultMcpContextProvider{registry: registry, extractor: extractor, selector: selector}
}

// BuildContext executes registered MCP tools and returns prompt-ready context text.
func (p *DefaultMcpContextProvider) BuildContext(ctx context.Context, question string) (string, error) {
	if p == nil || p.registry == nil || p.registry.Size() == 0 {
		return "", nil
	}

	executors := p.selectExecutors(ctx, question)
	if len(executors) == 0 {
		return "", nil
	}

	var b strings.Builder
	for i, executor := range executors {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		tool := executor.GetToolDefinition()
		params := map[string]interface{}{}
		var err error
		if p.extractor != nil {
			params, err = p.extractor.ExtractParameters(ctx, question, tool)
			if params == nil {
				params = map[string]interface{}{}
			}
		}

		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("工具：")
		b.WriteString(tool.Name)
		b.WriteString("\n")
		if err != nil {
			b.WriteString("错误：")
			b.WriteString(err.Error())
			continue
		}

		result, err := executor.Execute(ctx, params)
		if err != nil {
			b.WriteString("错误：")
			b.WriteString(err.Error())
			continue
		}
		b.WriteString("结果：")
		b.WriteString(formatMcpResult(result))
	}
	return b.String(), nil
}

func (p *DefaultMcpContextProvider) selectExecutors(ctx context.Context, question string) []McpToolExecutor {
	executors := p.registry.ListAllExecutors()
	if p.selector == nil || len(executors) <= 1 {
		return executors
	}

	tools := make([]ToolDefinition, 0, len(executors))
	for _, executor := range executors {
		tools = append(tools, executor.GetToolDefinition())
	}
	selected, err := p.selector.SelectTools(ctx, question, tools)
	if err != nil {
		slog.Warn("mcp tool selection failed, fallback to all tools", "err", err)
		return executors
	}
	if len(selected) == 0 {
		return nil
	}

	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		name = strings.TrimSpace(name)
		if name != "" {
			selectedSet[name] = true
		}
	}
	filtered := make([]McpToolExecutor, 0, len(selectedSet))
	for _, executor := range executors {
		if selectedSet[executor.GetToolDefinition().Name] {
			filtered = append(filtered, executor)
		}
	}
	return filtered
}

func formatMcpResult(result map[string]interface{}) string {
	if len(result) == 0 {
		return "无"
	}
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprint(result[key]))
	}
	return strings.Join(parts, "；")
}

// McpContext holds the result of MCP tool execution for prompt formatting.
type McpContext struct {
	ToolResults []McpToolResult
}

// McpToolResult is the output of a single MCP tool execution.
type McpToolResult struct {
	ToolName string
	Result   map[string]interface{}
	Error    error
}
