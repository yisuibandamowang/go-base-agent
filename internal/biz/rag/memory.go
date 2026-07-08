package rag

import (
	"context"

	"go-base-agent/internal/infra/chat"
)

// MemoryService loads and persists conversation history.
// Aligns with Java ConversationMemoryService.
type MemoryService interface {
	LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error)
	SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error
}

// NoopMemoryService returns empty history and discards saves.
type NoopMemoryService struct{}

func (n *NoopMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return nil, nil
}
func (n *NoopMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	return nil
}
