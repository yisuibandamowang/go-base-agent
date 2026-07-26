package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	knowledgeHandler "go-base-agent/internal/biz/knowledge/handler"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	knowledgeService "go-base-agent/internal/biz/knowledge/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKnowledgeBaseHandler_JavaCompatibleCreateAndRenameResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := knowledgeService.NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	h := knowledgeHandler.NewKnowledgeBaseHandler(svc)
	r := gin.New()
	r.POST("/api/ragent/knowledge-base", h.Create)
	r.PUT("/api/ragent/knowledge-base/:id", h.Update)

	createBody := `{"name":"会员知识库","embeddingModel":"emb-1","collectionName":"member_kb"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var createResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	kbID, ok := createResp["data"].(string)
	if !ok || strings.TrimSpace(kbID) == "" {
		t.Fatalf("expected create data to be new id string, got %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/ragent/knowledge-base/"+kbID, strings.NewReader(`{"name":"会员知识库 v2"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var updateResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp["code"] != "0" || updateResp["data"] != nil {
		t.Fatalf("expected rename to return empty success, got %s", w.Body.String())
	}

	var stored knowledgeModel.KnowledgeBase
	if err := gdb.Where("id = ?", kbID).First(&stored).Error; err != nil {
		t.Fatalf("load stored kb: %v", err)
	}
	if stored.Name != "会员知识库 v2" || stored.EmbeddingModel != "emb-1" || stored.CollectionName != "member_kb" {
		t.Fatalf("expected rename to preserve non-name fields, got %+v", stored)
	}
}
