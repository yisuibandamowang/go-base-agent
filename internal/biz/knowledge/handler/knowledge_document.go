package handler

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/framework/convention"

	"github.com/gin-gonic/gin"
)

// DocumentHandler 文档管理 HTTP 处理层。
type DocumentHandler struct {
	svc       *service.DocumentService
	fileStore *FileStore
}

// NewDocumentHandler 创建 DocumentHandler。
func NewDocumentHandler(svc *service.DocumentService, fs *FileStore) *DocumentHandler {
	return &DocumentHandler{svc: svc, fileStore: fs}
}

// Upload POST /knowledge-base/:id/docs/upload
func (h *DocumentHandler) Upload(c *gin.Context) {
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
		req.FileURL = vals[0]
		if req.DocName == "" {
			req.DocName = vals[0]
		}
	}

	// Process mode & chunk strategy
	if vals, ok := form.Value["processMode"]; ok && len(vals) > 0 {
		if vals[0] == "pipeline" {
			if pvals, ok := form.Value["pipelineId"]; ok && len(pvals) > 0 {
				req.ChunkStrategy = "pipeline"
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
		h.fileStore.Put(resp.ID, header.Filename, data)
	}

	c.JSON(http.StatusOK, convention.Success(resp))
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
	_, err := h.svc.UpdateDocument(c.Request.Context(), id, dto.UpdateDocumentReq{
		DocName: "",
		Enabled: &req.Enabled,
	}, userID(c))
	if err != nil {
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

// CreateChunkStub POST /knowledge-base/docs/:docId/chunks (前端兼容路径)
func (h *DocumentHandler) CreateChunkStub(c *gin.Context) {
	c.JSON(http.StatusOK, convention.Success(map[string]string{
		"message": "create chunk stub",
	}))
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
		contentType := detectMIME(f.Name)
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
