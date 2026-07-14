package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeishuSourceFetchDocxDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal/":
			_ = r.ParseForm()
			_, _ = w.Write([]byte(`{"tenant_access_token":"tenant-token"}`))
		case r.Method == http.MethodHead && r.URL.Path == "/docx/doc-123":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/docx/v1/documents/doc-123/raw_content":
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Fatalf("unexpected authorization header: %q", got)
			}
			_, _ = w.Write([]byte(`{"data":{"content":"# 飞书文档\n正文"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := NewFeishuSource(FeishuSourceConfig{
		Name:          "feishu-doc",
		URL:           server.URL + "/docx/doc-123",
		FileName:      "飞书文档.md",
		AppID:         "app-id",
		AppSecret:     "app-secret",
		TokenEndpoint: server.URL + "/open-apis/auth/v3/tenant_access_token/internal/",
		BaseURL:       server.URL,
	})

	metas, err := source.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].Title != "飞书文档.md" || metas[0].SourceName != "feishu-doc" {
		t.Fatalf("unexpected list meta: %+v", metas[0])
	}

	doc, err := source.FetchDocument(context.Background(), metas[0].ID)
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if !strings.Contains(string(doc.Content), "飞书文档") || !strings.Contains(string(doc.Content), "正文") {
		t.Fatalf("unexpected doc content: %q", string(doc.Content))
	}
	if doc.Meta.SourceName != "feishu-doc" || doc.Meta.Extra["source_type"] != "feishu" {
		t.Fatalf("unexpected doc meta: %+v", doc.Meta)
	}
}

func TestFeishuSourceDirectUrl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer direct-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		_, _ = w.Write([]byte("plain content"))
	}))
	defer server.Close()

	source := NewFeishuSource(FeishuSourceConfig{
		Name:        "feishu-direct",
		URL:         server.URL + "/plain.txt",
		AccessToken: "direct-token",
		BaseURL:     server.URL,
	})

	doc, err := source.FetchDocument(context.Background(), "")
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if string(doc.Content) != "plain content" {
		t.Fatalf("unexpected content: %q", string(doc.Content))
	}
}
