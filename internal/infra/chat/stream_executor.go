package chat

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// StreamExecutor executes a streaming HTTP request and feeds SSE lines to a callback.
type StreamExecutor struct {
	client *http.Client
}

// NewStreamExecutor creates a new StreamExecutor.
func NewStreamExecutor(client *http.Client) *StreamExecutor {
	if client == nil {
		client = http.DefaultClient
	}
	return &StreamExecutor{client: client}
}

// Execute starts a streaming request in a background goroutine.
// Returns a StreamHandle for cancellation.
func (e *StreamExecutor) Execute(
	ctx context.Context,
	req *http.Request,
	cb StreamCallback,
	reasoningEnabled bool,
) (StreamHandle, error) {
	req = req.WithContext(ctx)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}

	cancelled := &atomic.Bool{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			cb.OnError(&streamError{code: resp.StatusCode, body: string(body)})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			if cancelled.Load() {
				return
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			event := ParseSSELine(line, reasoningEnabled)
			if event.HasReasoning() {
				cb.OnThinking(event.Reasoning())
			}
			if event.HasContent() {
				cb.OnContent(event.Content())
			}
			if event.Completed() {
				cb.OnComplete()
				return
			}
		}

		if err := scanner.Err(); err != nil && !cancelled.Load() {
			cb.OnError(err)
			return
		}

		if !cancelled.Load() {
			// Stream ended normally (connection closed) without explicit finish_reason.
			// This is common with providers that don't send [DONE]. Treat as completion.
			cb.OnComplete()
		}
	}()

	return &streamHandle{cancelled: cancelled, done: done}, nil
}

type streamHandle struct {
	cancelled *atomic.Bool
	done      chan struct{}
}

func (h *streamHandle) Cancel() {
	h.cancelled.Store(true)
}

// Wait blocks until the stream completes or is cancelled.
func (h *streamHandle) Wait() {
	<-h.done
}

type streamError struct {
	code int
	body string
}

func (e *streamError) Error() string {
	if e.code != 0 {
		return "stream HTTP " + http.StatusText(e.code) + ": " + e.body
	}
	return "stream error: " + e.body
}
