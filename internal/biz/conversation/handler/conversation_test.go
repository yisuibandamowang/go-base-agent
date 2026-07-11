package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/biz/conversation/service"
	appctx "go-base-agent/internal/framework/context"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConversationListResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	conv := conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "当前会员agent都支持哪些能力？",
		LastTime:       time.Now(),
	}
	if err := gdb.Create(&conv).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	fbRepo := repo.NewFeedbackRepo(gdb)
	svc := service.NewConversationService(convRepo, msgRepo, fbRepo)
	h := NewConversationHandler(svc)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "user-1", Username: "admin"})
		c.Next()
	})
	r.GET("/api/ragent/conversations", h.List)

	t.Run("chat page returns array data", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/conversations?current=1&size=10", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["code"] != "0" {
			t.Fatalf("expected code 0, got %v", resp["code"])
		}
		data, ok := resp["data"].([]interface{})
		if !ok {
			t.Fatalf("expected data array for chat page, got %T: %s", resp["data"], w.Body.String())
		}
		if len(data) != 1 {
			t.Fatalf("expected one conversation, got %d", len(data))
		}
	})

	t.Run("paged mode keeps page data", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/conversations?current=1&size=10&paged=true", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		data, ok := resp["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected data object for paged mode, got %T: %s", resp["data"], w.Body.String())
		}
		if _, ok := data["records"].([]interface{}); !ok {
			t.Fatalf("expected data.records array for paged mode, got %T: %s", data["records"], w.Body.String())
		}
	})
}
