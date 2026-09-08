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
	// 对齐 Java DefaultContextFormatter.formatMcpContext：按 toolId → 意图映射，
	// 在每个工具结果段前注入该意图的 promptSnippet（<rules> 段）。
	intentRules := buildMcpIntentPromptSnippets(subIntents)
	results := p.executeExecutors(ctx, question, executors, promptTemplates, intentRules)
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

// buildMcpIntentPromptSnippets 构建 toolId → 意图 promptSnippet 映射。
func buildMcpIntentPromptSnippets(subIntents []SubQuestionIntent) map[string]string {
	if len(subIntents) == 0 {
		return nil
	}
	snippets := make(map[string]string)
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			if ns.Node.Kind != IntentKindMCP {
				continue
			}
			toolID := strings.TrimSpace(ns.Node.McpToolID)
			snippet := strings.TrimSpace(ns.Node.PromptSnippet)
			if toolID == "" || snippet == "" {
				continue
			}
			if _, exists := snippets[toolID]; !exists {
				snippets[toolID] = snippet
			}
		}
	}
	if len(snippets) == 0 {
		return nil
	}
	return snippets
}

type mcpToolExecutionResult struct {
	successText string
	errorText   string
}

func (p *DefaultMcpContextProvider) executeExecutors(ctx context.Context, question string, executors []McpToolExecutor, promptTemplates map[string]string, intentRules map[string]string) []mcpToolExecutionResult {
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
			params, proceed := p.extractToolParams(ctx, question, tool, promptTemplates, idx, &results[idx])
			if !proceed {
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
			// 对齐 Java mcp-section 模板：工具意图配置了 promptSnippet 时，
			// 该工具的结果段前置 <rules> 回答规则。
			section := ""
			if snippet := strings.TrimSpace(intentRules[tool.Name]); snippet != "" {
				section = "<rules>\n" + snippet + "\n</rules>\n"
			}
			results[idx].successText = section + "工具：" + tool.Name + "\n" + text
		}(i, executor)
	}
	wg.Wait()
	return results
}

// extractToolParams 提取工具参数并按三态分流：
// 仅 SUCCESS 才返回 proceed=true 让调用方真正执行工具；
// NEED_CLARIFICATION 注入澄清提示（作为正文进上下文，便于 LLM 据此向用户追问）；
// FAILED 注入失败提示进「工具调用失败」段。
// 对齐 Java RetrievalEngine 的按提参结局分流。
func (p *DefaultMcpContextProvider) extractToolParams(ctx context.Context, question string, tool ToolDefinition, promptTemplates map[string]string, idx int, result *mcpToolExecutionResult) (map[string]interface{}, bool) {
	customPrompt := ""
	if len(promptTemplates) > 0 {
		customPrompt = strings.TrimSpace(promptTemplates[tool.Name])
	}

	if p.extractor == nil {
		return map[string]interface{}{}, true
	}

	// 优先走三态校验提取
	if validator, ok := p.extractor.(McpParameterExtractionValidator); ok {
		extraction := validator.ExtractParametersValidated(ctx, question, tool, customPrompt)
		switch extraction.Status {
		case McpExtractionSuccess:
			return extraction.Params, true
		case McpExtractionNeedClarification:
			result.successText = mcpClarificationNote(tool.Name, extraction.MissingRequired)
			return nil, false
		default:
			result.errorText = formatMcpToolError("未能为工具【" + tool.Name + "】提取到有效参数，已跳过调用。")
			return nil, false
		}
	}

	// 兼容旧两态提取器
	var params map[string]interface{}
	var err error
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
	if err != nil {
		result.errorText = formatMcpToolError(err.Error())
		return nil, false
	}
	if params == nil {
		params = map[string]interface{}{}
	}
	return params, true
}

// mcpClarificationNote 构造缺少必填参数的澄清提示。
// isError=false 使其作为正文进入上下文（而非「工具调用失败」段），便于 LLM 直接据此追问。
func mcpClarificationNote(toolName string, missingRequired []string) string {
	missing := "必要信息"
	if len(missingRequired) > 0 {
		missing = strings.Join(missingRequired, "、")
	}
	return fmt.Sprintf("调用工具【%s】需要参数：%s，但用户问题中未提供。请在回答中主动向用户询问这些信息，不要编造。", toolName, missing)
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

// McpExtractionStatus 枚举 MCP 参数提取结局。
type McpExtractionStatus int

const (
	// McpExtractionSuccess 参数已就绪，可调用工具。
	McpExtractionSuccess McpExtractionStatus = iota
	// McpExtractionNeedClarification 缺少必填参数（用户未提供），不调用工具、需向用户追问。
	McpExtractionNeedClarification
	// McpExtractionFailed 无法提取到有效参数（协议畸形 / 值非法），不调用工具。
	McpExtractionFailed
)

// McpExtractionResult MCP 参数提取结局。
// 区分三态供消费端决定是否调用工具。对齐 Java McpExtractionResult。
type McpExtractionResult struct {
	Status McpExtractionStatus
	// Params 已提取的有效参数（SUCCESS 用于调用；其余态仅作记录）。
	Params map[string]interface{}
	// MissingRequired 用户未提供的必填参数名（仅 NEED_CLARIFICATION 非空）。
	MissingRequired []string
}

// McpExtractionSucceeded 构造成功结局。
func McpExtractionSucceeded(params map[string]interface{}) McpExtractionResult {
	if params == nil {
		params = map[string]interface{}{}
	}
	return McpExtractionResult{Status: McpExtractionSuccess, Params: params}
}

// McpExtractionNeedsClarification 构造缺少必填参数结局。
func McpExtractionNeedsClarification(params map[string]interface{}, missingRequired []string) McpExtractionResult {
	if params == nil {
		params = map[string]interface{}{}
	}
	return McpExtractionResult{Status: McpExtractionNeedClarification, Params: params, MissingRequired: missingRequired}
}

// McpExtractionFailedResult 构造提取失败结局。
func McpExtractionFailedResult() McpExtractionResult {
	return McpExtractionResult{Status: McpExtractionFailed}
}

// McpParameterExtractionValidator 支持三态校验的参数提取器。
// SUCCESS / NEED_CLARIFICATION / FAILED 的判定规则与 Java validateMcpParams 一致：
// JSON 解析失败或值类型/枚举非法＝FAILED；必填无默认参数缺失＝NEED_CLARIFICATION。
type McpParameterExtractionValidator interface {
	ExtractParametersValidated(ctx context.Context, question string, tool ToolDefinition, customPromptTemplate string) McpExtractionResult
}
