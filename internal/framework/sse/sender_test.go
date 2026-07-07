package sse_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"go-base-agent/internal/framework/sse"
)

func setupGin() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestSend_SingleEvent(t *testing.T) {
	r := setupGin()
	r.GET("/sse", func(c *gin.Context) {
		sender := sse.NewSender(c)
		defer sender.Close()

		if err := sender.Send("meta", `{"convId":"c1"}`); err != nil {
			t.Errorf("send failed: %v", err)
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "event: meta") {
		t.Fatalf("expected event meta, got: %s", body)
	}
	if !strings.Contains(body, `data: {"convId":"c1"}`) {
		t.Fatalf("expected data, got: %s", body)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got: %s", w.Header().Get("Content-Type"))
	}
}

func TestSend_MultipleEvents(t *testing.T) {
	r := setupGin()
	r.GET("/sse", func(c *gin.Context) {
		sender := sse.NewSender(c)
		defer sender.Close()

		sender.Send("meta", `{"id":1}`)
		sender.Send("message", "hello")
		sender.Send("finish", "done")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "event: meta") {
		t.Fatal("missing meta event")
	}
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
	if !strings.Contains(body, "event: finish") {
		t.Fatal("missing finish event")
	}
}

func TestSend_ReturnsErrorAfterClose(t *testing.T) {
	r := setupGin()
	r.GET("/sse", func(c *gin.Context) {
		sender := sse.NewSender(c)
		sender.Send("meta", "ok")
		sender.Close()

		err := sender.Send("meta", "should fail")
		if err == nil {
			t.Fatal("expected error after close")
		}
		if !strings.Contains(err.Error(), "already closed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
	r.ServeHTTP(w, req)
}

func TestSend_ConcurrentSafety(t *testing.T) {
	r := setupGin()
	r.GET("/sse", func(c *gin.Context) {
		sender := sse.NewSender(c)
		defer sender.Close()

		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				sender.Send("message", fmt.Sprintf("msg-%d", n))
			}(i)
		}
		wg.Wait()
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
	r.ServeHTTP(w, req)

	body := w.Body.String()
	count := strings.Count(body, "event: message")
	if count != 10 {
		t.Fatalf("expected 10 events, got %d. body: %s", count, body)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/sse", func(c *gin.Context) {
		sender := sse.NewSender(c)
		if sender.IsClosed() {
			t.Fatal("expected not closed initially")
		}
		sender.Close()
		if !sender.IsClosed() {
			t.Fatal("expected closed after Close()")
		}
		sender.Close()
		if !sender.IsClosed() {
			t.Fatal("expected still closed")
		}
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/sse", nil)
	r.ServeHTTP(w, req)
}
