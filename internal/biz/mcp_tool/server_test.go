package mcp_tool

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer_FiltersToolsByTenantDomain(t *testing.T) {
	server, err := NewServer([]*Tool{
		newSalesQueryTool(),
		newTicketQueryTool(),
		newWeatherQueryTool(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
	listReq.Header.Set("X-Tenant-Domain", "ticket")
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)

	body := listRec.Body.String()
	if strings.Contains(body, "sales_query") {
		t.Fatalf("expected sales tool to be hidden, got %s", body)
	}
	if !strings.Contains(body, "ticket_query") || !strings.Contains(body, "weather_query") {
		t.Fatalf("expected ticket and public tools to be visible, got %s", body)
	}

	callReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"sales_query","arguments":{}}}`))
	callReq.Header.Set("X-Tenant-Domain", "ticket")
	callRec := httptest.NewRecorder()
	server.ServeHTTP(callRec, callReq)

	if !strings.Contains(callRec.Body.String(), "unknown tool: sales_query") {
		t.Fatalf("expected unauthorized tool to be hidden, got %s", callRec.Body.String())
	}
}

func TestServer_ToolsListExposesJavaEnumAndDefaultSchema(t *testing.T) {
	server, err := NewServer([]*Tool{
		newSalesQueryTool(),
		newTicketQueryTool(),
		newWeatherQueryTool(),
		newYouComSearchTool("http://example.invalid/search", "key", nil),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	listTools := func(domain string) string {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
		if domain != "" {
			req.Header.Set("X-Tenant-Domain", domain)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	for _, want := range []string{
		`"enum":["current","forecast"]`,
		`"default":"current"`,
		`"default":3`,
		`"enum":["day","week","month","year"]`,
	} {
		if body := listTools(""); !strings.Contains(body, want) {
			t.Fatalf("expected public tools/list schema to contain %s, got %s", want, body)
		}
	}
	for _, want := range []string{
		`"enum":["summary","ranking","detail","trend"]`,
		`"default":"本月"`,
		`"default":10`,
	} {
		if body := listTools("sales"); !strings.Contains(body, want) {
			t.Fatalf("expected sales tools/list schema to contain %s, got %s", want, body)
		}
	}
	for _, want := range []string{
		`"enum":["summary","list","stats"]`,
		`"default":10`,
	} {
		if body := listTools("ticket"); !strings.Contains(body, want) {
			t.Fatalf("expected ticket tools/list schema to contain %s, got %s", want, body)
		}
	}
}

func TestServer_RequiresReadOnlyHintOnRegistration(t *testing.T) {
	// 工具必须自报是读还是写：调用方按 readOnlyHint 决定要不要拦下来让用户确认
	hint := false
	if _, err := NewServer([]*Tool{
		{Name: "write_tool", ReadOnlyHint: &hint, Execute: func(context.Context, map[string]interface{}) ([]toolContent, error) { return nil, nil }},
	}); err != nil {
		t.Fatalf("explicit write hint should register: %v", err)
	}
	if _, err := NewServer([]*Tool{
		{Name: "undeclared_tool", Execute: func(context.Context, map[string]interface{}) ([]toolContent, error) { return nil, nil }},
	}); err == nil {
		t.Fatal("expected registration to fail when readOnlyHint is undeclared")
	} else if !strings.Contains(err.Error(), "undeclared_tool") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServer_ListToolsCarriesAnnotations(t *testing.T) {
	server, err := NewServer([]*Tool{newSalesQueryTool(), newWeatherQueryTool()})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"annotations":{"readOnlyHint":true}`) {
		t.Fatalf("expected readOnlyHint annotations in tools/list, got %s", body)
	}
}

func TestServer_BusinessErrorSetsIsError(t *testing.T) {
	// 业务校验错误必须置 isError=true，调用方才能把错误从事实数据中分离
	server, err := NewServer([]*Tool{newWeatherQueryTool()})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"weather_query","arguments":{"queryType":"current"}}}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"isError":true`) {
		t.Fatalf("expected business validation error to set isError, got %s", body)
	}
	if !strings.Contains(body, "请提供城市名称") {
		t.Fatalf("expected city validation message, got %s", body)
	}
}
