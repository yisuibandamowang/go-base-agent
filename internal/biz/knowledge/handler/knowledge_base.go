package handler

import (
	"net/http"
	"strconv"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// KnowledgeBaseHandler 知识库 HTTP 处理层。
type KnowledgeBaseHandler struct {
	svc *service.KnowledgeBaseService
}

// NewKnowledgeBaseHandler 创建 KnowledgeBaseHandler。
func NewKnowledgeBaseHandler(svc *service.KnowledgeBaseService) *KnowledgeBaseHandler {
	return &KnowledgeBaseHandler{svc: svc}
}

// Create POST /knowledge-base
func (h *KnowledgeBaseHandler) Create(c *gin.Context) {
	var req dto.CreateKnowledgeBaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Create(c.Request.Context(), req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// List GET /knowledge-base
func (h *KnowledgeBaseHandler) List(c *gin.Context) {
	page, size := pagination(c)
	records, total, err := h.svc.List(c.Request.Context(), page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// Get GET /knowledge-base/:id
func (h *KnowledgeBaseHandler) Get(c *gin.Context) {
	id := c.Param("id")
	resp, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Update PUT /knowledge-base/:id
func (h *KnowledgeBaseHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateKnowledgeBaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Update(c.Request.Context(), id, req, userID(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Delete DELETE /knowledge-base/:id
func (h *KnowledgeBaseHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ChunkStrategies GET /knowledge-base/chunk-strategies
func (h *KnowledgeBaseHandler) ChunkStrategies(c *gin.Context) {
	strategies := []map[string]interface{}{
		{"value": "fixed_size", "label": "固定大小分块", "defaultConfig": map[string]int{"size": 500, "overlap": 50}},
		{"value": "paragraph", "label": "段落分块", "defaultConfig": map[string]int{"maxChars": 1000}},
		{"value": "semantic", "label": "语义分块（Markdown标题）", "defaultConfig": map[string]int{"maxChars": 1500}},
	}
	c.JSON(http.StatusOK, convention.Success(strategies))
}

// userID 从请求上下文获取当前用户 ID。
func userID(c *gin.Context) string {
	if user := middleware.GetLoginUser(c); user != nil {
		return user.UserID
	}
	return "system"
}

func pagination(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(c.DefaultQuery("size", "10"))
	if err != nil || size < 1 {
		size = 10
	}
	return page, size
}
