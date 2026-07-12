package handler

import (
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
