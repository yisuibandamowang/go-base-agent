package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSourceFetchDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("expected bearer token header, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte("# 会员 Agent\n\n正文"))
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{
		Name:     "member-doc",
		URL:      server.URL + "/doc.md",
		FileName: "会员Agent说明.md",
		Token:    "secret-token",
		MaxBytes: 1024,
	})

	metas, err := source.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].MimeType != "text/markdown" || metas[0].Title != "会员Agent说明.md" {
		t.Fatalf("unexpected metadata: %+v", metas[0])
	}

	doc, err := source.FetchDocument(context.Background(), metas[0].ID)
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if string(doc.Content) != "# 会员 Agent\n\n正文" {
		t.Fatalf("unexpected content: %q", string(doc.Content))
	}
	if doc.Meta.SourceName != "member-doc" || doc.Meta.URL != server.URL+"/doc.md" {
		t.Fatalf("unexpected doc meta: %+v", doc.Meta)
	}
}

func TestHTTPSourceRejectsOversizedDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "64")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{
		Name:     "oversized",
		URL:      server.URL,
		MaxBytes: 8,
	})
	if _, err := source.FetchDocument(context.Background(), server.URL); err == nil {
		t.Fatal("expected oversized document error")
	}
}
