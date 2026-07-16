package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/framework/ratelimit"

	"github.com/gin-gonic/gin"
)

type fakeUploadLimiter struct {
	called bool
	req    ratelimit.AcquireRequest
}

func (f *fakeUploadLimiter) Acquire(_ context.Context, req ratelimit.AcquireRequest) error {
	f.called = true
	f.req = req
	if req.OnTimeout != nil {
		req.OnTimeout()
	}
	return context.DeadlineExceeded
}

func TestDocumentHandler_UploadUsesLimiterBeforeMultipartParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewDocumentHandler(&service.DocumentService{}, NewFileStore())
	limiter := &fakeUploadLimiter{}
	h.SetUploadLimiter(limiter, time.Second)

	r := gin.New()
	r.POST("/api/ragent/knowledge-base/:id/docs/upload", h.Upload)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ragent/knowledge-base/kb-1/docs/upload", strings.NewReader("not-a-multipart-body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=broken")

	r.ServeHTTP(w, req)

	if !limiter.called {
		t.Fatal("expected upload limiter to be called")
	}
	if limiter.req.MaxWait != time.Second {
		t.Fatalf("expected max wait 1s, got %v", limiter.req.MaxWait)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "文档上传请求过于频繁") {
		t.Fatalf("expected upload timeout response, got %s", body)
	}
}
