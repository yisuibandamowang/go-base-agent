package sse

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type Sender struct {
	c      *gin.Context
	mu     sync.Mutex
	closed atomic.Bool
}

func NewSender(c *gin.Context) *Sender {
	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	w.Flush()
	return &Sender{c: c}
}

func (s *Sender) Send(event, data string) error {
	if s.closed.Load() {
		return fmt.Errorf("sse: connection already closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := fmt.Fprintf(s.c.Writer, "event: %s\ndata: %s\n\n", event, data)
	if err != nil {
		return fmt.Errorf("sse: write event %q: %w", event, err)
	}
	s.c.Writer.Flush()
	return nil
}

func (s *Sender) Close() {
	s.closed.CompareAndSwap(false, true)
}

func (s *Sender) IsClosed() bool {
	return s.closed.Load()
}
