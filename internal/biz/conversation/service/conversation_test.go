package service

import (
	"context"
	"testing"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	appctx "go-base-agent/internal/framework/context"
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
	store := NewDBMemoryStore(gdb, convRepo, msgRepo)
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
