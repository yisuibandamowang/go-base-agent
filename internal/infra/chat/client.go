package chat

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// ChatClient is a provider-specific chat implementation.
// Aligns with Java ChatClient.
type ChatClient interface {
	// Provider returns the provider identifier (e.g. "openai", "anthropic").
	Provider() string

	// Chat performs a synchronous chat request.
	Chat(ctx context.Context, req Request, target model.Target) (string, error)

	// StreamChat performs a streaming chat request.
	// The caller should call cancelFn or StreamHandle.Cancel on the returned handle to release resources.
	StreamChat(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error)
}
