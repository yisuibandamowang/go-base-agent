package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	appctx "go-base-agent/internal/framework/context"
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
	Domains     []string
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

// McpIntentAwareContextProvider can build MCP context with resolved intent candidates.
type McpIntentAwareContextProvider interface {
	BuildContextWithIntents(ctx context.Context, question string, subIntents []SubQuestionIntent) (string, error)
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
	return p.BuildContextWithIntents(ctx, question, nil)
}

// BuildContextWithIntents executes registered MCP tools and returns prompt-ready context text.
func (p *DefaultMcpContextProvider) BuildContextWithIntents(ctx context.Context, question string, subIntents []SubQuestionIntent) (string, error) {
	if p == nil || p.registry == nil || p.registry.Size() == 0 {
		return "", nil
	}

	executors := p.selectExecutors(ctx, question)
	if len(executors) == 0 {
		return "", nil
	}

	promptTemplates := buildMcpParameterPromptTemplates(subIntents)
	results := p.executeExecutors(ctx, question, executors, promptTemplates)
	successTexts := make([]string, 0, len(executors))
	errorTexts := make([]string, 0)
	for _, result := range results {
		if result.errorText != "" {
			errorTexts = append(errorTexts, result.errorText)
		}
		if result.successText != "" {
			successTexts = append(successTexts, result.successText)
		}
	}
	return formatMcpContextSections(successTexts, errorTexts), nil
}

type mcpToolExecutionResult struct {
	successText string
	errorText   string
}

func (p *DefaultMcpContextProvider) executeExecutors(ctx context.Context, question string, executors []McpToolExecutor, promptTemplates map[string]string) []mcpToolExecutionResult {
	results := make([]mcpToolExecutionResult, len(executors))
	var wg sync.WaitGroup
	wg.Add(len(executors))
	for i, executor := range executors {
		go func(idx int, exec McpToolExecutor) {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				results[idx].errorText = formatMcpToolError(err.Error())
				return
			}

			tool := exec.GetToolDefinition()
			params := map[string]interface{}{}
			var err error
			if p.extractor != nil {
				customPrompt := ""
				if len(promptTemplates) > 0 {
					customPrompt = strings.TrimSpace(promptTemplates[tool.Name])
				}
				if customPrompt != "" {
					if templateAware, ok := p.extractor.(interface {
						ExtractParametersWithTemplate(ctx context.Context, question string, tool ToolDefinition, customPromptTemplate string) (map[string]interface{}, error)
					}); ok {
						params, err = templateAware.ExtractParametersWithTemplate(ctx, question, tool, customPrompt)
					} else {
						params, err = p.extractor.ExtractParameters(ctx, question, tool)
					}
				} else {
					params, err = p.extractor.ExtractParameters(ctx, question, tool)
				}
				if params == nil {
					params = map[string]interface{}{}
				}
			}

			if err != nil {
				results[idx].errorText = formatMcpToolError(err.Error())
				return
			}

			result, err := exec.Execute(ctx, params)
			if err != nil {
				results[idx].errorText = formatMcpToolError(err.Error())
				return
			}
			text := formatMcpResult(result)
			if mcpResultIsError(result) {
				if text != "" {
					results[idx].errorText = formatMcpToolError(text)
				}
				return
			}
			if text == "" {
				return
			}
			results[idx].successText = "工具：" + tool.Name + "\n" + text
		}(i, executor)
	}
	wg.Wait()
	return results
}

func buildMcpParameterPromptTemplates(subIntents []SubQuestionIntent) map[string]string {
	if len(subIntents) == 0 {
		return nil
	}
	templates := make(map[string]string)
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			if ns.Node.Kind != IntentKindMCP {
				continue
			}
			toolID := strings.TrimSpace(ns.Node.McpToolID)
			template := strings.TrimSpace(ns.Node.ParamPromptTemplate)
			if toolID == "" || template == "" {
				continue
			}
			if _, exists := templates[toolID]; !exists {
				templates[toolID] = template
			}
		}
	}
	if len(templates) == 0 {
		return nil
	}
	return templates
}

func (p *DefaultMcpContextProvider) selectExecutors(ctx context.Context, question string) []McpToolExecutor {
	executors := filterExecutorsByTenant(p.registry.ListAllExecutors(), tenantDomain(ctx))
	if len(executors) == 0 {
		return nil
	}
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

func filterExecutorsByTenant(executors []McpToolExecutor, domain string) []McpToolExecutor {
	if len(executors) == 0 {
		return nil
	}
	filtered := make([]McpToolExecutor, 0, len(executors))
	for _, executor := range executors {
		if toolDefinitionVisibleToDomain(executor.GetToolDefinition(), domain) {
			filtered = append(filtered, executor)
		}
	}
	return filtered
}

func tenantDomain(ctx context.Context) string {
	tenant := appctx.Tenant(ctx)
	if tenant == nil {
		return ""
	}
	return strings.TrimSpace(tenant.Domain)
}

func toolDefinitionVisibleToDomain(def ToolDefinition, domain string) bool {
	if len(def.Domains) == 0 {
		return true
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return false
	}
	for _, allowed := range def.Domains {
		if strings.EqualFold(strings.TrimSpace(allowed), domain) {
			return true
		}
	}
	return false
}

func formatMcpResult(result map[string]interface{}) string {
	if len(result) == 0 {
		return "无"
	}
	if text, ok := result["text"]; ok {
		return fmt.Sprint(text)
	}
	keys := make([]string, 0, len(result))
	for key := range result {
		if key == "isError" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprint(result[key]))
	}
	return strings.Join(parts, "；")
}

func mcpResultIsError(result map[string]interface{}) bool {
	value, ok := result["isError"]
	if !ok {
		return false
	}
	if isError, ok := value.(bool); ok {
		return isError
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "true")
}

func formatMcpToolError(text string) string {
	return "- 工具调用失败: " + text
}

func formatMcpContextSections(successTexts, errorTexts []string) string {
	var b strings.Builder
	if len(successTexts) > 0 {
		b.WriteString("<data>\n")
		b.WriteString(strings.Join(successTexts, "\n\n"))
		b.WriteString("\n</data>")
	}
	if len(errorTexts) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("<errors>\n")
		b.WriteString(strings.Join(errorTexts, "\n"))
		b.WriteString("\n</errors>")
	}
	return b.String()
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
