package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	conversationHandler "go-base-agent/internal/biz/conversation/handler"
	conversationRepo "go-base-agent/internal/biz/conversation/repo"
	conversationService "go-base-agent/internal/biz/conversation/service"
	intentHandler "go-base-agent/internal/biz/intent_tree/handler"
	intentRepo "go-base-agent/internal/biz/intent_tree/repo"
	intentService "go-base-agent/internal/biz/intent_tree/service"
	knowledgeHandler "go-base-agent/internal/biz/knowledge/handler"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	knowledgeService "go-base-agent/internal/biz/knowledge/service"
	"go-base-agent/internal/biz/rag"
	userHandler "go-base-agent/internal/biz/user/handler"
	userRepoPkg "go-base-agent/internal/biz/user/repo"
	userService "go-base-agent/internal/biz/user/service"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/middleware"
	"go-base-agent/internal/framework/ratelimit"
	"go-base-agent/internal/infra/chat"
	"go-base-agent/internal/infra/embedding"
	"go-base-agent/internal/infra/model"
	"go-base-agent/internal/infra/rerank"

	"github.com/gin-gonic/gin"
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
		slog.Warn("database not available, starting without DB", "err", err)
	}

	rdb := cfg.Redis.NewClient()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pingCancel()
	if _, err := rdb.Ping(pingCtx).Result(); err != nil {
		slog.Warn("redis not available, rate limiter disabled", "err", err)
	}

	queueLimiter := ratelimit.NewFairQueueLimiter(
		"rag:chat",
		rdb,
		ratelimit.LimiterConfig{
			MaxConcurrent:  cfg.RAG.RateLimit.Global.MaxConcurrent,
			MaxWaitSeconds: cfg.RAG.RateLimit.Global.MaxWaitSeconds,
			LeaseSeconds:   cfg.RAG.RateLimit.Global.LeaseSeconds,
			PollIntervalMs: cfg.RAG.RateLimit.Global.PollIntervalMs,
		},
	)
	defer queueLimiter.Shutdown()

	llmService, embService := setupAI(cfg, logger)

	userRepo := userRepoPkg.NewUserRepo(gormDB)
	authSvc := userService.NewAuthService(userRepo, cfg.Auth)
	authHandler := userHandler.NewAuthHandler(authSvc)

	kbRepo := knowledgeRepo.NewKnowledgeBaseRepo(gormDB)
	kbSvc := knowledgeService.NewKnowledgeBaseService(kbRepo)
	kbHandler := knowledgeHandler.NewKnowledgeBaseHandler(kbSvc)

	docRepo := knowledgeRepo.NewKnowledgeDocumentRepo(gormDB)
	chunkRepo := knowledgeRepo.NewKnowledgeChunkRepo(gormDB)
	docSvc := knowledgeService.NewDocumentService(docRepo, chunkRepo, kbRepo)
	docHandler := knowledgeHandler.NewDocumentHandler(docSvc)

	convRepo := conversationRepo.NewConversationRepo(gormDB)
	msgRepo := conversationRepo.NewMessageRepo(gormDB)
	fbRepo := conversationRepo.NewFeedbackRepo(gormDB)
	convSvc := conversationService.NewConversationService(convRepo, msgRepo, fbRepo)
	convHandler := conversationHandler.NewConversationHandler(convSvc)

	dbMemStore := conversationService.NewDBMemoryStore(gormDB, convRepo, msgRepo)
	memSvc := rag.NewDefaultMemoryService(dbMemStore, cfg.RAG.Memory.HistoryKeepTurns)

	intentTreeRepo := intentRepo.NewIntentRepo(gormDB)
	termMappingRepo := intentRepo.NewTermMappingRepo(gormDB)
	intentSvc := intentService.NewIntentService(intentTreeRepo, termMappingRepo, gormDB)
	intentTreeHandler := intentHandler.NewIntentHandler(intentSvc)

	pgRetriever := rag.NewPgRetriever(gormDB, embService, kbRepo, 10)
	llmRewriter := rag.NewLLMRewriter(llmService,
		cfg.RAG.QueryRewrite.MaxHistoryMessages,
		cfg.RAG.QueryRewrite.MaxHistoryChars,
	)

	ragCtl := rag.NewController(rag.NewPipeline(llmService,
		rag.NewDefaultPromptBuilder(),
		llmRewriter,
		pgRetriever,
		memSvc,
	))

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.Recover(),
		middleware.TraceID(),
		middleware.RequestLog(),
		middleware.DB(gormDB),
		middleware.Auth(authSvc),
	)

	api := r.Group("/api/ragent")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, convention.Success("ok"))
		})
		api.GET("/limiter-test", func(c *gin.Context) {
			err := queueLimiter.Acquire(c.Request.Context(), ratelimit.AcquireRequest{
				MaxWait: time.Duration(cfg.RAG.RateLimit.Global.MaxWaitSeconds) * time.Second,
				OnAcquire: func() {
					c.JSON(http.StatusOK, convention.Success("acquired"))
				},
				OnTimeout: func() {
					c.JSON(http.StatusOK, convention.Failure("A000001", "系统繁忙，请稍后再试"))
				},
			})
			if err != nil {
				c.JSON(http.StatusOK, convention.Failure("A000001", "队列超时"))
			}
		})

		auth := api.Group("/auth")
		{
			auth.POST("/login", authHandler.Login)
			auth.POST("/logout", authHandler.Logout)
			auth.GET("/current-user", authHandler.CurrentUser)
		}

		conv := api.Group("/conversations")
		{
			conv.GET("", convHandler.List)
			conv.GET("/:conversationId", convHandler.Get)
			conv.GET("/:conversationId/messages", convHandler.Messages)
			conv.PUT("/:conversationId/title", convHandler.UpdateTitle)
			conv.DELETE("/:conversationId", convHandler.Delete)
			conv.POST("/feedback", convHandler.SubmitFeedback)
		}

		it := api.Group("/intent-tree")
		{
			it.GET("/tree", intentTreeHandler.GetTree)
			it.GET("/nodes", intentTreeHandler.ListNodes)
			it.POST("/nodes", intentTreeHandler.CreateNode)
			it.GET("/nodes/:id", intentTreeHandler.GetNode)
			it.PUT("/nodes/:id", intentTreeHandler.UpdateNode)
			it.DELETE("/nodes/:id", intentTreeHandler.DeleteNode)
			it.PATCH("/nodes/:id/enable", intentTreeHandler.ToggleNode)

			it.GET("/term-mappings", intentTreeHandler.ListTermMappings)
			it.POST("/term-mappings", intentTreeHandler.CreateTermMapping)
			it.PUT("/term-mappings/:id", intentTreeHandler.UpdateTermMapping)
			it.DELETE("/term-mappings/:id", intentTreeHandler.DeleteTermMapping)
		}

		kb := api.Group("/knowledge-base")
		{
			kb.GET("/chunk-strategies", kbHandler.ChunkStrategies)

			kb.POST("/:kbId/docs/upload", docHandler.Upload)
			kb.GET("/:kbId/docs", docHandler.ListDocs)
			kb.GET("/docs/search", docHandler.SearchDocs)
			kb.GET("/docs/:docId/chunk-logs", docHandler.ChunkLogs)
			kb.GET("/docs/:docId/chunks", docHandler.ListChunks)
			kb.PUT("/docs/:docId/chunks/:chunkId", docHandler.UpdateChunk)
			kb.DELETE("/docs/:docId/chunks/:chunkId", docHandler.DeleteChunk)
			kb.PATCH("/docs/:docId/chunks/:chunkId/enable", docHandler.ToggleChunk)
			kb.PATCH("/docs/:docId/chunks/batch-enable", docHandler.BatchToggleChunks)
			kb.POST("/docs/:docId/chunk", docHandler.ChunkDoc)
			kb.GET("/docs/:docId", docHandler.GetDoc)
			kb.PUT("/docs/:docId", docHandler.UpdateDoc)
			kb.DELETE("/docs/:docId", docHandler.DeleteDoc)
			kb.PATCH("/docs/:docId/enable", docHandler.ToggleDoc)

			kb.POST("", kbHandler.Create)
			kb.GET("", kbHandler.List)
			kb.GET("/:id", kbHandler.Get)
			kb.PUT("/:id", kbHandler.Update)
			kb.DELETE("/:id", kbHandler.Delete)
		}
	}

	ragGroup := r.Group("/rag/v3")
	{
		ragGroup.GET("/chat", ragCtl.Chat)
		ragGroup.POST("/stop", ragCtl.Stop)
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("starting server", "port", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "err", err)
	}
	if gormDB != nil {
		if err := db.Close(gormDB); err != nil {
			slog.Error("failed to close database", "err", err)
		}
	}
	slog.Info("server exited")
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
		case "anthropic":
			logger := slog.Default()
			logger.Warn("anthropic protocol not yet implemented, skipping", "provider", name)
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
