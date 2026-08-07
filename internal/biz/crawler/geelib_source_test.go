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
		t.Fatalf("expected all docs with content, got %d", len(docs))
	}
	if docs[0].Meta.Title != "根文档.md" || !strings.Contains(string(docs[0].Content), "根文档正文") {
		t.Fatalf("unexpected first document: %+v", docs[0].Meta)
	}
	if docs[0].Meta.SourceName != "geelib" || docs[0].Meta.URL != "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274" {
		t.Fatalf("unexpected source metadata: %+v", docs[0].Meta)
	}
	if docs[1].Meta.Extra["parent_doc_id"] != "425274" || docs[2].Meta.Extra["parent_doc_id"] != "111" {
		t.Fatalf("unexpected parent metadata: %+v %+v", docs[1].Meta.Extra, docs[2].Meta.Extra)
	}
	if docs[0].Meta.Extra["has_children"] != "true" || docs[1].Meta.Extra["has_children"] != "true" {
		t.Fatalf("non-leaf documents should carry has_children metadata: %+v %+v", docs[0].Meta.Extra, docs[1].Meta.Extra)
	}
}

func TestGeelibSourceFetchDocumentsSkipsDirectoryNodes(t *testing.T) {
	runner := fakeGeelibRunner{t: t, results: map[string]string{}}
	runner.results["editor-cli tree -s 5 -p 368231 --deep --json"] = `{"errno":2000,"errmsg":"Success","data":[{"id":368235,"title":"2026-03-06","hasChildren":1,"children":[{"id":368263,"title":"项目整体启动流程梳理和组件使用","hasChildren":0}]},{"id":437010,"title":"扶摇","hasChildren":1,"children":[{"id":437011,"title":"向会员后台开放诊断的具体诊断规则","hasChildren":0}]},{"id":437090,"title":"会员中台游客模式体系理解","hasChildren":0}]}`
	runner.results["editor-cli read 368235 --json"] = `{"content":[{"type":"text","text":"该文档内容为空"}]}`
	runner.results["editor-cli read 437010 --json"] = `{"content":[{"type":"text","text":"该文档内容为空"}]}`
	runner.results["editor-cli read 368263 --json"] = `{"content":[{"type":"text","text":"# 项目整体启动流程梳理和组件使用\n\n正文"}]}`
	runner.results["editor-cli read 437011 --json"] = `{"content":[{"type":"text","text":"# 向会员后台开放诊断的具体诊断规则\n\n正文"}]}`
	runner.results["editor-cli read 437090 --json"] = `{"content":[{"type":"text","text":"# 会员中台游客模式体系理解\n\n正文"}]}`

	source := NewGeelibSource(GeelibSourceConfig{
		Command: "editor-cli",
		Runner:  runner,
	})

	docs, err := source.FetchDocuments(context.Background(), "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231")
	if err != nil {
		t.Fatalf("fetch documents: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs with content, got %d", len(docs))
	}
	for _, doc := range docs {
		if doc.Meta.Title == "扶摇.md" || doc.Meta.Title == "2026-03-06.md" {
			t.Fatalf("directory node should not be returned: %+v", doc.Meta)
		}
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

func TestGeelibReadContentExtractsEditorCLIWrappedText(t *testing.T) {
	payload := []byte(`{"success":true,"command":"read","data":{"docId":437010,"format":"markdown","content":{"content":[{"type":"text","text":"该文档内容为空"}]}}}`)
	content, err := extractGeelibReadContent(payload)
	if err != nil {
		t.Fatalf("extract geelib read content: %v", err)
	}
	if string(content) != "该文档内容为空" {
		t.Fatalf("expected wrapped text content, got %q", string(content))
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
