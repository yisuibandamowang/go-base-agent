package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/knowledge/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreateChunkCreatesStoredChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}, &model.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "doc.md", FileURL: "upload://doc.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	svc := service.NewDocumentService(
		repo.NewKnowledgeDocumentRepo(gdb),
		repo.NewKnowledgeChunkRepo(gdb),
		repo.NewKnowledgeBaseRepo(gdb),
		gdb,
		nil,
		nil,
		nil,
	)
	h := NewDocumentHandler(svc, NewFileStore())
	r := gin.New()
	r.POST("/api/ragent/knowledge-base/docs/:docId/chunks", h.CreateChunk)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/docs/"+doc.ID+"/chunks", strings.NewReader(`{"content":"手工补充分块"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"content":"手工补充分块"`) || strings.Contains(body, "stub") {
		t.Fatalf("expected created chunk response, got %s", body)
	}
	var count int64
	if err := gdb.Model(&model.KnowledgeChunk{}).Where("doc_id = ? AND deleted = 0", doc.ID).Count(&count).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one stored chunk, got %d", count)
	}
}

func TestSearchDocsReturnsJavaStyleArrayWithKBName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}, &model.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "会员知识库", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent能力.md", FileURL: "upload://doc.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	svc := service.NewDocumentService(repo.NewKnowledgeDocumentRepo(gdb), repo.NewKnowledgeChunkRepo(gdb), repo.NewKnowledgeBaseRepo(gdb), gdb, nil, nil, nil)
	h := NewDocumentHandler(svc, NewFileStore())
	r := gin.New()
	r.GET("/api/ragent/knowledge-base/docs/search", h.SearchDocs)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/docs/search?keyword=Agent&limit=8", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected Java-style array data, got %T: %s", resp["data"], w.Body.String())
	}
	if len(data) != 1 {
		t.Fatalf("expected one result, got %s", w.Body.String())
	}
	item, ok := data[0].(map[string]any)
	if !ok || item["docName"] != "会员Agent能力.md" || item["kbName"] != "会员知识库" {
		t.Fatalf("unexpected search item: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/docs/search", nil)
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("expected blank keyword to return empty array, got %s", w.Body.String())
	}
}

func TestToggleDocumentAcceptsJavaValueQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}, &model.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "doc.md", FileURL: "upload://doc.md", FileType: "md", Status: "success", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	svc := service.NewDocumentService(repo.NewKnowledgeDocumentRepo(gdb), repo.NewKnowledgeChunkRepo(gdb), repo.NewKnowledgeBaseRepo(gdb), gdb, nil, nil, nil)
	h := NewDocumentHandler(svc, NewFileStore())
	r := gin.New()
	r.PATCH("/api/ragent/knowledge-base/docs/:docId/enable", h.ToggleDoc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/ragent/knowledge-base/docs/"+doc.ID+"/enable?value=false", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"0"`) {
		t.Fatalf("expected success, got %d %s", w.Code, w.Body.String())
	}
	var enabled int16
	if err := gdb.Model(&model.KnowledgeDocument{}).Select("enabled").Where("id = ?", doc.ID).Scan(&enabled).Error; err != nil {
		t.Fatalf("load doc enabled: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("expected document disabled by query value, got %d", enabled)
	}
}

func TestBatchToggleChunksAcceptsJavaChunkIDsAndValueQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}, &model.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	doc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "doc.md", FileURL: "upload://doc.md", FileType: "md", Status: "success", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	chunk := &model.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "chunk", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	svc := service.NewDocumentService(repo.NewKnowledgeDocumentRepo(gdb), repo.NewKnowledgeChunkRepo(gdb), repo.NewKnowledgeBaseRepo(gdb), gdb, nil, nil, nil)
	h := NewDocumentHandler(svc, NewFileStore())
	r := gin.New()
	r.PATCH("/api/ragent/knowledge-base/docs/:docId/chunks/batch-enable", h.BatchToggleChunks)

	w := httptest.NewRecorder()
	body := strings.NewReader(`{"chunkIds":["` + chunk.ID + `"]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/ragent/knowledge-base/docs/"+doc.ID+"/chunks/batch-enable?value=false", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"0"`) {
		t.Fatalf("expected success, got %d %s", w.Code, w.Body.String())
	}
	var enabled int16
	if err := gdb.Model(&model.KnowledgeChunk{}).Select("enabled").Where("id = ?", chunk.ID).Scan(&enabled).Error; err != nil {
		t.Fatalf("load chunk enabled: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("expected chunk disabled by Java-compatible payload, got %d", enabled)
	}
}
