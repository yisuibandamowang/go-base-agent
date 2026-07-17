package handler

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/ratelimit"

	"github.com/gin-gonic/gin"
)

// DocumentHandler 文档管理 HTTP 处理层。
type DocumentHandler struct {
	svc           *service.DocumentService
	fileStore     *FileStore
	uploadLimiter uploadLimiter
	uploadMaxWait time.Duration
}

type uploadLimiter interface {
	Acquire(ctx context.Context, req ratelimit.AcquireRequest) error
}

// NewDocumentHandler 创建 DocumentHandler。
func NewDocumentHandler(svc *service.DocumentService, fs *FileStore) *DocumentHandler {
	return &DocumentHandler{svc: svc, fileStore: fs}
}

// SetUploadLimiter 为文档上传接口设置并发限流器。
func (h *DocumentHandler) SetUploadLimiter(limiter uploadLimiter, maxWait time.Duration) {
	h.uploadLimiter = limiter
	h.uploadMaxWait = maxWait
}

// Upload POST /knowledge-base/:id/docs/upload
func (h *DocumentHandler) Upload(c *gin.Context) {
	if h.uploadLimiter == nil {
		h.uploadDocument(c)
		return
	}
	var once sync.Once
	done := make(chan struct{})
	finish := func() {
		once.Do(func() {
			close(done)
		})
	}
	err := h.uploadLimiter.Acquire(c.Request.Context(), ratelimit.AcquireRequest{
		MaxWait: h.uploadMaxWait,
		OnAcquire: func() {
			defer finish()
			h.uploadDocument(c)
		},
		OnTimeout: func() {
			defer finish()
			c.JSON(http.StatusOK, convention.Failure("A000001", "文档上传请求过于频繁，请稍后再试"))
		},
	})
	if err != nil {
		select {
		case <-done:
		case <-c.Request.Context().Done():
		}
		return
	}
	<-done
}

func (h *DocumentHandler) uploadDocument(c *gin.Context) {
	kbID := c.Param("id")

	// Parse multipart form (max 50MB)
	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("解析上传表单失败: %v", err)))
		return
	}

	form := c.Request.MultipartForm
	var req dto.CreateDocumentReq
	var file multipart.File
	var header *multipart.FileHeader

	// File upload
	if files := form.File["file"]; len(files) > 0 {
		header = files[0]
		f, err := header.Open()
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("读取文件失败: %v", err)))
			return
		}
		file = f
		defer file.Close()
		req.DocName = header.Filename
		req.FileSize = header.Size
		req.FileType = strings.TrimPrefix(filepath.Ext(header.Filename), ".")
		req.FileURL = fmt.Sprintf("upload://%s", header.Filename)
	}

	// Source type (file or url)
	if vals, ok := form.Value["sourceType"]; ok && len(vals) > 0 {
		req.SourceType = vals[0]
	}

	// Source location for URL type
	if vals, ok := form.Value["sourceLocation"]; ok && len(vals) > 0 {
		req.SourceLocation = vals[0]
		req.FileURL = vals[0]
		if req.DocName == "" {
			req.DocName = vals[0]
		}
	}

	if vals, ok := form.Value["scheduleEnabled"]; ok && len(vals) > 0 {
		req.ScheduleEnabled = parseInt16(vals[0])
	}
	if vals, ok := form.Value["scheduleCron"]; ok && len(vals) > 0 {
		req.ScheduleCron = vals[0]
	}

	// Process mode & chunk strategy
	if vals, ok := form.Value["processMode"]; ok && len(vals) > 0 {
		if vals[0] == "pipeline" {
			if pvals, ok := form.Value["pipelineId"]; ok && len(pvals) > 0 {
				req.ChunkStrategy = "pipeline"
				req.PipelineID = pvals[0]
				req.ChunkConfig = fmt.Sprintf(`{"pipelineId":"%s"}`, pvals[0])
			}
		}
	}
	if vals, ok := form.Value["chunkStrategy"]; ok && len(vals) > 0 {
		req.ChunkStrategy = vals[0]
	}
	if vals, ok := form.Value["chunkConfig"]; ok && len(vals) > 0 {
		req.ChunkConfig = vals[0]
	}

	if req.DocName == "" {
		c.JSON(http.StatusOK, convention.Failure("A000001", "缺少文件名"))
		return
	}
	if req.FileType == "" {
		req.FileType = "unknown"
	}

	_ = file // processed in pipeline

	resp, err := h.svc.CreateDocument(c.Request.Context(), kbID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}

	// Save file content for later retrieval
	if file != nil {
		data, _ := io.ReadAll(file)
		if err := h.fileStore.PutWithContext(c.Request.Context(), resp.ID, header.Filename, data); err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", "保存上传文件失败: "+err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, convention.Success(resp))
}

func parseInt16(value string) int16 {
	switch strings.TrimSpace(value) {
	case "1", "true", "TRUE", "True":
		return 1
	default:
		return 0
	}
}

