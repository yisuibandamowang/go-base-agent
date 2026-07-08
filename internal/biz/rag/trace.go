package rag

import (
	"context"
	"log/slog"

	appctx "go-base-agent/internal/framework/context"
)

// TraceLogger returns a logger with trace_id from context.
func TraceLogger(ctx context.Context) *slog.Logger {
	traceID := appctx.TraceID(ctx)
	if traceID == "" {
		return slog.Default()
	}
	return slog.Default().With("trace_id", traceID)
}

// WithTrace returns a context with the given trace_id injected.
func WithTrace(ctx context.Context, traceID string) context.Context {
	return appctx.WithTraceID(ctx, traceID)
}
