package chat

import "context"

// LLMService is the top-level chat service interface consumed by business layers.
// Aligns with Java LLMService.
type LLMService interface {
	// Chat performs a synchronous chat with the default model.
	Chat(ctx context.Context, req Request) (string, error)

	// ChatWithModel performs a synchronous chat with a specific model ID.
	ChatWithModel(ctx context.Context, req Request, modelID string) (string, error)

	// StreamChat performs a streaming chat with the default model.
	StreamChat(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error)
}
