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

// TieredLLMService is optionally implemented by model routers that support
// explicit chat tiers. Keeping it separate preserves the small business-layer
// LLMService contract used by tests and integrations.
type TieredLLMService interface {
	ChatWithTier(ctx context.Context, req Request, tier string) (string, error)
	ChatWithTierAndModel(ctx context.Context, req Request, tier, modelID string) (string, error)
	StreamChatWithTier(ctx context.Context, req Request, cb StreamCallback, tier string) (StreamHandle, error)
}

// ChatWithTier routes through an explicit tier when the service supports it.
// Older lightweight test doubles transparently retain their default behavior.
func ChatWithTier(ctx context.Context, service LLMService, req Request, tier string) (string, error) {
	if tiered, ok := service.(TieredLLMService); ok {
		return tiered.ChatWithTier(ctx, req, tier)
	}
	return service.Chat(ctx, req)
}

// StreamChatWithTier routes a stream through an explicit tier when supported.
func StreamChatWithTier(ctx context.Context, service LLMService, req Request, cb StreamCallback, tier string) (StreamHandle, error) {
	if tiered, ok := service.(TieredLLMService); ok {
		return tiered.StreamChatWithTier(ctx, req, cb, tier)
	}
	return service.StreamChat(ctx, req, cb)
}
