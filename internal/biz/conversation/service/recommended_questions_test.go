package service

import (
	"context"
	"encoding/json"
	"testing"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLLMRecommendedQuestionGenerator_ParseQuestions(t *testing.T) {
	gen := NewLLMRecommendedQuestionGenerator(&fakeRecommendedQuestionsLLM{
		raw: "```json\n[\"下一步怎么做？\",\"如何排查？\",\"下一步怎么做？\",null,\"这条会被截断\" ]\n```",
	}, "")

	got := gen.Generate(context.Background(), "当前会员 agent 支持哪些能力？", "支持知识库问答", nil)
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
		RetrievedChunks: func() string {
			data, _ := json.Marshal([]rag.GroundingChunk{{DocName: "会员手册", Text: "白金会员权益"}})
			return string(data)
		}(),
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
	if len(gen.chunks) != 1 || gen.chunks[0].DocName != "会员手册" {
		t.Fatalf("expected assistant grounding chunks, got %+v", gen.chunks)
	}
}

func TestConversationService_GenerateRecommendedQuestionsUsesCachedSuccess(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Conversation{ConversationID: "conv-cache", UserID: "user-1", Title: "缓存"}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{BaseModel: db.BaseModel{ID: "cache-user"}, ConversationID: "conv-cache", UserID: "user-1", Role: "user", Content: "原问题"}).Error; err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	assistant := conversationModel.Message{
		BaseModel:            db.BaseModel{ID: "cache-assistant"},
		ConversationID:       "conv-cache",
		UserID:               "user-1",
		Role:                 "assistant",
		Content:              "原回答",
		RecommendedQuestions: ` ["缓存问题"] `,
	}
	if err := gdb.Create(&assistant).Error; err != nil {
		t.Fatalf("seed assistant message: %v", err)
	}
	gen := &captureRecommendedQuestionsGenerator{}
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), nil)
	svc.SetRecommendedQuestionGenerator(gen)

	got, err := svc.GenerateRecommendedQuestions(context.Background(), assistant.ID, "user-1")
	if err != nil {
		t.Fatalf("load cached recommended questions: %v", err)
	}
	if got.Status != RecommendedQuestionsStatusSuccess || len(got.Questions) != 1 || got.Questions[0] != "缓存问题" {
		t.Fatalf("unexpected cached payload: %+v", got)
	}
	if gen.calls != 0 {
		t.Fatalf("expected cached result to skip generation, calls=%d", gen.calls)
	}
}

func TestConversationService_GenerateRecommendedQuestionsUsesCachedEmptyAsNegativeCache(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Conversation{ConversationID: "conv-empty", UserID: "user-1", Title: "空缓存"}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	assistant := conversationModel.Message{
		BaseModel:            db.BaseModel{ID: "empty-assistant"},
		ConversationID:       "conv-empty",
		UserID:               "user-1",
		Role:                 "assistant",
		Content:              "原回答",
		RecommendedQuestions: `[]`,
	}
	if err := gdb.Create(&assistant).Error; err != nil {
		t.Fatalf("seed assistant message: %v", err)
	}
	gen := &captureRecommendedQuestionsGenerator{}
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), nil)
	svc.SetRecommendedQuestionGenerator(gen)

	got, err := svc.GenerateRecommendedQuestions(context.Background(), assistant.ID, "user-1")
	if err != nil {
		t.Fatalf("load cached empty recommended questions: %v", err)
	}
	if got.Status != RecommendedQuestionsStatusEmpty || len(got.Questions) != 0 {
		t.Fatalf("unexpected cached empty payload: %+v", got)
	}
	if gen.calls != 0 {
		t.Fatalf("expected cached empty result to skip generation, calls=%d", gen.calls)
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
	chunks   []rag.GroundingChunk
	payload  RecommendedQuestionsPayload
	calls    int
}

func (g *captureRecommendedQuestionsGenerator) Generate(_ context.Context, question, answer string, chunks []rag.GroundingChunk) RecommendedQuestionsPayload {
	g.question = question
	g.answer = answer
	g.chunks = chunks
	g.calls++
	if g.payload.Status == "" {
		return RecommendedQuestionsSuccess([]string{"下一步怎么做？"})
	}
	return g.payload
}
