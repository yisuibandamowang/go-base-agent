package mcp_tool

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer_FiltersToolsByTenantDomain(t *testing.T) {
	server := NewServer([]*Tool{
		newSalesQueryTool(),
		newTicketQueryTool(),
		newWeatherQueryTool(),
	})

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
	server := NewServer([]*Tool{
		newSalesQueryTool(),
		newTicketQueryTool(),
		newWeatherQueryTool(),
		newYouComSearchTool("http://example.invalid/search", "key", nil),
	})

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
