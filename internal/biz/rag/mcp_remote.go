package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// McpServerSpec describes a remote MCP server endpoint.
type McpServerSpec struct {
	Name string
	URL  string
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type remoteToolListResponse struct {
	Tools []remoteToolDefinition `json:"tools"`
}

type remoteToolDefinition struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	InputSchema remoteToolInputSchema `json:"inputSchema"`
}

type remoteToolInputSchema struct {
	Type       string                           `json:"type"`
	Properties map[string]remoteToolParamSchema `json:"properties"`
	Required   []string                         `json:"required"`
}

type remoteToolParamSchema struct {
	Type         string        `json:"type"`
	Description  string        `json:"description"`
	DefaultValue interface{}   `json:"default,omitempty"`
	Enum         []interface{} `json:"enum,omitempty"`
}

type remoteCallToolResult struct {
	Content []remoteCallToolContent `json:"content"`
	IsError bool                    `json:"isError"`
}

type remoteCallToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type remoteMcpToolExecutor struct {
	client   *http.Client
	endpoint string
	tool     ToolDefinition
}

// RegisterRemoteMcpServers connects to remote MCP servers and registers their tools.
func RegisterRemoteMcpServers(ctx context.Context, registry McpToolRegistry, servers []McpServerSpec, client *http.Client) error {
	if registry == nil || len(servers) == 0 {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}

	var errs []error
	for _, server := range servers {
		if err := registerRemoteMcpServer(ctx, registry, server, client); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

func registerRemoteMcpServer(ctx context.Context, registry McpToolRegistry, server McpServerSpec, client *http.Client) error {
	endpoint, err := normalizeMcpEndpoint(server.URL)
	if err != nil {
		return fmt.Errorf("normalize mcp server %q url: %w", server.Name, err)
	}

	slog.Info("register remote mcp server", "name", server.Name, "url", endpoint)
	if _, err := callMcp(ctx, client, endpoint, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"clientInfo": map[string]interface{}{
			"name":    "go-base-agent",
			"version": "1.0.0",
		},
	}); err != nil {
		return fmt.Errorf("initialize mcp server %q: %w", server.Name, err)
	}

	rawResult, err := callMcp(ctx, client, endpoint, "tools/list", map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("list tools from mcp server %q: %w", server.Name, err)
	}

	var toolList remoteToolListResponse
	if err := json.Unmarshal(rawResult, &toolList); err != nil {
		return fmt.Errorf("decode mcp tools for %q: %w", server.Name, err)
	}
	if len(toolList.Tools) == 0 {
		slog.Info("remote mcp server has no tools", "name", server.Name)
		return nil
	}

	for _, tool := range toolList.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		def := toToolDefinition(tool)
		registry.Register(&remoteMcpToolExecutor{
			client:   client,
			endpoint: endpoint,
			tool:     def,
		})
	}
	return nil
}

func toToolDefinition(tool remoteToolDefinition) ToolDefinition {
	params := make([]ToolParam, 0, len(tool.InputSchema.Properties))
	keys := make([]string, 0, len(tool.InputSchema.Properties))
	for name := range tool.InputSchema.Properties {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	required := make(map[string]bool, len(tool.InputSchema.Required))
	for _, name := range tool.InputSchema.Required {
		required[name] = true
	}
	for _, name := range keys {
		schema := tool.InputSchema.Properties[name]
		params = append(params, ToolParam{
			Name:         name,
			Type:         schema.Type,
			Description:  schema.Description,
			Required:     required[name],
			DefaultValue: schema.DefaultValue,
			Enum:         convertEnumValues(schema.Enum),
		})
	}
	return ToolDefinition{
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  params,
	}
}

// GetToolDefinition returns the tool definition.
func (e *remoteMcpToolExecutor) GetToolDefinition() ToolDefinition {
	if e == nil {
		return ToolDefinition{}
	}
	return e.tool
}

// Execute invokes the remote MCP tool and returns its text content.
func (e *remoteMcpToolExecutor) Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	rawResult, err := callMcp(ctx, e.client, e.endpoint, "tools/call", map[string]interface{}{
		"name":      e.tool.Name,
		"arguments": params,
	})
	if err != nil {
		return nil, err
	}

	var result remoteCallToolResult
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return nil, fmt.Errorf("decode remote mcp tool result for %s: %w", e.tool.Name, err)
	}

	texts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		if strings.TrimSpace(item.Text) != "" {
			texts = append(texts, item.Text)
		}
	}
	text := strings.Join(texts, "\n")
	out := map[string]interface{}{
		"text": text,
	}
	if result.IsError {
		out["isError"] = true
	}
	return out, nil
}

