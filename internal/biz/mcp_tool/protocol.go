package mcp_tool

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MCP standard JSON-RPC error codes.
const (
	ErrParse    = -32700
	ErrInvalid  = -32600
	ErrMethod   = -32601
	ErrParams   = -32602
	ErrInternal = -32603
)

// MCP protocol method names.
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"
	MethodPing       = "ping"
)

type initResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      serverInfo         `json:"serverInfo"`
	Capabilities    serverCapabilities `json:"capabilities"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type serverCapabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// tools/list params/result.
type listToolsResult struct {
	Tools      []toolDesc `json:"tools"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type toolDesc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]propDesc `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type propDesc struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// tools/call params/result.
type callToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type callToolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// pingResult returns an empty object.
func pingResult() json.RawMessage {
	return json.RawMessage("{}")
}

func marshalInitResult() json.RawMessage {
	r := initResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo: serverInfo{
			Name:    "ragent-mcp-server",
			Version: "1.0.0",
		},
		Capabilities: serverCapabilities{
			Tools: &toolsCapability{ListChanged: false},
		},
	}
	b, _ := json.Marshal(r)
	return b
}

func newSuccessResp(id json.RawMessage, result interface{}) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

func newErrorResp(id json.RawMessage, code int, msg string) jsonRPCResponse {
	rid := interface{}(nil)
	if len(id) > 0 {
		rid = id
	}
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      rid,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func parseCallParams(raw json.RawMessage) (callToolParams, error) {
	var p callToolParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("invalid params: %w", err)
	}
	return p, nil
}
