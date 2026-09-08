package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go-base-agent/internal/biz/codeqna"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/infra/chat"

	"github.com/redis/go-redis/v9"
)

// Pipeline orchestrates the RAG chat flow.
// Aligns with Java StreamChatPipeline.
type Pipeline struct {
	llm              chat.LLMService
	preferredLLM     chat.LLMService
	prompt           PromptBuilder
	rewrite          QueryRewriter
	retrieve         Retriever
	memory           MemoryService
	mcp              McpContextProvider
	intentResolver   IntentResolutionService
	guidance         *IntentGuidanceService
	trace            TraceRecorder
	tasks            *streamTaskManager
	answerCache      AnswerCacheManager
	answerCacheOn    bool
	answerCacheTTL   time.Duration
	messageChunkSize int
	streamTimeout    time.Duration
	defaultTopK      int
	codeRepoPath     string
	citationEnabled  bool
}

// NewPipeline creates a new RAG pipeline.
func NewPipeline(llm chat.LLMService, prompt PromptBuilder, rewrite QueryRewriter, retrieve Retriever, memory MemoryService) *Pipeline {
	return &Pipeline{llm: llm, prompt: prompt, rewrite: rewrite, retrieve: retrieve, memory: memory, tasks: newStreamTaskManager()}
}

// SetMcpContextProvider sets an optional MCP context provider for chat prompts.
func (p *Pipeline) SetMcpContextProvider(provider McpContextProvider) {
	p.mcp = provider
}

// SetIntentResolver sets an optional intent resolver for intent-aware retrieval and guidance.
func (p *Pipeline) SetIntentResolver(resolver IntentResolutionService) {
	p.intentResolver = resolver
}

// SetIntentGuidanceService sets an optional ambiguity guidance service.
func (p *Pipeline) SetIntentGuidanceService(guidance *IntentGuidanceService) {
	p.guidance = guidance
}

// SetTraceRecorder sets an optional recorder for RAG trace runs and nodes.
func (p *Pipeline) SetTraceRecorder(recorder TraceRecorder) {
	p.trace = recorder
}

// SetMessageChunkSize sets the SSE message delta chunk size in runes.
func (p *Pipeline) SetMessageChunkSize(size int) {
	p.messageChunkSize = size
}

// SetStreamTimeout sets the SSE pipeline timeout.
func (p *Pipeline) SetStreamTimeout(timeout time.Duration) {
	p.streamTimeout = timeout
}

// SetDefaultTopK sets the default retrieval TopK for the pipeline.
func (p *Pipeline) SetDefaultTopK(topK int) {
	p.defaultTopK = topK
}

// SetCodeRepoPath sets an optional code repository path for code Q&A evidence.
func (p *Pipeline) SetCodeRepoPath(repoPath string) {
	p.codeRepoPath = strings.TrimSpace(repoPath)
}

// SetCitationEnabled 设置知识库回答的行内引用开关。
func (p *Pipeline) SetCitationEnabled(enabled bool) {
	if p != nil {
		p.citationEnabled = enabled
	}
}

// SetPreferredLLMService sets the lightweight LLM used for non-RAG responses.
func (p *Pipeline) SetPreferredLLMService(llm chat.LLMService) {
	p.preferredLLM = llm
}

// SetStreamTaskRedis enables cross-instance stream cancellation through Redis.
func (p *Pipeline) SetStreamTaskRedis(client *redis.Client) {
	if p != nil && p.tasks != nil {
		p.tasks.setRedisClient(client)
	}
}

// Close releases the stream task cancellation subscription.
func (p *Pipeline) Close() {
	if p != nil && p.tasks != nil {
		p.tasks.close()
	}
}

// SetAnswerCache configures completed answer cache for standalone RAG questions.
func (p *Pipeline) SetAnswerCache(cache AnswerCacheManager, enabled bool, ttl time.Duration) {
	p.answerCache = cache
	p.answerCacheOn = enabled
	p.answerCacheTTL = ttl
}

