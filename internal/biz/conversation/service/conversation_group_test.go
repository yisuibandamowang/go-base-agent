package service

import (
	"context"
	"testing"
	"time"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConversationGroupServiceQueriesConversationWindow(t *testing.T) {
	ctx := context.Background()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Conversation{}, &conversationModel.Message{}, &conversationModel.ConversationSummary{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	conv := &conversationModel.Conversation{ConversationID: "conv-1", UserID: "user-1", Title: "会员咨询", LastTime: time.Now()}
	if err := gdb.Create(conv).Error; err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	messages := []conversationModel.Message{
		{ConversationID: "conv-1", UserID: "user-1", Role: "user", Content: "第一问"},
		{ConversationID: "conv-1", UserID: "user-1", Role: "assistant", Content: "第一答"},
		{ConversationID: "conv-1", UserID: "user-1", Role: "user", Content: "第二问"},
		{ConversationID: "conv-1", UserID: "user-2", Role: "user", Content: "别人问题"},
	}
	for i := range messages {
		if err := gdb.Create(&messages[i]).Error; err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}
	summaries := []conversationModel.ConversationSummary{
		{
			ConversationID: "conv-1",
			UserID:         "user-1",
			LastMessageID:  messages[1].ID,
			Content:        "时间较新的旧摘要",
		},
		{
			ConversationID: "conv-1",
			UserID:         "user-1",
			LastMessageID:  messages[2].ID,
			Content:        "ID 较新的摘要",
		},
	}
	summaries[0].ID = "10"
	summaries[0].CreateTime = time.Now().Add(time.Minute)
	summaries[1].ID = "20"
	summaries[1].CreateTime = time.Now()
	for i := range summaries {
		if err := gdb.Create(&summaries[i]).Error; err != nil {
			t.Fatalf("seed summary %d: %v", i, err)
		}
	}

	svc := NewConversationGroupService(
		repo.NewConversationRepo(gdb),
		repo.NewMessageRepo(gdb),
		repo.NewConversationSummaryRepo(gdb),
	)

	latest, err := svc.ListLatestUserOnlyMessages(ctx, "conv-1", "user-1", 2)
	if err != nil {
		t.Fatalf("list latest user messages: %v", err)
	}
	if len(latest) != 2 || latest[0].Content != "第二问" || latest[1].Content != "第一问" {
		t.Fatalf("unexpected latest user messages: %+v", latest)
	}

	between, err := svc.ListMessagesBetweenIDs(ctx, "conv-1", "user-1", messages[0].ID, messages[2].ID)
	if err != nil {
		t.Fatalf("list messages between ids: %v", err)
	}
	if len(between) != 1 || between[0].Content != "第一答" {
		t.Fatalf("unexpected between messages: %+v", between)
	}

	count, err := svc.CountUserMessages(ctx, "conv-1", "user-1")
	if err != nil {
		t.Fatalf("count user messages: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two user messages, got %d", count)
	}

	loadedSummary, err := svc.FindLatestSummary(ctx, "conv-1", "user-1")
	if err != nil {
		t.Fatalf("find latest summary: %v", err)
	}
	if loadedSummary == nil || loadedSummary.Content != "ID 较新的摘要" {
		t.Fatalf("unexpected summary: %+v", loadedSummary)
	}

	loadedConv, err := svc.FindConversation(ctx, "conv-1", "user-1")
	if err != nil {
		t.Fatalf("find conversation: %v", err)
	}
	if loadedConv == nil || loadedConv.Title != "会员咨询" {
		t.Fatalf("unexpected conversation: %+v", loadedConv)
	}
}
