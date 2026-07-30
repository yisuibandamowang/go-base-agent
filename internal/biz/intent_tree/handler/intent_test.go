package handler

import (
	"encoding/json"
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

func TestIntentTreeJavaCompatCreateAndUpdateResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IntentNode{}); err != nil {
		t.Fatalf("migrate intent node: %v", err)
	}
	h := NewIntentHandler(service.NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb))
	r := gin.New()
	r.POST("/api/ragent/intent-tree", h.CreateNodeCompat)
	r.PUT("/api/ragent/intent-tree/:id", h.UpdateNodeCompat)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/intent-tree", strings.NewReader(`{"intentCode":"member","name":"会员","level":0,"enabled":1,"examples":["查会员","会员权益"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var createResp struct {
		Code string `json:"code"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Code != "0" {
		t.Fatalf("expected code 0, got %s: %s", createResp.Code, w.Body.String())
	}
	id, ok := createResp.Data.(string)
	if !ok || id == "" {
		t.Fatalf("expected Java-compatible create data to be id string, got %T: %v", createResp.Data, createResp.Data)
	}
	var node model.IntentNode
	if err := gdb.First(&node, "id = ?", id).Error; err != nil {
		t.Fatalf("load created node: %v", err)
	}
	if node.Examples != `["查会员","会员权益"]` {
		t.Fatalf("expected Java-compatible examples JSON array, got %s", node.Examples)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/ragent/intent-tree/"+id, strings.NewReader(`{"name":"会员服务","examples":["积分查询"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var updateResp struct {
		Code string `json:"code"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.Code != "0" {
		t.Fatalf("expected code 0, got %s: %s", updateResp.Code, w.Body.String())
	}
	if updateResp.Data != nil {
		t.Fatalf("expected Java-compatible update data to be null, got %T: %v", updateResp.Data, updateResp.Data)
	}
	if err := gdb.First(&node, "id = ?", id).Error; err != nil {
		t.Fatalf("load updated node: %v", err)
	}
	if node.Examples != `["积分查询"]` {
		t.Fatalf("expected updated Java-compatible examples JSON array, got %s", node.Examples)
	}
}

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

func TestListTermMappingsFiltersByKeywordLikeJavaMappingsPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.QueryTermMapping{}); err != nil {
		t.Fatalf("migrate mapping: %v", err)
	}
	if err := gdb.Create(&model.QueryTermMapping{SourceTerm: "VIP会员", TargetTerm: "会员", Priority: 1, Enabled: 1}).Error; err != nil {
		t.Fatalf("seed member mapping: %v", err)
	}
	if err := gdb.Create(&model.QueryTermMapping{SourceTerm: "订单", TargetTerm: "订单状态", Priority: 2, Enabled: 1}).Error; err != nil {
		t.Fatalf("seed order mapping: %v", err)
	}
	h := NewIntentHandler(service.NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb))
	r := gin.New()
	r.GET("/api/ragent/mappings", h.ListTermMappings)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/mappings?keyword=会员&current=1&size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":1`) || !strings.Contains(body, `"sourceTerm":"VIP会员"`) || strings.Contains(body, `"sourceTerm":"订单"`) {
		t.Fatalf("expected keyword-filtered mappings, got %s", body)
	}
}

func TestJavaCompatMappingsCreateAndUpdateResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.QueryTermMapping{}); err != nil {
		t.Fatalf("migrate mapping: %v", err)
	}
	h := NewIntentHandler(service.NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb))
	r := gin.New()
	r.POST("/api/ragent/mappings", h.CreateTermMappingCompat)
	r.PUT("/api/ragent/mappings/:id", h.UpdateTermMappingCompat)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/mappings", strings.NewReader(`{"domain":"member","sourceTerm":"VIP","targetTerm":"会员","priority":1,"enabled":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var createResp struct {
		Code string `json:"code"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Code != "0" {
		t.Fatalf("expected code 0, got %s: %s", createResp.Code, w.Body.String())
	}
	id, ok := createResp.Data.(string)
	if !ok || id == "" {
		t.Fatalf("expected mappings create to return id string, got %T: %v", createResp.Data, createResp.Data)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/ragent/mappings/"+id, strings.NewReader(`{"remark":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var updateResp struct {
		Code string `json:"code"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.Code != "0" || updateResp.Data != nil {
		t.Fatalf("expected mappings update to return null success, got %s %v", updateResp.Code, updateResp.Data)
	}
}
