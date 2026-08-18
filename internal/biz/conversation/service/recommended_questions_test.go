package service

import (
	"context"
	"testing"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLLMRecommendedQuestionGenerator_ParseQuestions(t *testing.T) {
	gen := NewLLMRecommendedQuestionGenerator(&fakeRecommendedQuestionsLLM{
		raw: "```json\n[\"下一步怎么做？\",\"如何排查？\",\"下一步怎么做？\",null,\"这条会被截断\" ]\n```",
	}, "")

	got := gen.Generate(context.Background(), "当前会员 agent 支持哪些能力？", "支持知识库问答")
	if got.Status != RecommendedQuestionsStatusSuccess {
		t.Fatalf("expected success, got %+v", got)
	}
	if len(got.Questions) != 3 {
		t.Fatalf("expected 3 questions, got %+v", got)
	}
	if got.Questions[0] != "下一步怎么做？" || got.Questions[1] != "如何排查？" {
		t.Fatalf("unexpected questions: %+v", got.Questions)
	}
}

func TestConversationService_GenerateRecommendedQuestionsUsesPreviousUserMessage(t *testing.T) {
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

	userMsg := conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "100"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "user",
		Content:        "会员 agent 支持哪些能力？",
	}
	assistantMsg := conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "200"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "支持知识库问答和推荐追问",
	}
	if err := gdb.Create(&userMsg).Error; err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	if err := gdb.Create(&assistantMsg).Error; err != nil {
		t.Fatalf("seed assistant message: %v", err)
	}

	gen := &captureRecommendedQuestionsGenerator{}
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), nil)
	svc.SetRecommendedQuestionGenerator(gen)

	got, err := svc.GenerateRecommendedQuestions(context.Background(), assistantMsg.ID, "user-1")
	if err != nil {
		t.Fatalf("generate recommended questions: %v", err)
	}
	if got.Status != RecommendedQuestionsStatusSuccess {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if gen.question != userMsg.Content {
		t.Fatalf("expected previous user question, got %q", gen.question)
	}
	if gen.answer != assistantMsg.Content {
		t.Fatalf("expected assistant answer, got %q", gen.answer)
	}
}

type fakeRecommendedQuestionsLLM struct {
	raw string
}

func (f *fakeRecommendedQuestionsLLM) Chat(context.Context, chat.Request) (string, error) {
	return f.raw, nil
}

func (f *fakeRecommendedQuestionsLLM) ChatWithModel(context.Context, chat.Request, string) (string, error) {
	return f.raw, nil
}

func (f *fakeRecommendedQuestionsLLM) StreamChat(context.Context, chat.Request, chat.StreamCallback) (chat.StreamHandle, error) {
	return nil, nil
}

type captureRecommendedQuestionsGenerator struct {
	question string
	answer   string
	payload  RecommendedQuestionsPayload
}

func (g *captureRecommendedQuestionsGenerator) Generate(_ context.Context, question, answer string) RecommendedQuestionsPayload {
	g.question = question
	g.answer = answer
	if g.payload.Status == "" {
		return RecommendedQuestionsSuccess([]string{"下一步怎么做？"})
	}
	return g.payload
}
