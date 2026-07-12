package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	"go-base-agent/internal/biz/intent_tree/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGetTermMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.QueryTermMapping{}); err != nil {
		t.Fatalf("migrate mapping: %v", err)
	}
	m := &model.QueryTermMapping{Domain: "member", SourceTerm: "VIP", TargetTerm: "会员", Enabled: 1}
	if err := gdb.Create(m).Error; err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	h := NewIntentHandler(service.NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb))
	r := gin.New()
	r.GET("/api/ragent/mappings/:id", h.GetTermMapping)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/mappings/"+m.ID, nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"sourceTerm":"VIP"`) || !strings.Contains(body, `"targetTerm":"会员"`) {
		t.Fatalf("expected mapping detail, got %s", body)
	}
}
