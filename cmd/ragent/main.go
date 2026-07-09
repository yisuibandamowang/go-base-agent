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

	adminHandler "go-base-agent/internal/biz/admin/handler"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	adminService "go-base-agent/internal/biz/admin/service"
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

	adminRepoObj := adminRepo.NewAdminRepo(gormDB)
	sampleQRepo := adminRepo.NewSampleQuestionRepo(gormDB)
	adminSvc := adminService.NewAdminService(adminRepoObj, sampleQRepo, gormDB)
	adminH := adminHandler.NewAdminHandler(adminSvc)

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

		// Auth — 同时注册 /auth/* 和 /user/me
		api.POST("/auth/login", authHandler.Login)
		api.POST("/auth/logout", authHandler.Logout)
		api.GET("/auth/current-user", authHandler.CurrentUser)
		api.GET("/user/me", authHandler.CurrentUser)

		// Users
		api.GET("/users", adminH.ListUsers)
		api.POST("/users", adminH.CreateUser)
		api.PUT("/users/:id", adminH.UpdateUser)
		api.DELETE("/users/:id", adminH.DeleteUser)

		// Conversations
		conv := api.Group("/conversations")
		{
			conv.GET("", convHandler.List)
			conv.GET("/:conversationId", convHandler.Get)
			conv.GET("/:conversationId/messages", convHandler.Messages)
			conv.PUT("/:conversationId", convHandler.UpdateTitle)
			conv.PUT("/:conversationId/title", convHandler.UpdateTitle)
			conv.DELETE("/:conversationId", convHandler.Delete)
			conv.POST("/feedback", convHandler.SubmitFeedback)
			conv.POST("/messages/:messageId/feedback", convHandler.SubmitFeedback)
		}

		// Intent tree — 同时注册 /intent-tree/* 和旧风格路径
		api.GET("/intent-tree/trees", intentTreeHandler.GetTree)
		api.POST("/intent-tree", intentTreeHandler.CreateNode)
		api.PUT("/intent-tree/:id", intentTreeHandler.UpdateNode)
		api.DELETE("/intent-tree/:id", intentTreeHandler.DeleteNode)

		it := api.Group("/intent-tree")
		{
			it.GET("/tree", intentTreeHandler.GetTree)
			it.GET("/nodes", intentTreeHandler.ListNodes)
			it.POST("/nodes", intentTreeHandler.CreateNode)
			it.GET("/nodes/:id", intentTreeHandler.GetNode)
			it.PUT("/nodes/:id", intentTreeHandler.UpdateNode)
			it.DELETE("/nodes/:id", intentTreeHandler.DeleteNode)
			it.PATCH("/nodes/:id/enable", intentTreeHandler.ToggleNode)
		}

		// Term mappings — 同时注册 /mappings/* 和 /intent-tree/term-mappings/*
		api.GET("/mappings", intentTreeHandler.ListTermMappings)
		api.POST("/mappings", intentTreeHandler.CreateTermMapping)
		api.PUT("/mappings/:id", intentTreeHandler.UpdateTermMapping)
		api.DELETE("/mappings/:id", intentTreeHandler.DeleteTermMapping)

		it.GET("/term-mappings", intentTreeHandler.ListTermMappings)
		it.POST("/term-mappings", intentTreeHandler.CreateTermMapping)
		it.PUT("/term-mappings/:id", intentTreeHandler.UpdateTermMapping)
		it.DELETE("/term-mappings/:id", intentTreeHandler.DeleteTermMapping)

		// Admin — 同时注册 /admin/* 和 /rag/* 兼容路径
		api.GET("/admin/dashboard/overview", adminH.Dashboard)
		api.GET("/admin/dashboard", adminH.Dashboard)
		api.GET("/admin/dashboard/performance", stub("performance"))
		api.GET("/admin/dashboard/trends", stub("trends"))

		api.GET("/admin/traces", adminH.ListTraceRuns)
		api.GET("/admin/traces/:traceId", adminH.TraceDetail)

		api.GET("/rag/traces/runs", adminH.ListTraceRuns)
		api.GET("/rag/traces/runs/:id", adminH.TraceDetail)
		api.GET("/rag/traces/runs/:id/nodes", traceNodesStub)

		// Sample questions — /rag/*, /sample-questions, /admin/sample-questions
		api.GET("/rag/sample-questions", adminH.ListSampleQuestions)
		api.GET("/sample-questions", adminH.ListSampleQuestions)
		api.POST("/sample-questions", adminH.CreateSampleQuestion)
		api.PUT("/sample-questions/:id", adminH.UpdateSampleQuestion)
		api.DELETE("/sample-questions/:id", adminH.DeleteSampleQuestion)

		api.GET("/admin/sample-questions", adminH.ListSampleQuestions)
		api.POST("/admin/sample-questions", adminH.CreateSampleQuestion)
		api.PUT("/admin/sample-questions/:id", adminH.UpdateSampleQuestion)
		api.DELETE("/admin/sample-questions/:id", adminH.DeleteSampleQuestion)

		api.GET("/admin/users", adminH.ListUsers)
		api.POST("/admin/users", adminH.CreateUser)
		api.PUT("/admin/users/:id", adminH.UpdateUser)
		api.DELETE("/admin/users/:id", adminH.DeleteUser)

		// RAG settings stub
		api.GET("/rag/settings", ragSettings(cfg))

		// Knowledge base
		kb := api.Group("/knowledge-base")
		{
			kb.GET("/chunk-strategies", kbHandler.ChunkStrategies)

			kb.POST("/:id/docs/upload", docHandler.Upload)
			kb.GET("/:id/docs", docHandler.ListDocs)
			kb.GET("/docs/search", docHandler.SearchDocs)
			kb.GET("/docs/:docId/chunk-logs", docHandler.ChunkLogs)
			kb.GET("/docs/:docId/chunks", docHandler.ListChunks)
			kb.POST("/docs/:docId/chunks", docHandler.CreateChunkStub)
			kb.PUT("/docs/:docId/chunks/:chunkId", docHandler.UpdateChunk)
			kb.DELETE("/docs/:docId/chunks/:chunkId", docHandler.DeleteChunk)
			kb.PATCH("/docs/:docId/chunks/:chunkId/enable", docHandler.ToggleChunk)
			kb.PATCH("/docs/:docId/chunks/batch-enable", docHandler.BatchToggleChunks)
			kb.POST("/docs/:docId/chunk", docHandler.ChunkDoc)
			kb.GET("/docs/:docId", docHandler.GetDoc)
			kb.PUT("/docs/:docId", docHandler.UpdateDoc)
			kb.DELETE("/docs/:docId", docHandler.DeleteDoc)
			kb.PATCH("/docs/:docId/enable", docHandler.ToggleDoc)
			kb.GET("/docs/:docId/preview", docHandler.Preview)
			kb.GET("/docs/:docId/file", stub("file"))

			kb.POST("", kbHandler.Create)
			kb.GET("", kbHandler.List)
			kb.GET("/:id", kbHandler.Get)
			kb.PUT("/:id", kbHandler.Update)
			kb.DELETE("/:id", kbHandler.Delete)
		}

		// Ingestion stubs
		api.GET("/ingestion/pipelines", stub("pipelines"))
		api.GET("/ingestion/tasks", stub("tasks"))
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