// StreamChat implements Service.StreamChat.
func (p *Pipeline) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	// Timeout context: the entire pipeline should complete within the configured window.
	timeout := p.streamTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	persistenceCtx := context.WithoutCancel(ctx)
	task := p.tasks.register(taskID, sender, cancel)
	defer p.tasks.unregister(taskID)

	traceRun := p.startTraceRun(ctx, conversationID, taskID)
	finishTraceRun := func(status string, err error) {
		if traceRun == nil || p.trace == nil {
			return
		}
		if finishErr := p.trace.FinishRun(persistenceCtx, traceRun.TraceID, status, err); finishErr != nil {
			slog.Warn("rag trace: finish run failed", "traceId", traceRun.TraceID, "err", finishErr)
		}
	}

	// Start heartbeat to keep SSE connection alive during LLM processing
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				if sender.IsClosed() {
					return
				}
				_ = sender.SendMessage("heartbeat", "")
			}
		}
	}()
	defer close(heartbeatDone)

	historySpan := p.startTraceNode(ctx, traceRun, "", "load-history", "MEMORY", 0)
	history, err := p.memory.LoadHistory(ctx, conversationID)
	if err != nil {
		slog.Warn("rag memory: load history failed", "conversationId", conversationID, "err", err)
		history = nil
		historySpan.finish(traceStatusError, err)
	} else {
		historySpan.finish(traceStatusSuccess, nil)
	}

	rewriteSpan := p.startTraceNode(ctx, traceRun, "", "rewrite", "REWRITE", 0)
	result, err := p.rewrite.Rewrite(ctx, question, history)
	q := question
	var subQuestions []string
	if err != nil {
		slog.Warn("rag: rewrite failed", "err", err)
		rewriteSpan.finish(traceStatusError, err)
	} else if result != nil {
		rewriteSpan.finish(traceStatusSuccess, nil)
		subQuestions = result.SubQuestions
		if result.RewrittenQuestion != "" {
			slog.Info("rag: query rewritten", "from", question, "to", result.RewrittenQuestion)
			q = result.RewrittenQuestion
		}
	} else {
		rewriteSpan.finish(traceStatusSuccess, nil)
	}

	var resolvedSubIntents []SubQuestionIntent
	if p.intentResolver != nil {
		intentSpan := p.startTraceNode(ctx, traceRun, "", "intent-resolve", "INTENT", 0)
		intentQuestions := subQuestions
		if len(intentQuestions) == 0 {
			intentQuestions = []string{q}
		}
		resolvedSubIntents, err = p.intentResolver.ResolveQuestions(ctx, intentQuestions)
		if err != nil {
			slog.Warn("rag: intent resolve failed", "err", err)
			if intentSpan != nil {
				intentSpan.finish(traceStatusError, err)
			}
		} else if intentSpan != nil {
			intentSpan.finish(traceStatusSuccess, nil)
		}
	}

	if p.guidance != nil {
		guidanceSpan := p.startTraceNode(ctx, traceRun, "", "guidance-detect", "GUIDANCE", 0)
		decision := p.guidance.DetectAmbiguity(ctx, q, resolvedSubIntents)
		if guidanceSpan != nil {
			guidanceSpan.finish(traceStatusSuccess, nil)
		}
		if decision.Action == GuidanceActionPrompt && strings.TrimSpace(decision.Prompt) != "" {
			sendTitleOnComplete := shouldSendTitleOnComplete(persistenceCtx, p.memory, conversationID)
			_, _ = appendConversationMessage(persistenceCtx, p.memory, conversationID, chat.NewUserMessage(question))
			sender.SendMessage(MsgTypeResponse, decision.Prompt)
			sender.SendFinishWithStatus("", resolveConversationTitle(persistenceCtx, p.memory, conversationID, sendTitleOnComplete), nil, MessageStatusNormal)
			sender.SendDone()
			sender.Close()
			finishTraceRun(traceStatusSuccess, nil)
			return
		}
	}

	if systemPrompt, ok := p.systemOnlyPrompt(resolvedSubIntents); ok {
		p.streamSystemOnlyResponse(ctx, persistenceCtx, q, conversationID, history, task, sender, traceRun, p.lightweightLLM(), systemPrompt)
		return
	}

	mcpCtx := p.buildMcpContext(ctx, q, resolvedSubIntents)
	retrieveSpan := p.startTraceNode(ctx, traceRun, "", "retrieve", "RETRIEVE", 0)
	chunks, directedIntentIDs, err := p.retrieveChunks(ctx, q, subQuestions, resolvedSubIntents, p.resolveDefaultTopK())
	var kbCtx string
	var answerCacheKey string
	if err != nil {
		retrieveSpan.finish(traceStatusError, err)
		slog.Warn("rag: retrieve failed", "err", err)
		p.streamRetrievalFallback(ctx, persistenceCtx, conversationID, question, sender, task, p.lightweightLLM(), "检索失败原因：知识库检索执行失败："+err.Error())
		finishTraceRun(traceStatusSuccess, nil)
		return
	} else if len(chunks) > 0 {
		retrieveSpan.finish(traceStatusSuccess, nil)
		slog.Info("rag: chunks retrieved", "count", len(chunks))
		chunks = selectFinalEvidenceChunks(chunks, q, p.resolveDefaultTopK(), intentEvidenceCollections(resolvedSubIntents))
		answerCacheKey = p.answerCacheKey(question, deepThinking, history, mcpCtx, chunks)
		if answerCacheKey != "" {
			if cached, hit := p.loadCachedAnswer(ctx, answerCacheKey); hit {
				p.streamCachedAnswer(persistenceCtx, conversationID, question, cached, sender, task)
				finishTraceRun(traceStatusSuccess, nil)
				return
			}
		}
		for _, c := range chunks {
			kbCtx += c.Text + "\n"
		}
	} else {
		retrieveSpan.finish(traceStatusSuccess, nil)
		if strings.TrimSpace(mcpCtx) == "" {
			slog.Warn("rag: no chunks found for question", "question", runeLimit(q, 50))
			p.streamRetrievalFallback(ctx, persistenceCtx, conversationID, question, sender, task, p.lightweightLLM(), "检索失败原因：知识库中未检索到相关内容，已完成向量检索但没有召回与问题相关的文档片段。")
			finishTraceRun(traceStatusSuccess, nil)
			return
		}
	}

	sendTitleOnComplete := shouldSendTitleOnComplete(persistenceCtx, p.memory, conversationID)
	codeCtx := p.buildCodeContext(ctx, q)

	// 按库推导意图归属：只有真正贡献了证据的意图才允许参与提示词模板选择，
	// 区分知识定向检索未命中意图与全局回退场景。对齐 Java 9d80d7a/cf2697c。
	mergedGroup := MergeIntentGroup(resolvedSubIntents)
	eligibleIntentIDs := EligibleIntentIDs(chunks, mergedGroup.KBIntents, directedIntentIDs)
	sources := AssembleSources(chunks)
	grounding := AssembleGroundingChunks(chunks)
	kbContext := buildKbSnippetSection(mergedGroup.KBIntents, eligibleIntentIDs) +
		EnrichCitationContext(withChunkSources(chunks, kbCtx), sources, p.citationEnabled)

	req := p.prompt.Build(PromptContext{
		Question:          q,
		SubQuestions:      subQuestions,
		History:           history,
		KbContext:         kbContext,
		McpContext:        mcpCtx,
		CodeContext:       codeCtx,
		KbIntents:         mergedGroup.KBIntents,
		McpIntents:        mergedGroup.MCPIntents,
		EligibleIntentIds: eligibleIntentIDs,
	})
	thinkingVal := deepThinking
	req.Thinking = &thinkingVal
	// 对齐 Java streamLLMResponse：MCP 场景稍微放宽温度，纯 KB 场景追求稳定输出。
	if strings.TrimSpace(mcpCtx) != "" {
		req.Temperature = floatPtr(0.3)
		req.TopP = floatPtr(0.8)
	} else {
		req.Temperature = floatPtr(0)
		req.TopP = floatPtr(1)
	}
	answerLLM := p.llm
	if len(chunks) == 0 && strings.TrimSpace(mcpCtx) == "" {
		answerLLM = p.lightweightLLM()
	}

	questionMessageID, err := appendConversationMessage(persistenceCtx, p.memory, conversationID, chat.NewUserMessage(question))
	if err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
	}

	cb := &pipelineCallback{
		ctx:                 persistenceCtx,
		conversationID:      conversationID,
		memory:              p.memory,
		sender:              sender,
		citations:           formatCitations(chunks),
		sources:             sources,
		grounding:           grounding,
		replyToMessageID:    questionMessageID,
		task:                task,
		traceRecorder:       p.trace,
		traceRun:            traceRun,
		sendTitleOnComplete: sendTitleOnComplete,
		messageChunkSize:    p.messageChunkSize,
		answerCache:         p.answerCache,
		answerCacheKey:      answerCacheKey,
		answerCacheTTL:      p.answerCacheTTL,
	}
	task.setCancelPayloadFn(cb.buildCompletionPayloadOnCancel)
	llmSpan := p.startTraceNode(ctx, traceRun, "", "llm-stream", "LLM", 0)
	cb.traceSpan = llmSpan
	if ctx.Err() != nil {
		if task.isCancelled() {
			llmSpan.finish(traceStatusCancelled, nil)
			finishTraceRun(traceStatusCancelled, nil)
			return
		}
		slog.Info("rag pipeline: cancelled before llm call", "err", ctx.Err())
		llmSpan.finish(traceStatusError, ctx.Err())
		sender.SendFinish("", "")
		sender.SendDone()
		sender.Close()
		finishTraceRun(traceStatusError, ctx.Err())
		return
	}
	handle, err := answerLLM.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: stream chat failed", "err", err)
		llmSpan.finish(traceStatusError, err)
		cb.OnError(err)
		finishTraceRun(traceStatusError, err)
		return
	}
	task.bindHandle(handle)
	slog.Info("rag pipeline: llm stream started")
	handle.Wait()
	if task.isCancelled() {
		llmSpan.finish(traceStatusCancelled, nil)
		finishTraceRun(traceStatusCancelled, nil)
		return
	}
	llmSpan.finish(traceStatusSuccess, nil)
	finishTraceRun(traceStatusSuccess, nil)
}

