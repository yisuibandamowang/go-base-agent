package rag

import (
	"context"
	"fmt"
	"strings"
	"time"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/cache"

	"github.com/redis/go-redis/v9"
)

const queryTermMappingCacheTTL = 7 * 24 * time.Hour

// QueryTermMappingCacheManager manages cached query term mappings.
type QueryTermMappingCacheManager interface {
	LoadMappings(ctx context.Context, domain string) ([]intentModel.QueryTermMapping, bool, error)
	SaveMappings(ctx context.Context, domain string, mappings []intentModel.QueryTermMapping) error
	ClearMappings(ctx context.Context, domain string) error
}

// RedisQueryTermMappingCacheManager stores term mappings in Redis.
type RedisQueryTermMappingCacheManager struct {
	cache *cache.RedisCache
}

// NewRedisQueryTermMappingCacheManager creates a Redis-backed mapping cache manager.
func NewRedisQueryTermMappingCacheManager(client *redis.Client) *RedisQueryTermMappingCacheManager {
	if client == nil {
		return &RedisQueryTermMappingCacheManager{}
	}
	return &RedisQueryTermMappingCacheManager{cache: cache.New(client)}
}

func queryTermMappingCacheKey(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = "__global__"
	}
	return "ragent:query-term:mappings:" + domain
}

// LoadMappings loads mappings from Redis.
// 一条规则都没配也要认缓存（hit=true 且 mappings 为空切片），
// 否则每次提问都会白读一次数据库；规则增删改各自会清缓存。
func (m *RedisQueryTermMappingCacheManager) LoadMappings(ctx context.Context, domain string) ([]intentModel.QueryTermMapping, bool, error) {
	if m == nil || m.cache == nil {
		return nil, false, nil
	}
	exists, err := m.cache.Exists(ctx, queryTermMappingCacheKey(domain))
	if err != nil {
		return nil, false, fmt.Errorf("check query term mappings cache: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	var mappings []intentModel.QueryTermMapping
	if err := m.cache.GetJSON(ctx, queryTermMappingCacheKey(domain), &mappings); err != nil {
		return nil, false, fmt.Errorf("load query term mappings from cache: %w", err)
	}
	return mappings, true, nil
}

// SaveMappings stores mappings in Redis.
// 空列表也写入：配合 LoadMappings 的 exists 判断，让"没配规则"同样走缓存而不回源数据库。
func (m *RedisQueryTermMappingCacheManager) SaveMappings(ctx context.Context, domain string, mappings []intentModel.QueryTermMapping) error {
	if m == nil || m.cache == nil {
		return nil
	}
	if mappings == nil {
		mappings = []intentModel.QueryTermMapping{}
	}
	if err := m.cache.SetJSON(ctx, queryTermMappingCacheKey(domain), mappings, queryTermMappingCacheTTL); err != nil {
		return fmt.Errorf("save query term mappings to cache: %w", err)
	}
	return nil
}

// ClearMappings removes cached mappings for a domain.
func (m *RedisQueryTermMappingCacheManager) ClearMappings(ctx context.Context, domain string) error {
	if m == nil || m.cache == nil {
		return nil
	}
	if err := m.cache.Delete(ctx, queryTermMappingCacheKey(domain)); err != nil {
		return fmt.Errorf("clear query term mappings cache: %w", err)
	}
	return nil
}
