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
	agentHandler "go-base-agent/internal/biz/agent/handler"
	agentRepo "go-base-agent/internal/biz/agent/repo"
	agentService "go-base-agent/internal/biz/agent/service"
	auditHandler "go-base-agent/internal/biz/audit/handler"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	conversationHandler "go-base-agent/internal/biz/conversation/handler"
	conversationRepo "go-base-agent/internal/biz/conversation/repo"
	conversationService "go-base-agent/internal/biz/conversation/service"
	coreparser "go-base-agent/internal/biz/core/parser"
	"go-base-agent/internal/biz/crawler"
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
	ragHandler "go-base-agent/internal/biz/rag/handler"
	userHandler "go-base-agent/internal/biz/user/handler"
	userRepoPkg "go-base-agent/internal/biz/user/repo"
	userService "go-base-agent/internal/biz/user/service"
	"go-base-agent/internal/framework/cache"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/idempotent"
	redislock "go-base-agent/internal/framework/lock"
	"go-base-agent/internal/framework/middleware"
	"go-base-agent/internal/framework/mq"
	"go-base-agent/internal/framework/ratelimit"
	"go-base-agent/internal/framework/snowflake"
	"go-base-agent/internal/infra/chat"
	"go-base-agent/internal/infra/embedding"
	"go-base-agent/internal/infra/model"
	"go-base-agent/internal/infra/rerank"
	"go-base-agent/internal/infra/storage"
	"go-base-agent/internal/infra/vlm"

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
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		slog.Warn("database not available, starting without DB", "err", err)
	}

	rdb := cfg.Redis.NewClient()
	redisAvailable := false
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pingCancel()
	if _, err := rdb.Ping(pingCtx).Result(); err != nil {
		slog.Warn("redis not available, rate limiter disabled", "err", err)
	} else {
		redisAvailable = true
	}
	if redisAvailable && strings.TrimSpace(os.Getenv("SNOWFLAKE_WORKER_ID")) == "" {
		idCtx, idCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := snowflake.ConfigureFromRedis(idCtx, rdb); err != nil {
			slog.Warn("snowflake redis allocation failed, fallback to local worker id", "err", err)
		}
		idCancel()
	}
	idempotentGuard := idempotent.New(rdb)

	globalRateLimitCfg := cfg.RAG.RateLimit.Global
	chatMaxConcurrent := globalRateLimitCfg.MaxConcurrent
	if !globalRateLimitCfg.IsEnabledByDefault() {
		chatMaxConcurrent = 0
	}
	queueLimiter := ratelimit.NewFairQueueLimiter(
		"rag:chat",
		rdb,
		ratelimit.LimiterConfig{
			MaxConcurrent:  chatMaxConcurrent,
			MaxWaitSeconds: globalRateLimitCfg.MaxWaitSeconds,
			LeaseSeconds:   globalRateLimitCfg.LeaseSeconds,
			PollIntervalMs: globalRateLimitCfg.PollIntervalMs,
		},
	)
	defer queueLimiter.Shutdown()

	uploadSemaphoreCfg := cfg.RAG.Semaphore.DocumentUpload
	uploadSemaphoreName := strings.TrimSpace(uploadSemaphoreCfg.Name)
	if uploadSemaphoreName == "" {
		uploadSemaphoreName = "rag:document:upload"
	}
	documentUploadLimiter := ratelimit.NewFairQueueLimiter(
		uploadSemaphoreName,
		rdb,
		ratelimit.LimiterConfig{
			MaxConcurrent:  uploadSemaphoreCfg.MaxConcurrent,
			MaxWaitSeconds: uploadSemaphoreCfg.MaxWaitSeconds,
			LeaseSeconds:   uploadSemaphoreCfg.LeaseSeconds,
			PollIntervalMs: globalRateLimitCfg.PollIntervalMs,
		},
	)
	defer documentUploadLimiter.Shutdown()
	documentUploadMaxWait := time.Duration(uploadSemaphoreCfg.MaxWaitSeconds) * time.Second
	if documentUploadMaxWait <= 0 {
		documentUploadMaxWait = time.Second
	}

	llmService, preferredLLMService, embService, rerankService, vlmService, hasVLM := setupAI(cfg, logger)
	mqProducer, mqConsumer, shutdownMQ, mqEnabled := setupMQ(cfg.RocketMQ)
	defer shutdownMQ()

	userRepo := userRepoPkg.NewUserRepo(gormDB)
	authSvc := userService.NewAuthService(userRepo, cfg.Auth, cache.New(rdb))
	authHandler := userHandler.NewAuthHandler(authSvc)
	auditSvc := auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gormDB))
	authSvc.SetAuditRecorder(auditSvc)

	kbRepo := knowledgeRepo.NewKnowledgeBaseRepo(gormDB)
	kbSvc := knowledgeService.NewKnowledgeBaseService(kbRepo)
	kbSvc.SetAuditRecorder(auditSvc)
	kbHandler := knowledgeHandler.NewKnowledgeBaseHandler(kbSvc)

	docRepo := knowledgeRepo.NewKnowledgeDocumentRepo(gormDB)
	chunkRepo := knowledgeRepo.NewKnowledgeChunkRepo(gormDB)
	scheduleRepo := knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gormDB)
	vecStore, err := rag.NewVectorStore(context.Background(), cfg.RAG.Vector.Type, cfg.Milvus.URI, gormDB, cfg.RAG.Default.Dimension, cfg.RAG.Default.MetricType)
	if err != nil {
		slog.Error("failed to initialize vector store", "err", err)
		os.Exit(1)
	}
	var graphClient *rag.LightRagClient
	if strings.EqualFold(cfg.RAG.Graph.Type, "lightrag") {
		graphClient = rag.NewLightRagClient(
			cfg.RAG.Graph.Lightrag.BaseURL,
			cfg.RAG.Graph.Lightrag.APIKey,
			nil,
			cfg.RAG.Search.Channels.TimeoutMs,
		)
		vecStore = rag.NewGraphSyncingVectorStore(vecStore, graphClient)
	}
	fileStore, err := knowledgeHandler.NewConfiguredFileStore(cfg.RustFS)
	if err != nil {
		slog.Warn("rustfs file store unavailable, fallback to memory", "err", err)
		fileStore = knowledgeHandler.NewFileStore()
	}
	kbSvc.SetVectorStore(vecStore)
	kbSvc.SetFileDeleter(fileStore)
	kbSvc.SetMQProducer(mqProducer, mqEnabled)
	docSvc := knowledgeService.NewDocumentService(docRepo, chunkRepo, kbRepo, gormDB, embService, vecStore, fileStore)
	docSvc.SetAuditRecorder(auditSvc)
	docSvc.SetScheduleRepo(scheduleRepo)
	docSvc.SetScheduleMinIntervalSeconds(cfg.RAG.Knowledge.Schedule.MinIntervalSeconds)
	docSvc.SetLLMService(llmService)
	docSvc.SetDefaultCollectionName(cfg.RAG.Default.CollectionName)
	docSvc.SetMQProducer(mqProducer, mqEnabled)
	docSvc.SetChunkLock(redislock.New(rdb))
	mqProducer.RegisterTransactionChecker(knowledgeService.KnowledgeDocumentChunkTopic, docSvc.CheckChunkTransaction)
	mqProducer.RegisterTransactionChecker(knowledgeService.KnowledgeBaseCleanupTopic, kbSvc.CheckCleanupTransaction)
	var minerUPermitRunner coreparser.MinerUPermitRunner
	if redisAvailable && cfg.MinerU.ConcurrencyLimit > 0 {
		minerUSemaphoreName := strings.TrimSpace(cfg.MinerU.SemaphoreName)
		if minerUSemaphoreName == "" {
			minerUSemaphoreName = "rag:mineru:parse"
		}
		minerUSemaphore := ratelimit.NewExpirableSemaphore(
			minerUSemaphoreName,
			rdb,
			int(cfg.MinerU.ConcurrencyLimit),
		)
		defer minerUSemaphore.Shutdown()
		minerUPermitRunner = minerUSemaphore
	}
	if parserRegistry := buildDocumentParserRegistryWithPermitRunner(cfg, vlmService, hasVLM, fileStore, minerUPermitRunner); parserRegistry != nil {
		docSvc.SetParserRegistry(parserRegistry)
	}
	docHandler := knowledgeHandler.NewDocumentHandler(docSvc, fileStore)
	docHandler.SetUploadLimiter(documentUploadLimiter, documentUploadMaxWait)
	docHandler.SetUploadLimits(cfg.RAG.Upload.MaxFileSizeBytes, cfg.RAG.Upload.MaxRequestSizeBytes)
	if err := docHandler.SetInternalURLImportTaskStore(gormDB, time.Duration(cfg.RAG.Knowledge.Geelib.ImportTaskTimeoutMinutes)*time.Minute); err != nil {
		slog.Warn("failed to initialize internal url import task store", "err", err)
	}
	var geelibSource *crawler.GeelibSource
	if geelibCfg := cfg.RAG.Knowledge.Geelib; geelibCfg.IsEnabledByDefault() {
		geelibSource = crawler.NewGeelibSource(crawler.GeelibSourceConfig{
			Command:  geelibCfg.Command,
			WorkDir:  geelibCfg.WorkDir,
			Timeout:  time.Duration(geelibCfg.TimeoutSeconds) * time.Second,
			MaxBytes: geelibCfg.MaxBytes,
			Domains:  geelibCfg.Domains,
		})
		docHandler.SetInternalURLFetcher(geelibSource)
	}

	documentScheduleSvc := knowledgeService.NewDocumentScheduleService(
		gormDB,
		docRepo,
		kbRepo,
		scheduleRepo,
		fileStore,
		docSvc,
		cfg.RAG.Knowledge.Schedule,
	)
	documentScheduleMaxBytes := cfg.RAG.Upload.MaxFileSizeBytes
	documentScheduleSvc.RegisterSource(crawler.NewHTTPSource(crawler.HTTPSourceConfig{Name: "url", MaxBytes: documentScheduleMaxBytes}))
	documentScheduleSvc.RegisterSource(crawler.NewHTTPSource(crawler.HTTPSourceConfig{Name: "http", MaxBytes: documentScheduleMaxBytes}))
	if geelibSource != nil {
		documentScheduleSvc.RegisterSource(geelibSource)
	}
	documentScheduleSvc.RegisterSource(crawler.NewFeishuSource(crawler.FeishuSourceConfig{
		Name:        "feishu",
		AppID:       cfg.RAG.Knowledge.Feishu.AppID,
		AppSecret:   cfg.RAG.Knowledge.Feishu.AppSecret,
		AccessToken: cfg.RAG.Knowledge.Feishu.AccessToken,
		TenantToken: cfg.RAG.Knowledge.Feishu.TenantToken,
		BaseURL:     cfg.RAG.Knowledge.Feishu.BaseURL,
		MaxBytes:    documentScheduleMaxBytes,
	}))
	documentScheduleSvc.RegisterSource(crawler.NewConfluenceSource(crawler.ConfluenceSourceConfig{
		Name:        "confluence",
		BaseURL:     cfg.RAG.Knowledge.Confluence.BaseURL,
		Username:    cfg.RAG.Knowledge.Confluence.Username,
		APIKey:      cfg.RAG.Knowledge.Confluence.APIKey,
		AccessToken: cfg.RAG.Knowledge.Confluence.AccessToken,
		MaxBytes:    documentScheduleMaxBytes,
	}))
	go documentScheduleSvc.Run(appCtx)

	convRepo := conversationRepo.NewConversationRepo(gormDB)
	msgRepo := conversationRepo.NewMessageRepo(gormDB)
	fbRepo := conversationRepo.NewFeedbackRepo(gormDB)
	sumRepo := conversationRepo.NewConversationSummaryRepo(gormDB)
	convSvc := conversationService.NewConversationService(convRepo, msgRepo, fbRepo, sumRepo)
	convSvc.SetTitleMaxChars(cfg.RAG.Memory.TitleMaxLength)
	convSvc.SetFeedbackMQProducer(mqProducer, mqEnabled)
	convHandler := conversationHandler.NewConversationHandler(convSvc)

	if mqEnabled {
		if err := knowledgeService.RegisterKnowledgeDocumentChunkConsumer(mqConsumer, docSvc); err != nil {
			slog.Warn("register knowledge document chunk consumer failed, fallback to inline chunking", "err", err)
			mqEnabled = false
			kbSvc.SetMQProducer(nil, false)
			docSvc.SetMQProducer(nil, false)
			convSvc.SetFeedbackMQProducer(nil, false)
		}
		if mqEnabled {
			if err := knowledgeService.RegisterKnowledgeBaseCleanupConsumer(mqConsumer, kbSvc); err != nil {
				slog.Warn("register knowledge base cleanup consumer failed, fallback to inline cleanup", "err", err)
				mqEnabled = false
				kbSvc.SetMQProducer(nil, false)
				docSvc.SetMQProducer(nil, false)
				convSvc.SetFeedbackMQProducer(nil, false)
			}
		}
		if mqEnabled {
			if err := conversationService.RegisterMessageFeedbackConsumer(mqConsumer, convSvc); err != nil {
				slog.Warn("register message feedback consumer failed, fallback to inline feedback", "err", err)
				convSvc.SetFeedbackMQProducer(nil, false)
			}
		}
		if mqEnabled {
			if err := mqConsumer.Start(); err != nil {
				slog.Warn("start mq consumer failed, fallback to inline knowledge tasks", "err", err)
				mqEnabled = false
				kbSvc.SetMQProducer(nil, false)
				docSvc.SetMQProducer(nil, false)
				convSvc.SetFeedbackMQProducer(nil, false)
			}
		}
	}

	agentRepoObj := agentRepo.NewAgentRepo(gormDB)
	agentPromptResolver := agentService.NewPromptResolver(
		agentRepoObj,
		resolveEngineType(cfg),
		agentService.NewRedisPromptCacheManager(rdb),
	)
	if err := agentPromptResolver.Refresh(context.Background()); err != nil {
		slog.Warn("failed to load agent prompts", "err", err)
	}
	convSvc.SetRecommendedQuestionGenerator(conversationService.NewLLMRecommendedQuestionGenerator(preferredLLMService, "", agentPromptResolver))

	summaryGenerator := conversationService.NewLLMSummaryGenerator(preferredLLMService, "", agentPromptResolver)
	dbMemStore := conversationService.NewDBMemoryStore(
		gormDB,
		convRepo,
		msgRepo,
		sumRepo,
		summaryGenerator,
		cfg.RAG.Memory.SummaryEnabled,
		cfg.RAG.Memory.SummaryStartTurns,
		cfg.RAG.Memory.SummaryMaxChars,
		cfg.RAG.Memory.TitleMaxLength,
		cfg.RAG.Memory.HistoryKeepTurns,
	)
	dbMemStore.SetSummaryLock(redislock.New(rdb), 30*time.Second)
	dbMemStore.SetSummaryTaskRunner(func(fn func()) {
		go fn()
	})
	dbMemStore.SetTitleGenerator(
		conversationService.NewLLMTitleGenerator(preferredLLMService, "", cfg.RAG.Memory.TitleMaxLength),
		cfg.RAG.Memory.TitleMaxLength,
	)
	memSvc := rag.NewDefaultMemoryService(dbMemStore, cfg.RAG.Memory.HistoryKeepTurns)

	intentTreeRepo := intentRepo.NewIntentRepo(gormDB)
	termMappingRepo := intentRepo.NewTermMappingRepo(gormDB)
	intentSvc := intentService.NewIntentService(intentTreeRepo, termMappingRepo, gormDB)
	intentSvc.SetAuditRecorder(auditSvc)
	intentNodeCacheManager := rag.NewRedisIntentNodeCacheManager(rdb)
	queryTermCacheManager := rag.NewRedisQueryTermMappingCacheManager(rdb)
	intentSvc.SetIntentNodeCacheManager(intentNodeCacheManager)
	intentSvc.SetQueryTermMappingCacheManager(queryTermCacheManager)
	if err := maybeInitIntentTreeFromFactory(context.Background(), cfg.App, intentSvc); err != nil {
		slog.Error("failed to initialize intent tree from factory", "err", err)
		os.Exit(1)
	}
	intentTreeHandler := intentHandler.NewIntentHandler(intentSvc)
	cachedIntentTreeLister := rag.NewCachedIntentNodeLister(intentTreeRepo, intentNodeCacheManager)
	// 意图分类准入使用 Java RAGConstant 的固定阈值（0.35 / 3 个），
	// 不再借用检索通道的 min-intent-score（0.4，仅作用于检索作用域判定）。
	intentResolverSvc := rag.NewIntentResolver(cachedIntentTreeLister, rag.IntentResolverOptions{
		MinScore:   rag.IntentMinScore,
		MaxIntents: rag.MaxIntentCount,
	})
	intentResolverSvc.SetLLMService(preferredLLMService)
	intentGuidanceSvc := rag.NewIntentGuidanceService(rag.GuidanceOptions{
		Enabled:             cfg.RAG.Guidance.IsEnabledByDefault(),
		AmbiguityScoreRatio: cfg.RAG.Guidance.AmbiguityScoreRatio,
		AmbiguityMargin:     cfg.RAG.Guidance.AmbiguityMargin,
		MaxOptions:          cfg.RAG.Guidance.MaxOptions,
	})
	intentGuidanceSvc.SetIntentNodeLister(cachedIntentTreeLister)
	intentGuidanceSvc.SetAmbiguityChecker(rag.NewLLMAmbiguityChecker(preferredLLMService))

	adminRepoObj := adminRepo.NewAdminRepo(gormDB)
	sampleQRepo := adminRepo.NewSampleQuestionRepo(gormDB)
	adminSvc := adminService.NewAdminService(adminRepoObj, sampleQRepo, gormDB)
	adminSvc.SetAuditRecorder(auditSvc)
	adminH := adminHandler.NewAdminHandler(adminSvc)

	agentSvc := agentService.NewAgentService(agentRepoObj, resolveEngineType(cfg))
	agentSvc.SetAuditRecorder(auditSvc)
	agentSvc.SetPromptResolver(agentPromptResolver)
	agentH := agentHandler.NewAgentHandler(agentSvc)

	auditH := auditHandler.NewAuditHandler(auditSvc)

	ingestionPipelineSvc := ingestionService.NewPipelineService(ingestionRepo.NewPipelineRepo(gormDB), gormDB)
	ingestionPipelineSvc.SetAuditRecorder(auditSvc)
	ingestionTaskSvc := ingestionService.NewTaskService(ingestionRepo.NewTaskRepo(gormDB), ingestionPipelineSvc, gormDB)
	ingestionTaskSvc.SetAuditRecorder(auditSvc)
	docSvc.SetIngestionPipelineGetter(ingestionPipelineSvc)
	docSvc.SetIngestionTaskStarter(ingestionTaskSvc)
	ingestionTaskSvc.SetExecutor(docSvc)
	ingestionPipelineH := ingestionHandler.NewPipelineHandler(ingestionPipelineSvc)
	ingestionTaskH := ingestionHandler.NewTaskHandler(ingestionTaskSvc)
	ingestionTaskH.SetUploadLimits(cfg.RAG.Upload.MaxFileSizeBytes, cfg.RAG.Upload.MaxRequestSizeBytes)

	vectorRetriever := rag.NewVectorRetriever(vecStore, embService, kbRepo, cfg.RAG.Search.DefaultTopK)
	searchBackend := rag.NewKnowledgeSearchBackend(gormDB, kbRepo)
	searchChannels := make([]rag.SearchChannel, 0, 5)
	if cfg.RAG.Search.Channels.IntentDirected.IsEnabledByDefault() {
		intentChannel := rag.NewBackendIntentDirectedSearchChannel(searchBackend, vecStore, embService, 1)
		intentChannel.SetIntentOptions(
			cfg.RAG.Search.Channels.IntentDirected.MinIntentScore,
			cfg.RAG.Search.Channels.IntentDirected.TopKMultiplier,
		)
		intentChannel.SetSupplementRatio(cfg.RAG.Search.SupplementRatio)
		searchChannels = append(searchChannels, intentChannel)
	}
	if keywordSearchChannelEnabled(cfg.RAG.Search.Channels.Keyword) {
		keywordChannel := rag.NewBackendKeywordSearchChannel(searchBackend, 5)
		keywordChannel.SetKeywordOptions(
			cfg.RAG.Search.Channels.Keyword.Mode,
			cfg.RAG.Search.Channels.Keyword.TopKMultiplier,
		)
		keywordChannel.SetSupplementRatio(cfg.RAG.Search.SupplementRatio)
		searchChannels = append(searchChannels, keywordChannel)
	}
	if strings.EqualFold(cfg.RAG.Graph.Type, "lightrag") && cfg.RAG.Search.Channels.Graph.IsEnabledByDefaultWith(false) {
		graphChannel := rag.NewGraphSearchChannel(searchBackend, graphClient, cfg.RAG.Graph.Lightrag.QueryMode, 8)
		graphChannel.SetScopeOptions(
			cfg.RAG.Search.Channels.VectorGlobal.ConfidenceThreshold,
			cfg.RAG.Search.Channels.IntentDirected.MinIntentScore,
			cfg.RAG.Search.SupplementRatio,
		)
		searchChannels = append(searchChannels, graphChannel)
	}
	if cfg.RAG.Search.Channels.VectorGlobal.IsEnabledByDefault() {
		vectorGlobalChannel := rag.NewRetrieverSearchChannel("VectorGlobalSearch", rag.ChannelVectorGlobal, 10, vectorRetriever)
		vectorGlobalChannel.SetVectorGlobalOptions(
			cfg.RAG.Search.Channels.IntentDirected.IsEnabledByDefault(),
			cfg.RAG.Search.Channels.VectorGlobal.ConfidenceThreshold,
			cfg.RAG.Search.Channels.VectorGlobal.TopKMultiplier,
			cfg.RAG.Search.Channels.VectorGlobal.SingleIntentSupplementThreshold,
		)
		vectorGlobalChannel.SetVectorGlobalCandidateBudget(cfg.RAG.Search.Channels.VectorGlobal.CandidateBudget)
		searchChannels = append(searchChannels, vectorGlobalChannel)
	}
	webSearchCfg := cfg.RAG.Search.Channels.WebSearch
	// API Key 优先取配置，为空回退环境变量 YDC_API_KEY（对齐 Java WebSearchChannel.resolveApiKey）
	searchChannels = append(searchChannels, rag.NewYouComWebSearchChannel(
		webSearchCfg.APIURL,
		rag.ResolveWebSearchAPIKey(webSearchCfg.APIKey),
		webSearchCfg.Count,
		webSearchCfg.TimeoutSeconds,
		webSearchCfg.Enabled,
	))
	slog.Info("rag search channels wired", "channels", searchChannelNames(searchChannels))
	postProcessors := []rag.SearchResultPostProcessor{&rag.DedupPostProcessor{}}
	fusionStrategy := strings.TrimSpace(cfg.RAG.Search.Fusion.Strategy)
	if fusionStrategy == "" || strings.EqualFold(fusionStrategy, "rrf") {
		postProcessors = append(postProcessors, rag.NewFusionPostProcessorWithWeights(
			cfg.RAG.Search.Fusion.RRFK,
			cfg.RAG.Search.Fusion.RerankCandidateLimit,
			rag.FusionChannelWeights{
				Vector:    cfg.RAG.Search.Fusion.ChannelWeight("vector"),
				Keyword:   cfg.RAG.Search.Fusion.ChannelWeight("keyword"),
				Graph:     cfg.RAG.Search.Fusion.ChannelWeight("graph"),
				WebSearch: cfg.RAG.Search.Fusion.ChannelWeight("web-search"),
			},
		))
	}
	multiEngine := rag.NewMultiChannelRetrievalEngine(searchChannels, postProcessors)
	multiEngine.SetRetrievalScopeResolver(rag.NewRetrievalScopeResolver(
		searchBackend,
		cfg.RAG.Search.Channels.VectorGlobal.ConfidenceThreshold,
		cfg.RAG.Search.Channels.IntentDirected.MinIntentScore,
	))
	multiEngine.SetRetrievalScopeOptions(
		cfg.RAG.Search.Channels.VectorGlobal.ConfidenceThreshold,
		cfg.RAG.Search.Channels.IntentDirected.MinIntentScore,
	)
	multiEngine.SetChannelTimeout(time.Duration(cfg.RAG.Search.Channels.TimeoutMs) * time.Millisecond)
	multiRetriever := rag.NewMultiChannelRetriever(multiEngine)
	retriever := maybeWrapRerankRetriever(multiRetriever, rerankService, cfg.RAG.Rerank.IsEnabledByDefault(), cfg.RAG.Search.Evidence.EffectiveMinRerankScore())
	enrichedRetriever := maybeWrapMetadataEnrichingRetriever(
		retriever,
		rag.NewDBChunkMetadataResolver(gormDB),
		cfg.RAG.Context.Enrich.IsEnabledByDefault(),
	)
	queryNormalizer := rag.NewDBQueryTermNormalizer(termMappingRepo)
	queryNormalizer.SetCacheManager(queryTermCacheManager)
	baseRewriter := rag.NewLLMRewriter(preferredLLMService,
		cfg.RAG.QueryRewrite.MaxHistoryMessages,
		cfg.RAG.QueryRewrite.MaxHistoryChars,
		cfg.RAG.QueryRewrite.IsEnabledByDefault(),
	)
	llmRewriter := rag.NewNormalizingRewriter(queryNormalizer, baseRewriter)

	mcpRegistry := rag.NewMcpToolRegistry()
	mcpExtractor := rag.NewLLMMcpParameterExtractor(preferredLLMService)
	mcpSelector := rag.NewLLMMcpToolSelector(preferredLLMService)
	mcpContextProvider := rag.NewDefaultMcpContextProvider(mcpRegistry, mcpExtractor, mcpSelector)
	graphH := ragHandler.NewGraphHandler(rag.NewGraphQueryService(graphClient))
	if len(cfg.RAG.MCP.Servers) > 0 {
		registerCtx, registerCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := rag.RegisterRemoteMcpServers(registerCtx, mcpRegistry, toMcpServerSpecs(cfg.RAG.MCP.Servers), &http.Client{Timeout: 10 * time.Second}); err != nil {
			slog.Warn("failed to register remote mcp servers", "err", err)
		}
		registerCancel()
	}

	ragPipeline := rag.NewPipeline(llmService,
		func() *rag.DefaultPromptBuilder {
			builder := rag.NewDefaultPromptBuilder(agentPromptResolver)
			builder.SetEngineMode(resolveEngineType(cfg))
			builder.SetCitationEnabled(cfg.RAG.Citation.IsEnabledByDefault())
			return builder
		}(),
		llmRewriter,
		enrichedRetriever,
		memSvc,
	)
	ragPipeline.SetMessageChunkSize(cfg.AI.Stream.MessageChunkSize)
	ragPipeline.SetStreamTimeout(cfg.RAG.Default.SSETimeoutDuration())
	ragPipeline.SetDefaultTopK(cfg.RAG.Search.DefaultTopK)
	ragPipeline.SetCodeRepoPath(cfg.RAG.Code.RepoPath)
	ragPipeline.SetCitationEnabled(cfg.RAG.Citation.IsEnabledByDefault())
	ragPipeline.SetPreferredLLMService(preferredLLMService)
	if redisAvailable {
		ragPipeline.SetStreamTaskRedis(rdb)
	}
	ragPipeline.SetAnswerCache(rag.NewRedisAnswerCacheManager(rdb), cfg.RAG.AnswerCache.IsEnabledByDefault(), cfg.RAG.AnswerCache.TTLDuration())
	ragPipeline.SetMcpContextProvider(mcpContextProvider)
	ragPipeline.SetIntentResolver(intentResolverSvc)
	ragPipeline.SetIntentGuidanceService(intentGuidanceSvc)
	if cfg.RAG.Trace.IsEnabledByDefault() {
		ragPipeline.SetTraceRecorder(rag.NewDBTraceRecorder(gormDB, cfg.RAG.Trace.MaxErrorLength))
	}
	ragChatService := rag.NewQueuedChatService(
		ragPipeline,
		queueLimiter,
		memSvc,
		time.Duration(globalRateLimitCfg.MaxWaitSeconds)*time.Second,
	)
	ragCtl := rag.NewController(ragChatService)
	ragCtl.SetIdempotentGuard(idempotentGuard)
	ragCtl.SetEvalEnabled(cfg.App.Eval.Enabled)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.Recover(),
		middleware.TraceID(),
		middleware.RequestLog(),
		middleware.DB(gormDB),
		middleware.Tenant(),
		middleware.DemoMode(cfg.App.DemoMode),
		middleware.Auth(authSvc),
	)

	registerStatusRoutes(r)

	api := r.Group("/api/ragent")
	{
		api.GET("/limiter-test", func(c *gin.Context) {
			err := queueLimiter.Acquire(c.Request.Context(), ratelimit.AcquireRequest{
				MaxWait: time.Duration(globalRateLimitCfg.MaxWaitSeconds) * time.Second,
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
			conv.POST("/messages/:messageId/recommended-questions", convHandler.RecommendedQuestions)
			conv.PUT("/:conversationId", convHandler.UpdateTitle)
			conv.PUT("/:conversationId/title", convHandler.UpdateTitle)
			conv.DELETE("/:conversationId", convHandler.Delete)
			conv.POST("/feedback", convHandler.SubmitFeedback)
			conv.POST("/messages/:messageId/feedback", convHandler.SubmitFeedback)
			conv.DELETE("/messages/:messageId/feedback", convHandler.DeleteFeedback)
		}

		// Intent tree — 同时注册 /intent-tree/* 和旧风格路径
		registerIntentRoutes(api, intentTreeHandler)

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

		// Sample questions — /rag/* for chat welcome, /sample-questions for Java-compatible admin page.
		api.GET("/rag/sample-questions", adminH.ListRAGSampleQuestions)
		api.GET("/sample-questions", adminH.ListSampleQuestions)
		api.GET("/sample-questions/:id", adminH.GetSampleQuestion)
		api.POST("/sample-questions", adminH.CreateSampleQuestion)
		api.PUT("/sample-questions/:id", adminH.UpdateSampleQuestion)
		api.DELETE("/sample-questions/:id", adminH.DeleteSampleQuestion)

		api.GET("/admin/sample-questions", adminH.ListSampleQuestions)
		api.POST("/admin/sample-questions", adminH.CreateSampleQuestion)
		api.PUT("/admin/sample-questions/:id", adminH.UpdateSampleQuestion)
		api.DELETE("/admin/sample-questions/:id", adminH.DeleteSampleQuestion)

		// Agents — 同时注册 /agents/* 和 /admin/agents/* 兼容路径
		api.GET("/agents", agentH.List)
		api.POST("/agents", agentH.Create)
		api.PUT("/agents/:id", agentH.Update)
		api.DELETE("/agents/:id", agentH.Delete)
		api.POST("/agents/:id/activate", agentH.Activate)
		api.GET("/agents/:id/prompts", agentH.Prompts)
		api.PUT("/agents/:id/prompts/:slotKey", agentH.SavePrompt)
		api.GET("/agents/prompt-slots/:slotKey/default", agentH.DefaultPrompt)

		api.GET("/admin/agents", agentH.List)
		api.POST("/admin/agents", agentH.Create)
		api.PUT("/admin/agents/:id", agentH.Update)
		api.DELETE("/admin/agents/:id", agentH.Delete)
		api.POST("/admin/agents/:id/activate", agentH.Activate)
		api.GET("/admin/agents/:id/prompts", agentH.Prompts)
		api.PUT("/admin/agents/:id/prompts/:slotKey", agentH.SavePrompt)
		api.GET("/admin/agents/prompt-slots/:slotKey/default", agentH.DefaultPrompt)

		api.GET("/admin/users", adminH.ListUsers)
		api.POST("/admin/users", adminH.CreateUser)
		api.PUT("/admin/users/:id", adminH.UpdateUser)
		api.DELETE("/admin/users/:id", adminH.DeleteUser)

		// Audit change logs — 对齐 Java /biz-change-logs
		api.GET("/biz-change-logs", auditH.List)
		api.GET("/biz-change-logs/:id", auditH.Get)

		// RAG settings
		api.GET("/rag/settings", ragSettings(cfg))
		api.GET("/admin/kg/graph", graphH.Graph)
		api.GET("/admin/kg/labels", graphH.Labels)
		registerRagEvalRoute(api, llmRewriter, &ragEvalRetriever{Retriever: enrichedRetriever, mcp: mcpContextProvider}, cfg.App.Eval.Enabled, cfg.RAG.Search.DefaultTopK, intentResolverSvc)
		demoH := newDemoHandler()
		if hasVLM {
			demoH = newDemoHandlerWithVLM(vlmService)
		}
		registerDemoRoutes(r, api, demoH)

		// Knowledge base
		kb := api.Group("/knowledge-base")
		{
			kb.GET("/chunk-strategies", kbHandler.ChunkStrategies)
			kb.GET("/docs/ingestion-spec-schema", docHandler.IngestionSpecSchema)

			kb.POST("/:id/docs/upload", docHandler.Upload)
			kb.GET("/:id/docs/internal-url-import-tasks/:taskId", docHandler.GetInternalURLImportTask)
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
	appCancel()
	ragPipeline.Close()
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

func setupMQ(cfg config.RocketMQConfig) (mq.Producer, mq.Consumer, func(), bool) {
	nameServers := splitCSV(cfg.NameServer)
	if len(nameServers) == 0 {
		producer := mq.NewNoopProducer()
		consumer := mq.NewNoopConsumer()
		return producer, consumer, func() {}, false
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
		return noopProducer, noopConsumer, func() {}, false
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
	return producer, consumer, shutdown, consumer != nil && !isNoopConsumer(consumer)
}

func isNoopConsumer(consumer mq.Consumer) bool {
	_, ok := consumer.(*mq.NoopConsumer)
	return ok
}

func registerIntentRoutes(api *gin.RouterGroup, intentTreeHandler *intentHandler.IntentHandler) {
	api.GET("/intent-tree/trees", intentTreeHandler.GetTree)
	api.POST("/intent-tree", intentTreeHandler.CreateNodeCompat)
	api.PUT("/intent-tree/:id", intentTreeHandler.UpdateNodeCompat)
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

		it.GET("/term-mappings", intentTreeHandler.ListTermMappings)
		it.GET("/term-mappings/:id", intentTreeHandler.GetTermMapping)
		it.POST("/term-mappings", intentTreeHandler.CreateTermMapping)
		it.PUT("/term-mappings/:id", intentTreeHandler.UpdateTermMapping)
		it.DELETE("/term-mappings/:id", intentTreeHandler.DeleteTermMapping)
	}

	api.GET("/mappings", intentTreeHandler.ListTermMappings)
	api.GET("/mappings/:id", intentTreeHandler.GetTermMapping)
	api.POST("/mappings", intentTreeHandler.CreateTermMappingCompat)
	api.PUT("/mappings/:id", intentTreeHandler.UpdateTermMappingCompat)
	api.DELETE("/mappings/:id", intentTreeHandler.DeleteTermMapping)
}

type intentTreeFactoryInitializer interface {
	InitFromFactory(context.Context) (int, error)
}

func maybeInitIntentTreeFromFactory(ctx context.Context, cfg config.AppConfig, svc intentTreeFactoryInitializer) error {
	if !cfg.IntentTree.InitFromFactory {
		return nil
	}
	created, err := svc.InitFromFactory(ctx)
	if err != nil {
		return fmt.Errorf("init intent tree from factory: %w", err)
	}
	slog.Info("intent tree initialized from factory", "created", created)
	return nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

func registerRagEvalRoute(api *gin.RouterGroup, rewriter rag.QueryRewriter, retriever rag.Retriever, enabled bool, topK int, resolvers ...rag.IntentResolutionService) {
	if !enabled {
		return
	}
	var resolver rag.IntentResolutionService
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	api.GET("/rag/eval", ragEval(rewriter, retriever, resolver, topK))
}

func setupAI(cfg *config.Config, logger *slog.Logger) (chat.LLMService, chat.LLMService, embedding.Service, rerank.Service, vlm.Service, bool) {
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
	preferredLLMService := buildPreferredLLMService(aiCfg, health, executor, chatClients, llmService)

	embClients := buildEmbeddingClients(aiCfg)
	embService := embedding.NewRoutingEmbeddingService(
		executor, selector,
		embClients,
		embeddingDimension(aiCfg),
	)

	rerankClients := buildRerankClients(aiCfg)
	rerankService := rerank.NewRoutingRerankService(executor, selector, rerankClients)

	vlmClients := buildVlmClients(aiCfg)
	vlmService := vlm.NewRoutingService(selector, executor, vlmClients)

	logger.Info("ai infra wired",
		"chat_providers", len(chatClients),
		"embed_providers", len(embClients),
		"rerank_providers", len(rerankClients),
		"vlm_providers", len(vlmClients),
	)

	return llmService, preferredLLMService, embService, rerankService, vlmService, len(vlmClients) > 0
}

const preferredLocalChatProvider = "ollama"
const preferredLocalChatID = "qwen3-local"
const preferredLocalChatModel = "qwen3.6:latest"
const defaultOllamaURL = "http://localhost:11434"

func buildPreferredLLMService(aiCfg config.AIConfig, health *model.HealthStore, executor *model.RoutingExecutor, chatClients []chat.ChatClient, fallback chat.LLMService) chat.LLMService {
	localCfg, ok := buildLocalPreferredChatConfig(aiCfg)
	if !ok {
		return fallback
	}

	localClients := buildChatClients(localCfg)
	if len(localClients) == 0 {
		return fallback
	}

	localSelector := model.NewSelector(localCfg, health)
	localService := chat.NewRoutingLLMService(
		localSelector,
		health,
		executor,
		localClients,
		chat.NewFirstPacketProbe(),
		time.Duration(aiCfg.Selection.FirstPacketTimeoutSeconds)*time.Second,
	)
	return chat.NewFallbackLLMService(localService, fallback)
}

func buildLocalPreferredChatConfig(aiCfg config.AIConfig) (config.AIConfig, bool) {
	localCfg := aiCfg
	localCfg.Chat.Candidates = nil
	localCfg.Chat.DeepThinkingModel = ""

	for _, candidate := range aiCfg.Chat.Candidates {
		if candidate.Provider == preferredLocalChatProvider && strings.TrimSpace(candidate.Model) == preferredLocalChatModel {
			if strings.TrimSpace(candidate.ID) == "" {
				candidate.ID = preferredLocalChatID
			}
			localCfg.Chat.Candidates = []config.AICandidateConfig{candidate}
			localCfg.Chat.DefaultModel = candidate.ID
			if provider, ok := localCfg.Providers[preferredLocalChatProvider]; ok {
				localCfg.Providers[preferredLocalChatProvider] = normalizeOllamaProvider(provider)
			}
			filterLocalPreferredChatTiers(&localCfg.Chat)
			return localCfg, true
		}
	}

	if provider, ok := localCfg.Providers[preferredLocalChatProvider]; ok {
		localCfg.Providers[preferredLocalChatProvider] = normalizeOllamaProvider(provider)
		localCfg.Chat.DefaultModel = preferredLocalChatID
		localCfg.Chat.Candidates = []config.AICandidateConfig{{
			ID:       preferredLocalChatID,
			Provider: preferredLocalChatProvider,
			Model:    preferredLocalChatModel,
			Priority: -1000,
		}}
		filterLocalPreferredChatTiers(&localCfg.Chat)
		return localCfg, true
	}

	return aiCfg, false
}

func filterLocalPreferredChatTiers(chatCfg *config.AIChatConfig) {
	if chatCfg == nil || len(chatCfg.Tiers) == 0 {
		return
	}
	// The local-only service must not inherit cloud tier membership: an
	// explicit "fast" request should still reach the local candidate when the
	// cloud configuration has no matching tier or excludes that candidate.
	chatCfg.DefaultTier = ""
	chatCfg.DeepThinkingTier = ""
	chatCfg.Tiers = nil
}

func normalizeOllamaProvider(provider config.AIProviderConfig) config.AIProviderConfig {
	switch strings.ToLower(strings.TrimSpace(provider.Protocol)) {
	case "", "ollama":
		provider.Protocol = "openai-compatible"
	}
	if strings.TrimSpace(provider.URL) == "" {
		provider.URL = defaultOllamaURL
	}
	if provider.Endpoints == nil {
		provider.Endpoints = map[string]string{}
	}
	if strings.TrimSpace(provider.Endpoints["chat"]) == "" {
		provider.Endpoints["chat"] = "/v1/chat/completions"
	}
	return provider
}

func buildChatClients(aiCfg config.AIConfig) []chat.ChatClient {
	var clients []chat.ChatClient
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			client := chat.NewOpenAICompatibleChatClient(name, nil)
			// enable_thinking 是 DashScope 系私有扩展，只对声明认识它的提供商发送，
			// 对齐 Java BaiLianChatClient/SiliconFlowChatClient 的 supportsEnableThinkingParam。
			if supportsEnableThinkingParam(name) {
				client.SupportsEnableThinkingParam = true
			}
			clients = append(clients, client)
		case "anthropic":
			clients = append(clients, chat.NewAnthropicChatClient(name, nil))
		case "noop":
		default:
			slog.Warn("unknown protocol, skipping", "provider", name, "protocol", provider.Protocol)
		}
	}
	return clients
}

// supportsEnableThinkingParam 对齐 Java 覆写了 supportsEnableThinkingParam 的客户端：
// 百炼（DashScope 兼容模式）与 SiliconFlow；其余网关收到该字段会被判为 unknown_parameter。
func supportsEnableThinkingParam(providerName string) bool {
	switch strings.TrimSpace(providerName) {
	case "bailian", "siliconflow", "dashscope":
		return true
	default:
		return false
	}
}

func buildEmbeddingClients(aiCfg config.AIConfig) []embedding.Client {
	var clients []embedding.Client
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			client := embedding.NewOpenAICompatibleEmbeddingClient(name, nil)
			// Ollama 本地服务无鉴权，对齐 Java OllamaEmbeddingClient.requiresApiKey=false。
			if name == "ollama" {
				client.RequiresAPIKey = false
			}
			clients = append(clients, client)
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

func buildVlmClients(aiCfg config.AIConfig) []vlm.Client {
	var clients []vlm.Client
	for name, provider := range aiCfg.Providers {
		switch provider.Protocol {
		case "openai-compatible":
			clients = append(clients, vlm.NewOpenAICompatibleClient(name, nil))
		case "noop":
		default:
			slog.Warn("unknown vlm protocol, skipping", "provider", name, "protocol", provider.Protocol)
		}
	}
	return clients
}

func buildDocumentParserRegistry(cfg *config.Config, vlmService vlm.Service, hasVLM bool, fileStore *knowledgeHandler.FileStore) *coreparser.Registry {
	return buildDocumentParserRegistryWithPermitRunner(cfg, vlmService, hasVLM, fileStore, nil)
}

func buildDocumentParserRegistryWithPermitRunner(cfg *config.Config, vlmService vlm.Service, hasVLM bool, fileStore *knowledgeHandler.FileStore, permitRunner coreparser.MinerUPermitRunner) *coreparser.Registry {
	coreparser.SetDefaultTikaURL(cfg.RAG.Parser.TikaURL)
	reg := coreparser.NewRegistry(nil)
	assetUploader, err := storage.NewRustFSUploader(context.Background(), cfg.RustFS, cfg.RustFS.AssetBucket)
	if strings.TrimSpace(cfg.MinerU.APIKey) != "" {
		minerUClient := coreparser.NewMinerUClient(firstNonEmpty(cfg.MinerU.APIURL, "https://mineru.net/api/v4"), cfg.MinerU.APIKey, nil)
		var uploader storage.Uploader
		if assetUploader != nil {
			uploader = assetUploader
		}
		unpacker := coreparser.NewMinerUResultUnpacker(uploader)
		opts := coreparser.MinerUOptions{
			PollInterval:     time.Duration(cfg.MinerU.PollIntervalSecs) * time.Second,
			Timeout:          time.Duration(cfg.MinerU.TimeoutSecs) * time.Second,
			EnableTable:      cfg.MinerU.EnableTable,
			EnableFormula:    cfg.MinerU.EnableFormula,
			OCR:              cfg.MinerU.OCR,
			Language:         firstNonEmpty(cfg.MinerU.Language, "ch"),
			ConcurrencyLimit: cfg.MinerU.ConcurrencyLimit,
			MaxWait:          time.Duration(cfg.MinerU.MaxWaitSeconds) * time.Second,
			Lease:            time.Duration(cfg.MinerU.LeaseSeconds) * time.Second,
			PermitRunner:     permitRunner,
		}
		reg.Register(coreparser.NewMinerUParser(minerUClient, unpacker, opts))
	}
	if err == nil && vlmService != nil && hasVLM {
		reg.Register(coreparser.NewImageParser(
			vlmService,
			assetUploader,
			cfg.RAG.ImageParse.DescriptionPrompt,
			cfg.RAG.ImageParse.MaxOutputTokens,
		))
	} else if err != nil {
		slog.Warn("asset uploader unavailable, image parser disabled", "err", err)
	}
	reg.Register(&coreparser.MarkdownParser{})
	reg.Register(&coreparser.CSVParser{})
	reg.Register(&coreparser.XLSParser{})
	reg.Register(&coreparser.XLSXParser{})
	reg.Register(&coreparser.PDFParser{})
	reg.Register(&coreparser.DOCXParser{})
	reg.Register(&coreparser.PPTXParser{})
	reg.Register(&coreparser.HTMLParser{})
	reg.Register(&coreparser.XMLParser{})
	reg.Register(&coreparser.PlainTextParser{})
	if tikaURL := strings.TrimSpace(cfg.RAG.Parser.TikaURL); tikaURL != "" {
		reg.Register(coreparser.NewTikaParser(tikaURL))
	}

	_ = fileStore
	return reg
}

func embeddingDimension(aiCfg config.AIConfig) int {
	if len(aiCfg.Embedding.Candidates) > 0 {
		if d := aiCfg.Embedding.Candidates[0].Dimension; d > 0 {
			return d
		}
	}
	return 1536
}

func toMcpServerSpecs(servers []config.RAGMCPServerConfig) []rag.McpServerSpec {
	specs := make([]rag.McpServerSpec, 0, len(servers))
	for _, server := range servers {
		specs = append(specs, rag.McpServerSpec{
			Name:    server.Name,
			URL:     server.URL,
			Domains: append([]string(nil), server.Domains...),
		})
	}
	return specs
}

func ragSettings(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, convention.Success(ragSettingsPayload(cfg)))
	}
}

func ragEval(rewriter rag.QueryRewriter, retriever rag.Retriever, resolver rag.IntentResolutionService, topK int) gin.HandlerFunc {
	return func(c *gin.Context) {
		question := c.Query("question")
		if question == "" {
			c.JSON(http.StatusOK, convention.Failure("A000001", "question不能为空"))
			return
		}
		start := time.Now()
		subQuestions := []string{question}
		rewrittenQuestion := question
		if rewriter != nil {
			if result, err := rewriter.Rewrite(c.Request.Context(), question, nil); err == nil && result != nil {
				if strings.TrimSpace(result.RewrittenQuestion) != "" {
					rewrittenQuestion = strings.TrimSpace(result.RewrittenQuestion)
				}
				if len(result.SubQuestions) > 0 {
					subQuestions = result.SubQuestions
				}
			}
		}
		intentLeafIDs := nullableIntentLeafIDs(subQuestions)
		subIntents := []rag.SubQuestionIntent(nil)
		if resolver != nil {
			if resolved, err := resolver.ResolveQuestions(c.Request.Context(), evalIntentQuestions(rewrittenQuestion, subQuestions)); err == nil && len(resolved) > 0 {
				subIntents = resolved
				intentLeafIDs = rag.IntentLeafIDs(resolved)
			}
		}
		searchContext := rag.SearchContext{
			OriginalQuestion:  question,
			RewrittenQuestion: rewrittenQuestion,
			SubQuestions:      subQuestions,
			Intents:           subIntents,
			TopK:              resolveEvalTopK(topK),
		}
		mcpContext := ""
		hasMcp := false
		if provider, ok := retriever.(interface {
			BuildMcpContext(context.Context, string, []rag.SubQuestionIntent) (string, error)
		}); ok {
			if ctxText, err := provider.BuildMcpContext(c.Request.Context(), rewrittenQuestion, subIntents); err == nil {
				mcpContext = strings.TrimSpace(ctxText)
				hasMcp = mcpContext != ""
			}
		}
		chunks, err := evalRetrieve(c.Request.Context(), retriever, searchContext)
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
				docID = evalBusinessDocID(chunk.Metadata)
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
			"hasMcp":                 hasMcp,
			"mcpContext":             mcpContext,
			"subIntents":             subQuestions,
			"intentLeafIds":          intentLeafIDs,
			"latencyMs":              time.Since(start).Milliseconds(),
		}))
	}
}

