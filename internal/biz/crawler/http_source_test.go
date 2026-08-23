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
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Thu, 16 Jul 2026 10:00:00 GMT")
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
	if doc.Meta.Extra["etag"] != `"v1"` || doc.Meta.Extra["last_modified"] != "Thu, 16 Jul 2026 10:00:00 GMT" {
		t.Fatalf("expected remote validators in metadata, got %+v", doc.Meta.Extra)
	}
}

func TestHTTPSourceFetchDocumentIfChangedUsesETagBeforeLastModified(t *testing.T) {
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Last-Modified", "Thu, 16 Jul 2026 10:00:00 GMT")
		if r.Method == http.MethodGet {
			getCalls++
			_, _ = w.Write([]byte("new content"))
		}
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{URL: server.URL + "/doc.md"})
	doc, changed, err := source.FetchDocumentIfChanged(context.Background(), source.cfg.URL, `"v1"`, "Thu, 16 Jul 2026 10:00:00 GMT", "old-hash")
	if err != nil {
		t.Fatalf("fetch changed document: %v", err)
	}
	if !changed || doc == nil || string(doc.Content) != "new content" {
		t.Fatalf("expected download after etag change, changed=%v doc=%+v", changed, doc)
	}
	if getCalls != 1 {
		t.Fatalf("expected one GET, got %d", getCalls)
	}
}

func TestHTTPSourceFetchDocumentIfChangedKeepsHEADValidatorsWhenGETOmitsThem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("ETag", `"v2"`)
			w.Header().Set("Last-Modified", "Thu, 16 Jul 2026 10:00:00 GMT")
			return
		}
		_, _ = w.Write([]byte("new content"))
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{URL: server.URL + "/doc.md"})
	doc, changed, err := source.FetchDocumentIfChanged(context.Background(), source.cfg.URL, `"v1"`, "Thu, 16 Jul 2026 10:00:00 GMT", "old-hash")
	if err != nil {
		t.Fatalf("fetch changed document: %v", err)
	}
	if !changed || doc.Meta.Extra["etag"] != `"v2"` || doc.Meta.Extra["last_modified"] != "Thu, 16 Jul 2026 10:00:00 GMT" {
		t.Fatalf("expected HEAD validators to survive GET, changed=%v meta=%+v", changed, doc.Meta)
	}
}

func TestHTTPSourceFetchDocumentIfChangedSkipsWhenETagMatches(t *testing.T) {
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Thu, 16 Jul 2026 10:00:01 GMT")
		if r.Method == http.MethodGet {
			getCalls++
		}
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{URL: server.URL + "/doc.md"})
	doc, changed, err := source.FetchDocumentIfChanged(context.Background(), source.cfg.URL, `"v1"`, "Thu, 16 Jul 2026 10:00:00 GMT", "old-hash")
	if err != nil {
		t.Fatalf("check changed document: %v", err)
	}
	if changed || doc == nil || len(doc.Content) != 0 {
		t.Fatalf("expected validator skip, changed=%v doc=%+v", changed, doc)
	}
	if getCalls != 0 {
		t.Fatalf("expected no GET after matching etag, got %d", getCalls)
	}
}

func TestHTTPSourceFetchDocumentIfChangedFallsBackToLastModified(t *testing.T) {
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Thu, 16 Jul 2026 10:00:00 GMT")
		if r.Method == http.MethodGet {
			getCalls++
			_, _ = w.Write([]byte("same content"))
		}
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{URL: server.URL + "/doc.md"})
	doc, changed, err := source.FetchDocumentIfChanged(context.Background(), source.cfg.URL, `"old-etag"`, "Thu, 16 Jul 2026 10:00:00 GMT", "old-hash")
	if err != nil {
		t.Fatalf("check changed document: %v", err)
	}
	if changed || doc == nil || getCalls != 0 {
		t.Fatalf("expected last-modified skip, changed=%v doc=%+v getCalls=%d", changed, doc, getCalls)
	}
}

func TestHTTPSourceFetchDocumentIfChangedUsesHashWhenValidatorsMissing(t *testing.T) {
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCalls++
			_, _ = w.Write([]byte("same content"))
		}
	}))
	defer server.Close()

	source := NewHTTPSource(HTTPSourceConfig{URL: server.URL + "/doc.md"})
	contentHash := sha256Hex([]byte("same content"))
	doc, changed, err := source.FetchDocumentIfChanged(context.Background(), source.cfg.URL, "", "", contentHash)
	if err != nil {
		t.Fatalf("check changed document: %v", err)
	}
	if changed || doc == nil || getCalls != 1 {
		t.Fatalf("expected hash skip after one GET, changed=%v doc=%+v getCalls=%d", changed, doc, getCalls)
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