func (p *Pipeline) lightweightLLM() chat.LLMService {
	if p != nil && p.preferredLLM != nil {
		return p.preferredLLM
	}
	if p != nil {
		return p.llm
	}
	return nil
}

func (p *Pipeline) resolveDefaultTopK() int {
	if p != nil && p.defaultTopK > 0 {
		return p.defaultTopK
	}
	return 10
}

func (p *Pipeline) answerCacheKey(question string, deepThinking bool, history []chat.Message, mcpCtx string, chunks []RetrievedChunk) string {
	if p == nil || !p.answerCacheOn || p.answerCache == nil || p.answerCacheTTL <= 0 {
		return ""
	}
	if len(history) > 0 || strings.TrimSpace(mcpCtx) != "" || len(chunks) == 0 {
		return ""
	}
	return buildAnswerCacheKey(question, deepThinking, answerCacheEvidenceKeys(chunks))
}

func (p *Pipeline) loadCachedAnswer(ctx context.Context, key string) (*CachedAnswer, bool) {
	if p == nil || p.answerCache == nil || strings.TrimSpace(key) == "" {
		return nil, false
	}
	answer, hit, err := p.answerCache.LoadAnswer(ctx, key)
	if err != nil {
		slog.Warn("rag answer cache: load failed", "err", err)
		return nil, false
	}
	if !hit || answer == nil {
		return nil, false
	}
	return answer, true
}

func (p *Pipeline) streamCachedAnswer(ctx context.Context, conversationID, question string, cached *CachedAnswer, sender *SSESender, task *streamTask) {
	sendTitleOnComplete := shouldSendTitleOnComplete(ctx, p.memory, conversationID)
	questionMessageID, err := appendConversationMessage(ctx, p.memory, conversationID, chat.NewUserMessage(question))
	if err != nil {
		slog.Warn("rag memory: save cached user message failed", "conversationId", conversationID, "err", err)
	}
	if task != nil && task.isCancelled() {
		return
	}
	cb := &pipelineCallback{
		ctx:                 ctx,
		conversationID:      conversationID,
		memory:              p.memory,
		sender:              sender,
		answerPrefix:        cached.Content,
		citations:           cached.Citations,
		sources:             unmarshalSources(cached.SourcesJSON),
		grounding:           ParseGroundingChunks(cached.GroundingJSON),
		replyToMessageID:    questionMessageID,
		sendTitleOnComplete: sendTitleOnComplete,
		thinkingDuration:    cached.ThinkingDuration,
	}
	msg := chat.Message{
		Role:             chat.RoleAssistant,
		Content:          cached.fullContent(),
		ThinkingContent:  cached.ThinkingContent,
		ThinkingDuration: cached.ThinkingDuration,
		Sources:          cached.SourcesJSON,
		RetrievedChunks:  cached.GroundingJSON,
		ReplyToMessageID: questionMessageID,
		MessageStatus:    chat.MessageStatusNormal,
	}
	messageID, err := appendConversationMessage(ctx, p.memory, conversationID, msg)
	if err != nil {
		slog.Warn("rag memory: save cached assistant message failed", "conversationId", conversationID, "err", err)
		messageID = ""
	}
	sendChunkedToSender(sender, MsgTypeResponse, cached.Content, p.messageChunkSize)
	if cached.Citations != "" {
		_ = sender.SendMessage(MsgTypeResponse, cached.Citations)
	}
	sender.SendFinishWithSources(messageID, cb.resolveConversationTitle(), cb.sources)
	sender.SendDone()
	sender.Close()
}

func (p *Pipeline) startTraceRun(ctx context.Context, conversationID, taskID string) *TraceRunRecord {
	if p.trace == nil {
		return nil
	}
	run, err := p.trace.StartRun(ctx, conversationID, taskID)
	if err != nil {
		slog.Warn("rag trace: start run failed", "conversationId", conversationID, "taskId", taskID, "err", err)
		return nil
	}
	if run != nil && run.TraceID != "" {
		ctx = appctx.WithTraceID(ctx, run.TraceID)
	}
	return run
}

func (p *Pipeline) startTraceNode(ctx context.Context, run *TraceRunRecord, parentNodeID, nodeName, nodeType string, depth int) *traceSpan {
	if p.trace == nil || run == nil || run.TraceID == "" {
		return nil
	}
	finishCtx := context.WithoutCancel(ctx)
	node, err := p.trace.StartNode(ctx, run.TraceID, parentNodeID, nodeName, nodeType, depth)
	if err != nil {
		slog.Warn("rag trace: start node failed", "traceId", run.TraceID, "node", nodeName, "err", err)
		return nil
	}
	return &traceSpan{ctx: finishCtx, recorder: p.trace, traceID: run.TraceID, nodeID: node.NodeID}
}

func (p *Pipeline) buildMcpContext(ctx context.Context, question string, subIntents []SubQuestionIntent) string {
	if p.mcp == nil {
		return ""
	}
	if provider, ok := p.mcp.(McpIntentAwareContextProvider); ok {
		mcpCtx, err := provider.BuildContextWithIntents(ctx, question, subIntents)
		if err != nil {
			slog.Warn("rag: build mcp context failed", "err", err)
			return ""
		}
		return mcpCtx
	}
	mcpCtx, err := p.mcp.BuildContext(ctx, question)
	if err != nil {
		slog.Warn("rag: build mcp context failed", "err", err)
		return ""
	}
	return mcpCtx
}

