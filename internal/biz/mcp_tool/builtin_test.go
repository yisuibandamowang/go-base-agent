package mcp_tool

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRegisterToolsIncludesBuiltinMcpTools(t *testing.T) {
	t.Setenv("YDC_API_KEY", "test-key")

	tools := RegisterTools(nil, nil, nil, nil, nil)
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}

	for _, want := range []string{"sales_query", "weather_query", "youcom_search"} {
		if !names[want] {
			t.Fatalf("expected tool %q to be registered, got %+v", want, names)
		}
	}
}

func TestSalesQueryTool_Summary(t *testing.T) {
	tool := newSalesQueryTool()
	content, err := tool.Execute(context.Background(), map[string]interface{}{
		"region":    "华东",
		"period":    "本月",
		"queryType": "summary",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(content) != 1 {
		t.Fatalf("expected one text content, got %+v", content)
	}
	text := content[0].Text
	for _, want := range []string{"华东", "销售数据汇总", "总销售额", "成交订单"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected summary text to contain %q, got %q", want, text)
		}
	}
}

func TestWeatherQueryTool_Forecast(t *testing.T) {
	tool := newWeatherQueryTool()
	content, err := tool.Execute(context.Background(), map[string]interface{}{
		"city":      "北京",
		"queryType": "forecast",
		"days":      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := content[0].Text
	for _, want := range []string{"北京", "未来2天天气预报", "今天", "明天"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected forecast text to contain %q, got %q", want, text)
		}
	}
}

func TestYouComSearchTool_FormatsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "secret" {
			t.Errorf("unexpected api key header: %q", got)
			return
		}
		if got := r.URL.Query().Get("query"); got != "会员Agent" {
			t.Errorf("unexpected query: %q", got)
			return
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Errorf("unexpected count: %q", got)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":{"web":[{"title":"会员Agent 说明","url":"https://example.com/a","description":"支持错误排查","snippets":["支持多轮问答"]}]}}`)
	}))
	defer srv.Close()

	tool := newYouComSearchTool(srv.URL, "secret", srv.Client())
	content, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "会员Agent",
		"count": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := content[0].Text
	for _, want := range []string{"会员Agent 说明", "https://example.com/a", "支持错误排查"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected search text to contain %q, got %q", want, text)
		}
	}
}

func TestSearchDocumentsTool_FiltersByKBID(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	docRepo := repo.NewKnowledgeDocumentRepo(gdb)
	if err := gdb.Create(&model.KnowledgeDocument{KbID: "kb-1", DocName: "会员权益说明.md", FileType: "md"}).Error; err != nil {
		t.Fatalf("seed doc1: %v", err)
	}
	if err := gdb.Create(&model.KnowledgeDocument{KbID: "kb-2", DocName: "退款说明.md", FileType: "md"}).Error; err != nil {
		t.Fatalf("seed doc2: %v", err)
	}

	tool := searchDocsTool(docRepo)
	content, err := tool.Execute(context.Background(), map[string]interface{}{
		"keyword": "会员",
		"kb_id":   "kb-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := content[0].Text
	if !strings.Contains(text, "会员权益说明.md") || strings.Contains(text, "退款说明.md") {
		t.Fatalf("expected only kb-1 doc in result, got %q", text)
	}
}
