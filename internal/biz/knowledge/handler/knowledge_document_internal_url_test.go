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
	docs      []crawler.Document
	docsByURL map[string][]crawler.Document
	err       error
}

func (f fakeInternalURLFetcher) FetchDocuments(_ context.Context, location string) ([]crawler.Document, error) {
	if f.docsByURL != nil {
		if docs, ok := f.docsByURL[location]; ok {
			return docs, f.err
		}
	}
	return f.docs, f.err
}

func postInternalURLUpload(t *testing.T, r http.Handler, kbID, sourceLocation string, scheduleEnabled bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("sourceType", "internal_url"); err != nil {
		t.Fatalf("write sourceType: %v", err)
	}
	if err := writer.WriteField("sourceLocation", sourceLocation); err != nil {
		t.Fatalf("write sourceLocation: %v", err)
	}
	if scheduleEnabled {
		if err := writer.WriteField("scheduleEnabled", "1"); err != nil {
			t.Fatalf("write scheduleEnabled: %v", err)
		}
		if err := writer.WriteField("scheduleCron", "@every 1h"); err != nil {
			t.Fatalf("write scheduleCron: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/"+kbID+"/docs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)
	return w
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

func TestUploadInternalURLKeepsEmptyFolderNodeWithoutSourceFile(t *testing.T) {
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

	const (
		rootURL   = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231"
		folderURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=folder"
		leafURL   = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=leaf"
	)
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
			{Meta: crawler.DocumentMeta{ID: "368231", Title: "根文档.md", URL: rootURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# 根文档")},
			{Meta: crawler.DocumentMeta{ID: "folder", Title: "目录节点.md", URL: folderURL, MimeType: "text/markdown", SourceName: "geelib", Extra: map[string]string{"parent_url": rootURL, "has_children": "true"}}, Content: nil},
			{Meta: crawler.DocumentMeta{ID: "leaf", Title: "正文文档.md", URL: leafURL, MimeType: "text/markdown", SourceName: "geelib", Extra: map[string]string{"parent_url": folderURL}}, Content: []byte("# 正文")},
		},
	})
	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	w := postInternalURLUpload(t, r, kb.ID, rootURL, false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var folder model.KnowledgeDocument
	if err := gdb.First(&folder, "file_url = ?", folderURL).Error; err != nil {
		t.Fatalf("find folder node: %v", err)
	}
	if folder.SourceNodeType != "folder" || folder.Status != "success" || folder.ChunkCount != 0 {
		t.Fatalf("expected folder node to stay success without chunks, got %+v", folder)
	}
	if _, ok, err := fileStore.GetWithCollection(context.Background(), kb.CollectionName, folder.ID); err != nil || ok {
		t.Fatalf("expected no empty source file for folder, ok=%v err=%v", ok, err)
	}
	var chunkCount int64
	if err := gdb.Model(&model.KnowledgeChunk{}).Where("doc_id = ?", folder.ID).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count folder chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("expected no folder chunks, got %d", chunkCount)
	}
}

func TestUploadInternalURLParentReusesExistingChildDocuments(t *testing.T) {
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

	const (
		sURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=s"
		aURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=a"
		bURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=b"
		cURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=c"
		dURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=d"
		eURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=e"
		fURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=f"
	)

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
		docsByURL: map[string][]crawler.Document{
			aURL: {
				{Meta: crawler.DocumentMeta{ID: "a", Title: "A.md", URL: aURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# A")},
				{Meta: crawler.DocumentMeta{ID: "b", Title: "B.md", URL: bURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# B")},
				{Meta: crawler.DocumentMeta{ID: "c", Title: "C.md", URL: cURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# C")},
			},
			sURL: {
				{Meta: crawler.DocumentMeta{ID: "s", Title: "S.md", URL: sURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# S")},
				{Meta: crawler.DocumentMeta{ID: "a", Title: "A.md", URL: aURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# A")},
				{Meta: crawler.DocumentMeta{ID: "b", Title: "B.md", URL: bURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# B")},
				{Meta: crawler.DocumentMeta{ID: "c", Title: "C.md", URL: cURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# C")},
				{Meta: crawler.DocumentMeta{ID: "d", Title: "D.md", URL: dURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# D")},
				{Meta: crawler.DocumentMeta{ID: "e", Title: "E.md", URL: eURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# E")},
				{Meta: crawler.DocumentMeta{ID: "f", Title: "F.md", URL: fURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# F")},
			},
		},
	})
	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	first := postInternalURLUpload(t, r, kb.ID, aURL, false)
	if first.Code != http.StatusOK {
		t.Fatalf("first upload expected 200, got %d", first.Code)
	}
	var oldA model.KnowledgeDocument
	if err := gdb.First(&oldA, "file_url = ?", aURL).Error; err != nil {
		t.Fatalf("find uploaded a: %v", err)
	}

	second := postInternalURLUpload(t, r, kb.ID, sURL, false)
	if second.Code != http.StatusOK {
		t.Fatalf("second upload expected 200, got %d", second.Code)
	}

	var count int64
	if err := gdb.Model(&model.KnowledgeDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 7 {
		t.Fatalf("expected parent upload to reuse existing a/b/c and keep 7 docs total, got %d", count)
	}
	var currentA model.KnowledgeDocument
	if err := gdb.First(&currentA, "file_url = ?", aURL).Error; err != nil {
		t.Fatalf("find current a: %v", err)
	}
	if currentA.ID != oldA.ID {
		t.Fatalf("expected existing a doc to be reused, old=%s current=%s", oldA.ID, currentA.ID)
	}
	if currentA.SourceRootKey != "internal_url:geelib:5:s" {
		t.Fatalf("expected a to be reassigned under s root, got source_root_key=%q", currentA.SourceRootKey)
	}
}

func TestUploadInternalURLParentScheduleTakesOverChildSchedule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.KnowledgeBase{}, &model.KnowledgeDocument{}, &model.KnowledgeChunk{}, &model.KnowledgeDocumentSchedule{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &model.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	const (
		sURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=s"
		aURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=a"
		bURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=b"
		cURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=c"
		dURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=d"
		eURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=e"
		fURL = "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=f"
	)

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
	svc.SetScheduleRepo(repo.NewKnowledgeDocumentScheduleRepo(gdb))
	h := NewDocumentHandler(svc, fileStore)
	h.SetInternalURLFetcher(fakeInternalURLFetcher{
		docsByURL: map[string][]crawler.Document{
			aURL: {
				{Meta: crawler.DocumentMeta{ID: "a", Title: "A.md", URL: aURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# A")},
				{Meta: crawler.DocumentMeta{ID: "b", Title: "B.md", URL: bURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# B")},
				{Meta: crawler.DocumentMeta{ID: "c", Title: "C.md", URL: cURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# C")},
			},
			sURL: {
				{Meta: crawler.DocumentMeta{ID: "s", Title: "S.md", URL: sURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# S")},
				{Meta: crawler.DocumentMeta{ID: "a", Title: "A.md", URL: aURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# A")},
				{Meta: crawler.DocumentMeta{ID: "b", Title: "B.md", URL: bURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# B")},
				{Meta: crawler.DocumentMeta{ID: "c", Title: "C.md", URL: cURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# C")},
				{Meta: crawler.DocumentMeta{ID: "d", Title: "D.md", URL: dURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# D")},
				{Meta: crawler.DocumentMeta{ID: "e", Title: "E.md", URL: eURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# E")},
				{Meta: crawler.DocumentMeta{ID: "f", Title: "F.md", URL: fURL, MimeType: "text/markdown", SourceName: "geelib"}, Content: []byte("# F")},
			},
		},
	})
	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	first := postInternalURLUpload(t, r, kb.ID, aURL, true)
	if first.Code != http.StatusOK {
		t.Fatalf("first upload expected 200, got %d", first.Code)
	}
	var oldA model.KnowledgeDocument
	if err := gdb.First(&oldA, "file_url = ?", aURL).Error; err != nil {
		t.Fatalf("find uploaded a: %v", err)
	}

	second := postInternalURLUpload(t, r, kb.ID, sURL, true)
	if second.Code != http.StatusOK {
		t.Fatalf("second upload expected 200, got %d", second.Code)
	}

	var docCount int64
	if err := gdb.Model(&model.KnowledgeDocument{}).Count(&docCount).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 7 {
		t.Fatalf("expected 7 docs after parent takeover, got %d", docCount)
	}
	var scheduleCount int64
	if err := gdb.Model(&model.KnowledgeDocumentSchedule{}).Count(&scheduleCount).Error; err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if scheduleCount != 1 {
		t.Fatalf("expected only parent schedule to remain, got %d", scheduleCount)
	}
	var childScheduleCount int64
	if err := gdb.Model(&model.KnowledgeDocumentSchedule{}).Where("doc_id = ?", oldA.ID).Count(&childScheduleCount).Error; err != nil {
		t.Fatalf("count child schedules: %v", err)
	}
	if childScheduleCount != 0 {
		t.Fatalf("expected old a schedule to be removed, got %d", childScheduleCount)
	}
	var parentSchedule model.KnowledgeDocumentSchedule
	if err := gdb.First(&parentSchedule).Error; err != nil {
		t.Fatalf("find parent schedule: %v", err)
	}
	var parentDoc model.KnowledgeDocument
	if err := gdb.First(&parentDoc, "id = ?", parentSchedule.DocID).Error; err != nil {
		t.Fatalf("find parent schedule doc: %v", err)
	}
	if parentDoc.FileURL != sURL || parentDoc.ScheduleEnabled != 1 {
		t.Fatalf("expected s to own schedule, got doc=%+v schedule=%+v", parentDoc, parentSchedule)
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
