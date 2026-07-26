package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/biz/conversation/service"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"

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
	svc := service.NewConversationService(convRepo, msgRepo, fbRepo, nil)
	h := NewConversationHandler(svc)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "user-1", Username: "admin"})
		c.Next()
	})
	r.GET("/api/ragent/conversations", h.List)
	r.GET("/api/ragent/conversations/:conversationId/messages", h.Messages)
	r.POST("/api/ragent/conversations/messages/:messageId/feedback", h.SubmitFeedback)
	r.DELETE("/api/ragent/conversations/messages/:messageId/feedback", h.DeleteFeedback)

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

func TestConversationFeedbackVoteAndDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "当前会员agent都支持哪些能力？",
		LastTime:       time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "支持知识库问答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	var msg conversationModel.Message
	if err := gdb.Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").First(&msg).Error; err != nil {
		t.Fatalf("load message: %v", err)
	}
	if err := gdb.Create(&conversationModel.MessageFeedback{
		MessageID:      msg.ID,
		ConversationID: "conv-1",
		UserID:         "user-1",
		Vote:           1,
	}).Error; err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	fbRepo := repo.NewFeedbackRepo(gdb)
	svc := service.NewConversationService(convRepo, msgRepo, fbRepo, nil)
	h := NewConversationHandler(svc)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "user-1", Username: "admin"})
		c.Next()
	})
	r.GET("/api/ragent/conversations/:conversationId/messages", h.Messages)
	r.POST("/api/ragent/conversations/messages/:messageId/feedback", h.SubmitFeedback)
	r.DELETE("/api/ragent/conversations/messages/:messageId/feedback", h.DeleteFeedback)

	t.Run("message list includes vote", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/conversations/conv-1/messages", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			Code string `json:"code"`
			Data []struct {
				Role string `json:"role"`
				Vote *int16 `json:"vote"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Code != "0" {
			t.Fatalf("expected code 0, got %s", resp.Code)
		}
		if len(resp.Data) != 1 {
			t.Fatalf("expected one message, got %d", len(resp.Data))
		}
		if resp.Data[0].Vote == nil || *resp.Data[0].Vote != 1 {
			t.Fatalf("expected vote 1, got %+v", resp.Data[0].Vote)
		}
	})

	t.Run("delete feedback removes vote", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/ragent/conversations/messages/"+msg.ID+"/feedback", nil)
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

		var count int64
		if err := gdb.Scopes(db.NotDeletedScope()).Model(&conversationModel.MessageFeedback{}).
			Where("message_id = ? AND user_id = ?", msg.ID, "user-1").
			Count(&count).Error; err != nil {
			t.Fatalf("count feedback: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected feedback deleted, got %d rows", count)
		}
	})

	t.Run("path message feedback submit works", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/ragent/conversations/messages/"+msg.ID+"/feedback", strings.NewReader(`{"conversationId":"conv-1","vote":-1,"reason":"not good","comment":"too short"}`))
		req.Header.Set("Content-Type", "application/json")
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
	})

	t.Run("path message feedback submit does not require conversation id", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/ragent/conversations/messages/"+msg.ID+"/feedback", strings.NewReader(`{"vote":1,"reason":"good"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp["code"] != "0" {
			t.Fatalf("expected code 0, got %v body=%s", resp["code"], w.Body.String())
		}
	})
}

func TestConversationMessagesDefaultDoesNotTruncateLikeJava(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-long",
		UserID:         "user-1",
		Title:          "长会话",
		LastTime:       time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	baseTime := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 101; i++ {
		if err := gdb.Create(&conversationModel.Message{
			BaseModel:      db.BaseModel{CreateTime: baseTime.Add(time.Duration(i) * time.Second)},
			ConversationID: "conv-long",
			UserID:         "user-1",
			Role:           "user",
			Content:        "message-" + strconv.Itoa(i),
		}).Error; err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}

	svc := service.NewConversationService(
		repo.NewConversationRepo(gdb),
		repo.NewMessageRepo(gdb),
		repo.NewFeedbackRepo(gdb),
		nil,
	)
	h := NewConversationHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "user-1", Username: "admin"})
		c.Next()
	})
	r.GET("/api/ragent/conversations/:conversationId/messages", h.Messages)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/conversations/conv-long/messages", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != "0" {
		t.Fatalf("expected code 0, got %s", resp.Code)
	}
	if len(resp.Data) != 101 {
		t.Fatalf("expected all 101 messages by default, got %d", len(resp.Data))
	}
	if resp.Data[0].Content != "message-0" || resp.Data[100].Content != "message-100" {
		t.Fatalf("expected ascending full history, first=%q last=%q", resp.Data[0].Content, resp.Data[len(resp.Data)-1].Content)
	}
}
