package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDemoModeAllowsGETQueries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(DemoMode(true))
	r.GET("/knowledge-base", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge-base", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Fatalf("expected handler to run, got %q", w.Body.String())
	}
}

func TestDemoModeRejectsWriteRequestsWithJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(DemoMode(true))
	r.POST("/knowledge-base", func(c *gin.Context) {
		c.String(http.StatusOK, "created")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-base", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"A000001"`) || !strings.Contains(body, "体验环境仅支持查询操作") {
		t.Fatalf("expected demo-mode JSON rejection, got %s", body)
	}
	if strings.Contains(body, "created") {
		t.Fatalf("expected handler not to run, got %s", body)
	}
}

func TestDemoModeRejectsRagChatAsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(DemoMode(true))
	r.GET("/api/ragent/rag/v3/chat", func(c *gin.Context) {
		c.String(http.StatusOK, "stream")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/v3/chat", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", contentType)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: reject") || !strings.Contains(body, "event: done") || !strings.Contains(body, "体验环境仅支持查询操作") {
		t.Fatalf("expected demo-mode SSE rejection, got %s", body)
	}
	if strings.Contains(body, "stream") {
		t.Fatalf("expected handler not to run, got %s", body)
	}
}
