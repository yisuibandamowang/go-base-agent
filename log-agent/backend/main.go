package main

import (
	"log/slog"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := loadConfig()
	reader := LogReader(NewScriptLogReader(cfg.LogReader))
	reader = NewAnalyzingLogReader(reader, NewZhinuoAnalyzer(cfg.Analyzer, nil), cfg.Analyzer)
	router := newRouter(cfg, reader)

	slog.Info("log-agent backend starting", "addr", cfg.Address, "script_path", cfg.LogReader.ScriptPath)
	if err := router.Run(cfg.Address); err != nil {
		slog.Error("log-agent backend stopped", "err", err)
		os.Exit(1)
	}
}
