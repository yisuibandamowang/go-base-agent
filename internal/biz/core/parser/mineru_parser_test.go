package parser

import (
	"context"
	"testing"
	"time"
)

type minerUTestPermitRunner struct {
	called  bool
	maxWait time.Duration
}

func (r *minerUTestPermitRunner) Run(ctx context.Context, maxWait, lease time.Duration, fn func() error) error {
	r.called = true
	r.maxWait = maxWait
	return fn()
}

func TestMinerUParserUsesDistributedPermitRunner(t *testing.T) {
	runner := &minerUTestPermitRunner{}
	parser := NewMinerUParser(&MinerUClient{}, &MinerUResultUnpacker{}, MinerUOptions{
		PermitRunner: runner,
		MaxWait:      17 * time.Second,
	})

	_, err := parser.Parse(context.Background(), []byte("pdf"), "application/pdf", nil)
	if err == nil {
		t.Fatal("expected MinerU client configuration error")
	}
	if !runner.called {
		t.Fatal("expected distributed permit runner to be used")
	}
	if runner.maxWait != 17*time.Second {
		t.Fatalf("expected max wait 17s, got %s", runner.maxWait)
	}
}
