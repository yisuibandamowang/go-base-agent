package chat

import (
	"context"
	"errors"
	"fmt"
)

// FallbackLLMService tries a primary LLM service first and delegates to fallback on failure.
type FallbackLLMService struct {
	primary  LLMService
	fallback LLMService
}

// NewFallbackLLMService creates an LLM service with primary-first fallback behavior.
func NewFallbackLLMService(primary, fallback LLMService) *FallbackLLMService {
	return &FallbackLLMService{primary: primary, fallback: fallback}
}

// Chat performs a synchronous chat request with primary-first fallback.
func (s *FallbackLLMService) Chat(ctx context.Context, req Request) (string, error) {
	if s == nil {
		return "", errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		result, err := s.primary.Chat(ctx, req)
		if err == nil {
			return result, nil
		}
		primaryErr = err
	}
	if s.fallback == nil {
		if primaryErr != nil {
			return "", fmt.Errorf("primary chat failed: %w", primaryErr)
		}
		return "", errors.New("fallback llm service has no available backend")
	}
	result, err := s.fallback.Chat(ctx, req)
	if err != nil {
		return "", wrapFallbackError("chat", primaryErr, err)
	}
	return result, nil
}

// ChatWithTier performs a tiered primary-first chat and preserves tier routing
// when the cloud fallback also supports it.
func (s *FallbackLLMService) ChatWithTier(ctx context.Context, req Request, tier string) (string, error) {
	if s == nil {
		return "", errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		result, err := ChatWithTier(ctx, s.primary, req, tier)
		if err == nil {
			return result, nil
		}
		primaryErr = err
	}
	if s.fallback == nil {
		if primaryErr != nil {
			return "", fmt.Errorf("primary tiered chat failed: %w", primaryErr)
		}
		return "", errors.New("fallback llm service has no available backend")
	}
	result, err := ChatWithTier(ctx, s.fallback, req, tier)
	if err != nil {
		return "", wrapFallbackError("tiered chat", primaryErr, err)
	}
	return result, nil
}

// ChatWithTierAndModel routes a preferred model through a tier when available.
func (s *FallbackLLMService) ChatWithTierAndModel(ctx context.Context, req Request, tier, modelID string) (string, error) {
	if s == nil {
		return "", errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		if tiered, ok := s.primary.(TieredLLMService); ok {
			result, err := tiered.ChatWithTierAndModel(ctx, req, tier, modelID)
			if err == nil {
				return result, nil
			}
			primaryErr = err
		} else {
			result, err := s.primary.ChatWithModel(ctx, req, modelID)
			if err == nil {
				return result, nil
			}
			primaryErr = err
		}
	}
	if s.fallback == nil {
		return "", wrapFallbackError("tiered chat with model", primaryErr, errors.New("fallback llm service has no available backend"))
	}
	result, err := ChatWithTier(ctx, s.fallback, req, tier)
	if err != nil {
		return "", wrapFallbackError("tiered chat with model", primaryErr, err)
	}
	return result, nil
}

// ChatWithModel performs a model-specific chat request with primary-first fallback.
func (s *FallbackLLMService) ChatWithModel(ctx context.Context, req Request, modelID string) (string, error) {
	if modelID == "" {
		return s.Chat(ctx, req)
	}
	if s == nil {
		return "", errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		result, err := s.primary.ChatWithModel(ctx, req, modelID)
		if err == nil {
			return result, nil
		}
		primaryErr = err
	}
	if s.fallback == nil {
		if primaryErr != nil {
			return "", fmt.Errorf("primary chat with model failed: %w", primaryErr)
		}
		return "", errors.New("fallback llm service has no available backend")
	}
	result, err := s.fallback.Chat(ctx, req)
	if err != nil {
		return "", wrapFallbackError("chat with model", primaryErr, err)
	}
	return result, nil
}

// StreamChat performs a streaming chat request with primary-first fallback.
func (s *FallbackLLMService) StreamChat(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
	if s == nil {
		return nil, errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		handle, err := s.primary.StreamChat(ctx, req, cb)
		if err == nil && handle != nil {
			return handle, nil
		}
		if err != nil {
			primaryErr = err
		} else {
			primaryErr = errors.New("primary stream chat returned nil handle")
		}
	}
	if s.fallback == nil {
		if primaryErr != nil {
			return nil, fmt.Errorf("primary stream chat failed: %w", primaryErr)
		}
		return nil, errors.New("fallback llm service has no available backend")
	}
	handle, err := s.fallback.StreamChat(ctx, req, cb)
	if err != nil {
		return nil, wrapFallbackError("stream chat", primaryErr, err)
	}
	if handle == nil {
		return nil, wrapFallbackError("stream chat", primaryErr, errors.New("fallback returned nil handle"))
	}
	return handle, nil
}

// StreamChatWithTier performs a tiered primary-first stream.
func (s *FallbackLLMService) StreamChatWithTier(ctx context.Context, req Request, cb StreamCallback, tier string) (StreamHandle, error) {
	if s == nil {
		return nil, errors.New("fallback llm service is nil")
	}
	var primaryErr error
	if s.primary != nil {
		if tiered, ok := s.primary.(TieredLLMService); ok {
			handle, err := tiered.StreamChatWithTier(ctx, req, cb, tier)
			if err == nil && handle != nil {
				return handle, nil
			}
			if err != nil {
				primaryErr = err
			} else {
				primaryErr = errors.New("primary tiered stream chat returned nil handle")
			}
		} else {
			handle, err := s.primary.StreamChat(ctx, req, cb)
			if err == nil && handle != nil {
				return handle, nil
			}
			if err != nil {
				primaryErr = err
			} else {
				primaryErr = errors.New("primary stream chat returned nil handle")
			}
		}
	}
	if s.fallback == nil {
		return nil, wrapFallbackError("tiered stream chat", primaryErr, errors.New("fallback llm service has no available backend"))
	}
	handle, err := StreamChatWithTier(ctx, s.fallback, req, cb, tier)
	if err != nil || handle == nil {
		if err == nil {
			err = errors.New("fallback tiered stream chat returned nil handle")
		}
		return nil, wrapFallbackError("tiered stream chat", primaryErr, err)
	}
	return handle, nil
}

func wrapFallbackError(operation string, primaryErr, fallbackErr error) error {
	if primaryErr == nil {
		return fmt.Errorf("fallback %s failed: %w", operation, fallbackErr)
	}
	return fmt.Errorf("primary %s failed: %v; fallback %s failed: %w", operation, primaryErr, operation, fallbackErr)
}
