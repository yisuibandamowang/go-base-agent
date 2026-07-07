package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/exception"
	"go-base-agent/internal/framework/middleware"
)

func TestRecover_PanicReturnsResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.Recover())
	r.GET("/panic", func(c *gin.Context) {
		panic(exception.NewServiceError("something went wrong"))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/panic", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"code"`) || !contains(body, `"B000001"`) {
		t.Fatalf("expected error result, got: %s", body)
	}
}

func TestHealthEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.Recover())
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, convention.Success("ok"))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"code":"0"`) || !contains(body, `"data":"ok"`) {
		t.Fatalf("expected success result, got: %s", body)
	}
}

func TestTraceID_InjectsHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.TraceID())
	r.GET("/trace", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/trace", nil)
	r.ServeHTTP(w, req)

	traceID := w.Header().Get("X-Trace-Id")
	if traceID == "" {
		t.Fatal("expected X-Trace-Id header")
	}
}

func TestRecover_ClientErrorReturnsResult(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.Recover())
	r.GET("/client-error", func(c *gin.Context) {
		panic(exception.NewClientError("bad input"))
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/client-error", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"code":"A000001"`) {
		t.Fatalf("expected client error code, got: %s", body)
	}
}

func TestRecover_GenericPanicReturnsServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.Recover())
	r.GET("/generic", func(c *gin.Context) {
		panic("unexpected string panic")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/generic", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, `"code":"B000001"`) {
		t.Fatalf("expected service error code for generic panic, got: %s", body)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