func resolveEvalTopK(topK int) int {
	if topK > 0 {
		return topK
	}
	return 10
}

type ragEvalRetriever struct {
	rag.Retriever
	mcp rag.McpContextProvider
}

func (r *ragEvalRetriever) BuildMcpContext(ctx context.Context, question string, subIntents []rag.SubQuestionIntent) (string, error) {
	if r == nil || r.mcp == nil {
		return "", nil
	}
	if aware, ok := r.mcp.(rag.McpIntentAwareContextProvider); ok {
		return aware.BuildContextWithIntents(ctx, question, subIntents)
	}
	return r.mcp.BuildContext(ctx, question)
}

func maybeWrapMetadataEnrichingRetriever(base rag.Retriever, resolver rag.ChunkMetadataResolver, enabled bool) rag.Retriever {
	if !enabled {
		return base
	}
	return rag.NewMetadataEnrichingRetriever(base, resolver)
}

func maybeWrapRerankRetriever(base rag.Retriever, rerankSvc rerank.Service, enabled bool, minRerankScore float64) rag.Retriever {
	if !enabled {
		return base
	}
	return rag.NewRerankRetriever(base, rerankSvc, minRerankScore)
}

func keywordSearchChannelEnabled(cfg config.RAGSearchChannelConfig) bool {
	return cfg.IsEnabledByDefault()
}

