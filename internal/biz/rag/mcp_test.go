package rag

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appctx "go-base-agent/internal/framework/context"
)

type testMcpExecutor struct {
	tool          ToolDefinition
	params        map[string]interface{}
	result        map[string]interface{}
	err           error
	calls         int
	beforeExecute func()
}

func (e *testMcpExecutor) GetToolDefinition() ToolDefinition { return e.tool }
func (e *testMcpExecutor) Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	e.calls++
	if e.beforeExecute != nil {
		e.beforeExecute()
	}
	e.params = params
	if e.err != nil {
		return nil, e.err
	}
	if e.result != nil {
		return e.result, nil
	}
	return map[string]interface{}{"result": "ok"}, nil
}

type testMcpExtractor struct {
	params map[string]interface{}
	err    error
}

func (e *testMcpExtractor) ExtractParameters(ctx context.Context, question string, tool ToolDefinition) (map[string]interface{}, error) {
	if e.err != nil {
		return nil, e.err
	}
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

func TestDefaultMcpContextProvider_FiltersToolsByTenantDomain(t *testing.T) {
	registry := NewMcpToolRegistry()
	salesExec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "sales_query", Description: "查询销售", Domains: []string{"sales"}},
		result: map[string]interface{}{"text": "sales"},
	}
	ticketExec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "ticket_query", Description: "查询工单", Domains: []string{"ticket"}},
		result: map[string]interface{}{"text": "ticket"},
	}
	registry.Register(salesExec)
	registry.Register(ticketExec)

	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{params: map[string]interface{}{}})
	ctx := appctx.WithTenant(context.Background(), &appctx.TenantContext{Domain: "ticket"})

	contextText, err := provider.BuildContext(ctx, "查工单")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if salesExec.calls != 0 {
		t.Fatalf("expected sales tool to be hidden, got %d calls", salesExec.calls)
	}
	if ticketExec.calls != 1 {
		t.Fatalf("expected ticket tool to be executed once, got %d", ticketExec.calls)
	}
	if !strings.Contains(contextText, "ticket_query") || strings.Contains(contextText, "sales_query") {
		t.Fatalf("unexpected filtered context: %q", contextText)
	}
}

func TestDefaultMcpContextProvider_FormatsDataAndErrorsLikeJavaContext(t *testing.T) {
	registry := NewMcpToolRegistry()
	registry.Register(&testMcpExecutor{
		tool:   ToolDefinition{Name: "member_profile", Description: "查询会员画像"},
		result: map[string]interface{}{"text": "会员等级：金卡"},
	})
	registry.Register(&testMcpExecutor{
		tool:   ToolDefinition{Name: "ticket_query", Description: "查询工单"},
		result: map[string]interface{}{"text": "权限不足", "isError": true},
	})
	registry.Register(&testMcpExecutor{
		tool: ToolDefinition{Name: "weather_query", Description: "查询天气"},
		err:  errors.New("timeout"),
	})

	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{})
	contextText, err := provider.BuildContext(context.Background(), "帮我查会员等级和工单")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"<data>",
		"工具：member_profile\n会员等级：金卡",
		"</data>",
		"<errors>",
		"- 工具调用失败: 权限不足",
		"- 工具调用失败: timeout",
		"</errors>",
	} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("expected MCP context to contain %q, got %q", want, contextText)
		}
	}
	for _, notWant := range []string{"结果：", "text=会员等级：金卡", "isError=true"} {
		if strings.Contains(contextText, notWant) {
			t.Fatalf("expected MCP context not to expose %q, got %q", notWant, contextText)
		}
	}
}

