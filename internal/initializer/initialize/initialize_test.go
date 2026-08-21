package initialize

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultEnterpriseKnowledgeBaseDataset(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	datasetDir := filepath.Join(filepath.Dir(sourceFile), "../../../resources/initializer/enterprise-knowledge-base")
	dataset, err := LoadDataset(datasetDir)
	if err != nil {
		t.Fatalf("load default enterprise dataset: %v", err)
	}
	if len(dataset.KnowledgeBases) != 2 {
		t.Fatalf("expected 2 knowledge bases, got %d", len(dataset.KnowledgeBases))
	}
	if len(dataset.Intents) != 23 {
		t.Fatalf("expected 23 intents, got %d", len(dataset.Intents))
	}
	if len(dataset.Questions) != 15 {
		t.Fatalf("expected 15 questions, got %d", len(dataset.Questions))
	}
}

func TestVerifyChecksumsAcceptsMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledge-bases.properties")
	content := []byte("knowledge-base.refs=group\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write checksum target: %v", err)
	}
	sum := sha256.Sum256(content)
	writeTestFile(t, filepath.Join(dir, "checksums.sha256"), fmt.Sprintf("%x  knowledge-bases.properties\n", sum))

	if err := verifyChecksums(dir); err != nil {
		t.Fatalf("verifyChecksums: %v", err)
	}
}

func TestVerifyChecksumsRejectsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.md")
	writeTestFile(t, path, "原始内容")
	sum := sha256.Sum256([]byte("原始内容"))
	writeTestFile(t, filepath.Join(dir, "checksums.sha256"), fmt.Sprintf("%x  document.md\n", sum))
	writeTestFile(t, path, "被篡改内容")

	if err := verifyChecksums(dir); err == nil || !strings.Contains(err.Error(), "checksum 不一致") {
		t.Fatalf("expected checksum mismatch, got: %v", err)
	}
}

