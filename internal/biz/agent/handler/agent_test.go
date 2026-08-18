package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentModel "go-base-agent/internal/biz/agent/model"
	agentRepo "go-base-agent/internal/biz/agent/repo"
	agentService "go-base-agent/internal/biz/agent/service"
	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAgentHandlerRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, gdb := newAgentHandlerTestService(t)
	builtin := &agentModel.AgentProfile{
		BaseModel:   db.BaseModel{ID: "agent-builtin"},
		Name:        "默认助手",
		Description: "系统默认人设",
		Avatar:      "orbit-indigo",
		Builtin:     1,
		Active:      1,
	}
	custom := &agentModel.AgentProfile{
		BaseModel: db.BaseModel{ID: "agent-custom"},
		Name:      "客服助手",
		Avatar:    "orbit-green",
	}
	if err := gdb.Create(builtin).Error; err != nil {
		t.Fatalf("create builtin: %v", err)
	}
	if err := gdb.Create(custom).Error; err != nil {
		t.Fatalf("create custom: %v", err)
	}
	if err := gdb.Create(&agentModel.AgentPrompt{
		BaseModel: db.BaseModel{ID: "prompt-1"},
		AgentID:   builtin.ID,
		SlotKey:   "SYSTEM_CHAT",
		Content:   "builtin chat",
	}).Error; err != nil {
		t.Fatalf("create prompt: %v", err)
	}

	h := NewAgentHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "admin-1", Username: "管理员", Role: "admin"})
		c.Next()
	})
	api := r.Group("/api/ragent")
	api.GET("/admin/agents", h.List)
	api.GET("/admin/agents/prompt-slots/:slotKey/default", h.DefaultPrompt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/agents", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"0"`) {
		t.Fatalf("unexpected list response: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"mode":"WORKFLOW"`) {
		t.Fatalf("unexpected list payload: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ragent/admin/agents/prompt-slots/SYSTEM_CHAT/default", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"builtin chat"`) {
		t.Fatalf("unexpected default prompt response: %s", w.Body.String())
	}
}

func TestAgentHandlerRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _ := newAgentHandlerTestService(t)
	h := NewAgentHandler(svc)
	r := gin.New()
	api := r.Group("/api/ragent")
	api.GET("/admin/agents", h.List)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/agents", nil)
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "请先登录") {
		t.Fatalf("expected auth rejection, got %s", w.Body.String())
	}
}

func newAgentHandlerTestService(t *testing.T) (*agentService.AgentService, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&agentModel.AgentProfile{}, &agentModel.AgentPrompt{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	svc := agentService.NewAgentService(agentRepo.NewAgentRepo(gdb), agentService.ModeWorkflow)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	return svc, gdb
}
