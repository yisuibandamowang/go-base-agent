package rag

import (
	"context"
	"testing"
)

type testMcpExecutor struct {
	tool ToolDefinition
}

func (e *testMcpExecutor) GetToolDefinition() ToolDefinition { return e.tool }
func (e *testMcpExecutor) Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"result": "ok"}, nil
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
