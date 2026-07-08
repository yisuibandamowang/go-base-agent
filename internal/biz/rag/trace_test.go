package rag

import (
	"context"
	"testing"

	appctx "go-base-agent/internal/framework/context"
)

func TestTraceLogger(t *testing.T) {
	ctx := appctx.WithTraceID(context.Background(), "test-trace-123")
	logger := TraceLogger(ctx)
	if logger == nil {
		t.Fatal("logger should not be nil")
	}
}

func TestTraceLogger_Empty(t *testing.T) {
	logger := TraceLogger(context.Background())
	if logger == nil {
		t.Fatal("logger should not be nil even without trace")
	}
}

func TestWithTrace(t *testing.T) {
	ctx := WithTrace(context.Background(), "trace-456")
	if appctx.TraceID(ctx) != "trace-456" {
		t.Fatal("trace_id not set")
	}
}