func searchChannelNames(channels []rag.SearchChannel) []string {
	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		names = append(names, channel.Name())
	}
	return names
}

func evalRetrieve(ctx context.Context, retriever rag.Retriever, sc rag.SearchContext) ([]rag.RetrievedChunk, error) {
	if retriever == nil {
		return nil, nil
	}
	if intentAware, ok := retriever.(rag.IntentAwareRetriever); ok {
		return intentAware.RetrieveWithContext(ctx, sc)
	}
	return retriever.Retrieve(ctx, firstNonEmpty(sc.RewrittenQuestion, sc.OriginalQuestion), sc.TopK)
}

func evalBusinessDocID(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}
	docName := strings.TrimSpace(metadata["doc_name"])
	if docName != "" {
		return stripEvalDocExtension(docName)
	}
	return strings.TrimSpace(metadata["doc_id"])
}

func stripEvalDocExtension(docName string) string {
	docName = strings.TrimSpace(docName)
	if docName == "" {
		return ""
	}
	dot := strings.LastIndex(docName, ".")
	if dot > 0 && dot < len(docName)-1 {
		return docName[:dot]
	}
	return docName
}

func evalIntentQuestions(rewrittenQuestion string, subQuestions []string) []string {
	if len(subQuestions) > 0 {
		return subQuestions
	}
	if strings.TrimSpace(rewrittenQuestion) == "" {
		return nil
	}
	return []string{rewrittenQuestion}
}

func nullableIntentLeafIDs(subQuestions []string) []*string {
	ids := make([]*string, 0, len(subQuestions))
	for range subQuestions {
		ids = append(ids, nil)
	}
	return ids
}