// ListDocs GET /knowledge-base/:id/docs
func (h *DocumentHandler) ListDocs(c *gin.Context) {
	kbID := c.Param("id")
	page, size := pagination(c)
	records, total, err := h.svc.ListDocumentsByKB(c.Request.Context(), kbID, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// GetDoc GET /knowledge-base/docs/:docId
func (h *DocumentHandler) GetDoc(c *gin.Context) {
	id := c.Param("docId")
	resp, err := h.svc.GetDocument(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateDoc PUT /knowledge-base/docs/:docId
func (h *DocumentHandler) UpdateDoc(c *gin.Context) {
	id := c.Param("docId")
	var req dto.UpdateDocumentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	resp, err := h.svc.UpdateDocument(c.Request.Context(), id, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// DeleteDoc DELETE /knowledge-base/docs/:docId
func (h *DocumentHandler) DeleteDoc(c *gin.Context) {
	id := c.Param("docId")
	if err := h.svc.DeleteDocument(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// SearchDocs GET /knowledge-base/docs/search
func (h *DocumentHandler) SearchDocs(c *gin.Context) {
	keyword := c.Query("keyword")
	page, size := pagination(c)
	records, total, err := h.svc.SearchDocuments(c.Request.Context(), keyword, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// ToggleDoc PATCH /knowledge-base/docs/:docId/enable
func (h *DocumentHandler) ToggleDoc(c *gin.Context) {
	id := c.Param("docId")
	var req dto.ToggleChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	if err := h.svc.ToggleDocument(c.Request.Context(), id, req.Enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ListChunks GET /knowledge-base/docs/:docId/chunks
func (h *DocumentHandler) ListChunks(c *gin.Context) {
	docID := c.Param("docId")
	page, size := pagination(c)
	records, total, err := h.svc.ListChunks(c.Request.Context(), docID, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// UpdateChunk PUT /knowledge-base/docs/:docId/chunks/:chunkId
func (h *DocumentHandler) UpdateChunk(c *gin.Context) {
	chunkID := c.Param("chunkId")
	var req dto.UpdateChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	resp, err := h.svc.UpdateChunk(c.Request.Context(), chunkID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// DeleteChunk DELETE /knowledge-base/docs/:docId/chunks/:chunkId
func (h *DocumentHandler) DeleteChunk(c *gin.Context) {
	chunkID := c.Param("chunkId")
	if err := h.svc.DeleteChunk(c.Request.Context(), chunkID); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ToggleChunk PATCH /knowledge-base/docs/:docId/chunks/:chunkId/enable
func (h *DocumentHandler) ToggleChunk(c *gin.Context) {
	chunkID := c.Param("chunkId")
	var req dto.ToggleChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	if err := h.svc.ToggleChunk(c.Request.Context(), chunkID, req.Enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// BatchToggleChunks PATCH /knowledge-base/docs/:docId/chunks/batch-enable
func (h *DocumentHandler) BatchToggleChunks(c *gin.Context) {
	docID := c.Param("docId")
	var req dto.BatchEnableChunksReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	if err := h.svc.BatchToggleChunks(c.Request.Context(), docID, req.IDs, req.Enabled); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ChunkLogs GET /knowledge-base/docs/:docId/chunk-logs
func (h *DocumentHandler) ChunkLogs(c *gin.Context) {
	docID := c.Param("docId")
	page, size := pagination(c)
	logs, total, err := h.svc.GetChunkLogs(c.Request.Context(), docID, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(logs, total, page, size)))
}

// ChunkDoc POST /knowledge-base/docs/:docId/chunk
func (h *DocumentHandler) ChunkDoc(c *gin.Context) {
	docID := c.Param("docId")
	if err := h.svc.StartChunk(c.Request.Context(), docID, userID(c)); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(map[string]string{
		"message": "文档分块任务已提交",
	}))
}

// CreateChunk POST /knowledge-base/docs/:docId/chunks
func (h *DocumentHandler) CreateChunk(c *gin.Context) {
	docID := c.Param("docId")
	var req dto.CreateChunkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	resp, err := h.svc.CreateChunk(c.Request.Context(), docID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Preview GET /knowledge-base/docs/:docId/preview
func (h *DocumentHandler) Preview(c *gin.Context) {
	docID := c.Param("docId")
	content, err := h.svc.PreviewDocument(c.Request.Context(), docID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(content))
}

// File GET /knowledge-base/docs/:docId/file
func (h *DocumentHandler) File(c *gin.Context) {
	docID := c.Param("docId")
	f, ok := h.fileStore.Get(docID)
	if ok {
		contentType := f.ContentType
		if contentType == "" {
			contentType = detectMIME(f.Name)
		}
		c.Data(http.StatusOK, contentType, f.Data)
		return
	}
	// Fallback: return chunk content as inline text
	content, err := h.svc.PreviewDocument(c.Request.Context(), docID)
	if err != nil {
		c.String(http.StatusOK, "")
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(content))
}

func detectMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".txt", ".csv", ".json":
		return "text/plain; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}
