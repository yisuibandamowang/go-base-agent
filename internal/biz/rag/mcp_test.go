package rag

import (
	"context"
	"strings"
	"testing"
)

type testMcpExecutor struct {
	tool   ToolDefinition
	params map[string]interface{}
	result map[string]interface{}
	calls  int
}

func (e *testMcpExecutor) GetToolDefinition() ToolDefinition { return e.tool }
func (e *testMcpExecutor) Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	e.calls++
	e.params = params
	if e.result != nil {
		return e.result, nil
	}
	return map[string]interface{}{"result": "ok"}, nil
}

type testMcpExtractor struct {
	params map[string]interface{}
}

func (e *testMcpExtractor) ExtractParameters(ctx context.Context, question string, tool ToolDefinition) (map[string]interface{}, error) {
	return e.params, nil
}

func TestMcpToolRegistry_Register(t *testing.T) {
	r := NewMcpToolRegistry()
	r.Register(&testMcpExecutor{tool: ToolDefinition{Name: "weather", Description: "get weather"}})

	if r.Size() != 1 {
		t.Fatalf("expected 1 tool, got %d", r.Size())
	}

	exec, ok := r.GetExecutor("weather")
	if !ok {
		t.Fatal("expected tool to exist")
	}
	if exec.GetToolDefinition().Name != "weather" {
		t.Fatal("unexpected tool name")
	}
}

func TestMcpToolRegistry_Unregister(t *testing.T) {
	r := NewMcpToolRegistry()
	r.Register(&testMcpExecutor{tool: ToolDefinition{Name: "weather"}})
	r.Unregister("weather")

	if r.Size() != 0 {
		t.Fatalf("expected 0 after unregister, got %d", r.Size())
	}
	_, ok := r.GetExecutor("weather")
	if ok {
		t.Fatal("should not find unregistered tool")
	}
}

func TestMcpToolRegistry_ListAll(t *testing.T) {
	r := NewMcpToolRegistry()
	r.Register(&testMcpExecutor{tool: ToolDefinition{Name: "w1"}})
	r.Register(&testMcpExecutor{tool: ToolDefinition{Name: "w2"}})

	tools := r.ListAllTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	executors := r.ListAllExecutors()
	if len(executors) != 2 {
		t.Fatalf("expected 2 executors, got %d", len(executors))
	}
}

func TestMcpToolExecutor(t *testing.T) {
	exec := &testMcpExecutor{tool: ToolDefinition{
		Name:        "query",
		Description: "query data",
		Parameters: []ToolParam{
			{Name: "id", Type: "string", Required: true},
		},
	}}

	result, err := exec.Execute(context.Background(), map[string]interface{}{"id": "123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["result"] != "ok" {
		t.Fatal("unexpected result")
	}
}

func TestDefaultMcpContextProvider_BuildContextExecutesRegisteredTools(t *testing.T) {
	registry := NewMcpToolRegistry()
	exec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "member_profile", Description: "查询会员画像"},
		result: map[string]interface{}{"level": "gold", "points": 120},
	}
	registry.Register(exec)
	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{
		params: map[string]interface{}{"userId": "u-1"},
	})

	contextText, err := provider.BuildContext(context.Background(), "查询会员等级")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.params["userId"] != "u-1" {
		t.Fatalf("expected extractor params to be passed to executor, got %+v", exec.params)
	}
	for _, want := range []string{"工具：member_profile", "level=gold", "points=120"} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("expected MCP context to contain %q, got %q", want, contextText)
		}
	}
}

func TestDefaultMcpContextProvider_SelectsRelevantTools(t *testing.T) {
	registry := NewMcpToolRegistry()
	memberExec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "member_profile", Description: "查询会员画像"},
		result: map[string]interface{}{"level": "gold"},
	}
	weatherExec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "weather_query", Description: "查询天气"},
		result: map[string]interface{}{"city": "北京"},
	}
	registry.Register(memberExec)
	registry.Register(weatherExec)

	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{
		params: map[string]interface{}{"userId": "u-1"},
	}, &testMcpSelector{selected: []string{"member_profile"}})

	contextText, err := provider.BuildContext(context.Background(), "帮我查会员等级")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if memberExec.calls != 1 {
		t.Fatalf("expected selected tool to be executed once, got %d", memberExec.calls)
	}
	if weatherExec.calls != 0 {
		t.Fatalf("expected unselected tool to be skipped, got %d", weatherExec.calls)
	}
	if !strings.Contains(contextText, "member_profile") || strings.Contains(contextText, "weather_query") {
		t.Fatalf("unexpected selected context: %q", contextText)
	}
}

type testMcpSelector struct {
	selected []string
}

func (s *testMcpSelector) SelectTools(ctx context.Context, question string, tools []ToolDefinition) ([]string, error) {
	return s.selected, nil
}