func (p *Pipeline) retrieveChunks(ctx context.Context, question string, subQuestions []string, subIntents []SubQuestionIntent, topK int) ([]RetrievedChunk, map[string]struct{}, error) {
	if aware, ok := p.retrieve.(IntentAwareRetriever); ok {
		return p.retrieveChunksWithContext(ctx, question, subQuestions, subIntents, topK, aware)
	}
	queries := retrievalQueries(question, subQuestions)
	allChunks := make([]RetrievedChunk, 0)
	for _, query := range queries {
		chunks, err := p.retrieve.Retrieve(ctx, query, topK)
		if err != nil {
			return nil, nil, err
		}
		allChunks = append(allChunks, chunks...)
	}
	return deduplicateChunks(allChunks), nil, nil
}

func (p *Pipeline) retrieveChunksWithContext(ctx context.Context, question string, subQuestions []string, subIntents []SubQuestionIntent, topK int, aware IntentAwareRetriever) ([]RetrievedChunk, map[string]struct{}, error) {
	queries := retrievalQueries(question, subQuestions)
	if len(subIntents) == 0 {
		result, err := retrieveWithScope(ctx, aware, SearchContext{
			OriginalQuestion:  question,
			RewrittenQuestion: question,
			SubQuestions:      queries,
			TopK:              topK,
		})
		if err != nil {
			return nil, nil, err
		}
		return deduplicateChunks(result.Chunks), result.DirectedIntentIDs, nil
	}

	// 子问题级并行检索，对齐 Java RetrievalEngine.retrieve：每个子问题独立并发执行，
	// 单个子问题失败降级为空结果继续（error 日志），不中断其余子问题。
	// 按索引回填保持子问题顺序，避免并发下合并结果不稳定。
	results := make([]RetrievalResult, len(subIntents))
	var wg sync.WaitGroup
	for i, subIntent := range subIntents {
		query := strings.TrimSpace(subIntent.SubQuestion)
		if query == "" {
			query = question
		}
		sc := SearchContext{
			OriginalQuestion:  question,
			RewrittenQuestion: query,
			SubQuestions:      []string{query},
			Intents:           []SubQuestionIntent{subIntent},
			TopK:              topK,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := retrieveWithScope(ctx, aware, sc)
			if err != nil {
				slog.Error("子问题上下文构建失败，降级为空上下文", "question", query, "err", err)
				return
			}
			results[i] = result
		}()
	}
	wg.Wait()

	allChunks := make([]RetrievedChunk, 0)
	directedIntentIDs := make(map[string]struct{})
	for _, result := range results {
		allChunks = append(allChunks, result.Chunks...)
		for id := range result.DirectedIntentIDs {
			directedIntentIDs[id] = struct{}{}
		}
	}
	return deduplicateChunks(allChunks), directedIntentIDs, nil
}

func retrieveWithScope(ctx context.Context, aware IntentAwareRetriever, sc SearchContext) (RetrievalResult, error) {
	if scoped, ok := aware.(ScopedIntentAwareRetriever); ok {
		return scoped.RetrieveWithContextResult(ctx, sc)
	}
	chunks, err := aware.RetrieveWithContext(ctx, sc)
	return RetrievalResult{Chunks: chunks}, err
}

func retrievalQueries(question string, subQuestions []string) []string {
	seen := make(map[string]bool)
	queries := make([]string, 0, len(subQuestions)+1)
	for _, query := range append([]string{question}, subQuestions...) {
		query = strings.TrimSpace(query)
		if query == "" || seen[query] {
			continue
		}
		seen[query] = true
		queries = append(queries, query)
	}
	return queries
}

func deduplicateChunks(chunks []RetrievedChunk) []RetrievedChunk {
	seen := make(map[string]bool)
	deduped := make([]RetrievedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		key := chunk.ID
		if key == "" {
			key = chunk.Text
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, chunk)
	}
	return deduped
}

func (p *Pipeline) streamRetrievalFallback(ctx, persistenceCtx context.Context, conversationID, question string, sender *SSESender, task *streamTask, llm chat.LLMService, reason string) {
	sendTitleOnComplete := shouldSendTitleOnComplete(persistenceCtx, p.memory, conversationID)
	questionMessageID, err := appendConversationMessage(persistenceCtx, p.memory, conversationID, chat.NewUserMessage(question))
	if err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
	}
	if task != nil && task.isCancelled() {
		return
	}
	prefix := reason + "\n\n"
	_ = sender.SendMessage(MsgTypeResponse, prefix)

	req := chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage("你是一个RAG问答助手。当前知识库检索没有可用结果，请不要回答用户原问题的事实内容，只做提问引导。"),
			chat.NewUserMessage("用户原问题：" + question + "\n" + reason + "\n请基于以上原因，引导用户补充关键词、确认知识库范围、检查文档是否已入库，或换一种问法。"),
		},
	}
	cb := &pipelineCallback{
		ctx:                 persistenceCtx,
		conversationID:      conversationID,
		memory:              p.memory,
		sender:              sender,
		answerPrefix:        prefix,
		replyToMessageID:    questionMessageID,
		task:                task,
		sendTitleOnComplete: sendTitleOnComplete,
		messageChunkSize:    p.messageChunkSize,
	}
	task.setCancelPayloadFn(cb.buildCompletionPayloadOnCancel)
	if llm == nil {
		llm = p.llm
	}
	handle, err := llm.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: retrieval fallback stream failed", "err", err)
		cb.OnError(err)
		return
	}
	if task != nil {
		task.bindHandle(handle)
	}
	handle.Wait()
}

func (p *Pipeline) buildCodeContext(ctx context.Context, question string) string {
	if p == nil {
		return ""
	}
	repoPath := strings.TrimSpace(appctx.CodeRepoPath(ctx))
	if repoPath == "" {
		repoPath = strings.TrimSpace(p.codeRepoPath)
	}
	if repoPath == "" {
		return ""
	}
	items, err := codeqna.Search(ctx, codeqna.SearchRequest{
		RepoPath: repoPath,
		Question: question,
		MaxLines: 20,
	})
	if err != nil || len(items) == 0 {
		return ""
	}
	return codeqna.FormatEvidence(items)
}

// StopTask implements Service.StopTask.
func (p *Pipeline) StopTask(taskID string) {
	slog.Info("rag pipeline: stop task", "taskId", taskID)
	p.tasks.cancel(taskID)
}

// pipelineCallback converts LLM StreamCallback events to SSE events.
type pipelineCallback struct {
	ctx                 context.Context
	conversationID      string
	memory              MemoryService
	sender              *SSESender
	answer              strings.Builder
	thinking            strings.Builder
	answerPrefix        string
	citations           string
	sources             []SourceRef
	grounding           []GroundingChunk
	replyToMessageID    string
	task                *streamTask
	traceRecorder       TraceRecorder
	traceRun            *TraceRunRecord
	traceSpan           *traceSpan
	sendTitleOnComplete bool
	thinkingStart       time.Time
	thinkingDuration    int
	messageChunkSize    int
	firstPacket         bool
	answerCache         AnswerCacheManager
	answerCacheKey      string
	answerCacheTTL      time.Duration
}

