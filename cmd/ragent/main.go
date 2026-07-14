package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	adminHandler "go-base-agent/internal/biz/admin/handler"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	adminService "go-base-agent/internal/biz/admin/service"
	auditHandler "go-base-agent/internal/biz/audit/handler"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	conversationHandler "go-base-agent/internal/biz/conversation/handler"
	conversationRepo "go-base-agent/internal/biz/conversation/repo"
	conversationService "go-base-agent/internal/biz/conversation/service"
	ingestionHandler "go-base-agent/internal/biz/ingestion/handler"
	ingestionRepo "go-base-agent/internal/biz/ingestion/repo"
	ingestionService "go-base-agent/internal/biz/ingestion/service"
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
	"go-base-agent/internal/framework/mq"
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

	llmService, embService, rerankService := setupAI(cfg, logger)
	mqProducer, mqConsumer, shutdownMQ := setupMQ(cfg.RocketMQ)
	defer shutdownMQ()
	_ = mqProducer
	_ = mqConsumer

	userRepo := userRepoPkg.NewUserRepo(gormDB)
	authSvc := userService.NewAuthService(userRepo, cfg.Auth)
	authHandler := userHandler.NewAuthHandler(authSvc)

	kbRepo := knowledgeRepo.NewKnowledgeBaseRepo(gormDB)
	kbSvc := knowledgeService.NewKnowledgeBaseService(kbRepo)
	kbHandler := knowledgeHandler.NewKnowledgeBaseHandler(kbSvc)

	docRepo := knowledgeRepo.NewKnowledgeDocumentRepo(gormDB)
	chunkRepo := knowledgeRepo.NewKnowledgeChunkRepo(gormDB)
	fileStore := knowledgeHandler.NewFileStore()
	docSvc := knowledgeService.NewDocumentService(docRepo, chunkRepo, kbRepo, gormDB, embService, rag.NewPgVectorStore(gormDB), fileStore)
	docHandler := knowledgeHandler.NewDocumentHandler(docSvc, fileStore)

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

	auditSvc := auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gormDB))
	auditH := auditHandler.NewAuditHandler(auditSvc)

	ingestionPipelineSvc := ingestionService.NewPipelineService(ingestionRepo.NewPipelineRepo(gormDB), gormDB)
	ingestionTaskSvc := ingestionService.NewTaskService(ingestionRepo.NewTaskRepo(gormDB), ingestionPipelineSvc, gormDB)
	docSvc.SetIngestionTaskStarter(ingestionTaskSvc)
	ingestionTaskSvc.SetExecutor(docSvc)
	ingestionPipelineH := ingestionHandler.NewPipelineHandler(ingestionPipelineSvc)
	ingestionTaskH := ingestionHandler.NewTaskHandler(ingestionTaskSvc)

	pgRetriever := rag.NewPgRetriever(gormDB, embService, kbRepo, 10)
	retriever := rag.NewRerankRetriever(pgRetriever, rerankService)
	llmRewriter := rag.NewLLMRewriter(llmService,
		cfg.RAG.QueryRewrite.MaxHistoryMessages,
		cfg.RAG.QueryRewrite.MaxHistoryChars,
	)

	ragCtl := rag.NewController(rag.NewPipeline(llmService,
		rag.NewDefaultPromptBuilder(),
		llmRewriter,
		retriever,
		memSvc,
	))

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.Recover(),
		middleware.TraceID(),
		middleware.RequestLog(),
		middleware.DB(gormDB),
		middleware.Tenant(),
		middleware.Auth(authSvc),
	)

	registerStatusRoutes(r)

	api := r.Group("/api/ragent")
	{
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
		api.PUT("/user/password", authHandler.ChangePassword)

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
		api.POST("/intent-tree/batch/enable", intentTreeHandler.BatchEnable)
		api.POST("/intent-tree/batch/disable", intentTreeHandler.BatchDisable)
		api.POST("/intent-tree/batch/delete", intentTreeHandler.BatchDelete)

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
		api.GET("/mappings/:id", intentTreeHandler.GetTermMapping)
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
		api.GET("/admin/dashboard/performance", adminH.Performance)
		api.GET("/admin/dashboard/trends", adminH.Trends)

		api.GET("/admin/traces", adminH.ListTraceRuns)
		api.GET("/admin/traces/:traceId", adminH.TraceDetail)

		api.GET("/rag/traces/runs", adminH.ListTraceRuns)
		api.GET("/rag/traces/runs/:id", adminH.TraceDetail)
		api.GET("/rag/traces/runs/:id/nodes", adminH.TraceNodes)

		// Sample questions — /rag/*, /sample-questions, /admin/sample-questions
		api.GET("/rag/sample-questions", adminH.ListRAGSampleQuestions)
		api.GET("/sample-questions", adminH.ListRAGSampleQuestions)
		api.GET("/sample-questions/:id", adminH.GetSampleQuestion)
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

		// Audit change logs — 对齐 Java /biz-change-logs
		api.GET("/biz-change-logs", auditH.List)
		api.GET("/biz-change-logs/:id", auditH.Get)

		// RAG settings
		api.GET("/rag/settings", ragSettings(cfg))
		api.GET("/rag/eval", ragEval(pgRetriever))

		// Knowledge base
		kb := api.Group("/knowledge-base")
		{
			kb.GET("/chunk-strategies", kbHandler.ChunkStrategies)

			kb.POST("/:id/docs/upload", docHandler.Upload)
			kb.GET("/:id/docs", docHandler.ListDocs)
			kb.GET("/docs/search", docHandler.SearchDocs)
			kb.GET("/docs/:docId/chunk-logs", docHandler.ChunkLogs)
			kb.GET("/docs/:docId/chunks", docHandler.ListChunks)
			kb.POST("/docs/:docId/chunks", docHandler.CreateChunk)
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
			kb.GET("/docs/:docId/file", docHandler.File)

			kb.POST("", kbHandler.Create)
			kb.GET("", kbHandler.List)
			kb.GET("/:id", kbHandler.Get)
			kb.PUT("/:id", kbHandler.Update)
			kb.DELETE("/:id", kbHandler.Delete)
		}

		// Ingestion
		api.POST("/ingestion/pipelines", ingestionPipelineH.Create)
		api.PUT("/ingestion/pipelines/:id", ingestionPipelineH.Update)
		api.GET("/ingestion/pipelines/:id", ingestionPipelineH.Get)
		api.GET("/ingestion/pipelines", ingestionPipelineH.List)
		api.DELETE("/ingestion/pipelines/:id", ingestionPipelineH.Delete)
		api.POST("/ingestion/tasks", ingestionTaskH.Create)
		api.POST("/ingestion/tasks/upload", ingestionTaskH.Upload)
		api.GET("/ingestion/tasks/:id", ingestionTaskH.Get)
		api.GET("/ingestion/tasks/:id/nodes", ingestionTaskH.Nodes)
		api.GET("/ingestion/tasks", ingestionTaskH.List)
	}

	ragGroup := r.Group("/rag/v3")
	{
		ragGroup.GET("/chat", ragCtl.Chat)
		ragGroup.POST("/stop", ragCtl.Stop)
	}
	// Also register under /api/ragent prefix for frontend compatibility
	apiRag := api.Group("/rag/v3")
	{
		apiRag.GET("/chat", ragCtl.Chat)
		apiRag.POST("/stop", ragCtl.Stop)
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

func setupMQ(cfg config.RocketMQConfig) (mq.Producer, mq.Consumer, func()) {
	nameServers := splitCSV(cfg.NameServer)
	if len(nameServers) == 0 {
		producer := mq.NewNoopProducer()
		consumer := mq.NewNoopConsumer()
		return producer, consumer, func() {}
	}

	producer, err := mq.NewRocketProducer(mq.RocketProducerConfig{
		NameServers:        nameServers,
		Group:              cfg.Producer.Group,
		SendMessageTimeout: cfg.Producer.SendMessageTimeout,
	})
	if err != nil {
		slog.Warn("rocketmq producer unavailable, fallback to noop", "err", err)
		noopProducer := mq.NewNoopProducer()
		noopConsumer := mq.NewNoopConsumer()
		return noopProducer, noopConsumer, func() {}
	}

	consumerGroup := cfg.Producer.Group
	if consumerGroup == "" {
		consumerGroup = "ragent-consumer"
	} else {
		consumerGroup += "-consumer"
	}
	var consumer mq.Consumer
	rocketConsumer, err := mq.NewRocketConsumer(mq.RocketConsumerConfig{
		NameServers: nameServers,
		Group:       consumerGroup,
	})
	if err != nil {
		slog.Warn("rocketmq consumer unavailable, fallback consumer to noop", "err", err)
		consumer = mq.NewNoopConsumer()
	} else {
		consumer = rocketConsumer
	}

	shutdown := func() {
		if p, ok := any(producer).(interface{ Shutdown() error }); ok {
			if err := p.Shutdown(); err != nil {
				slog.Warn("rocketmq producer shutdown failed", "err", err)
			}
		}
		if err := consumer.Shutdown(); err != nil {
			slog.Warn("mq consumer shutdown failed", "err", err)
		}
	}
	return producer, consumer, shutdown
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func registerStatusRoutes(r *gin.Engine) {
	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, convention.Success("ok"))
	}
	for _, path := range []string{
		"/health",
		"/healthz",
		"/live",
		"/livez",
		"/ready",
		"/readyz",
		"/api/ragent/health",
	} {
		r.GET(path, healthHandler)
	}
	r.GET("/metrics", func(c *gin.Context) {
		c.String(http.StatusOK, "# HELP ragent_up RAgent process availability.\n# TYPE ragent_up gauge\nragent_up 1\n")
	})
}

