package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/knowledge/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUploadURLDocumentFetchesAndStoresRemoteFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	remoteBody := []byte("# 远端文档\n会员权益说明")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(remoteBody)
	}))
	defer remote.Close()

	svc := service.NewDocumentService(
		repo.NewKnowledgeDocumentRepo(gdb),
		repo.NewKnowledgeChunkRepo(gdb),
		repo.NewKnowledgeBaseRepo(gdb),
		gdb,
		nil,
		nil,
		nil,
	)
	fileStore := NewFileStore()
	h := NewDocumentHandler(svc, fileStore)
	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("sourceType", "url"); err != nil {
		t.Fatalf("write sourceType: %v", err)
	}
	if err := writer.WriteField("sourceLocation", remote.URL+"/guide.md"); err != nil {
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
			ID       string `json:"id"`
			DocName  string `json:"docName"`
			FileType string `json:"fileType"`
			FileSize int64  `json:"fileSize"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if resp.Code != "0" {
		t.Fatalf("expected success, got %s", w.Body.String())
	}
	if resp.Data.DocName != "guide.md" || resp.Data.FileType != "md" || resp.Data.FileSize != int64(len(remoteBody)) {
		t.Fatalf("expected remote file metadata, got %s", w.Body.String())
	}
	stored, err := fileStore.ReadWithCollection(context.Background(), kb.CollectionName, resp.Data.ID)
	if err != nil {
		t.Fatalf("read stored remote file: %v", err)
	}
	if string(stored) != string(remoteBody) {
		t.Fatalf("expected stored remote body %q, got %q", string(remoteBody), string(stored))
	}
}

func TestUploadPipelineDocumentRequiresPipelineID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
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
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "doc.md")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("# 文档")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.WriteField("sourceType", "file"); err != nil {
		t.Fatalf("write sourceType: %v", err)
	}
	if err := writer.WriteField("processMode", "pipeline"); err != nil {
		t.Fatalf("write processMode: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/"+kb.ID+"/docs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] == "0" || !strings.Contains(w.Body.String(), "使用Pipeline模式时，必须指定Pipeline ID") {
		t.Fatalf("expected missing pipeline id failure, got %s", w.Body.String())
	}
	var count int64
	if err := gdb.Model(&model.KnowledgeDocument{}).Where("kb_id = ? AND deleted = 0", kb.ID).Count(&count).Error; err != nil {
		t.Fatalf("count docs: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected failed upload not to create document, got %d", count)
	}
}

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

func TestUpdateChunkReturnsJavaStyleEmptySuccess(t *testing.T) {
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
	chunk := &model.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "旧内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
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
	r.PUT("/api/ragent/knowledge-base/docs/:docId/chunks/:chunkId", h.UpdateChunk)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/ragent/knowledge-base/docs/"+doc.ID+"/chunks/"+chunk.ID, strings.NewReader(`{"content":"新内容"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] != "0" || resp["data"] != nil {
		t.Fatalf("expected empty success response, got %s", w.Body.String())
	}
	var stored model.KnowledgeChunk
	if err := gdb.First(&stored, "id = ?", chunk.ID).Error; err != nil {
		t.Fatalf("load chunk: %v", err)
	}
	if stored.Content != "新内容" {
		t.Fatalf("expected updated content, got %+v", stored)
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
	olderKB := &model.KnowledgeBase{Name: "旧知识库", EmbeddingModel: "emb", CollectionName: "old_kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(olderKB).Error; err != nil {
		t.Fatalf("seed older kb: %v", err)
	}
	olderDoc := &model.KnowledgeDocument{KbID: olderKB.ID, DocName: "旧会员Agent能力.md", FileURL: "upload://old-doc.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	newerDoc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent能力.md", FileURL: "upload://doc.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(olderDoc).Error; err != nil {
		t.Fatalf("seed older doc: %v", err)
	}
	if err := gdb.Create(newerDoc).Error; err != nil {
		t.Fatalf("seed newer doc: %v", err)
	}
	if err := gdb.Model(&model.KnowledgeDocument{}).Where("id = ?", olderDoc.ID).Updates(map[string]any{"update_time": time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatalf("update older doc time: %v", err)
	}
	if err := gdb.Model(&model.KnowledgeDocument{}).Where("id = ?", newerDoc.ID).Updates(map[string]any{"update_time": time.Now()}).Error; err != nil {
		t.Fatalf("update newer doc time: %v", err)
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
	if len(data) != 2 {
		t.Fatalf("expected two results, got %s", w.Body.String())
	}
	first, ok := data[0].(map[string]any)
	if !ok || first["docName"] != "会员Agent能力.md" || first["kbName"] != "会员知识库" {
		t.Fatalf("unexpected first search item: %s", w.Body.String())
	}
	second, ok := data[1].(map[string]any)
	if !ok || second["docName"] != "旧会员Agent能力.md" || second["kbName"] != "旧知识库" {
		t.Fatalf("unexpected second search item: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/docs/search", nil)
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("expected blank keyword to return empty array, got %s", w.Body.String())
	}
}

func TestListDocsSupportsStatusAndKeywordFilters(t *testing.T) {
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
	matched := &model.KnowledgeDocument{KbID: kb.ID, DocName: "会员权益说明.md", FileURL: "upload://match.md", FileType: "md", Status: "success", Enabled: 1, CreatedBy: "tester"}
	mismatchedStatus := &model.KnowledgeDocument{KbID: kb.ID, DocName: "会员权益草稿.md", FileURL: "upload://draft.md", FileType: "md", Status: "pending", Enabled: 1, CreatedBy: "tester"}
	mismatchedKeyword := &model.KnowledgeDocument{KbID: kb.ID, DocName: "理赔说明.md", FileURL: "upload://claim.md", FileType: "md", Status: "success", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(matched).Error; err != nil {
		t.Fatalf("seed matched doc: %v", err)
	}
	if err := gdb.Create(mismatchedStatus).Error; err != nil {
		t.Fatalf("seed pending doc: %v", err)
	}
	if err := gdb.Create(mismatchedKeyword).Error; err != nil {
		t.Fatalf("seed keyword doc: %v", err)
	}

	svc := service.NewDocumentService(repo.NewKnowledgeDocumentRepo(gdb), repo.NewKnowledgeChunkRepo(gdb), repo.NewKnowledgeBaseRepo(gdb), gdb, nil, nil, nil)
	h := NewDocumentHandler(svc, NewFileStore())
	r := gin.New()
	r.GET("/api/ragent/knowledge-base/:id/docs", h.ListDocs)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/"+kb.ID+"/docs?status=success&keyword=权益", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	page, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected paged response, got %T: %s", resp["data"], w.Body.String())
	}
	items, ok := page["records"].([]any)
	if !ok {
		t.Fatalf("expected records array, got %T: %s", page["records"], w.Body.String())
	}
	if len(items) != 1 {
		t.Fatalf("expected one filtered document, got %s", w.Body.String())
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["docName"] != "会员权益说明.md" || item["status"] != "success" {
		t.Fatalf("unexpected filtered doc: %s", w.Body.String())
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

func TestBatchToggleChunksMissingBodyReturnsBusinessValidation(t *testing.T) {
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
	r.PATCH("/api/ragent/knowledge-base/docs/:docId/chunks/batch-enable", h.BatchToggleChunks)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/ragent/knowledge-base/docs/"+doc.ID+"/chunks/batch-enable?value=false", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"B000001"`) || !strings.Contains(body, "请指定需要操作的 Chunk") {
		t.Fatalf("expected business validation error, got %s", body)
	}
	if strings.Contains(body, "参数校验失败") {
		t.Fatalf("expected handler to allow missing body and delegate to service, got %s", body)
	}
}

func TestDocumentFileReadsStoredObjectFromKnowledgeCollection(t *testing.T) {
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
	doc := &model.KnowledgeDocument{KbID: kb.ID, DocName: "会员说明.md", FileURL: "upload://会员说明.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	fileStore := NewFileStore()
	if err := fileStore.PutWithCollection(context.Background(), kb.CollectionName, doc.ID, doc.DocName, []byte("# 原始文件\n会员权益")); err != nil {
		t.Fatalf("put collection file: %v", err)
	}
	svc := service.NewDocumentService(repo.NewKnowledgeDocumentRepo(gdb), repo.NewKnowledgeChunkRepo(gdb), repo.NewKnowledgeBaseRepo(gdb), gdb, nil, nil, nil)
	h := NewDocumentHandler(svc, fileStore)
	r := gin.New()
	r.GET("/api/ragent/knowledge-base/docs/:docId/file", h.File)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/docs/"+doc.ID+"/file", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "# 原始文件\n会员权益" {
		t.Fatalf("expected stored original file, got %q", body)
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "text/markdown") {
		t.Fatalf("expected markdown content type, got %q", contentType)
	}
}

func TestDetectMIMEAlignsJavaDocumentFileTypes(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "csv", file: "会员数据.csv", want: "text/csv"},
		{name: "xls", file: "会员数据.xls", want: "application/vnd.ms-excel"},
		{name: "xlsx", file: "会员数据.xlsx", want: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectMIME(tt.file); !strings.Contains(got, tt.want) {
				t.Fatalf("expected %s content type to contain %q, got %q", tt.file, tt.want, got)
			}
		})
	}
}
