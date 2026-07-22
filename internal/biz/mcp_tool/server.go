package mcp_tool

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	appctx "go-base-agent/internal/framework/context"
)

// Server handles MCP JSON-RPC 2.0 requests over HTTP.
type Server struct {
	tools []*Tool
}

// NewServer creates a new MCP server.
func NewServer(tools []*Tool) *Server {
	return &Server{tools: tools}
}

// ServeHTTP implements http.Handler, handles POST requests with JSON-RPC payloads.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, newErrorResp(nil, ErrMethod, "only POST allowed"))
		return
	}

	domain := firstNonEmptyHeader(r.Header.Get("X-Tenant-Domain"))
	if domain != "" {
		r = r.WithContext(appctx.WithTenant(r.Context(), &appctx.TenantContext{Domain: domain}))
	}

	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		writeJSON(w, http.StatusOK, newErrorResp(nil, ErrParse, "failed to read body"))
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, newErrorResp(nil, ErrParse, "invalid JSON: "+err.Error()))
		return
	}

	slog.Info("mcp request", "method", req.Method, "id", string(req.ID))

	resp := s.dispatch(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatch(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case MethodInitialize:
		return newSuccessResp(req.ID, marshalInitResult())

	case MethodPing:
		return newSuccessResp(req.ID, pingResult())

	case MethodToolsList:
		return s.handleListTools(ctx, req.ID)

	case MethodToolsCall:
		return s.handleCallTool(ctx, req.ID, req.Params)

	default:
		return newErrorResp(req.ID, ErrMethod, "unknown method: "+req.Method)
	}
}

func (s *Server) handleListTools(ctx context.Context, id json.RawMessage) jsonRPCResponse {
	domain := ""
	if tenant := appctx.Tenant(ctx); tenant != nil {
		domain = strings.TrimSpace(tenant.Domain)
	}
	descs := make([]toolDesc, 0, len(s.tools))
	for _, t := range s.tools {
		if !t.VisibleToDomain(domain) {
			continue
		}
		descs = append(descs, t.toDesc())
	}
	return newSuccessResp(id, listToolsResult{Tools: descs})
}

func (s *Server) handleCallTool(ctx context.Context, id json.RawMessage, raw json.RawMessage) jsonRPCResponse {
	params, err := parseCallParams(raw)
	if err != nil {
		return newErrorResp(id, ErrParams, err.Error())
	}

	domain := ""
	if tenant := appctx.Tenant(ctx); tenant != nil {
		domain = strings.TrimSpace(tenant.Domain)
	}
	for _, t := range s.tools {
		if t.Name == params.Name && t.VisibleToDomain(domain) {
			content, err := t.Execute(ctx, params.Arguments)
			if err != nil {
				slog.Error("mcp tool execution failed", "tool", params.Name, "err", err)
				return newSuccessResp(id, callToolResult{
					Content: []toolContent{{Type: "text", Text: "工具执行失败: " + err.Error()}},
					IsError: true,
				})
			}
			return newSuccessResp(id, callToolResult{Content: content})
		}
	}
	return newErrorResp(id, ErrMethod, "unknown tool: "+params.Name)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func firstNonEmptyHeader(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