func (c *pipelineCallback) OnContent(content string) {
	if c.task != nil && c.task.isCancelled() {
		return
	}
	slog.Info("rag pipeline: llm content chunk", "len", len(content))
	c.recordFirstPacket()
	c.answer.WriteString(content)
	c.sendChunked(MsgTypeResponse, content)
}

func (c *pipelineCallback) OnThinking(content string) {
	if c.task != nil && c.task.isCancelled() {
		return
	}
	slog.Info("rag pipeline: llm thinking chunk", "len", len(content))
	c.recordFirstPacket()
	if c.thinkingStart.IsZero() {
		c.thinkingStart = time.Now()
	}
	c.thinking.WriteString(content)
	c.sendChunked(MsgTypeThink, content)
}

func (c *pipelineCallback) OnComplete() {
	if c.task != nil && c.task.isCancelled() {
		c.traceSpan.finish(traceStatusCancelled, nil)
		return
	}
	c.traceSpan.finish(traceStatusSuccess, nil)
	slog.Info("rag pipeline: llm stream complete")
	messageID := c.saveCompletedAssistantMessage()
	title := c.resolveConversationTitle()
	if c.citations != "" {
		_ = c.sender.SendMessage(MsgTypeResponse, c.citations)
	}
	c.sender.SendFinishWithStatus(messageID, title, c.sources, MessageStatusNormal)
	c.sender.SendDone()
	c.sender.Close()
}

func (c *pipelineCallback) OnError(err error) {
	if c.task != nil && c.task.isCancelled() {
		c.traceSpan.finish(traceStatusCancelled, nil)
		return
	}
	c.traceSpan.finish(traceStatusError, err)
	slog.Error("rag pipeline: llm error", "err", err)
	// Ignore send errors — client may have already disconnected
	_ = c.sender.SendFinish("", "")
	_ = c.sender.SendDone()
	c.sender.Close()
}

func (c *pipelineCallback) sendChunked(msgType, content string) {
	sendChunkedToSender(c.sender, msgType, content, c.messageChunkSize)
}

func sendChunkedToSender(sender *SSESender, msgType, content string, size int) {
	if sender == nil {
		return
	}
	if size <= 0 {
		sender.SendMessage(msgType, content)
		return
	}
	var b strings.Builder
	count := 0
	for _, r := range content {
		b.WriteRune(r)
		count++
		if count >= size {
			_ = sender.SendMessage(msgType, b.String())
			b.Reset()
			count = 0
		}
	}
	if b.Len() > 0 {
		_ = sender.SendMessage(msgType, b.String())
	}
}

func (c *pipelineCallback) buildCompletionPayloadOnCancel() CompletionPayload {
	return CompletionPayload{
		MessageID:     c.saveCancelledAssistantMessage(),
		Title:         c.resolveConversationTitle(),
		Sources:       c.sources,
		MessageStatus: MessageStatusInterrupted,
	}
}

func (c *pipelineCallback) recordFirstPacket() {
	if c.firstPacket || c.traceRecorder == nil || c.traceRun == nil {
		return
	}
	c.firstPacket = true
	node, err := c.traceRecorder.StartNode(c.ctx, c.traceRun.TraceID, "", "user-first-packet", "STREAM", 0)
	if err != nil {
		slog.Warn("rag trace: first packet node failed", "traceId", c.traceRun.TraceID, "err", err)
		return
	}
	if err := c.traceRecorder.FinishNode(c.ctx, c.traceRun.TraceID, node.NodeID, traceStatusSuccess, nil); err != nil {
		slog.Warn("rag trace: finish first packet node failed", "traceId", c.traceRun.TraceID, "err", err)
	}
}

func (c *pipelineCallback) resolveConversationTitle() string {
	const fallbackTitle = "新对话"
	if c == nil || c.memory == nil {
		return fallbackTitle
	}
	return resolveConversationTitle(c.ctx, c.memory, c.conversationID, c.sendTitleOnComplete)
}

func (c *pipelineCallback) saveCompletedAssistantMessage() string {
	if c == nil || c.memory == nil {
		return ""
	}
	sourcesJSON := marshalSources(c.sources)
	answer := CachedAnswer{
		Content:          c.answerPrefix + c.answer.String(),
		ThinkingContent:  c.thinking.String(),
		ThinkingDuration: c.resolveThinkingDuration(),
		Citations:        c.citations,
		SourcesJSON:      sourcesJSON,
		GroundingJSON:    MarshalGroundingChunks(c.grounding),
	}
	msg := chat.Message{
		Role:             chat.RoleAssistant,
		Content:          answer.fullContent(),
		ThinkingContent:  answer.ThinkingContent,
		ThinkingDuration: answer.ThinkingDuration,
		Sources:          sourcesJSON,
		RetrievedChunks:  answer.GroundingJSON,
		ReplyToMessageID: c.replyToMessageID,
		MessageStatus:    chat.MessageStatusNormal,
	}
	id, err := appendConversationMessage(c.ctx, c.memory, c.conversationID, msg)
	if err != nil {
		slog.Warn("rag memory: save assistant message failed", "conversationId", c.conversationID, "err", err)
		return ""
	}
	c.saveAnswerCache(answer)
	return id
}

func (c *pipelineCallback) saveCancelledAssistantMessage() string {
	if c == nil || c.memory == nil {
		return "null"
	}
	content := c.answer.String()
	if strings.TrimSpace(content) == "" {
		return "null"
	}
	msg := chat.Message{
		Role:             chat.RoleAssistant,
		Content:          content,
		ThinkingContent:  c.thinking.String(),
		ThinkingDuration: c.resolveThinkingDuration(),
		ReplyToMessageID: c.replyToMessageID,
		MessageStatus:    chat.MessageStatusInterrupted,
	}
	id, err := appendConversationMessage(c.ctx, c.memory, c.conversationID, msg)
	if err != nil {
		slog.Warn("rag memory: save assistant message failed", "conversationId", c.conversationID, "err", err)
		return "null"
	}
	return id
}

func (c *pipelineCallback) resolveThinkingDuration() int {
	if c == nil || c.thinkingStart.IsZero() {
		if c != nil && c.thinkingDuration > 0 {
			return c.thinkingDuration
		}
		return 0
	}
	duration := int(time.Since(c.thinkingStart).Round(time.Second) / time.Second)
	if duration < 1 {
		return 1
	}
	return duration
}

