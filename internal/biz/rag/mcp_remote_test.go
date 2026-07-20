package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

func TestRegisterRemoteMcpServersRegistersToolAndExecutesCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		method := req["method"].(string)
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"serverInfo": map[string]interface{}{
						"name":    "stub",
						"version": "1.0.0",
					},
					"capabilities": map[string]interface{}{
						"tools": map[string]interface{}{"listChanged": false},
					},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"tools": []map[string]interface{}{
						{
							"name":        "weather_query",
							"description": "查询天气",
							"inputSchema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"city": map[string]interface{}{
										"type":        "string",
										"description": "城市名称",
									},
								},
								"required": []string{"city"},
							},
						},
					},
				},
			})
		case "tools/call":
			params := req["params"].(map[string]interface{})
			if params["name"] != "weather_query" {
				t.Fatalf("unexpected tool call: %+v", params)
			}
			arguments := params["arguments"].(map[string]interface{})
			if arguments["city"] != "北京" {
				t.Fatalf("unexpected tool arguments: %+v", arguments)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": "北京 今日天气 晴"},
					},
					"isError": false,
				},
			})
		default:
			t.Fatalf("unexpected method %q", method)
		}
	}))
	defer srv.Close()

	registry := NewMcpToolRegistry()
	if err := RegisterRemoteMcpServers(context.Background(), registry, []McpServerSpec{{
		Name: "stub",
		URL:  srv.URL,
	}}, srv.Client()); err != nil {
		t.Fatalf("register remote mcp servers: %v", err)
	}
	if registry.Size() != 1 {
		t.Fatalf("expected 1 tool, got %d", registry.Size())
	}

	exec, ok := registry.GetExecutor("weather_query")
	if !ok {
		t.Fatal("expected remote tool to be registered")
	}
	result, err := exec.Execute(context.Background(), map[string]interface{}{"city": "北京"})
	if err != nil {
		t.Fatalf("execute remote tool: %v", err)
	}
	if !strings.Contains(fmt.Sprint(result), "北京 今日天气 晴") {
		t.Fatalf("expected remote result text, got %+v", result)
	}
}

func TestLLMMcpParameterExtractor_ParsesJsonResponse(t *testing.T) {
	extractor := NewLLMMcpParameterExtractor(&testLLMService{
		reply: `{"city":"北京","queryType":"forecast","days":2}`,
	})
	params, err := extractor.ExtractParameters(context.Background(), "帮我看北京未来两天天气", ToolDefinition{
		Name: "weather_query",
		Parameters: []ToolParam{
			{Name: "city", Type: "string", Required: true},
			{Name: "queryType", Type: "string"},
			{Name: "days", Type: "integer"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params["city"] != "北京" || params["queryType"] != "forecast" || params["days"].(int) != 2 {
		t.Fatalf("unexpected params: %+v", params)
	}
}

func TestLLMMcpParameterExtractor_FillsDefaultValues(t *testing.T) {
	extractor := NewLLMMcpParameterExtractor(&testLLMService{
		reply: `{"city":"北京"}`,
	})
	params, err := extractor.ExtractParameters(context.Background(), "帮我看天气", ToolDefinition{
		Name: "weather_query",
		Parameters: []ToolParam{
			{Name: "city", Type: "string", Required: true},
			{Name: "days", Type: "integer", DefaultValue: 3},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params["days"].(int) != 3 {
		t.Fatalf("expected default value to be filled, got %+v", params)
	}
}

func TestLLMMcpToolSelector_ParsesJsonArray(t *testing.T) {
	selector := NewLLMMcpToolSelector(&testLLMService{
		reply: `["member_profile"]`,
	})
	names, err := selector.SelectTools(context.Background(), "帮我查会员等级", []ToolDefinition{
		{Name: "member_profile", Description: "查询会员画像"},
		{Name: "weather_query", Description: "查询天气"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 1 || names[0] != "member_profile" {
		t.Fatalf("unexpected selected tools: %+v", names)
	}
}

type testLLMService struct {
	reply string
}

func (s *testLLMService) Chat(ctx context.Context, req chat.Request) (string, error) {
	return s.reply, nil
}

func (s *testLLMService) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return s.reply, nil
}

func (s *testLLMService) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	return &fakeHandle{}, nil
}
