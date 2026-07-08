package rag

import (
	"context"
	"sync"
)

// ToolParam describes a parameter of an MCP tool.
type ToolParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
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
