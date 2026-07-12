package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"

	"github.com/gin-gonic/gin"
)

func TestStatusProbeRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerStatusRoutes(r)

	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{name: "api health", path: "/api/ragent/health", contentType: "application/json", body: `"code":"0"`},
		{name: "root health", path: "/health", contentType: "application/json", body: `"code":"0"`},
		{name: "healthz", path: "/healthz", contentType: "application/json", body: `"code":"0"`},
		{name: "live", path: "/live", contentType: "application/json", body: `"code":"0"`},
		{name: "livez", path: "/livez", contentType: "application/json", body: `"code":"0"`},
		{name: "ready", path: "/ready", contentType: "application/json", body: `"code":"0"`},
		{name: "readyz", path: "/readyz", contentType: "application/json", body: `"code":"0"`},
		{name: "metrics", path: "/metrics", contentType: "text/plain", body: "# HELP ragent_up"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d with body %s", tt.path, w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Fatalf("expected Content-Type to contain %q, got %q", tt.contentType, ct)
			}
			if body := w.Body.String(); !strings.Contains(body, tt.body) {
				t.Fatalf("expected body to contain %q, got %s", tt.body, body)
			}
		})
	}
}

func TestRagEvalHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/api/ragent/rag/eval", ragEval(&fakeEvalRetriever{}))

	t.Run("missing question returns client error", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"A000001"`) {
			t.Fatalf("expected client error for missing question, got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("returns retrieved chunks", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
		r.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `"retrievedChunkIds":["chunk-1"]`) || !strings.Contains(body, `"hasKb":true`) {
			t.Fatalf("expected eval retrieval response, got %d %s", w.Code, body)
		}
	})
}

type fakeEvalRetriever struct{}

func (f *fakeEvalRetriever) Retrieve(ctx context.Context, question string, topK int) ([]rag.RetrievedChunk, error) {
	return []rag.RetrievedChunk{{
		ID:    "chunk-1",
		Text:  "hello context",
		Score: 0.91,
		Metadata: map[string]string{
			"doc_id":  "doc-1",
			"kb_name": "kb",
		},
	}}, nil
}
