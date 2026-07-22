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
