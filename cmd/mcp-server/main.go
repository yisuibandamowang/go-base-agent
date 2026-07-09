package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/mcp_tool"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"
	"go-base-agent/internal/infra/embedding"
	"go-base-agent/internal/infra/model"
	"go-base-agent/internal/infra/rerank"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load("configs/config.yaml")
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		slog.Error("database not available", "err", err)
		os.Exit(1)
	}

	_, embService := setupAI(cfg, logger)

	kbRepo := repo.NewKnowledgeBaseRepo(gormDB)
	docRepo := repo.NewKnowledgeDocumentRepo(gormDB)
	chunkRepo := repo.NewKnowledgeChunkRepo(gormDB)

	tools := mcp_tool.RegisterTools(gormDB, embService, kbRepo, docRepo, chunkRepo)

	mcpSrv := mcp_tool.NewServer(tools)

	port := 9099
	if len(cfg.RAG.MCP.Servers) > 0 {
		// Port is hardcoded for now; could parse from URL in future
	}

	mux := http.NewServeMux()
	mux.Handle("/", mcpSrv)

	addr := fmt.Sprintf(":%d", port)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("mcp-server starting", "port", port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("mcp-server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("mcp-server shutting down...")
	if err := db.Close(gormDB); err != nil {
		slog.Error("failed to close database", "err", err)
	}
	slog.Info("mcp-server exited")
}

func setupAI(cfg *config.Config, logger *slog.Logger) (chat.LLMService, embedding.Service) {
	aiCfg := cfg.AI

	health := model.NewHealthStore(aiCfg.Selection)
	selector := model.NewSelector(aiCfg, health)
	executor := model.NewRoutingExecutor(health)

	chatClients := buildChatClients(aiCfg)
	llmService := chat.NewRoutingLLMService(
		selector, health, executor,
		chatClients,
		chat.NewFirstPacketProbe(),
		time.Duration(aiCfg.Selection.FirstPacketTimeoutSeconds)*time.Second,
	)

	embClients := buildEmbeddingClients(aiCfg)
	embService := embedding.NewRoutingEmbeddingService(
		executor, selector,
		embClients,
		embeddingDimension(aiCfg),
	)

	rerankClients := buildRerankClients(aiCfg)
	rerankService := rerank.NewRoutingRerankService(executor, selector, rerankClients)

	logger.Info("ai infra wired",
		"chat_providers", len(chatClients),
		"embed_providers", len(embClients),
		"rerank_providers", len(rerankClients),
	)

	_ = rerankService
	return llmService, embService
}

func buildChatClients(aiCfg config.AIConfig) []chat.ChatClient {
	var clients []chat.ChatClient
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			clients = append(clients, chat.NewOpenAICompatibleChatClient(name, nil))
		case "noop":
		default:
			slog.Warn("unknown protocol, skipping", "provider", name, "protocol", provider.Protocol)
		}
	}
	return clients
}

func buildEmbeddingClients(aiCfg config.AIConfig) []embedding.Client {
	var clients []embedding.Client
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			clients = append(clients, embedding.NewOpenAICompatibleEmbeddingClient(name, nil))
		case "noop":
		default:
		}
	}
	return clients
}

func buildRerankClients(aiCfg config.AIConfig) []rerank.Client {
	return []rerank.Client{&rerank.NoopClient{}}
}

func embeddingDimension(aiCfg config.AIConfig) int {
	if len(aiCfg.Embedding.Candidates) > 0 {
		if d := aiCfg.Embedding.Candidates[0].Dimension; d > 0 {
			return d
		}
	}
	return 1536
}
