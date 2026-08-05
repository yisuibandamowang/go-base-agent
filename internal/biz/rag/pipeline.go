package rag

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/infra/chat"
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
	messageChunkSize int
	streamTimeout    time.Duration
	defaultTopK      int
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

// SetPreferredLLMService sets the lightweight LLM used for non-RAG responses.
func (p *Pipeline) SetPreferredLLMService(llm chat.LLMService) {
	p.preferredLLM = llm
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
	task := p.tasks.register(taskID, sender, cancel)
	defer p.tasks.unregister(taskID)

	traceRun := p.startTraceRun(ctx, conversationID, taskID)
	finishTraceRun := func(status string, err error) {
		if traceRun == nil || p.trace == nil {
			return
		}
		if finishErr := p.trace.FinishRun(ctx, traceRun.TraceID, status, err); finishErr != nil {
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
		decision := p.guidance.DetectAmbiguity(ctx, q, resolvedSubIntents)
		if decision.Action == GuidanceActionPrompt && strings.TrimSpace(decision.Prompt) != "" {
			sendTitleOnComplete := shouldSendTitleOnComplete(ctx, p.memory, conversationID)
			_ = p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question))
			sender.SendMessage(MsgTypeResponse, decision.Prompt)
			sender.SendFinish("", resolveConversationTitle(ctx, p.memory, conversationID, sendTitleOnComplete))
			sender.SendDone()
			sender.Close()
			finishTraceRun(traceStatusSuccess, nil)
			return
		}
	}

	if systemPrompt, ok := p.systemOnlyPrompt(resolvedSubIntents); ok {
		p.streamSystemOnlyResponse(ctx, q, conversationID, history, task, sender, traceRun, p.lightweightLLM(), systemPrompt)
		return
	}

	mcpCtx := p.buildMcpContext(ctx, q, resolvedSubIntents)
	retrieveSpan := p.startTraceNode(ctx, traceRun, "", "retrieve", "RETRIEVE", 0)
	chunks, err := p.retrieveChunks(ctx, q, subQuestions, resolvedSubIntents, p.resolveDefaultTopK())
	var kbCtx string
	if err != nil {
		retrieveSpan.finish(traceStatusError, err)
		slog.Warn("rag: retrieve failed", "err", err)
		p.streamRetrievalFallback(ctx, conversationID, question, sender, task, p.lightweightLLM(), "检索失败原因：知识库检索执行失败："+err.Error())
		finishTraceRun(traceStatusSuccess, nil)
		return
	} else if len(chunks) > 0 {
		retrieveSpan.finish(traceStatusSuccess, nil)
		slog.Info("rag: chunks retrieved", "count", len(chunks))
		for _, c := range chunks {
			kbCtx += c.Text + "\n"
		}
	} else {
		retrieveSpan.finish(traceStatusSuccess, nil)
		if strings.TrimSpace(mcpCtx) == "" {
			slog.Warn("rag: no chunks found for question", "question", runeLimit(q, 50))
			p.streamRetrievalFallback(ctx, conversationID, question, sender, task, p.lightweightLLM(), "检索失败原因：知识库中未检索到相关内容，已完成向量检索但没有召回与问题相关的文档片段。")
			finishTraceRun(traceStatusSuccess, nil)
			return
		}
	}

	sendTitleOnComplete := shouldSendTitleOnComplete(ctx, p.memory, conversationID)

	req := p.prompt.Build(PromptContext{
		Question:     q,
		SubQuestions: subQuestions,
		History:      history,
		KbContext:    withChunkSources(chunks, kbCtx),
		McpContext:   mcpCtx,
	})
	thinkingVal := deepThinking
	req.Thinking = &thinkingVal
	answerLLM := p.llm
	if len(chunks) == 0 && strings.TrimSpace(mcpCtx) == "" {
		answerLLM = p.lightweightLLM()
	}

	if err := p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
	}

	cb := &pipelineCallback{
		ctx:                 ctx,
		conversationID:      conversationID,
		memory:              p.memory,
		sender:              sender,
		citations:           formatCitations(chunks),
		task:                task,
		traceRecorder:       p.trace,
		traceRun:            traceRun,
		sendTitleOnComplete: sendTitleOnComplete,
		messageChunkSize:    p.messageChunkSize,
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
	node, err := p.trace.StartNode(ctx, run.TraceID, parentNodeID, nodeName, nodeType, depth)
	if err != nil {
		slog.Warn("rag trace: start node failed", "traceId", run.TraceID, "node", nodeName, "err", err)
		return nil
	}
	return &traceSpan{ctx: ctx, recorder: p.trace, traceID: run.TraceID, nodeID: node.NodeID}
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

func (p *Pipeline) retrieveChunks(ctx context.Context, question string, subQuestions []string, subIntents []SubQuestionIntent, topK int) ([]RetrievedChunk, error) {
	if aware, ok := p.retrieve.(IntentAwareRetriever); ok {
		return p.retrieveChunksWithContext(ctx, question, subQuestions, subIntents, topK, aware)
	}
	queries := retrievalQueries(question, subQuestions)
	allChunks := make([]RetrievedChunk, 0)
	for _, query := range queries {
		chunks, err := p.retrieve.Retrieve(ctx, query, topK)
		if err != nil {
			return nil, err
		}
		allChunks = append(allChunks, chunks...)
	}
	return deduplicateChunks(allChunks), nil
}

func (p *Pipeline) retrieveChunksWithContext(ctx context.Context, question string, subQuestions []string, subIntents []SubQuestionIntent, topK int, aware IntentAwareRetriever) ([]RetrievedChunk, error) {
	queries := retrievalQueries(question, subQuestions)
	if len(subIntents) == 0 {
		chunks, err := aware.RetrieveWithContext(ctx, SearchContext{
			OriginalQuestion:  question,
			RewrittenQuestion: question,
			SubQuestions:      queries,
			TopK:              topK,
		})
		if err != nil {
			return nil, err
		}
		return deduplicateChunks(chunks), nil
	}

	allChunks := make([]RetrievedChunk, 0)
	for _, subIntent := range subIntents {
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
		chunks, err := aware.RetrieveWithContext(ctx, sc)
		if err != nil {
			return nil, err
		}
		allChunks = append(allChunks, chunks...)
	}
	return deduplicateChunks(allChunks), nil
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

func (p *Pipeline) streamRetrievalFallback(ctx context.Context, conversationID, question string, sender *SSESender, task *streamTask, llm chat.LLMService, reason string) {
	sendTitleOnComplete := shouldSendTitleOnComplete(ctx, p.memory, conversationID)
	if err := p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
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
		ctx:                 ctx,
		conversationID:      conversationID,
		memory:              p.memory,
		sender:              sender,
		answerPrefix:        prefix,
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
	task                *streamTask
	traceRecorder       TraceRecorder
	traceRun            *TraceRunRecord
	traceSpan           *traceSpan
	sendTitleOnComplete bool
	thinkingStart       time.Time
	thinkingDuration    int
	messageChunkSize    int
	firstPacket         bool
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
	c.sender.SendFinish(messageID, title)
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
	size := c.messageChunkSize
	if size <= 0 {
		c.sender.SendMessage(msgType, content)
		return
	}
	var b strings.Builder
	count := 0
	for _, r := range content {
		b.WriteRune(r)
		count++
		if count >= size {
			_ = c.sender.SendMessage(msgType, b.String())
			b.Reset()
			count = 0
		}
	}
	if b.Len() > 0 {
		_ = c.sender.SendMessage(msgType, b.String())
	}
}

func (c *pipelineCallback) buildCompletionPayloadOnCancel() CompletionPayload {
	return CompletionPayload{
		MessageID: c.saveCancelledAssistantMessage(),
		Title:     c.resolveConversationTitle(),
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
	msg := chat.Message{
		Role:             chat.RoleAssistant,
		Content:          c.answer.String(),
		ThinkingContent:  c.thinking.String(),
		ThinkingDuration: c.resolveThinkingDuration(),
	}
	id, err := appendConversationMessage(c.ctx, c.memory, c.conversationID, msg)
	if err != nil {
		slog.Warn("rag memory: save assistant message failed", "conversationId", c.conversationID, "err", err)
		return ""
	}
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
		return 0
	}
	duration := int(time.Since(c.thinkingStart).Round(time.Second) / time.Second)
	if duration < 1 {
		return 1
	}
	return duration
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

func (p *Pipeline) systemOnlyPrompt(subIntents []SubQuestionIntent) (string, bool) {
	if len(subIntents) == 0 {
		return "", false
	}
	hasSystem := false
	for _, si := range subIntents {
		if len(si.NodeScores) == 0 {
			return "", false
		}
		for _, ns := range si.NodeScores {
			if ns.Node.Kind != IntentKindSystem {
				return "", false
			}
			if hasSystem {
				continue
			}
			if prompt := strings.TrimSpace(ns.Node.PromptTemplate); prompt != "" {
				hasSystem = true
			}
		}
	}
	if !hasSystem {
		return "", true
	}
	return firstSystemPromptTemplate(subIntents), true
}

func firstSystemPromptTemplate(subIntents []SubQuestionIntent) string {
	for _, si := range subIntents {
		for _, ns := range si.NodeScores {
			if ns.Node.Kind == IntentKindSystem {
				if prompt := strings.TrimSpace(ns.Node.PromptTemplate); prompt != "" {
					return prompt
				}
			}
		}
	}
	return ""
}

func (p *Pipeline) streamSystemOnlyResponse(ctx context.Context, question, conversationID string, history []chat.Message, task *streamTask, sender *SSESender, traceRun *TraceRunRecord, llm chat.LLMService, customPrompt string) {
	_ = p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question))
	req := p.buildSystemOnlyRequest(question, history, customPrompt)
	cb := &pipelineCallback{
		ctx:              ctx,
		conversationID:   conversationID,
		memory:           p.memory,
		sender:           sender,
		task:             task,
		traceRecorder:    p.trace,
		traceRun:         traceRun,
		messageChunkSize: p.messageChunkSize,
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

func (p *Pipeline) buildSystemOnlyRequest(question string, history []chat.Message, customPrompt string) chat.Request {
	req := p.prompt.Build(PromptContext{
		Question: question,
		History:  history,
	})
	if strings.TrimSpace(customPrompt) == "" {
		if falseVal := false; req.Thinking == nil {
			req.Thinking = &falseVal
		} else {
			*req.Thinking = false
		}
		return req
	}

	messages := make([]chat.Message, 0, len(req.Messages)+1)
	messages = append(messages, chat.NewSystemMessage(customPrompt))
	if len(req.Messages) > 0 {
		if req.Messages[0].Role == chat.RoleSystem {
			messages = append(messages, req.Messages[1:]...)
		} else {
			messages = append(messages, req.Messages...)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, chat.NewSystemMessage(customPrompt))
	}
	req.Messages = messages
	if falseVal := false; req.Thinking == nil {
		req.Thinking = &falseVal
	} else {
		*req.Thinking = false
	}
	return req
}

func runeLimit(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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
		title := sanitizeContextSource(group.title)
		if title != "" {
			b.WriteString(`<content source="`)
			b.WriteString(title)
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