func setupAI(cfg *config.Config, logger *slog.Logger) (chat.LLMService, embedding.Service, rerank.Service) {
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

	return llmService, embService, rerankService
}

func buildChatClients(aiCfg config.AIConfig) []chat.ChatClient {
	var clients []chat.ChatClient
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			clients = append(clients, chat.NewOpenAICompatibleChatClient(name, nil))
		case "anthropic":
			clients = append(clients, chat.NewAnthropicChatClient(name, nil))
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
	clients := make([]rerank.Client, 0, len(aiCfg.Providers)+1)
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			clients = append(clients, rerank.NewHTTPClient(name, nil))
		case "noop":
		default:
			slog.Warn("unknown rerank protocol, skipping", "provider", name, "protocol", provider.Protocol)
		}
	}
	clients = append(clients, &rerank.NoopClient{})
	return clients
}

func embeddingDimension(aiCfg config.AIConfig) int {
	if len(aiCfg.Embedding.Candidates) > 0 {
		if d := aiCfg.Embedding.Candidates[0].Dimension; d > 0 {
			return d
		}
	}
	return 1536
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

func ragEval(retriever rag.Retriever) gin.HandlerFunc {
	return func(c *gin.Context) {
		question := c.Query("question")
		if question == "" {
			c.JSON(http.StatusOK, convention.Failure("A000001", "question不能为空"))
			return
		}
		chunks, err := retriever.Retrieve(c.Request.Context(), question, 10)
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
			return
		}
		chunkIDs := make([]string, 0, len(chunks))
		contexts := make([]string, 0, len(chunks))
		contextDocIDs := make([]string, 0, len(chunks))
		docSeen := make(map[string]struct{})
		docIDs := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			chunkIDs = append(chunkIDs, chunk.ID)
			contexts = append(contexts, chunk.Text)
			docID := ""
			if chunk.Metadata != nil {
				docID = chunk.Metadata["doc_id"]
			}
			contextDocIDs = append(contextDocIDs, docID)
			if docID != "" {
				if _, ok := docSeen[docID]; !ok {
					docSeen[docID] = struct{}{}
					docIDs = append(docIDs, docID)
				}
			}
		}
		c.JSON(http.StatusOK, convention.Success(map[string]interface{}{
			"retrievedDocIds":        docIDs,
			"retrievedChunkIds":      chunkIDs,
			"retrievedContexts":      contexts,
			"retrievedContextDocIds": contextDocIDs,
			"hasKb":                  len(chunks) > 0,
			"hasMcp":                 false,
			"mcpContext":             nil,
			"subIntents":             []any{},
			"intentLeafIds":          []any{},
		}))
	}
}
