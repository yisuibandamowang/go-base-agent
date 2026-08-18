package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"
	initializerPreflight "go-base-agent/internal/initializer/preflight"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: initializer preflight [flags]")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "preflight":
		if err := runPreflight(os.Args[2:]); err != nil {
			slog.Error("preflight failed", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runPreflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "config file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "admin", "admin username")
	adminPassword := fs.String("admin-password", "admin", "admin password")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := strings.TrimSpace(*baseURL)
	if serviceBaseURL == "" {
		serviceBaseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(gormDB); closeErr != nil {
			slog.Warn("close database failed", "err", closeErr)
		}
	}()

	redisClient := cfg.Redis.NewClient()
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			slog.Warn("close redis client failed", "err", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis check failed: %w", err)
	}

	slog.Info("preflight checks start", "base_url", serviceBaseURL)
	if err := initializerPreflight.Run(context.Background(), initializerPreflight.Options{
		BaseURL:       serviceBaseURL,
		AdminUsername: *adminUsername,
		AdminPassword: *adminPassword,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		CheckRedis: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return redisClient.Ping(pingCtx).Err()
		},
	}); err != nil {
		return err
	}
	slog.Info("preflight checks passed", "base_url", serviceBaseURL)
	return nil
}