func (c *pipelineCallback) saveAnswerCache(answer CachedAnswer) {
	if c == nil || c.answerCache == nil || strings.TrimSpace(c.answerCacheKey) == "" || c.answerCacheTTL <= 0 {
		return
	}
	if err := c.answerCache.SaveAnswer(c.ctx, c.answerCacheKey, answer, c.answerCacheTTL); err != nil {
		slog.Warn("rag answer cache: save failed", "err", err)
	}
}

func shouldSendTitleOnComplete(ctx context.Context, memory MemoryService, conversationID string) bool {
	if memory == nil {
		return true
	}
	conv, err := memory.LoadConversation(ctx, conversationID)
	if err != nil || conv == nil || strings.TrimSpace(conv.Title) == "" {
		return true
	}
	return false
}

func resolveConversationTitle(ctx context.Context, memory MemoryService, conversationID string, sendTitleOnComplete bool) string {
	const fallbackTitle = "新对话"
	if !sendTitleOnComplete {
		return ""
	}
	if memory == nil {
		return fallbackTitle
	}
	conv, err := memory.LoadConversation(ctx, conversationID)
	if err != nil || conv == nil {
		return fallbackTitle
	}
	if title := strings.TrimSpace(conv.Title); title != "" {
		return title
	}
	return fallbackTitle
}

type messageAppender interface {
	AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error)
}

func appendConversationMessage(ctx context.Context, memory MemoryService, conversationID string, msg chat.Message) (string, error) {
	if memory == nil {
		return "", nil
	}
	if appender, ok := memory.(messageAppender); ok {
		return appender.AppendMessage(ctx, conversationID, msg)
	}
	if err := memory.SaveMessage(ctx, conversationID, msg); err != nil {
		return "", err
	}
	return "", nil
}

// systemOnlyPrompt 对齐 Java handleSystemOnly + IntentResolver.isSystemOnly：
// 每个子问题必须严格命中唯一一个 SYSTEM 意图才走系统闲聊短路；
// customPrompt 取所有命中节点中第一个非空 promptTemplate（不限 SYSTEM kind）。
// Java 的意图解析总会为每个子问题生成条目；Go 在未配置意图链路时 subIntents 为空，
// 此时不能判定为系统闲聊，保持走正常检索。
func (p *Pipeline) systemOnlyPrompt(subIntents []SubQuestionIntent) (string, bool) {
	if len(subIntents) == 0 {
		return "", false
	}
	for _, si := range subIntents {
		if !isSystemOnlyNodeScores(si.NodeScores) {
			return "", false
		}
	}
	return firstIntentPromptTemplate(subIntents), true
}

// isSystemOnlyNodeScores 对齐 Java IntentResolver.isSystemOnly：
// 严格单个节点且该节点为 SYSTEM，空列表或多个节点都不算。
func isSystemOnlyNodeScores(scores []NodeScore) bool {
	return len(scores) == 1 && scores[0].Node.Kind == IntentKindSystem
}

// firstIntentPromptTemplate 对齐 Java handleSystemOnly 的 flatMap：
// 遍历所有命中节点的 promptTemplate，取第一个非空者，不限定意图类型。
func firstIntentPromptTemplate(subIntents []SubQuestionIntent) string {
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			if prompt := strings.TrimSpace(ns.Node.PromptTemplate); prompt != "" {
				return prompt
			}
		}
	}
	return ""
}

func (p *Pipeline) streamSystemOnlyResponse(ctx, persistenceCtx context.Context, question, conversationID string, history []chat.Message, task *streamTask, sender *SSESender, traceRun *TraceRunRecord, llm chat.LLMService, customPrompt string) {
	questionMessageID, _ := appendConversationMessage(persistenceCtx, p.memory, conversationID, chat.NewUserMessage(question))
	req := p.buildSystemOnlyRequest(question, history, customPrompt)
	cb := &pipelineCallback{
		ctx:              persistenceCtx,
		conversationID:   conversationID,
		memory:           p.memory,
		sender:           sender,
		task:             task,
		traceRecorder:    p.trace,
		traceRun:         traceRun,
		messageChunkSize: p.messageChunkSize,
		replyToMessageID: questionMessageID,
	}
	task.setCancelPayloadFn(cb.buildCompletionPayloadOnCancel)
	llmSpan := p.startTraceNode(ctx, traceRun, "", "llm-stream", "LLM", 0)
	cb.traceSpan = llmSpan
	if ctx.Err() != nil {
		if task.isCancelled() {
			llmSpan.finish(traceStatusCancelled, nil)
			return
		}
		slog.Info("rag pipeline: cancelled before llm call", "err", ctx.Err())
		llmSpan.finish(traceStatusError, ctx.Err())
		sender.SendFinish("", "")
		sender.SendDone()
		sender.Close()
		return
	}
	if llm == nil {
		llm = p.llm
	}
	handle, err := llm.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: system-only stream chat failed", "err", err)
		llmSpan.finish(traceStatusError, err)
		cb.OnError(err)
		return
	}
	task.bindHandle(handle)
	slog.Info("rag pipeline: system-only llm stream started")
	handle.Wait()
	if task.isCancelled() {
		llmSpan.finish(traceStatusCancelled, nil)
		return
	}
	llmSpan.finish(traceStatusSuccess, nil)
}

// buildSystemOnlyRequest 对齐 Java streamSystemResponse：
// system 消息 = customPrompt（或槽位默认），消息 = system + history + user(问题)，
// temperature 0.7、thinking false，不设 maxTokens，不注入检索上下文。
func (p *Pipeline) buildSystemOnlyRequest(question string, history []chat.Message, customPrompt string) chat.Request {
	messages := make([]chat.Message, 0, len(history)+2)
	messages = append(messages, chat.NewSystemMessage(p.resolveSystemChatPrompt(customPrompt)))
	messages = append(messages, history...)
	messages = append(messages, chat.NewUserMessage(question))
	return chat.Request{
		Messages:    messages,
		Temperature: floatPtr(0.7),
		Thinking:    boolPtr(false),
	}
}

// resolveSystemChatPrompt systemOnly 场景的 system 提示词：
// 有意图模板用模板，否则解析 SYSTEM_CHAT 槽位，槽位为空时回退默认系统提示词文件。
func (p *Pipeline) resolveSystemChatPrompt(customPrompt string) string {
	if prompt := strings.TrimSpace(customPrompt); prompt != "" {
		return prompt
	}
	if p.prompt != nil {
		if builder, ok := p.prompt.(*DefaultPromptBuilder); ok && builder.resolver != nil {
			for _, slotKey := range []string{"SYSTEM_CHAT"} {
				if prompt := strings.TrimSpace(builder.resolver.Resolve(slotKey)); prompt != "" {
					return prompt
				}
			}
		}
		if builder, ok := p.prompt.(*DefaultPromptBuilder); ok && builder.loader != nil {
			if sysPrompt, err := builder.loader.Render(builder.systemFile, nil); err == nil {
				return strings.TrimSpace(sysPrompt)
			}
		}
	}
	return "你是一个有帮助的AI助手。"
}

