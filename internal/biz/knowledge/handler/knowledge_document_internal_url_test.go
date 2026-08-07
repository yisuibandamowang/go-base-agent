package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/biz/crawler"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	knowledgeService "go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/biz/rag"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeInternalURLFetcher struct {
	docs []crawler.Document
	err  error
}

func (f fakeInternalURLFetcher) FetchDocuments(context.Context, string) ([]crawler.Document, error) {
	return f.docs, f.err
}

func TestUploadInternalURLDocumentCreatesPendingDocsWithoutAutoChunk(t *testing.T) {
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

	fileStore := NewFileStore()
	svc := knowledgeService.NewDocumentService(
		repo.NewKnowledgeDocumentRepo(gdb),
		repo.NewKnowledgeChunkRepo(gdb),
		repo.NewKnowledgeBaseRepo(gdb),
		gdb,
		knowledgeServiceTestEmbeddingService{},
		&knowledgeServiceTestVectorStore{},
		fileStore,
	)
	h := NewDocumentHandler(svc, fileStore)
	h.SetInternalURLFetcher(fakeInternalURLFetcher{
		docs: []crawler.Document{
			{Meta: crawler.DocumentMeta{ID: "425274", Title: "根文档.md", URL: "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274", MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# 根文档\n\n根正文")},
			{Meta: crawler.DocumentMeta{ID: "111", Title: "子文档.md", URL: "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=111", MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# 子文档\n\n子正文")},
		},
	})

	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("sourceType", "internal_url"); err != nil {
		t.Fatalf("write sourceType: %v", err)
	}
	if err := writer.WriteField("sourceLocation", "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274"); err != nil {
		t.Fatalf("write sourceLocation: %v", err)
	}
	if err := writer.WriteField("chunkStrategy", "fixed_size"); err != nil {
		t.Fatalf("write chunkStrategy: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/"+kb.ID+"/docs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Total     int `json:"total"`
			Success   int `json:"success"`
			Failed    int `json:"failed"`
			Documents []struct {
				ID             string `json:"id"`
				DocName        string `json:"docName"`
				SourceLocation string `json:"sourceLocation"`
				Status         string `json:"status"`
				ChunkCount     int    `json:"chunkCount"`
			} `json:"documents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != "0" {
		t.Fatalf("expected success, got %s", w.Body.String())
	}
	if resp.Data.Total != 2 || resp.Data.Success != 2 || resp.Data.Failed != 0 {
		t.Fatalf("unexpected summary: %s", w.Body.String())
	}
	if len(resp.Data.Documents) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(resp.Data.Documents))
	}
	if resp.Data.Documents[0].Status != "pending" || resp.Data.Documents[0].ChunkCount != 0 {
		t.Fatalf("expected pending document, got %s", w.Body.String())
	}
	if resp.Data.Documents[0].SourceLocation != "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=425274" {
		t.Fatalf("expected root document source location to stay on root url, got %q", resp.Data.Documents[0].SourceLocation)
	}
	if resp.Data.Documents[1].SourceLocation != "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=111" {
		t.Fatalf("expected child document source location to stay on child url, got %q", resp.Data.Documents[1].SourceLocation)
	}
	var count int64
	if err := gdb.Model(&model.KnowledgeDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 created documents, got %d", count)
	}
	if err := gdb.Model(&model.KnowledgeChunk{}).Count(&count).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no auto chunks, got %d", count)
	}
}

func TestUploadInternalURLSingleDocumentRemainsPending(t *testing.T) {
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

	fileStore := NewFileStore()
	svc := knowledgeService.NewDocumentService(
		repo.NewKnowledgeDocumentRepo(gdb),
		repo.NewKnowledgeChunkRepo(gdb),
		repo.NewKnowledgeBaseRepo(gdb),
		gdb,
		knowledgeServiceTestEmbeddingService{},
		&knowledgeServiceTestVectorStore{},
		fileStore,
	)
	h := NewDocumentHandler(svc, fileStore)
	h.SetInternalURLFetcher(fakeInternalURLFetcher{
		docs: []crawler.Document{
			{Meta: crawler.DocumentMeta{ID: "437090", Title: "会员中台游客模式体系架构文档.md", URL: "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=437090", MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# 会员中台游客模式体系架构文档\n\n正文")},
		},
	})

	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("sourceType", "internal_url"); err != nil {
		t.Fatalf("write sourceType: %v", err)
	}
	if err := writer.WriteField("sourceLocation", "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=437090"); err != nil {
		t.Fatalf("write sourceLocation: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/"+kb.ID+"/docs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Total     int `json:"total"`
			Success   int `json:"success"`
			Failed    int `json:"failed"`
			Documents []struct {
				DocName    string `json:"docName"`
				Status     string `json:"status"`
				ChunkCount int    `json:"chunkCount"`
			} `json:"documents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != "0" {
		t.Fatalf("expected success, got %s", w.Body.String())
	}
	if resp.Data.Total != 1 || resp.Data.Success != 1 || resp.Data.Failed != 0 {
		t.Fatalf("unexpected summary: %s", w.Body.String())
	}
	if len(resp.Data.Documents) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.Data.Documents))
	}
	if resp.Data.Documents[0].Status != "pending" || resp.Data.Documents[0].ChunkCount != 0 {
		t.Fatalf("expected pending document, got %s", w.Body.String())
	}
	var count int64
	if err := gdb.Model(&model.KnowledgeChunk{}).Count(&count).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no auto chunks, got %d", count)
	}
}

type knowledgeServiceTestEmbeddingService struct{}

func (knowledgeServiceTestEmbeddingService) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (knowledgeServiceTestEmbeddingService) EmbedWithModel(context.Context, string, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (knowledgeServiceTestEmbeddingService) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (knowledgeServiceTestEmbeddingService) EmbedBatchWithModel(context.Context, []string, string) ([][]float32, error) {
	return nil, nil
}

func (knowledgeServiceTestEmbeddingService) Dimension() int {
	return 2
}

type knowledgeServiceTestVectorStore struct{}

func (*knowledgeServiceTestVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (*knowledgeServiceTestVectorStore) IndexDocumentChunks(context.Context, string, string, []rag.VectorChunk) error {
	return nil
}

func (*knowledgeServiceTestVectorStore) UpdateChunk(context.Context, string, string, rag.VectorChunk) error {
	return nil
}

func (*knowledgeServiceTestVectorStore) DeleteChunkByID(context.Context, string, string) error {
	return nil
}

func (*knowledgeServiceTestVectorStore) DeleteChunksByIDs(context.Context, string, []string) error {
	return nil
}
