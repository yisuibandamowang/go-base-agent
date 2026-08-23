package service

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/framework/cache"

	"github.com/redis/go-redis/v9"
)

const (
	agentPromptCacheKey = "ragent:agent:resolved-prompts"
	agentPromptCacheTTL = time.Hour
)

// PromptCacheManager manages resolved prompts shared by service instances.
type PromptCacheManager interface {
	Load(ctx context.Context) (map[string]string, bool, error)
	Save(ctx context.Context, prompts map[string]string) error
}

// RedisPromptCacheManager stores resolved prompts in Redis.
type RedisPromptCacheManager struct {
	cache *cache.RedisCache
}

// NewRedisPromptCacheManager creates a Redis-backed prompt cache manager.
func NewRedisPromptCacheManager(client *redis.Client) *RedisPromptCacheManager {
	if client == nil {
		return &RedisPromptCacheManager{}
	}
	return &RedisPromptCacheManager{cache: cache.New(client)}
}

// Load loads the shared prompt map and distinguishes an empty cached map from a miss.
func (m *RedisPromptCacheManager) Load(ctx context.Context) (map[string]string, bool, error) {
	if m == nil || m.cache == nil {
		return nil, false, nil
	}
	hit, err := m.cache.Exists(ctx, agentPromptCacheKey)
	if err != nil {
		return nil, false, fmt.Errorf("check agent prompts cache: %w", err)
	}
	if !hit {
		return nil, false, nil
	}
	var prompts map[string]string
	if err := m.cache.GetJSON(ctx, agentPromptCacheKey, &prompts); err != nil {
		return nil, false, fmt.Errorf("load agent prompts cache: %w", err)
	}
	if prompts == nil {
		prompts = map[string]string{}
	}
	return prompts, true, nil
}

// Save stores the resolved prompt map with a bounded TTL.
func (m *RedisPromptCacheManager) Save(ctx context.Context, prompts map[string]string) error {
	if m == nil || m.cache == nil {
		return nil
	}
	if prompts == nil {
		prompts = map[string]string{}
	}
	if err := m.cache.SetJSON(ctx, agentPromptCacheKey, prompts, agentPromptCacheTTL); err != nil {
		return fmt.Errorf("save agent prompts cache: %w", err)
	}
	return nil
}
