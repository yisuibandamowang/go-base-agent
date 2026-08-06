package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeGeelibRunner struct {
	t       *testing.T
	results map[string]string
}

func (r fakeGeelibRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	if command == "" {
		return nil, errors.New("missing command")
	}
	keyParts := append([]string{command}, args...)
	key := strings.Join(keyParts, " ")
	if value, ok := r.results[key]; ok {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("unexpected command: %s", key)
}

func TestGeelibSourceFetchDocumentsRecursesNestedTree(t *testing.T) {
	runner := fakeGeelibRunner{t: t, results: map[string]string{}}
	runner.results["editor-cli tree -s 5 -p 425274 --deep --json"] = `{"errno":2000,"errmsg":"Success","data":[{"docId":425274,"title":"根文档","children":[{"docId":111,"title":"一级子文档","children":[{"docId":222,"title":"二级子文档"}]}]}]}`
	runner.results["editor-cli read 425274 --json"] = `{"content":[{"type":"text","text":"# 根文档\n\n根文档正文"}]}`
	runner.results["editor-cli read 111 --json"] = `{"content":[{"type":"text","text":"# 一级子文档\n\n一级正文"}]}`
	runner.results["editor-cli read 222 --json"] = `{"content":[{"type":"text","text":"# 二级子文档\n\n二级正文"}]}`

	source := NewGeelibSource(GeelibSourceConfig{
		Command: "editor-cli",
		Runner:  runner,
	})

	docs, err := source.FetchDocuments(context.Background(), "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274")
	if err != nil {
		t.Fatalf("fetch documents: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}
	if docs[0].Meta.Title != "根文档.md" || !strings.Contains(string(docs[0].Content), "根文档正文") {
		t.Fatalf("unexpected first document: %+v", docs[0].Meta)
	}
	if docs[1].Meta.Title != "一级子文档.md" || docs[2].Meta.Title != "二级子文档.md" {
		t.Fatalf("unexpected nested documents: %+v %+v", docs[1].Meta, docs[2].Meta)
	}
	if docs[0].Meta.SourceName != "geelib" || docs[0].Meta.URL != "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274" {
		t.Fatalf("unexpected source metadata: %+v", docs[0].Meta)
	}
}

func TestGeelibSourceRejectsInvalidURL(t *testing.T) {
	source := NewGeelibSource(GeelibSourceConfig{Command: "editor-cli"})
	_, err := source.FetchDocuments(context.Background(), "https://example.com/not-geelib")
	if err == nil {
		t.Fatal("expected invalid geelib url error")
	}
	if !strings.Contains(err.Error(), "geelib") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeelibSourceParsesTreePayloadShape(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"errno":2000,"errmsg":"Success","data":{"tree":{"docId":425274,"title":"根文档","children":[{"docId":111,"title":"一级子文档"}]}}}`), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, ok := payload["data"]; !ok {
		t.Fatal("expected data key in payload")
	}
}
