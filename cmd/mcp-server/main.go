package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	slog.Info("mcp-server starting (placeholder, 阶段 6 实现)")
	slog.Info("mcp-server port: 9099")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("mcp-server exited")
}