// stub returns a handler that responds with "not implemented" for stubbed endpoints.
func stub(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, convention.Success(map[string]string{"message": name + " not yet implemented"}))
	}
}

func ragSettings(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		embModels := make([]map[string]interface{}, 0)
		for _, c := range cfg.AI.Embedding.Candidates {
			embModels = append(embModels, map[string]interface{}{
				"id":        c.ID,
				"model":     c.Model,
				"provider":  c.Provider,
				"dimension": c.Dimension,
			})
		}
		rerankModels := make([]map[string]interface{}, 0)
		for _, c := range cfg.AI.Rerank.Candidates {
			rerankModels = append(rerankModels, map[string]interface{}{
				"id":       c.ID,
				"model":    c.Model,
				"provider": c.Provider,
			})
		}
		c.JSON(http.StatusOK, convention.Success(map[string]interface{}{
			"upload": map[string]interface{}{
				"maxFileSize":  "50MB",
				"allowedTypes": []string{".pdf", ".docx", ".md", ".txt", ".html", ".csv"},
			},
			"rag": map[string]interface{}{
				"queryRewriteEnabled": cfg.RAG.QueryRewrite.Enabled,
				"deepThinkingEnabled": true,
			},
			"ai": map[string]interface{}{
				"embedding": map[string]interface{}{
					"defaultModel": cfg.AI.Embedding.DefaultModel,
					"candidates":   embModels,
				},
				"rerank": map[string]interface{}{
					"defaultModel": cfg.AI.Rerank.DefaultModel,
					"candidates":   rerankModels,
				},
			},
		}))
	}
}

func traceNodesStub(c *gin.Context) {
	c.JSON(http.StatusOK, convention.Success[any]([]any{}))
}