func TestLoadDatasetFromProperties(t *testing.T) {
	dir := t.TempDir()
	intentDir := filepath.Join(dir, "intents")
	if err := os.MkdirAll(intentDir, 0o755); err != nil {
		t.Fatalf("mkdir intents: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "knowledge-bases.properties"), `
knowledge-base.refs=group,biz
knowledge-base.group.name=集团信息化
knowledge-base.group.collection-name=tutorial_group
knowledge-base.group.embedding-model=qwen-emb-8b
knowledge-base.group.ingestion-spec={"parseProfile":"fast","maxChars":256}
knowledge-base.group.documents=docs/knowledge/group
knowledge-base.biz.name=业务系统
knowledge-base.biz.collection-name=tutorial_biz
knowledge-base.biz.embedding-model=qwen-emb-8b
knowledge-base.biz.documents=docs/knowledge/biz
`)
	writeTestFile(t, filepath.Join(intentDir, "000-group.properties"), `
code=group
name=集团信息化
level=0
kind=0
knowledge-base-ref=group
sort-order=0
enabled=true
mcp-tool-id=group_lookup
prompt-snippet-file=prompts/snippet.txt
prompt-template-file=prompts/answer.txt
param-prompt-template-file=prompts/params.txt
`)
	writeTestFile(t, filepath.Join(intentDir, "100-biz.properties"), `
code=biz
name=业务系统
level=0
kind=0
knowledge-base-ref=biz
sort-order=1
enabled=true
`)
	writeTestFile(t, filepath.Join(dir, "questions.properties"), `
question.refs=q01
question.q01.title=权限定位
question.q01.description=从症状判断根因
question.q01.text=VPN 连接成功但无权限怎么办？
question.q01.follow-ups=回答得不错，谢谢|还有别的建议吗
`)
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "prompts", "snippet.txt"), "意图片段")
	writeTestFile(t, filepath.Join(dir, "prompts", "answer.txt"), "回答模板")
	writeTestFile(t, filepath.Join(dir, "prompts", "params.txt"), "参数模板")
	for _, documentDir := range []string{
		filepath.Join(dir, "docs", "knowledge", "group"),
		filepath.Join(dir, "docs", "knowledge", "biz"),
	} {
		if err := os.MkdirAll(documentDir, 0o755); err != nil {
			t.Fatalf("mkdir documents: %v", err)
		}
		writeTestFile(t, filepath.Join(documentDir, "guide.md"), "文档内容")
	}

	dataset, err := LoadDataset(dir)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}
	if len(dataset.KnowledgeBases) != 2 {
		t.Fatalf("expected 2 knowledge bases, got %d", len(dataset.KnowledgeBases))
	}
	if dataset.KnowledgeBases[0].CollectionName != "tutorial_group" {
		t.Fatalf("unexpected collection name: %s", dataset.KnowledgeBases[0].CollectionName)
	}
	if dataset.KnowledgeBases[0].IngestionSpec != `{"parseProfile":"fast","maxChars":256}` {
		t.Fatalf("unexpected ingestion spec: %q", dataset.KnowledgeBases[0].IngestionSpec)
	}
	if len(dataset.Intents) != 2 {
		t.Fatalf("expected 2 intents, got %d", len(dataset.Intents))
	}
	if dataset.Intents[0].KnowledgeBaseRef != "group" {
		t.Fatalf("unexpected kb ref: %s", dataset.Intents[0].KnowledgeBaseRef)
	}
	if dataset.Intents[0].McpToolID != "group_lookup" || dataset.Intents[0].PromptSnippet != "意图片段" ||
		dataset.Intents[0].PromptTemplate != "回答模板" || dataset.Intents[0].ParamPromptTemplate != "参数模板" {
		t.Fatalf("expected intent MCP and prompt file fields, got: %+v", dataset.Intents[0])
	}
	if len(dataset.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(dataset.Questions))
	}
	if len(dataset.Questions[0].FollowUps) != 2 {
		t.Fatalf("expected 2 follow-ups, got %d", len(dataset.Questions[0].FollowUps))
	}
}

func TestLoadDatasetRejectsUnknownKBRef(t *testing.T) {
	dir := t.TempDir()
	intentDir := filepath.Join(dir, "intents")
	if err := os.MkdirAll(intentDir, 0o755); err != nil {
		t.Fatalf("mkdir intents: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "knowledge-bases.properties"), "knowledge-base.refs=group\nknowledge-base.group.name=集团\nknowledge-base.group.collection-name=c1\nknowledge-base.group.embedding-model=m\n")
	writeTestFile(t, filepath.Join(intentDir, "000-bad.properties"), "code=bad\nname=坏节点\nlevel=0\nkind=0\nknowledge-base-ref=missing\n")

	if _, err := LoadDataset(dir); err == nil {
		t.Fatal("expected error for unknown knowledge base ref")
	} else if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should mention missing ref, got: %v", err)
	}
}

func TestLoadDatasetRejectsKnowledgeBaseWithoutDocuments(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "intents"), 0o755); err != nil {
		t.Fatalf("mkdir intents: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "knowledge-bases.properties"), "knowledge-base.refs=group\nknowledge-base.group.name=集团\nknowledge-base.group.collection-name=c1\nknowledge-base.group.embedding-model=m\nknowledge-base.group.documents=docs\n")
	writeTestFile(t, filepath.Join(dir, "intents", "000-root.properties"), "code=root\nname=根\nlevel=0\nkind=0\nsort-order=0\n")
	if _, err := LoadDataset(dir); err == nil || (!strings.Contains(err.Error(), "没有文档") && !strings.Contains(err.Error(), "目录不存在")) {
		t.Fatalf("expected missing documents error, got: %v", err)
	}
}

func TestRunExecutesPhasesInOrder(t *testing.T) {
	var order []string
	opts := Options{
		BaseURL:      "http://127.0.0.1:8080",
		AgentTypeDir: "testdata",
		SkipWarmup:   true,
		Preflight:    func(ctx context.Context) error { order = append(order, "preflight"); return nil },
		Cleanup:      func(ctx context.Context) error { order = append(order, "cleanup"); return nil },
	}
	dataset := &Dataset{
		KnowledgeBases: []KnowledgeBase{{Ref: "g", Name: "g", CollectionName: "c1", EmbeddingModel: "m"}},
		Intents:        []Intent{{Code: "i1", Name: "i1"}},
		Questions:      []Question{{Ref: "q1", Text: "问题"}},
	}
	// 后续环节依赖真实服务，登录失败会终止；前两个注入环节的执行顺序是本测试关注点。
	err := Run(context.Background(), opts, dataset)
	if err == nil {
		t.Fatal("expected knowledge-base phase to fail without a live service")
	}
	expected := []string{"preflight", "cleanup"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d phases executed before failure, got %d: %v", len(expected), len(order), order)
	}
	for i, name := range expected {
		if order[i] != name {
			t.Fatalf("phase %d: expected %s, got %s", i, name, order[i])
		}
	}
}

func TestRunRequiresAgentTypeDir(t *testing.T) {
	err := Run(context.Background(), Options{}, &Dataset{})
	if err == nil || !strings.Contains(err.Error(), "智能体类型数据集目录") {
		t.Fatalf("expected agent type dir error, got: %v", err)
	}
}

func TestInitKnowledgeBasesIdempotent(t *testing.T) {
	var created int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/login"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "0",
				"data": map[string]any{"token": "t", "role": "admin"},
			})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "0",
				"data": []map[string]any{{"id": "1", "name": "集团信息化", "collectionName": "tutorial_group", "embeddingModel": "qwen-emb-8b"}},
			})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodPost:
			created++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": "100"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dataset := &Dataset{
		KnowledgeBases: []KnowledgeBase{
			{Ref: "group", Name: "集团信息化", CollectionName: "tutorial_group", EmbeddingModel: "qwen-emb-8b"},
			{Ref: "biz", Name: "业务系统", CollectionName: "tutorial_biz", EmbeddingModel: "qwen-emb-8b"},
		},
	}
	client, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err := initKnowledgeBases(context.Background(), client, dataset, false); err != nil {
		t.Fatalf("initKnowledgeBases: %v", err)
	}
	if created != 1 {
		t.Fatalf("expected only the missing kb to be created, got %d", created)
	}
}

func TestListDocumentsFetchesAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ragent/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"token": "t", "role": "admin"}})
			return
		}
		if r.URL.Path != "/api/ragent/knowledge-base/kb-1/docs" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		current := r.URL.Query().Get("current")
		if current == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"docName": "two.md"}}, "pages": 2,
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
			"records": []map[string]any{{"docName": "one.md"}}, "pages": 2,
		}})
	}))
	defer server.Close()

	c, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	documents, err := c.listDocuments(context.Background(), "kb-1")
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if len(documents) != 2 || strValue(documents[1]["docName"]) != "two.md" {
		t.Fatalf("expected both document pages, got: %#v", documents)
	}
}

func TestInitDocumentsUploadsChunksAndWaitsForSuccess(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "会员手册.md")
	writeTestFile(t, file, "会员规则")
	var uploaded, chunked int
	statusChecks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ragent/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"token": "t", "role": "admin"}})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{{"id": "kb-1", "collectionName": "collection"}}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"records": []any{}}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs/upload" && r.Method == http.MethodPost:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse upload: %v", err)
				return
			}
			if got := r.FormValue("processMode"); got != "chunk" {
				t.Errorf("expected chunk process mode, got %q", got)
			}
			if got := r.FormValue("ingestionSpec"); got != `{"parseProfile":"fast","maxChars":256}` {
				t.Errorf("expected ingestion spec, got %q", got)
			}
			if _, header, err := r.FormFile("file"); err != nil || header.Filename != "会员手册.md" {
				t.Errorf("unexpected uploaded file: %v, header=%v", err, header)
			}
			uploaded++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"id": "doc-1"}})
		case r.URL.Path == "/api/ragent/knowledge-base/docs/doc-1/chunk" && r.Method == http.MethodPost:
			chunked++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": nil})
		case r.URL.Path == "/api/ragent/knowledge-base/docs/doc-1" && r.Method == http.MethodGet:
			statusChecks++
			status := "running"
			if statusChecks > 1 {
				status = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"id": "doc-1", "status": status, "chunkCount": 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	dataset := &Dataset{KnowledgeBases: []KnowledgeBase{{Ref: "kb", Name: "知识库", CollectionName: "collection", DocumentsDir: dir, IngestionSpec: `{"parseProfile":"fast","maxChars":256}`}}}
	if err := initDocuments(context.Background(), c, dataset, false, time.Second, time.Millisecond); err != nil {
		t.Fatalf("initDocuments: %v", err)
	}
	if uploaded != 1 || chunked != 1 || statusChecks < 2 {
		t.Fatalf("expected upload/chunk/status flow, uploaded=%d chunked=%d statusChecks=%d", uploaded, chunked, statusChecks)
	}
}

func TestVerifyRejectsDocumentsOutsideDataset(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "expected.md"), "expected")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ragent/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"token": "t", "role": "admin"}})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{{"id": "kb-1", "collectionName": "collection"}}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"docName": "unexpected.md", "status": "success", "chunkCount": 1}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	dataset := &Dataset{KnowledgeBases: []KnowledgeBase{{
		Ref: "kb", Name: "知识库", CollectionName: "collection", DocumentsDir: dir,
	}}}
	if err := verify(context.Background(), c, dataset); err == nil || !strings.Contains(err.Error(), "当前智能体类型外") {
		t.Fatalf("expected unexpected document error, got: %v", err)
	}
}

func TestVerifyRejectsIntentWithoutExpectedCollection(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "expected.md"), "expected")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ragent/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"token": "t", "role": "admin"}})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{{"id": "kb-1", "collectionName": "collection"}}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"docName": "expected.md", "status": "success", "chunkCount": 1}}, "pages": 1,
			}})
		case r.URL.Path == "/api/ragent/intent-tree/trees" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{{
				"intentCode": "intent-1", "collectionNames": []string{"other-collection"},
			}}})
		case r.URL.Path == "/api/ragent/sample-questions" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"records": []any{}, "pages": 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	dataset := &Dataset{
		KnowledgeBases: []KnowledgeBase{{Ref: "kb", Name: "知识库", CollectionName: "collection", DocumentsDir: dir}},
		Intents:        []Intent{{Code: "intent-1", Name: "意图", KnowledgeBaseRef: "kb"}},
	}
	if err := verify(context.Background(), c, dataset); err == nil || !strings.Contains(err.Error(), "预期知识库") {
		t.Fatalf("expected intent collection binding error, got: %v", err)
	}
}

func TestInitDocumentsReplacesExistingDocumentByDefault(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "会员手册.md")
	writeTestFile(t, file, "新版会员规则")
	var deleted, uploaded int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ragent/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"token": "t", "role": "admin"}})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{{"id": "kb-1", "collectionName": "collection"}}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"id": "old-1", "docName": "会员手册.md", "status": "success", "chunkCount": 1}},
			}})
		case r.URL.Path == "/api/ragent/knowledge-base/docs/old-1" && r.Method == http.MethodDelete:
			deleted++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0"})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs/upload" && r.Method == http.MethodPost:
			uploaded++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"id": "new-1"}})
		case r.URL.Path == "/api/ragent/knowledge-base/docs/new-1/chunk" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0"})
		case r.URL.Path == "/api/ragent/knowledge-base/docs/new-1" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{"status": "success", "chunkCount": 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c, err := newClient(Options{BaseURL: server.URL, AdminUsername: "admin", AdminPassword: "admin"})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	dataset := &Dataset{KnowledgeBases: []KnowledgeBase{{
		Ref: "kb", Name: "知识库", CollectionName: "collection", DocumentsDir: dir,
	}}}
	if err := initDocuments(context.Background(), c, dataset, false, time.Second, time.Millisecond); err != nil {
		t.Fatalf("initDocuments: %v", err)
	}
	if deleted != 1 || uploaded != 1 {
		t.Fatalf("expected replacement, deleted=%d uploaded=%d", deleted, uploaded)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
