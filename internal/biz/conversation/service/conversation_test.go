package service

import (
	"context"
	"testing"
	"time"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDBMemoryStore_AppendMessageCreatesConversation(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	sumRepo := repo.NewConversationSummaryRepo(gdb)
	store := NewDBMemoryStore(gdb, convRepo, msgRepo, sumRepo, nil, false, 0, 0, 0)
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{UserID: "user-1"})

	if _, err := store.AppendMessage(ctx, "conv-1", chat.NewUserMessage("hello world")); err != nil {
		t.Fatalf("append message: %v", err)
	}

	var conv conversationModel.Conversation
	if err := gdb.Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").First(&conv).Error; err != nil {
		t.Fatalf("conversation should be created: %v", err)
	}
	if conv.Title != "hello world" {
		t.Fatalf("expected conversation title from first question, got %q", conv.Title)
	}

	var count int64
	if err := gdb.Model(&conversationModel.Message{}).
		Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").
		Count(&count).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 message, got %d", count)
	}
}

func TestDBMemoryStore_AppendMessageUsesTitleGenerator(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	sumRepo := repo.NewConversationSummaryRepo(gdb)
	store := NewDBMemoryStore(gdb, convRepo, msgRepo, sumRepo, nil, false, 0, 0, 0)
	titleGen := &fakeConversationTitleGenerator{output: "LLM标题"}
	store.SetTitleGenerator(titleGen)
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{UserID: "user-1"})

	if _, err := store.AppendMessage(ctx, "conv-1", chat.NewUserMessage("hello world")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if titleGen.calls != 1 {
		t.Fatalf("expected title generator to be called once, got %d", titleGen.calls)
	}

	var conv conversationModel.Conversation
	if err := gdb.Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").First(&conv).Error; err != nil {
		t.Fatalf("conversation should be created: %v", err)
	}
	if conv.Title != "LLM标题" {
		t.Fatalf("expected generated title, got %q", conv.Title)
	}
}

type fakeConversationSummaryGenerator struct {
	output string
	err    error
	calls  int
}

func (f *fakeConversationSummaryGenerator) Generate(ctx context.Context, history []chat.Message, previousSummary string, maxChars int) (string, error) {
	f.calls++
	return f.output, f.err
}

func TestDBMemoryStore_AppendsSummaryAndLoadsIt(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	now := time.Now()
	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "初始标题",
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Model(&conversationModel.Conversation{}).Where("conversation_id = ?", "conv-1").Updates(map[string]interface{}{"last_time": now}).Error; err != nil {
		t.Fatalf("seed last time: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	sumRepo := repo.NewConversationSummaryRepo(gdb)
	gen := &fakeConversationSummaryGenerator{output: "用户咨询了会员Agent能力（已解答）"}
	store := NewDBMemoryStore(gdb, convRepo, msgRepo, sumRepo, gen, true, 1, 120, 0)
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{UserID: "user-1"})

	if _, err := store.AppendMessage(ctx, "conv-1", chat.NewUserMessage("会员Agent支持哪些能力？")); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, err := store.AppendMessage(ctx, "conv-1", chat.NewAssistantMessage("支持知识库问答")); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	if gen.calls != 1 {
		t.Fatalf("expected summary generator to be called once, got %d", gen.calls)
	}

	var summary conversationModel.ConversationSummary
	if err := gdb.Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").
		Order("create_time DESC").
		First(&summary).Error; err != nil {
		t.Fatalf("summary should be created: %v", err)
	}
	if summary.Content != gen.output {
		t.Fatalf("unexpected summary content: %q", summary.Content)
	}

	history, err := store.LoadHistory(ctx, "conv-1")
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected summary plus 2 messages, got %d", len(history))
	}
	if history[0].Role != chat.RoleSystem {
		t.Fatalf("expected summary as first system message, got %s", history[0].Role)
	}
	if history[0].Content != "历史摘要："+gen.output {
		t.Fatalf("unexpected summary history content: %q", history[0].Content)
	}
}

func TestConversationService_DeleteConversationRemovesSummary(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "标题",
		LastTime:       time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Create(&conversationModel.ConversationSummary{
		ConversationID: "conv-1",
		UserID:         "user-1",
		LastMessageID:  "msg-1",
		Content:        "摘要内容",
	}).Error; err != nil {
		t.Fatalf("seed summary: %v", err)
	}

	convRepo := repo.NewConversationRepo(gdb)
	msgRepo := repo.NewMessageRepo(gdb)
	fbRepo := repo.NewFeedbackRepo(gdb)
	sumRepo := repo.NewConversationSummaryRepo(gdb)
	svc := NewConversationService(convRepo, msgRepo, fbRepo, sumRepo)

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{UserID: "user-1"})
	if err := svc.DeleteConversation(ctx, "conv-1", "user-1"); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}

	var activeCount int64
	if err := gdb.Scopes(db.NotDeletedScope()).
		Model(&conversationModel.ConversationSummary{}).
		Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count summary: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected summary to be soft deleted, got %d active rows", activeCount)
	}
}
