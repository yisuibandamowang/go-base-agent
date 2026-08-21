package cleanup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	initializerPreflight "go-base-agent/internal/initializer/preflight"
)

func TestRunExecutesPreflightThenCleanup(t *testing.T) {
	t.Helper()

	var calls []string
	err := Run(context.Background(), Options{
		BaseURL:       "http://127.0.0.1:9090",
		AdminUsername: "admin",
		AdminPassword: "admin",
		Confirm:       confirmationToken,
		CleanupFile:   "resources/database/cleanup_pg.sql",
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		CheckDB: func(context.Context) error {
			calls = append(calls, "db")
			return nil
		},
		CheckRedis: func(context.Context) error {
			calls = append(calls, "redis")
			return nil
		},
		RunPreflight: func(ctx context.Context, opts initializerPreflight.Options) error {
			calls = append(calls, "preflight")
			if opts.BaseURL != "http://127.0.0.1:9090" {
				t.Fatalf("unexpected base url: %s", opts.BaseURL)
			}
			if opts.AdminUsername != "admin" || opts.AdminPassword != "admin" {
				t.Fatalf("unexpected admin creds: %#v", opts)
			}
			if opts.HTTPClient == nil {
				t.Fatal("expected http client")
			}
			if opts.CheckDB == nil || opts.CheckRedis == nil {
				t.Fatal("expected check funcs")
			}
			if err := opts.CheckDB(ctx); err != nil {
				return err
			}
			if err := opts.CheckRedis(ctx); err != nil {
				return err
			}
			return nil
		},
		AcquireLock: func(context.Context, string, time.Duration) (bool, error) {
			calls = append(calls, "lock")
			return true, nil
		},
		DeleteDocuments: func(context.Context) error {
			calls = append(calls, "documents")
			return nil
		},
		ReleaseLock: func(context.Context, string) error {
			calls = append(calls, "release")
			return nil
		},
		ExecuteCleanup: func(context.Context, string) error {
			calls = append(calls, "cleanup")
			return nil
		},
		ClearCache: func(context.Context) error {
			calls = append(calls, "cache")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run cleanup: %v", err)
	}
	want := []string{"preflight", "db", "redis", "lock", "documents", "cleanup", "cache", "release"}
	if len(calls) != len(want) {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	for i, got := range calls {
		if got != want[i] {
			t.Fatalf("unexpected call order: %#v", calls)
		}
	}
}

func TestRunRejectsBadConfirm(t *testing.T) {
	err := Run(context.Background(), Options{
		Confirm:     "wrong",
		CleanupFile: "resources/database/cleanup_pg.sql",
		RunPreflight: func(context.Context, initializerPreflight.Options) error {
			t.Fatal("preflight should not run")
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteRemoteDocumentsDeletesCompletedDocuments(t *testing.T) {
	var deletedID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ragent/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"token": "token", "role": "admin",
			}})
		case r.URL.Path == "/api/ragent/knowledge-base" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"id": "kb-1"}}, "pages": 1,
			}})
		case r.URL.Path == "/api/ragent/knowledge-base/kb-1/docs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"records": []map[string]any{{"id": "doc-1", "docName": "a.md", "status": "success"}}, "pages": 1,
			}})
		case strings.HasPrefix(r.URL.Path, "/api/ragent/knowledge-base/docs/") && r.Method == http.MethodDelete:
			deletedID = strings.TrimPrefix(r.URL.Path, "/api/ragent/knowledge-base/docs/")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := deleteRemoteDocuments(context.Background(), Options{
		BaseURL:       server.URL,
		AdminUsername: "admin",
		AdminPassword: "admin",
		HTTPClient:    server.Client(),
	})
	if err != nil {
		t.Fatalf("deleteRemoteDocuments: %v", err)
	}
	if deletedID != "doc-1" {
		t.Fatalf("expected doc-1 to be deleted, got %q", deletedID)
	}
}
