package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache is a Redis-backed string cache with JSON marshalling support.
type RedisCache struct {
	client *redis.Client
}

// New creates a RedisCache.
func New(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// Set stores a string value with TTL.
func (c *RedisCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

// SetJSON marshals v to JSON and stores it.
func (c *RedisCache) SetJSON(ctx context.Context, key string, v interface{}, ttl time.Duration) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cache marshal: %w", err)
	}
	return c.Set(ctx, key, string(data), ttl)
}

// Get retrieves a string value.
func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cache get: %w", err)
	}
	return val, nil
}

// GetJSON retrieves and unmarshals JSON into dest.
func (c *RedisCache) GetJSON(ctx context.Context, key string, dest interface{}) error {
	data, err := c.Get(ctx, key)
	if err != nil {
		return err
	}
	if data == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(data), dest); err != nil {
		return fmt.Errorf("cache unmarshal: %w", err)
	}
	return nil
}

// Delete removes a key.
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// Exists checks if a key exists.
func (c *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	n, err := c.client.Exists(ctx, key).Result()
	return n > 0, err
}

// TTL returns remaining TTL of a key.
func (c *RedisCache) TTL(ctx context.Context, key string) (time.Duration, error) {
	return c.client.TTL(ctx, key).Result()
}

// Incr atomically increments a key and returns the new value.
func (c *RedisCache) Incr(ctx context.Context, key string) (int64, error) {
	return c.client.Incr(ctx, key).Result()
}

// Expire sets a new TTL on an existing key.
func (c *RedisCache) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.client.Expire(ctx, key, ttl).Err()
}

// Client returns the underlying Redis client.
func (c *RedisCache) Client() *redis.Client {
	return c.client
}
