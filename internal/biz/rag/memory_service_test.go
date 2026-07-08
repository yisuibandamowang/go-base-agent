package rag

import (
	"context"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type testMemoryStore struct {
	messages []chat.Message
}

func (s *testMemoryStore) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return s.messages, nil
}
func (s *testMemoryStore) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	s.messages = append(s.messages, msg)
	return "msg-1", nil
}
func (s *testMemoryStore) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return nil, nil
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
