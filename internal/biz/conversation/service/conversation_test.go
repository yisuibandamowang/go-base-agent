package service

import (
	"context"
	"strings"
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
	output            string
	err               error
	calls             int
	histories         [][]chat.Message
	previousSummaries []string
}

func (f *fakeConversationSummaryGenerator) Generate(ctx context.Context, history []chat.Message, previousSummary string, maxChars int) (string, error) {
	f.calls++
	f.histories = append(f.histories, history)
	f.previousSummaries = append(f.previousSummaries, previousSummary)
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
	if !strings.Contains(history[0].Content, "<conversation-summary>") {
		t.Fatalf("expected wrapped summary content, got: %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, gen.output) {
		t.Fatalf("expected summary content to be preserved, got: %q", history[0].Content)
	}
}

func TestDBMemoryStore_LoadHistory_NoMessagesReturnsEmptyEvenWithSummary(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "初始标题",
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := gdb.Create(&conversationModel.ConversationSummary{
		ConversationID: "conv-1",
		UserID:         "user-1",
		LastMessageID:  "msg-1",
		Content:        "已有摘要",
	}).Error; err != nil {
		t.Fatalf("seed summary: %v", err)
	}

	store := NewDBMemoryStore(gdb, repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewConversationSummaryRepo(gdb), nil, true, 1, 120, 0)
	history, err := store.LoadHistory(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history when there are no messages, got %d", len(history))
	}
}

func TestDBMemoryStore_SummaryDoesNotRefreshWhileCoverageStillOverlapsHistoryWindow(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	seedConversationWithSummaryWindow(t, gdb, "000035", "existing summary")

	gen := &fakeConversationSummaryGenerator{output: "updated summary"}
	store := NewDBMemoryStore(
		gdb,
		repo.NewConversationRepo(gdb),
		repo.NewMessageRepo(gdb),
		repo.NewConversationSummaryRepo(gdb),
		gen,
		true,
		5,
		120,
		0,
		4,
	)

	if _, err := store.AppendMessage(context.Background(), "conv-1", chat.NewAssistantMessage("current answer")); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if gen.calls != 0 {
		t.Fatalf("expected summary refresh to be skipped while coverage overlaps history window, got %d calls", gen.calls)
	}
}

func TestDBMemoryStore_SummaryRefreshesWhenCoverageFallsBehindHistoryWindow(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	seedConversationWithSummaryWindow(t, gdb, "000015", "existing summary")

	gen := &fakeConversationSummaryGenerator{output: "updated summary"}
	store := NewDBMemoryStore(
		gdb,
		repo.NewConversationRepo(gdb),
		repo.NewMessageRepo(gdb),
		repo.NewConversationSummaryRepo(gdb),
		gen,
		true,
		5,
		120,
		0,
		4,
	)

	if _, err := store.AppendMessage(context.Background(), "conv-1", chat.NewAssistantMessage("current answer")); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if gen.calls != 1 {
		t.Fatalf("expected summary refresh to run once, got %d calls", gen.calls)
	}
	if got := gen.previousSummaries[0]; got != "existing summary" {
		t.Fatalf("expected previous summary to be merged, got %q", got)
	}
	if len(gen.histories[0]) != 4 {
		t.Fatalf("expected summarized half-window history, got %d messages", len(gen.histories[0]))
	}
	if got := gen.histories[0][len(gen.histories[0])-1].Content; got != "assistant-000031" {
		t.Fatalf("expected summary cutoff before user 000040, got last content %q", got)
	}

	var summary conversationModel.ConversationSummary
	if err := gdb.Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ? AND content = ?", "conv-1", "user-1", gen.output).
		First(&summary).Error; err != nil {
		t.Fatalf("summary should be created: %v", err)
	}
	if summary.LastMessageID != "000031" {
		t.Fatalf("expected summary last message id 000031, got %q", summary.LastMessageID)
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
	if err := gdb.Create(&conversationModel.Message{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "user",
		Content:        "问题",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Create(&conversationModel.MessageFeedback{
		MessageID:      "msg-1",
		ConversationID: "conv-1",
		UserID:         "user-1",
		Vote:           1,
	}).Error; err != nil {
		t.Fatalf("seed feedback: %v", err)
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

	if err := gdb.Scopes(db.NotDeletedScope()).
		Model(&conversationModel.Message{}).
		Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected messages to be soft deleted, got %d active rows", activeCount)
	}

	if err := gdb.Scopes(db.NotDeletedScope()).
		Model(&conversationModel.MessageFeedback{}).
		Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count feedback: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected feedback to be soft deleted, got %d active rows", activeCount)
	}
}

func TestConversationService_CreateFeedbackRejectsNonAssistantMessage(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-user"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "user",
		Content:        "问题",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	err = svc.CreateFeedback(context.Background(), struct {
		MessageID      string
		ConversationID string
		UserID         string
		Vote           int16
		Reason         string
		Comment        string
	}{
		MessageID:      "msg-user",
		ConversationID: "conv-1",
		UserID:         "user-1",
		Vote:           1,
	})
	if err == nil || !strings.Contains(err.Error(), "仅支持对助手消息反馈") {
		t.Fatalf("expected assistant-only feedback error, got %v", err)
	}
}

func TestConversationService_CreateFeedbackDerivesConversationIDFromMessage(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-real",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := svc.CreateFeedback(context.Background(), struct {
		MessageID      string
		ConversationID string
		UserID         string
		Vote           int16
		Reason         string
		Comment        string
	}{
		MessageID:      "msg-assistant",
		ConversationID: "conv-from-request",
		UserID:         "user-1",
		Vote:           -1,
		Reason:         "bad",
		Comment:        "not helpful",
	}); err != nil {
		t.Fatalf("create feedback: %v", err)
	}

	var feedback conversationModel.MessageFeedback
	if err := gdb.Where("message_id = ? AND user_id = ?", "msg-assistant", "user-1").First(&feedback).Error; err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if feedback.ConversationID != "conv-real" {
		t.Fatalf("expected conversation id from message, got %q", feedback.ConversationID)
	}
}

func TestConversationService_UpdateTitleValidatesAndTrims(t *testing.T) {
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
		Title:          "原始标题",
		LastTime:       time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	svc.SetTitleMaxChars(5)

	if err := svc.UpdateTitle(context.Background(), "conv-1", "user-1", "   "); err == nil {
		t.Fatal("expected blank title to fail")
	}
	if err := svc.UpdateTitle(context.Background(), "conv-1", "user-1", "   这个标题太长了   "); err == nil {
		t.Fatal("expected too long title to fail")
	}
	if err := svc.UpdateTitle(context.Background(), "conv-1", "user-1", "  新标题  "); err != nil {
		t.Fatalf("update title: %v", err)
	}

	var conv conversationModel.Conversation
	if err := gdb.Where("conversation_id = ? AND user_id = ?", "conv-1", "user-1").First(&conv).Error; err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if conv.Title != "新标题" {
		t.Fatalf("expected trimmed title, got %q", conv.Title)
	}
}

func seedConversationWithSummaryWindow(t *testing.T, gdb *gorm.DB, summaryLastMessageID, summaryContent string) {
	t.Helper()

	if err := gdb.Create(&conversationModel.Conversation{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Title:          "初始标题",
		LastTime:       time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	messages := []struct {
		id   string
		role string
	}{
		{"000010", "user"},
		{"000011", "assistant"},
		{"000020", "user"},
		{"000021", "assistant"},
		{"000030", "user"},
		{"000031", "assistant"},
		{"000040", "user"},
		{"000041", "assistant"},
		{"000050", "user"},
	}
	for i, item := range messages {
		createdAt := base.Add(time.Duration(i) * time.Minute)
		if err := gdb.Create(&conversationModel.Message{
			BaseModel: db.BaseModel{
				ID:         item.id,
				CreateTime: createdAt,
				UpdateTime: createdAt,
			},
			ConversationID: "conv-1",
			UserID:         "user-1",
			Role:           item.role,
			Content:        item.role + "-" + item.id,
		}).Error; err != nil {
			t.Fatalf("seed message %s: %v", item.id, err)
		}
	}
	if err := gdb.Create(&conversationModel.ConversationSummary{
		BaseModel: db.BaseModel{
			ID:         "000090",
			CreateTime: base.Add(90 * time.Minute),
			UpdateTime: base.Add(90 * time.Minute),
		},
		ConversationID: "conv-1",
		UserID:         "user-1",
		LastMessageID:  summaryLastMessageID,
		Content:        summaryContent,
	}).Error; err != nil {
		t.Fatalf("seed summary: %v", err)
	}
}
