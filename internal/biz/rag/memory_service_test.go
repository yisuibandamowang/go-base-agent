package rag

import (
	"context"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type testMemoryStore struct {
	messages     []chat.Message
	conversation *Conversation
}

func (s *testMemoryStore) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return s.messages, nil
}
func (s *testMemoryStore) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	s.messages = append(s.messages, msg)
	return "msg-1", nil
}
func (s *testMemoryStore) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return s.conversation, nil
}
func (s *testMemoryStore) UpdateTitle(ctx context.Context, conversationID, title string) error {
	return nil
}

func TestDefaultMemoryService_LoadHistory(t *testing.T) {
	messages := make([]chat.Message, 0, 6)
	for range 6 {
		messages = append(messages, chat.NewUserMessage("hi"))
	}

	store := &testMemoryStore{messages: messages}
	svc := NewDefaultMemoryService(store, 2)

	history, err := svc.LoadHistory(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4 messages (2 turns * 2), got %d", len(history))
	}
}

func TestDefaultMemoryService_LoadHistory_PreservesSummaryWhenTruncating(t *testing.T) {
	messages := []chat.Message{chat.NewSystemMessage("历史摘要：用户已经咨询过会员能力")}
	for range 6 {
		messages = append(messages, chat.NewUserMessage("hi"))
	}

	store := &testMemoryStore{messages: messages}
	svc := NewDefaultMemoryService(store, 2)

	history, err := svc.LoadHistory(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 5 {
		t.Fatalf("expected summary plus 4 recent messages, got %d", len(history))
	}
	if history[0].Role != chat.RoleSystem || history[0].Content != "历史摘要：用户已经咨询过会员能力" {
		t.Fatalf("summary should be preserved as first message, got %+v", history[0])
	}
}

func TestDefaultMemoryService_LoadHistory_UnderLimit(t *testing.T) {
	messages := []chat.Message{chat.NewUserMessage("hi")}
	store := &testMemoryStore{messages: messages}
	svc := NewDefaultMemoryService(store, 2)

	history, err := svc.LoadHistory(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}
}

func TestDefaultMemoryService_SaveMessage(t *testing.T) {
	store := &testMemoryStore{}
	svc := NewDefaultMemoryService(store, 2)

	err := svc.SaveMessage(context.Background(), "conv-1", chat.NewUserMessage("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.messages) != 1 {
		t.Fatal("message should be saved")
	}
}

func TestDefaultMemoryService_LoadConversation(t *testing.T) {
	store := &testMemoryStore{conversation: &Conversation{ID: "conv-1", Title: "会员咨询"}}
	svc := NewDefaultMemoryService(store, 2)

	conv, err := svc.LoadConversation(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conv == nil || conv.Title != "会员咨询" {
		t.Fatalf("unexpected conversation: %+v", conv)
	}
}
