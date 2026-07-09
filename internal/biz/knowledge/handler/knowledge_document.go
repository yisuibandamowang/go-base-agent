package handler

import (
	"fmt"
	"net/http"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/framework/convention"

	"github.com/gin-gonic/gin"
)

// DocumentHandler 文档管理 HTTP 处理层。
type DocumentHandler struct {
	svc *service.DocumentService
}

// NewDocumentHandler 创建 DocumentHandler。
func NewDocumentHandler(svc *service.DocumentService) *DocumentHandler {
	return &DocumentHandler{svc: svc}
}

// Upload POST /knowledge-base/:id/docs/upload
func (h *DocumentHandler) Upload(c *gin.Context) {
	kbID := c.Param("id")
	var req dto.CreateDocumentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", fmt.Sprintf("参数校验失败: %v", err)))
		return
	}
	resp, err := h.svc.CreateDocument(c.Request.Context(), kbID, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
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
	c.JSON(http.StatusOK, convention.Success[any]([]any{}))
}

// ChunkDoc POST /knowledge-base/docs/:docId/chunk
func (h *DocumentHandler) ChunkDoc(c *gin.Context) {
	c.JSON(http.StatusOK, convention.Success(map[string]string{
		"message": "文档分块任务已提交",
	}))
}
