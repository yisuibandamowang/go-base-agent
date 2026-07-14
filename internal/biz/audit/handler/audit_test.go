package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	auditModel "go-base-agent/internal/biz/audit/model"
	"go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuditHandler_ListAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := gdb.Create(&auditModel.BizChangeLog{
		ID:            "2001",
		BizType:       "KNOWLEDGE_BASE",
		BizId:         "kb-1",
		OperationType: "UPDATE",
		ActionDesc:    "更新知识库",
		Success:       true,
		OperatorID:    "u-1",
		OperatorName:  "管理员",
		CreateTime:    time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewAuditHandler(auditService.NewBizChangeLogService(repo.NewBizChangeLogRepo(gdb)))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ragent/biz-change-logs?page=1&size=10", nil)

	h.List(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body == "" || body[0] != '{' || !contains(body, `"bizType":"KNOWLEDGE_BASE"`) {
		t.Fatalf("unexpected list body: %s", body)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ragent/biz-change-logs/2001", nil)
	c.Params = gin.Params{{Key: "id", Value: "2001"}}

	h.Get(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !contains(body, `"id":"2001"`) || !contains(body, `"actionDesc":"更新知识库"`) {
		t.Fatalf("unexpected detail body: %s", body)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
