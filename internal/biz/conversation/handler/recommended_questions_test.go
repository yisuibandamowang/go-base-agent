package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	conversationRepo "go-base-agent/internal/biz/conversation/repo"
	conversationService "go-base-agent/internal/biz/conversation/service"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConversationHandler_RecommendedQuestions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "会员咨询",
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "100"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "user",
		Content:        "会员 agent 支持哪些能力？",
	}).Error; err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "200"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "支持知识库问答和推荐追问",
	}).Error; err != nil {
		t.Fatalf("seed assistant message: %v", err)
	}

	svc := conversationService.NewConversationService(conversationRepo.NewConversationRepo(gdb), conversationRepo.NewMessageRepo(gdb), conversationRepo.NewFeedbackRepo(gdb), nil)
	svc.SetRecommendedQuestionGenerator(&captureRecommendedQuestionsGenerator{
		payload: conversationService.RecommendedQuestionsSuccess([]string{"下一步怎么做？"}),
	})
	h := NewConversationHandler(svc)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("loginUser", &appctx.LoginUser{UserID: "user-1", Username: "admin"})
		c.Next()
	})
	r.POST("/api/ragent/conversations/messages/:messageId/recommended-questions", h.RecommendedQuestions)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/conversations/messages/200/recommended-questions", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] != "0" {
		t.Fatalf("expected success response, got %s", w.Body.String())
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected response data: %T %s", resp["data"], w.Body.String())
	}
	if data["status"] != string(conversationService.RecommendedQuestionsStatusSuccess) {
		t.Fatalf("unexpected status: %#v", data)
	}
}

type captureRecommendedQuestionsGenerator struct {
	question string
	answer   string
	payload  conversationService.RecommendedQuestionsPayload
}

func (g *captureRecommendedQuestionsGenerator) Generate(_ context.Context, question, answer string, _ []rag.GroundingChunk) conversationService.RecommendedQuestionsPayload {
	g.question = question
	g.answer = answer
	if g.payload.Status == "" {
		return conversationService.RecommendedQuestionsSuccess([]string{"下一步怎么做？"})
	}
	return g.payload
}
