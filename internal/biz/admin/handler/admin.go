package handler

import (
	"net/http"
	"strconv"
	"strings"

	"go-base-agent/internal/biz/admin/dto"
	"go-base-agent/internal/biz/admin/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// AdminHandler 管理后台 HTTP 处理层。
type AdminHandler struct {
	svc *service.AdminService
}

// NewAdminHandler 创建 AdminHandler。
func NewAdminHandler(svc *service.AdminService) *AdminHandler {
	return &AdminHandler{svc: svc}
}

// Dashboard GET /api/ragent/admin/dashboard
func (h *AdminHandler) Dashboard(c *gin.Context) {
	resp, err := h.svc.GetDashboard(c.Request.Context(), c.Query("window"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Performance GET /api/ragent/admin/dashboard/performance
func (h *AdminHandler) Performance(c *gin.Context) {
	resp, err := h.svc.GetPerformance(c.Request.Context(), c.Query("window"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Trends GET /api/ragent/admin/dashboard/trends
func (h *AdminHandler) Trends(c *gin.Context) {
	resp, err := h.svc.GetTrends(c.Request.Context(), c.Query("metric"), c.Query("window"), c.Query("granularity"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// --- 链路追踪 ---

// ListTraceRuns GET /api/ragent/admin/traces
func (h *AdminHandler) ListTraceRuns(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("page", "1")))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	runs, total, err := h.svc.ListTraceRuns(c.Request.Context(), page, size, dto.TraceRunPageReq{
		TraceID:        c.Query("traceId"),
		ConversationID: c.Query("conversationId"),
		TaskID:         c.Query("taskId"),
		Status:         c.Query("status"),
	})
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(runs, total, page, size)))
}

// TraceDetail GET /api/ragent/admin/traces/:traceId
func (h *AdminHandler) TraceDetail(c *gin.Context) {
	traceID := c.Param("traceId")
	if traceID == "" {
		traceID = c.Param("id")
	}
	resp, err := h.svc.GetTraceDetail(c.Request.Context(), traceID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// TraceNodes GET /api/ragent/rag/traces/runs/:id/nodes
func (h *AdminHandler) TraceNodes(c *gin.Context) {
	traceID := c.Param("id")
	resp, err := h.svc.ListTraceNodes(c.Request.Context(), traceID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// --- 示例问题 ---

// ListSampleQuestions GET /api/ragent/admin/sample-questions
func (h *AdminHandler) ListSampleQuestions(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("page", "1")))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	items, total, err := h.svc.ListSampleQuestions(c.Request.Context(), page, size, c.Query("keyword"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(items, total, page, size)))
}

// ListRAGSampleQuestions GET /api/ragent/rag/sample-questions
func (h *AdminHandler) ListRAGSampleQuestions(c *gin.Context) {
	items, err := h.svc.ListRandomSampleQuestions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(items))
}

// GetSampleQuestion GET /api/ragent/sample-questions/:id
func (h *AdminHandler) GetSampleQuestion(c *gin.Context) {
	resp, err := h.svc.GetSampleQuestion(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// CreateSampleQuestion POST /api/ragent/admin/sample-questions
func (h *AdminHandler) CreateSampleQuestion(c *gin.Context) {
	var req dto.CreateSampleQuestionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateSampleQuestion(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp.ID))
}

// UpdateSampleQuestion PUT /api/ragent/admin/sample-questions/:id
func (h *AdminHandler) UpdateSampleQuestion(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateSampleQuestionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	_, err := h.svc.UpdateSampleQuestion(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// DeleteSampleQuestion DELETE /api/ragent/admin/sample-questions/:id
func (h *AdminHandler) DeleteSampleQuestion(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.DeleteSampleQuestion(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// --- 用户管理 ---

// ListUsers GET /api/ragent/admin/users
func (h *AdminHandler) ListUsers(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("page", "1")))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	users, total, err := h.svc.ListUsers(c.Request.Context(), page, size, c.Query("keyword"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(users, total, page, size)))
}

// CreateUser POST /api/ragent/admin/users
func (h *AdminHandler) CreateUser(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var req dto.CreateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateUser(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateUser PUT /api/ragent/admin/users/:id
func (h *AdminHandler) UpdateUser(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id := c.Param("id")
	var req dto.UpdateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateUser(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// DeleteUser DELETE /api/ragent/admin/users/:id
func (h *AdminHandler) DeleteUser(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id := c.Param("id")
	if err := h.svc.DeleteUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

func requireAdmin(c *gin.Context) bool {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "请先登录"))
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(user.Role), "admin") {
		c.JSON(http.StatusOK, convention.Failure("A000001", "无权限"))
		return false
	}
	return true
}
