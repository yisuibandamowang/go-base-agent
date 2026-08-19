package initialize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if len(dataset.Intents) != 2 {
		t.Fatalf("expected 2 intents, got %d", len(dataset.Intents))
	}
	if dataset.Intents[0].KnowledgeBaseRef != "group" {
		t.Fatalf("unexpected kb ref: %s", dataset.Intents[0].KnowledgeBaseRef)
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
