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

func TestKnowledgeBaseHandler_ChunkStrategiesMatchesJava(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := knowledgeHandler.NewKnowledgeBaseHandler(nil)
	r := gin.New()
	r.GET("/api/ragent/knowledge-base/chunk-strategies", h.ChunkStrategies)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/knowledge-base/chunk-strategies", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got %T", resp["data"])
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 chunk strategies, got %d: %s", len(data), w.Body.String())
	}

	first := data[0].(map[string]any)
	if first["value"] != "fixed_size" || first["label"] != "固定大小" {
		t.Fatalf("unexpected first chunk strategy: %+v", first)
	}
	defaultConfig := first["defaultConfig"].(map[string]any)
	if defaultConfig["chunkSize"] != float64(512) || defaultConfig["overlapSize"] != float64(128) {
		t.Fatalf("unexpected fixed_size default config: %+v", defaultConfig)
	}

	second := data[1].(map[string]any)
	if second["value"] != "structure_aware" || second["label"] != "语义感知（Markdown友好）" {
		t.Fatalf("unexpected second chunk strategy: %+v", second)
	}
	secondConfig := second["defaultConfig"].(map[string]any)
	wantKeys := map[string]bool{"targetChars": false, "overlapChars": false, "maxChars": false, "minChars": false}
	for k := range secondConfig {
		if _, ok := wantKeys[k]; !ok {
			t.Fatalf("unexpected structure_aware config key: %s in %+v", k, secondConfig)
		}
		wantKeys[k] = true
	}
	for k, seen := range wantKeys {
		if !seen {
			t.Fatalf("missing structure_aware config key: %s in %+v", k, secondConfig)
		}
	}
}
