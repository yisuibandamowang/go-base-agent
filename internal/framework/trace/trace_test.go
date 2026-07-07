package trace_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nageoffer/ragent-go/internal/framework/trace"
)

func TestTraced_SingleNode(t *testing.T) {
	tc := trace.NewTraceContext("test-run")
	ctx := trace.WithTrace(context.Background(), tc)

	fn := trace.Traced("parse", func(ctx context.Context, input string) (string, error) {
		return "parsed:" + input, nil
	})

	result, err := fn(ctx, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "parsed:hello" {
		t.Fatalf("expected parsed:hello, got %s", result)
	}

	if len(tc.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(tc.Nodes))
	}
	node := tc.Nodes[0]
	if node.NodeName != "parse" {
		t.Fatalf("expected node name 'parse', got %s", node.NodeName)
	}
	if node.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s", node.Status)
	}
	if node.DurationMs < 0 {
		t.Fatal("expected non-negative duration")
	}
	if node.Depth != 0 {
		t.Fatalf("expected depth 0, got %d", node.Depth)
	}
}

func TestTraced_ErrorNode(t *testing.T) {
	tc := trace.NewTraceContext("test-run")
	ctx := trace.WithTrace(context.Background(), tc)

	fn := trace.Traced("fetch", func(ctx context.Context, _ string) (string, error) {
		return "", errors.New("network timeout")
	})

	_, err := fn(ctx, "")
	if err == nil {
		t.Fatal("expected error")
	}

	if len(tc.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(tc.Nodes))
	}
	if tc.Nodes[0].Status != "ERROR" {
		t.Fatalf("expected ERROR status, got %s", tc.Nodes[0].Status)
	}
	if tc.Nodes[0].ErrorMessage != "network timeout" {
		t.Fatalf("expected error message, got %s", tc.Nodes[0].ErrorMessage)
	}
}

func TestTraced_NestedNodes(t *testing.T) {
	tc := trace.NewTraceContext("rag-query")
	ctx := trace.WithTrace(context.Background(), tc)

	retrieve := trace.Traced("retrieve", func(ctx context.Context, _ string) (string, error) {
		time.Sleep(10 * time.Millisecond)
		return "chunks", nil
	})

	rewrite := trace.Traced("rewrite", func(ctx context.Context, q string) (string, error) {
		return retrieve(ctx, q)
	})

	classify := trace.Traced("classify", func(ctx context.Context, q string) (string, error) {
		return rewrite(ctx, q)
	})

	result, err := classify(ctx, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "chunks" {
		t.Fatalf("expected chunks, got %s", result)
	}

	if len(tc.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(tc.Nodes))
	}

	depths := make(map[string]int)
	for _, n := range tc.Nodes {
		depths[n.NodeName] = n.Depth
	}
	if depths["classify"] != 0 {
		t.Fatalf("classify depth expected 0, got %d", depths["classify"])
	}
	if depths["rewrite"] != 1 {
		t.Fatalf("rewrite depth expected 1, got %d", depths["rewrite"])
	}
	if depths["retrieve"] != 2 {
		t.Fatalf("retrieve depth expected 2, got %d", depths["retrieve"])
	}

	// verify parent-child
	for _, n := range tc.Nodes {
		switch n.NodeName {
		case "rewrite":
			if n.ParentNodeID != tc.Nodes[0].NodeID { // classify
				t.Fatalf("rewrite parent should be classify")
			}
		case "retrieve":
			if n.ParentNodeID != tc.Nodes[1].NodeID { // rewrite
				t.Fatalf("retrieve parent should be rewrite")
			}
		}
	}
}

func TestTraced_NoTraceContext(t *testing.T) {
	fn := trace.Traced("parse", func(ctx context.Context, input string) (string, error) {
		return "ok", nil
	})

	// 没有注入 TraceContext 的 context
	result, err := fn(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected ok, got %s", result)
	}
}

func TestTracedVoid(t *testing.T) {
	tc := trace.NewTraceContext("void-task")
	ctx := trace.WithTrace(context.Background(), tc)

	called := false
	fn := trace.TracedVoid("cleanup", func(ctx context.Context) error {
		called = true
		return nil
	})

	if err := fn(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("function not called")
	}
	if len(tc.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(tc.Nodes))
	}
}

func TestTraceContext_Finish(t *testing.T) {
	tc := trace.NewTraceContext("run")
	ctx := trace.WithTrace(context.Background(), tc)

	fn := trace.Traced("step", func(ctx context.Context, _ string) (string, error) {
		return "ok", nil
	})
	fn(ctx, "")

	tc.Finish(nil)

	if tc.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s", tc.Status)
	}
	if tc.DurationMs() < 0 {
		t.Fatal("expected non-negative run duration")
	}
}

func TestTraceContext_FinishWithError(t *testing.T) {
	tc := trace.NewTraceContext("run")
	tc.Finish(errors.New("failed"))

	if tc.Status != "ERROR" {
		t.Fatalf("expected ERROR, got %s", tc.Status)
	}
	if tc.ErrorMsg != "failed" {
		t.Fatalf("expected 'failed', got %s", tc.ErrorMsg)
	}
}