func runeLimit(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// buildKbSnippetSection 对齐 Java DefaultContextFormatter.formatKbContext 的回答规则注入：
// 有证据归属的 KB 意图携带 promptSnippet 时，以 <rules> 段前置于文档块——
// 单一归属意图注入该意图的规则；多个归属意图去重后编号合并；无归属意图不注入。
func buildKbSnippetSection(kbIntents []NodeScore, eligibleIntentIDs map[string]struct{}) string {
	if len(kbIntents) == 0 || len(eligibleIntentIDs) == 0 {
		return ""
	}
	snippets := make([]string, 0, len(kbIntents))
	seen := make(map[string]struct{}, len(kbIntents))
	for _, ns := range kbIntents {
		if ns.Node.Kind != IntentKindKB {
			continue
		}
		if _, ok := eligibleIntentIDs[strings.TrimSpace(ns.Node.ID)]; !ok {
			continue
		}
		snippet := strings.TrimSpace(ns.Node.PromptSnippet)
		if snippet == "" {
			continue
		}
		if _, dup := seen[snippet]; dup {
			continue
		}
		seen[snippet] = struct{}{}
		snippets = append(snippets, snippet)
	}
	if len(snippets) == 0 {
		return ""
	}
	rules := snippets[0]
	if len(snippets) > 1 {
		numbered := make([]string, len(snippets))
		for i, snippet := range snippets {
			numbered[i] = fmt.Sprintf("%d. %s", i+1, snippet)
		}
		rules = strings.Join(numbered, "\n")
	}
	return "<rules>\n" + rules + "\n</rules>\n"
}

func withChunkSources(chunks []RetrievedChunk, fallback string) string {
	if len(chunks) == 0 {
		return fallback
	}
	groups := groupChunksByDocument(chunks)
	var b strings.Builder
	for i, group := range groups {
		if i > 0 {
			b.WriteString("\n")
		}
		docID := sanitizeContextSource(resolveContextSourceID(group.chunks))
		if docID != "" {
			b.WriteString(`<content data-ragent-doc-id="`)
			b.WriteString(docID)
			b.WriteString(`">`)
			b.WriteString("\n")
		} else {
			b.WriteString("<content>\n")
		}
		b.WriteString(joinContextChunkText(group.chunks))
		b.WriteString("\n</content>")
	}
	return b.String()
}

func selectFinalEvidenceChunks(chunks []RetrievedChunk, question string, limit int, preferredCollections ...[]string) []RetrievedChunk {
	if len(chunks) == 0 {
		return nil
	}
	selected := append([]RetrievedChunk(nil), chunks...)
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Score != selected[j].Score {
			return selected[i].Score > selected[j].Score
		}
		leftDoc := strings.TrimSpace(selected[i].Metadata["doc_id"])
		rightDoc := strings.TrimSpace(selected[j].Metadata["doc_id"])
		if leftDoc != rightDoc {
			return leftDoc < rightDoc
		}
		leftIndex, leftOK := contextChunkIndex(selected[i])
		rightIndex, rightOK := contextChunkIndex(selected[j])
		if leftOK != rightOK {
			return leftOK
		}
		if leftIndex != rightIndex {
			return leftIndex < rightIndex
		}
		return strings.TrimSpace(selected[i].ID) < strings.TrimSpace(selected[j].ID)
	})
	if preferred := filterPreferredCollectionEvidence(selected, preferredCollections...); len(preferred) > 0 {
		selected = preferred
	}
	if anchored := filterMetadataAnchorEvidence(selected, question); len(anchored) > 0 {
		selected = anchored
	}
	if related := filterQuestionRelatedEvidence(selected, question); len(related) > 0 {
		selected = related
	}
	if limit > 0 && len(selected) > limit {
		return selected[:limit]
	}
	return selected
}

func intentEvidenceCollections(subIntents []SubQuestionIntent) []string {
	seen := make(map[string]bool)
	collections := make([]string, 0)
	for _, subIntent := range subIntents {
		for _, score := range subIntent.NodeScores {
			if score.Node.Kind != IntentKindKB {
				continue
			}
			for _, collection := range score.Node.EffectiveCollectionNames() {
				if seen[collection] {
					continue
				}
				seen[collection] = true
				collections = append(collections, collection)
			}
		}
	}
	return collections
}

func filterPreferredCollectionEvidence(chunks []RetrievedChunk, preferredCollections ...[]string) []RetrievedChunk {
	allowed := make(map[string]bool)
	for _, group := range preferredCollections {
		for _, collection := range group {
			collection = strings.TrimSpace(collection)
			if collection != "" {
				allowed[collection] = true
			}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	filtered := make([]RetrievedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if allowed[strings.TrimSpace(chunk.Metadata["collection_name"])] {
			filtered = append(filtered, chunk)
		}
	}
	return filtered
}

func filterMetadataAnchorEvidence(chunks []RetrievedChunk, question string) []RetrievedChunk {
	anchorTerms := evidenceAnchorTerms(keywordSearchTerms(question))
	if len(anchorTerms) == 0 {
		return nil
	}
	allowedCollections := make(map[string]bool)
	for _, chunk := range chunks {
		metadataText := strings.ToLower(strings.Join([]string{
			chunk.Metadata["kb_name"],
			chunk.Metadata["collection_name"],
			chunk.Metadata["doc_name"],
			chunk.Metadata["source_location"],
			chunk.Metadata["source_url"],
		}, "\n"))
		if evidenceTextTermMatches(metadataText, anchorTerms) == 0 {
			continue
		}
		collection := strings.TrimSpace(chunk.Metadata["collection_name"])
		if collection != "" {
			allowedCollections[collection] = true
		}
	}
	if len(allowedCollections) == 0 {
		return nil
	}
	filtered := make([]RetrievedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		collection := strings.TrimSpace(chunk.Metadata["collection_name"])
		if collection != "" && allowedCollections[collection] {
			filtered = append(filtered, chunk)
		}
	}
	return filtered
}

func filterQuestionRelatedEvidence(chunks []RetrievedChunk, question string) []RetrievedChunk {
	terms := keywordSearchTerms(question)
	if len(terms) == 0 {
		return nil
	}
	anchorTerms := evidenceAnchorTerms(terms)
	requiredMatches := 1
	if len(terms) > 1 {
		requiredMatches = 2
	}
	related := make([]RetrievedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if len(anchorTerms) > 0 && evidenceTermMatches(chunk, anchorTerms) == 0 {
			continue
		}
		if evidenceTermMatches(chunk, terms) >= requiredMatches {
			related = append(related, chunk)
		}
	}
	return related
}

func evidenceAnchorTerms(terms []string) []string {
	cjkTerms := make([]string, 0, 4)
	latinTerms := make([]string, 0, 4)
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" || isGenericEvidenceTerm(term) {
			continue
		}
		if isLatinEvidenceTerm(term) {
			latinTerms = append(latinTerms, term)
			continue
		}
		cjkTerms = append(cjkTerms, term)
	}
	if len(cjkTerms) > 0 {
		return limitEvidenceTerms(cjkTerms, 4)
	}
	return limitEvidenceTerms(latinTerms, 4)
}

