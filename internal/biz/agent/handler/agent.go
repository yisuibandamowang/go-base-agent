package handler

import (
	"net/http"
	"strings"

	agentDto "go-base-agent/internal/biz/agent/dto"
	"go-base-agent/internal/biz/agent/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// AgentHandler 智能体管理 HTTP 处理层。
type AgentHandler struct {
	svc *service.AgentService
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(svc *service.AgentService) *AgentHandler {
	return &AgentHandler{svc: svc}
}

// List GET /api/ragent/admin/agents
func (h *AgentHandler) List(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	resp, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Create POST /api/ragent/admin/agents
func (h *AgentHandler) Create(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var req agentDto.CreateAgentProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	id, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(id))
}

// Update PUT /api/ragent/admin/agents/:id
func (h *AgentHandler) Update(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var req agentDto.UpdateAgentProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	resp, err := h.svc.Update(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// Delete DELETE /api/ragent/admin/agents/:id
func (h *AgentHandler) Delete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// Activate POST /api/ragent/admin/agents/:id/activate
func (h *AgentHandler) Activate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	if err := h.svc.Activate(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// Prompts GET /api/ragent/admin/agents/:id/prompts
func (h *AgentHandler) Prompts(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	resp, err := h.svc.LoadPrompts(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// SavePrompt PUT /api/ragent/admin/agents/:id/prompts/:slotKey
func (h *AgentHandler) SavePrompt(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var req agentDto.SaveAgentPromptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	if err := h.svc.SavePrompt(c.Request.Context(), c.Param("id"), c.Param("slotKey"), req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// DefaultPrompt GET /api/ragent/admin/agents/prompt-slots/:slotKey/default
func (h *AgentHandler) DefaultPrompt(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	resp, err := h.svc.DefaultPrompt(c.Request.Context(), c.Param("slotKey"))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success(resp))
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
