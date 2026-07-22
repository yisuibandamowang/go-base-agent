package rag

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/cache"

	"github.com/redis/go-redis/v9"
)

const (
	intentTreeCacheKey = "ragent:intent:tree"
	intentTreeCacheTTL = 7 * 24 * time.Hour
)

// IntentNodeCacheManager manages cached intent tree nodes.
type IntentNodeCacheManager interface {
	LoadNodes(ctx context.Context) ([]intentModel.IntentNode, bool, error)
	SaveNodes(ctx context.Context, nodes []intentModel.IntentNode) error
	ClearNodes(ctx context.Context) error
}

// RedisIntentNodeCacheManager stores intent tree nodes in Redis.
type RedisIntentNodeCacheManager struct {
	cache *cache.RedisCache
}

// NewRedisIntentNodeCacheManager creates a Redis-backed intent tree cache manager.
func NewRedisIntentNodeCacheManager(client *redis.Client) *RedisIntentNodeCacheManager {
	if client == nil {
		return &RedisIntentNodeCacheManager{}
	}
	return &RedisIntentNodeCacheManager{cache: cache.New(client)}
}

// LoadNodes loads cached intent tree nodes.
func (m *RedisIntentNodeCacheManager) LoadNodes(ctx context.Context) ([]intentModel.IntentNode, bool, error) {
	if m == nil || m.cache == nil {
		return nil, false, nil
	}
	var nodes []intentModel.IntentNode
	if err := m.cache.GetJSON(ctx, intentTreeCacheKey, &nodes); err != nil {
		return nil, false, fmt.Errorf("load intent tree cache: %w", err)
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	return nodes, true, nil
}

// SaveNodes stores intent tree nodes in cache.
func (m *RedisIntentNodeCacheManager) SaveNodes(ctx context.Context, nodes []intentModel.IntentNode) error {
	if m == nil || m.cache == nil {
		return nil
	}
	if err := m.cache.SetJSON(ctx, intentTreeCacheKey, nodes, intentTreeCacheTTL); err != nil {
		return fmt.Errorf("save intent tree cache: %w", err)
	}
	return nil
}

// ClearNodes removes cached intent tree nodes.
func (m *RedisIntentNodeCacheManager) ClearNodes(ctx context.Context) error {
	if m == nil || m.cache == nil {
		return nil
	}
	if err := m.cache.Delete(ctx, intentTreeCacheKey); err != nil {
		return fmt.Errorf("clear intent tree cache: %w", err)
	}
	return nil
}

type cachedIntentNodeLister struct {
	next  IntentNodeLister
	cache IntentNodeCacheManager
}

// NewCachedIntentNodeLister wraps an intent node lister with Redis cache support.
func NewCachedIntentNodeLister(next IntentNodeLister, cache IntentNodeCacheManager) IntentNodeLister {
	return &cachedIntentNodeLister{next: next, cache: cache}
}

// ListAll loads intent nodes from cache first, then falls back to the wrapped lister.
func (l *cachedIntentNodeLister) ListAll(ctx context.Context) ([]intentModel.IntentNode, error) {
	if l == nil || l.next == nil {
		return nil, nil
	}
	if l.cache != nil {
		nodes, hit, err := l.cache.LoadNodes(ctx)
		if err != nil {
			slog.Warn("load cached intent nodes failed", "err", err)
		} else if hit {
			return nodes, nil
		}
	}
	nodes, err := l.next.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if l.cache != nil {
		if err := l.cache.SaveNodes(ctx, nodes); err != nil {
			slog.Warn("save cached intent nodes failed", "err", err)
		}
	}
	return nodes, nil
}