func TestDefaultMcpContextProvider_FormatsParameterExtractionFailureAsError(t *testing.T) {
	registry := NewMcpToolRegistry()
	exec := &testMcpExecutor{
		tool:   ToolDefinition{Name: "member_profile", Description: "查询会员画像"},
		result: map[string]interface{}{"text": "会员等级：金卡"},
	}
	registry.Register(exec)

	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{err: errors.New("missing user id")})
	contextText, err := provider.BuildContext(context.Background(), "帮我查会员等级")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec.calls != 0 {
		t.Fatalf("expected executor not to be called after parameter extraction failure, got %d", exec.calls)
	}
	if !strings.Contains(contextText, "<errors>\n- 工具调用失败: missing user id\n</errors>") {
		t.Fatalf("expected extractor error to be formatted as MCP errors, got %q", contextText)
	}
	if strings.Contains(contextText, "<data>") || strings.Contains(contextText, "会员等级：金卡") {
		t.Fatalf("expected failed parameter extraction not to inject tool data, got %q", contextText)
	}
}

func TestDefaultMcpContextProvider_ExecutesToolsConcurrently(t *testing.T) {
	registry := NewMcpToolRegistry()
	started := make(chan string, 2)
	release := make(chan struct{})
	registry.Register(&testMcpExecutor{
		tool:   ToolDefinition{Name: "member_profile", Description: "查询会员画像"},
		result: map[string]interface{}{"text": "会员等级：金卡"},
		beforeExecute: func() {
			started <- "member_profile"
			<-release
		},
	})
	registry.Register(&testMcpExecutor{
		tool:   ToolDefinition{Name: "weather_query", Description: "查询天气"},
		result: map[string]interface{}{"text": "北京 晴"},
		beforeExecute: func() {
			started <- "weather_query"
			<-release
		},
	})

	provider := NewDefaultMcpContextProvider(registry, &testMcpExtractor{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		text, err := provider.BuildContext(ctx, "帮我查会员等级和天气")
		if err != nil {
			errCh <- err
			return
		}
		done <- text
	}()

	waitStarts := func(wants ...string) {
		t.Helper()
		remaining := make(map[string]bool, len(wants))
		for _, want := range wants {
			remaining[want] = true
		}
		deadline := time.NewTimer(200 * time.Millisecond)
		defer deadline.Stop()
		for len(remaining) > 0 {
			select {
			case got := <-started:
				if !remaining[got] {
					t.Fatalf("unexpected tool start %q", got)
				}
				delete(remaining, got)
			case <-deadline.C:
				t.Fatalf("timeout waiting for tool starts, remaining=%v", remaining)
			}
		}
	}

	waitStarts("member_profile", "weather_query")
	close(release)

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case text := <-done:
		for _, want := range []string{"工具：member_profile", "工具：weather_query"} {
			if !strings.Contains(text, want) {
				t.Fatalf("expected MCP context to contain %q, got %q", want, text)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for MCP context")
	}
}

func TestDefaultMcpContextProvider_UsesIntentPromptTemplate(t *testing.T) {
	registry := NewMcpToolRegistry()
	extractor := &recordingMcpPromptExtractor{}
	registry.Register(&testMcpExecutor{
		tool: ToolDefinition{
			Name: "weather_query",
			Parameters: []ToolParam{
				{Name: "city", Type: "string", Required: true},
			},
		},
	})

	provider := NewDefaultMcpContextProvider(registry, extractor)
	ctxText, err := provider.BuildContextWithIntents(context.Background(), "帮我看天气", []SubQuestionIntent{
		{
			SubQuestion: "帮我看天气",
			NodeScores: []NodeScore{
				{
					Node: IntentNode{
						Kind:                IntentKindMCP,
						McpToolID:           "weather_query",
						ParamPromptTemplate: "你是一个天气参数提取器。",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extractor.systemPrompt != "你是一个天气参数提取器。" {
		t.Fatalf("expected custom prompt to be forwarded, got %q", extractor.systemPrompt)
	}
	if !strings.Contains(ctxText, "weather_query") {
		t.Fatalf("unexpected context text: %q", ctxText)
	}
}

type testMcpSelector struct {
	selected []string
}

func (s *testMcpSelector) SelectTools(ctx context.Context, question string, tools []ToolDefinition) ([]string, error) {
	return s.selected, nil
}