func limitEvidenceTerms(terms []string, limit int) []string {
	if limit > 0 && len(terms) > limit {
		return terms[:limit]
	}
	return terms
}

func isLatinEvidenceTerm(term string) bool {
	for _, r := range term {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return term != ""
}

func isGenericEvidenceTerm(term string) bool {
	switch term {
	case "服务", "平台", "系统", "技术", "问题", "使用", "支持", "工具", "页面", "文档", "整体", "业务", "流程":
		return true
	default:
		return false
	}
}

func evidenceTermMatches(chunk RetrievedChunk, terms []string) int {
	haystack := strings.ToLower(strings.Join([]string{
		chunk.Text,
		chunk.Metadata["kb_name"],
		chunk.Metadata["doc_name"],
		chunk.Metadata["source_location"],
		chunk.Metadata["source_url"],
	}, "\n"))
	return evidenceTextTermMatches(haystack, terms)
}

func evidenceTextTermMatches(haystack string, terms []string) int {
	matches := 0
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			matches++
		}
	}
	return matches
}

type contextChunkGroup struct {
	title  string
	chunks []RetrievedChunk
}

func groupChunksByDocument(chunks []RetrievedChunk) []contextChunkGroup {
	groups := make([]contextChunkGroup, 0, len(chunks))
	groupByDocID := make(map[string]int)
	for _, chunk := range chunks {
		docID := strings.TrimSpace(chunk.Metadata["doc_id"])
		if docID == "" {
			groups = append(groups, contextChunkGroup{
				title:  resolveContextSourceTitle([]RetrievedChunk{chunk}),
				chunks: []RetrievedChunk{chunk},
			})
			continue
		}
		index, ok := groupByDocID[docID]
		if !ok {
			groupByDocID[docID] = len(groups)
			groups = append(groups, contextChunkGroup{})
			index = len(groups) - 1
		}
		if groups[index].title == "" {
			groups[index].title = resolveContextSourceTitle([]RetrievedChunk{chunk})
		}
		groups[index].chunks = append(groups[index].chunks, chunk)
	}
	for i := range groups {
		sort.SliceStable(groups[i].chunks, func(a, b int) bool {
			left, leftOK := contextChunkIndex(groups[i].chunks[a])
			right, rightOK := contextChunkIndex(groups[i].chunks[b])
			if leftOK != rightOK {
				return leftOK
			}
			return left < right
		})
	}
	return groups
}

func contextChunkIndex(chunk RetrievedChunk) (int, bool) {
	if len(chunk.Metadata) == 0 {
		return 0, false
	}
	value := strings.TrimSpace(firstNonEmpty(chunk.Metadata["chunk_index"], chunk.Metadata["index"]))
	if value == "" {
		return 0, false
	}
	index, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return index, true
}

func joinContextChunkText(chunks []RetrievedChunk) string {
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		text := chunk.Text
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n")
}

func resolveContextSourceTitle(chunks []RetrievedChunk) string {
	for _, chunk := range chunks {
		if len(chunk.Metadata) == 0 {
			continue
		}
		name := strings.TrimSpace(chunk.Metadata["doc_name"])
		if name == "" {
			continue
		}
		return stripContextSourceExtension(name)
	}
	return ""
}

func resolveContextSourceID(chunks []RetrievedChunk) string {
	for _, chunk := range chunks {
		if len(chunk.Metadata) == 0 {
			continue
		}
		if docID := strings.TrimSpace(chunk.Metadata["doc_id"]); docID != "" {
			return docID
		}
	}
	return ""
}

func stripContextSourceExtension(name string) string {
	dot := strings.LastIndex(name, ".")
	if dot > 0 && dot < len(name)-1 {
		return name[:dot]
	}
	return name
}

func sanitizeContextSource(source string) string {
	source = strings.NewReplacer(`"`, "", "<", "", ">", "").Replace(source)
	return strings.TrimSpace(source)
}

func formatCitations(chunks []RetrievedChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var evidence []string
	seen := make(map[string]bool)
	for _, chunk := range chunks {
		line := formatCitationLine(chunk.Metadata)
		if line != "" && !seen[line] {
			evidence = append(evidence, line)
			seen[line] = true
		}
	}
	if len(evidence) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n依据：\n")
	for i, item := range evidence {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	return b.String()
}

func formatCitationLine(meta map[string]string) string {
	source := formatCitationSource(meta)
	url := meta["source_url"]
	if !isHTTPURL(url) {
		return source
	}
	name := meta["doc_name"]
	if name == "" {
		name = url
	}
	link := "链接：[" + name + "](" + url + ")"
	if source == "" {
		return link
	}
	return source + "；" + link
}

func formatCitationSource(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	name := meta["doc_name"]
	if name == "" {
		return ""
	}
	page := meta["page_start"]
	lineStart := meta["line_start"]
	lineEnd := meta["line_end"]
	var b strings.Builder
	if kbName := firstNonEmpty(meta["kb_name"], meta["collection_name"]); kbName != "" {
		b.WriteString("知识库：")
		b.WriteString(kbName)
		b.WriteString("；")
	}
	b.WriteString("文档：")
	b.WriteString("《")
	b.WriteString(name)
	b.WriteString("》")
	if page != "" {
		b.WriteString("第")
		b.WriteString(page)
		b.WriteString("页")
	}
	if lineStart != "" {
		if page != "" {
			b.WriteString("，")
		}
		b.WriteString("第")
		b.WriteString(lineStart)
		if lineEnd != "" && lineEnd != lineStart {
			b.WriteString("-")
			b.WriteString(lineEnd)
		}
		b.WriteString("行")
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
