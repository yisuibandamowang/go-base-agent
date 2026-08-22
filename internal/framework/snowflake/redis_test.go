package snowflake

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestConfigureFromRedisAllocatesDistinctNodeParts(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	first, err := allocateNodeFromRedis(context.Background(), client)
	if err != nil {
		t.Fatalf("allocate first node: %v", err)
	}

	second, err := allocateNodeFromRedis(context.Background(), client)
	if err != nil {
		t.Fatalf("allocate second node: %v", err)
	}
	if first == second {
		t.Fatalf("expected distinct node IDs, got %d and %d", first, second)
	}
}

func TestConfigureFromRedisRejectsInvalidClient(t *testing.T) {
	if err := ConfigureFromRedis(context.Background(), nil); err == nil {
		t.Fatal("expected nil redis client to fail")
	}
}
