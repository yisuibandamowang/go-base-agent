package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfluenceSourceFetchPageDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/wiki/rest/api/content/12345":
			username, password, ok := r.BasicAuth()
			if !ok || username != "user@example.com" || password != "api-token" {
				t.Fatalf("unexpected basic auth: ok=%v username=%q password=%q", ok, username, password)
			}
			if got := r.URL.Query().Get("expand"); got != "body.storage,version" {
				t.Fatalf("unexpected expand: %q", got)
			}
			_, _ = w.Write([]byte(`{
				"id":"12345",
				"title":"Confluence 文档",
				"_links":{"webui":"/wiki/spaces/ENG/pages/12345/Confluence+文档"},
				"body":{"storage":{"value":"<h1>标题</h1><p>正文</p>","representation":"storage"}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	source := NewConfluenceSource(ConfluenceSourceConfig{
		Name:     "confluence",
		URL:      server.URL + "/wiki/spaces/ENG/pages/12345/Confluence+文档",
		Username: "user@example.com",
		APIKey:   "api-token",
		BaseURL:  server.URL,
	})

	doc, err := source.FetchDocument(context.Background(), "")
	if err != nil {
		t.Fatalf("fetch confluence document: %v", err)
	}
	if !strings.Contains(string(doc.Content), "标题") || !strings.Contains(string(doc.Content), "正文") {
		t.Fatalf("unexpected content: %q", string(doc.Content))
	}
	if doc.Meta.Title != "Confluence 文档" || doc.Meta.SourceName != "confluence" {
		t.Fatalf("unexpected meta: %+v", doc.Meta)
	}
	if doc.Meta.Extra["source_type"] != "confluence" || doc.Meta.Extra["page_id"] != "12345" {
		t.Fatalf("unexpected meta extras: %+v", doc.Meta.Extra)
	}
}
