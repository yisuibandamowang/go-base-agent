package handler

import (
	"net/http"
	"strconv"

	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// IntentHandler 意图树 HTTP 处理层。
type IntentHandler struct {
	svc *service.IntentService
}

// NewIntentHandler 创建 IntentHandler。
func NewIntentHandler(svc *service.IntentService) *IntentHandler {
	return &IntentHandler{svc: svc}
}

// --- 意图节点 ---

// CreateNode POST /api/ragent/intent-tree/nodes
func (h *IntentHandler) CreateNode(c *gin.Context) {
	var req dto.CreateIntentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateNode(c.Request.Context(), req, currentUser(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// GetNode GET /api/ragent/intent-tree/nodes/:id
func (h *IntentHandler) GetNode(c *gin.Context) {
	id := c.Param("id")
	resp, err := h.svc.GetNode(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateNode PUT /api/ragent/intent-tree/nodes/:id
func (h *IntentHandler) UpdateNode(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateIntentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateNode(c.Request.Context(), id, req, currentUser(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// DeleteNode DELETE /api/ragent/intent-tree/nodes/:id
func (h *IntentHandler) DeleteNode(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.DeleteNode(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ToggleNode PATCH /api/ragent/intent-tree/nodes/:id/enable
func (h *IntentHandler) ToggleNode(c *gin.Context) {
	id := c.Param("id")
	enabledStr := c.DefaultQuery("enabled", "1")
	enabled, _ := strconv.Atoi(enabledStr)
	if err := h.svc.ToggleNode(c.Request.Context(), id, int16(enabled)); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// GetTree GET /api/ragent/intent-tree/tree
func (h *IntentHandler) GetTree(c *gin.Context) {
	tree, err := h.svc.GetTree(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(tree))
}

// ListNodes GET /api/ragent/intent-tree/nodes
func (h *IntentHandler) ListNodes(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	records, total, err := h.svc.ListNode(c.Request.Context(), page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

// --- 关键词映射 ---

// CreateTermMapping POST /api/ragent/intent-tree/term-mappings
func (h *IntentHandler) CreateTermMapping(c *gin.Context) {
	var req dto.CreateTermMappingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateTermMapping(c.Request.Context(), req, currentUser(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateTermMapping PUT /api/ragent/intent-tree/term-mappings/:id
func (h *IntentHandler) UpdateTermMapping(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateTermMappingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateTermMapping(c.Request.Context(), id, req, currentUser(c))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// DeleteTermMapping DELETE /api/ragent/intent-tree/term-mappings/:id
func (h *IntentHandler) DeleteTermMapping(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.DeleteTermMapping(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// ListTermMappings GET /api/ragent/intent-tree/term-mappings
func (h *IntentHandler) ListTermMappings(c *gin.Context) {
	domain := c.Query("domain")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	records, total, err := h.svc.ListTermMappings(c.Request.Context(), domain, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
}

func currentUser(c *gin.Context) string {
	if user := middleware.GetLoginUser(c); user != nil {
		return user.UserID
	}
	return "system"
}
