package cleanup

import (
	"context"
	"net/http"
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
		ReleaseLock: func(context.Context, string) error {
			calls = append(calls, "release")
			return nil
		},
		ExecuteCleanup: func(context.Context, string) error {
			calls = append(calls, "cleanup")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run cleanup: %v", err)
	}
	want := []string{"preflight", "db", "redis", "lock", "cleanup", "release"}
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
