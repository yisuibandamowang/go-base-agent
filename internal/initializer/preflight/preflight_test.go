package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRunChecksCoreEndpointsAndAdminLogin(t *testing.T) {
	t.Helper()

	var mu sync.Mutex
	calls := make(map[string]int)
	var loginBody map[string]any
	var currentUserAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()

		switch r.URL.Path {
		case "/health", "/readyz", "/api/ragent/health", "/api/ragent/rag/settings":
			writeSuccess(t, w, "ok")
		case "/api/ragent/auth/login":
			defer r.Body.Close()
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read login body: %v", err)
			}
			if err := json.Unmarshal(body, &loginBody); err != nil {
				t.Fatalf("decode login body: %v", err)
			}
			writeSuccess(t, w, map[string]any{
				"userId": "u-1",
				"role":   "admin",
				"token":  "token-123",
				"avatar": "avatar.png",
			})
		case "/api/ragent/auth/current-user":
			currentUserAuth = r.Header.Get("Authorization")
			writeSuccess(t, w, map[string]any{
				"userId":   "u-1",
				"id":       "u-1",
				"username": "admin",
				"role":     "admin",
				"avatar":   "avatar.png",
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var dbChecks, redisChecks int
	err := Run(context.Background(), Options{
		BaseURL:       server.URL,
		AdminUsername: "admin",
		AdminPassword: "admin",
		HTTPClient:    server.Client(),
		CheckDB: func(context.Context) error {
			dbChecks++
			return nil
		},
		CheckRedis: func(context.Context) error {
			redisChecks++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run preflight: %v", err)
	}
	if dbChecks != 1 {
		t.Fatalf("expected one db check, got %d", dbChecks)
	}
	if redisChecks != 1 {
		t.Fatalf("expected one redis check, got %d", redisChecks)
	}
	if got := currentUserAuth; got != "Bearer token-123" {
		t.Fatalf("unexpected current-user auth header: %q", got)
	}
	if got := loginBody["username"]; got != "admin" {
		t.Fatalf("unexpected login username: %#v", got)
	}
	if got := loginBody["password"]; got != "admin" {
		t.Fatalf("unexpected login password: %#v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{
		"/health",
		"/readyz",
		"/api/ragent/health",
		"/api/ragent/rag/settings",
		"/api/ragent/auth/login",
		"/api/ragent/auth/current-user",
	} {
		if calls[path] != 1 {
			t.Fatalf("expected one call for %s, got %d", path, calls[path])
		}
	}
}

func TestRunRejectsNonAdminLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/readyz", "/api/ragent/health", "/api/ragent/rag/settings":
			writeSuccess(t, w, "ok")
		case "/api/ragent/auth/login":
			writeSuccess(t, w, map[string]any{
				"userId": "u-1",
				"role":   "user",
				"token":  "token-123",
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	err := Run(context.Background(), Options{
		BaseURL:       server.URL,
		AdminUsername: "admin",
		AdminPassword: "admin",
		HTTPClient:    server.Client(),
		CheckDB: func(context.Context) error {
			return nil
		},
		CheckRedis: func(context.Context) error {
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected preflight to reject non-admin login")
	}
	if got := err.Error(); got == "" || !containsAll(got, []string{"admin", "role"}) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeSuccess(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"code":    "0",
		"message": "",
		"data":    data,
	}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !bytes.Contains([]byte(s), []byte(part)) {
			return false
		}
	}
	return true
}
