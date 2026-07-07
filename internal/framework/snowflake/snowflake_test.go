package snowflake_test

import (
	"sync"
	"testing"

	"go-base-agent/internal/framework/snowflake"
)

func TestNextID_GeneratesUniqueIDs(t *testing.T) {
	seen := make(map[int64]bool)
	for range 1000 {
		id := snowflake.NextID()
		if id <= 0 {
			t.Fatal("expected positive ID")
		}
		if seen[id] {
			t.Fatalf("duplicate ID: %d", id)
		}
		seen[id] = true
	}
}

func TestNextID_Concurrent(t *testing.T) {
	seen := sync.Map{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				id := snowflake.NextIDStr()
				if _, loaded := seen.LoadOrStore(id, true); loaded {
					t.Errorf("concurrent duplicate ID: %s", id)
				}
			}
		}()
	}
	wg.Wait()
}