func callMcp(ctx context.Context, client *http.Client, endpoint, method string, params map[string]interface{}) (json.RawMessage, error) {
	if client == nil {
		client = http.DefaultClient
	}
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      method,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp request %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawReq))
	if err != nil {
		return nil, fmt.Errorf("create mcp request %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send mcp request %s: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read mcp response %s: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp %s returned http %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rpcResp mcpRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode mcp response %s: %w", method, err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp %s failed: %s", method, rpcResp.Error.Message)
	}
	if len(rpcResp.Result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return rpcResp.Result, nil
}

func normalizeMcpEndpoint(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if path == "" || path == "/" {
		path = "/mcp"
	} else if !strings.HasSuffix(path, "/mcp") {
		path += "/mcp"
	}
	parsed.Path = path
	return parsed.String(), nil
}

func joinErrors(errs []error) error {
	filtered := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			filtered = append(filtered, err)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return fmt.Errorf("register remote mcp servers: %w", errors.Join(filtered...))
}

// LLMMcpParameterExtractor extracts MCP tool parameters with an LLM.
type LLMMcpParameterExtractor struct {
	llm chat.LLMService
}

// NewLLMMcpParameterExtractor creates a new LLM-backed MCP parameter extractor.
func NewLLMMcpParameterExtractor(llm chat.LLMService) *LLMMcpParameterExtractor {
	return &LLMMcpParameterExtractor{llm: llm}
}

// ExtractParameters asks the LLM for tool parameters and normalizes the result.
func (e *LLMMcpParameterExtractor) ExtractParameters(ctx context.Context, question string, tool ToolDefinition) (map[string]interface{}, error) {
	return e.ExtractParametersWithTemplate(ctx, question, tool, "")
}

// ExtractParametersWithTemplate asks the LLM for tool parameters using an optional custom system prompt.
func (e *LLMMcpParameterExtractor) ExtractParametersWithTemplate(ctx context.Context, question string, tool ToolDefinition, customPromptTemplate string) (map[string]interface{}, error) {
	if e == nil || e.llm == nil || len(tool.Parameters) == 0 {
		return map[string]interface{}{}, nil
	}

	systemPrompt := "你是一个MCP参数提取器。只返回严格 JSON 对象，不要返回解释、代码块或多余文本。"
	if strings.TrimSpace(customPromptTemplate) != "" {
		systemPrompt = strings.TrimSpace(customPromptTemplate)
	}

	req := chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage(systemPrompt),
			chat.NewUserMessage(buildMcpParameterPrompt(question, tool)),
		},
		Temperature: floatPtr(0.1),
		TopP:        floatPtr(0.3),
	}

	raw, err := e.llm.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("extract mcp parameters for %s: %w", tool.Name, err)
	}

	parsed, err := parseJSONMap(raw)
	if err != nil {
		return nil, fmt.Errorf("parse mcp parameters for %s: %w", tool.Name, err)
	}

	params := make(map[string]interface{}, len(tool.Parameters))
	for _, param := range tool.Parameters {
		value, ok := parsed[param.Name]
		if !ok || value == nil {
			if param.DefaultValue != nil {
				params[param.Name] = coerceMcpValue(param.DefaultValue, param.Type)
			}
			continue
		}
		params[param.Name] = coerceMcpValue(value, param.Type)
	}
	return params, nil
}

func buildMcpParameterPrompt(question string, tool ToolDefinition) string {
	var b strings.Builder
	b.WriteString("工具名称：")
	b.WriteString(tool.Name)
	b.WriteString("\n工具描述：")
	b.WriteString(tool.Description)
	b.WriteString("\n参数定义：\n")
	for _, param := range tool.Parameters {
		b.WriteString("- ")
		b.WriteString(param.Name)
		b.WriteString(" (")
		b.WriteString(param.Type)
		if param.Required {
			b.WriteString(", 必填")
		}
		b.WriteString(")")
		if strings.TrimSpace(param.Description) != "" {
			b.WriteString(": ")
			b.WriteString(param.Description)
		}
		if param.DefaultValue != nil {
			b.WriteString(" [默认值: ")
			b.WriteString(fmt.Sprint(param.DefaultValue))
			b.WriteString("]")
		}
		if len(param.Enum) > 0 {
			b.WriteString(" [可选值: ")
			for i, item := range param.Enum {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(item)
			}
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	b.WriteString("用户问题：")
	b.WriteString(question)
	b.WriteString("\n只返回 JSON 对象，例如 {\"")
	if len(tool.Parameters) > 0 {
		b.WriteString(tool.Parameters[0].Name)
		b.WriteString("\":\"...\"}")
	} else {
		b.WriteString("key\":\"value\"}")
	}
	return b.String()
}

func parseJSONMap(raw string) (map[string]interface{}, error) {
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	if cleaned == "" {
		return map[string]interface{}{}, nil
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil {
		return parsed, nil
	}

	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), &parsed); err == nil {
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("response is not a JSON object")
}

func stripCodeFence(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func coerceMcpValue(value interface{}, typ string) interface{} {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "integer", "int", "long":
		switch v := value.(type) {
		case float64:
			return int(v)
		case float32:
			return int(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	case "number", "float", "double":
		switch v := value.(type) {
		case float64:
			return v
		case float32:
			return float64(v)
		case json.Number:
			if n, err := v.Float64(); err == nil {
				return n
			}
		case string:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				return n
			}
		}
	case "boolean", "bool":
		switch v := value.(type) {
		case bool:
			return v
		case string:
			if n, err := strconv.ParseBool(v); err == nil {
				return n
			}
		}
	default:
		if s, ok := value.(string); ok {
			return s
		}
	}
	return value
}

func floatPtr(v float64) *float64 {
	return &v
}

func convertEnumValues(values []interface{}) []string {
	if len(values) == 0 {
		return nil
	}
	converted := make([]string, 0, len(values))
	for _, value := range values {
		converted = append(converted, fmt.Sprint(value))
	}
	return converted
}
