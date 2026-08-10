package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	"go-base-agent/internal/framework/cache"

	"github.com/redis/go-redis/v9"
)

const answerCacheKeyPrefix = "ragent:rag:answer:"

// CachedAnswer is a completed RAG answer stored for deterministic repeated Q&A.
type CachedAnswer struct {
	Content          string `json:"content"`
	ThinkingContent  string `json:"thinkingContent,omitempty"`
	ThinkingDuration int    `json:"thinkingDuration,omitempty"`
	Citations        string `json:"citations,omitempty"`
}

func (a CachedAnswer) fullContent() string {
	return a.Content + a.Citations
}

// AnswerCacheManager manages completed answer cache entries.
type AnswerCacheManager interface {
	LoadAnswer(ctx context.Context, key string) (*CachedAnswer, bool, error)
	SaveAnswer(ctx context.Context, key string, answer CachedAnswer, ttl time.Duration) error
}

// RedisAnswerCacheManager stores completed RAG answers in Redis.
type RedisAnswerCacheManager struct {
	cache *cache.RedisCache
}

// NewRedisAnswerCacheManager creates a Redis-backed completed answer cache manager.
func NewRedisAnswerCacheManager(client *redis.Client) *RedisAnswerCacheManager {
	if client == nil {
		return &RedisAnswerCacheManager{}
	}
	return &RedisAnswerCacheManager{cache: cache.New(client)}
}

// LoadAnswer loads a completed answer from Redis.
func (m *RedisAnswerCacheManager) LoadAnswer(ctx context.Context, key string) (*CachedAnswer, bool, error) {
	if m == nil || m.cache == nil {
		return nil, false, nil
	}
	var answer CachedAnswer
	if err := m.cache.GetJSON(ctx, key, &answer); err != nil {
		return nil, false, fmt.Errorf("load answer cache: %w", err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		return nil, false, nil
	}
	return &answer, true, nil
}

// SaveAnswer stores a completed answer in Redis.
func (m *RedisAnswerCacheManager) SaveAnswer(ctx context.Context, key string, answer CachedAnswer, ttl time.Duration) error {
	if m == nil || m.cache == nil || ttl <= 0 || strings.TrimSpace(answer.Content) == "" {
		return nil
	}
	if err := m.cache.SetJSON(ctx, key, answer, ttl); err != nil {
		return fmt.Errorf("save answer cache: %w", err)
	}
	return nil
}

func buildAnswerCacheKey(question string, deepThinking bool, scopeParts []string) string {
	normalizedQuestion := normalizeAnswerCacheQuestion(question)
	scopes := make([]string, 0, len(scopeParts))
	for _, part := range scopeParts {
		part = strings.TrimSpace(part)
		if part != "" {
			scopes = append(scopes, part)
		}
	}
	parts := []string{
		"v2",
		normalizedQuestion,
		fmt.Sprintf("deepThinking=%t", deepThinking),
		"scope=" + strings.Join(scopes, "\n"),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return answerCacheKeyPrefix + hex.EncodeToString(sum[:])
}

func answerCacheEvidenceKeys(chunks []RetrievedChunk) []string {
	keys := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		textHash := sha256.Sum256([]byte(chunk.Text))
		meta := chunk.Metadata
		keys = append(keys, strings.Join([]string{
			"doc=" + strings.TrimSpace(meta["doc_id"]),
			"chunk=" + strings.TrimSpace(chunk.ID),
			"index=" + strings.TrimSpace(meta["chunk_index"]),
			"sourceHash=" + strings.TrimSpace(firstNonEmpty(meta["source_hash"], meta["content_hash"])),
			"sourceVersion=" + strings.TrimSpace(meta["source_version"]),
			"text=" + hex.EncodeToString(textHash[:]),
		}, "|"))
	}
	return keys
}

func normalizeAnswerCacheQuestion(question string) string {
	question = strings.TrimSpace(question)
	var b strings.Builder
	lastSpace := false
	for _, r := range question {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(b.String())
}
