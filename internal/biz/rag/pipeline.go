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
	llm            chat.LLMService
	prompt         PromptBuilder
	rewrite        QueryRewriter
	retrieve       Retriever
	memory         MemoryService
	mcp            McpContextProvider
	intentResolver IntentResolutionService
	guidance       *IntentGuidanceService
	trace          TraceRecorder
	tasks          *streamTaskManager
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

// StreamChat implements Service.StreamChat.
func (p *Pipeline) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	// Timeout context: the entire pipeline should complete within 120s
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
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
		decision := p.guidance.DetectAmbiguity(q, resolvedSubIntents)
		if decision.Action == GuidanceActionPrompt && strings.TrimSpace(decision.Prompt) != "" {
			_ = p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question))
			sender.SendMessage(MsgTypeResponse, decision.Prompt)
			sender.SendFinish("", "")
			sender.SendDone()
			sender.Close()
			finishTraceRun(traceStatusSuccess, nil)
			return
		}
	}

	retrieveSpan := p.startTraceNode(ctx, traceRun, "", "retrieve", "RETRIEVE", 0)
	chunks, err := p.retrieveChunks(ctx, q, subQuestions, 10)
	var kbCtx string
	if err != nil {
		retrieveSpan.finish(traceStatusError, err)
		slog.Warn("rag: retrieve failed", "err", err)
		p.streamRetrievalFallback(ctx, conversationID, question, sender, task, "检索失败原因：知识库检索执行失败："+err.Error())
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
		slog.Warn("rag: no chunks found for question", "question", runeLimit(q, 50))
		p.streamRetrievalFallback(ctx, conversationID, question, sender, task, "检索失败原因：知识库中未检索到相关内容，已完成向量检索但没有召回与问题相关的文档片段。")
		finishTraceRun(traceStatusSuccess, nil)
		return
	}

	var thinkingVal *bool
	if deepThinking {
		v := true
		thinkingVal = &v
	}

	req := p.prompt.Build(PromptContext{
		Question:   q,
		History:    history,
		KbContext:  withChunkSources(chunks, kbCtx),
		McpContext: p.buildMcpContext(ctx, q),
	})
	req.Thinking = thinkingVal

	if err := p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
	}

	cb := &pipelineCallback{
		ctx:            ctx,
		conversationID: conversationID,
		memory:         p.memory,
		sender:         sender,
		citations:      formatCitations(chunks),
		task:           task,
		traceRecorder:  p.trace,
		traceRun:       traceRun,
	}
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
	handle, err := p.llm.StreamChat(ctx, req, cb)
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

func (p *Pipeline) buildMcpContext(ctx context.Context, question string) string {
	if p.mcp == nil {
		return ""
	}
	mcpCtx, err := p.mcp.BuildContext(ctx, question)
	if err != nil {
		slog.Warn("rag: build mcp context failed", "err", err)
		return ""
	}
	return mcpCtx
}

func (p *Pipeline) retrieveChunks(ctx context.Context, question string, subQuestions []string, topK int) ([]RetrievedChunk, error) {
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

func (p *Pipeline) streamRetrievalFallback(ctx context.Context, conversationID, question string, sender *SSESender, task *streamTask, reason string) {
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
		ctx:            ctx,
		conversationID: conversationID,
		memory:         p.memory,
		sender:         sender,
		answerPrefix:   prefix,
		task:           task,
	}
	handle, err := p.llm.StreamChat(ctx, req, cb)
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
	ctx            context.Context
	conversationID string
	memory         MemoryService
	sender         *SSESender
	answer         strings.Builder
	answerPrefix   string
	citations      string
	task           *streamTask
	traceRecorder  TraceRecorder
	traceRun       *TraceRunRecord
	traceSpan      *traceSpan
	firstPacket    bool
}

func (c *pipelineCallback) OnContent(content string) {
	if c.task != nil && c.task.isCancelled() {
		return
	}
	slog.Info("rag pipeline: llm content chunk", "len", len(content))
	c.recordFirstPacket()
	c.answer.WriteString(content)
	c.sender.SendMessage(MsgTypeResponse, content)
}

func (c *pipelineCallback) OnThinking(content string) {
	if c.task != nil && c.task.isCancelled() {
		return
	}
	slog.Info("rag pipeline: llm thinking chunk", "len", len(content))
	c.recordFirstPacket()
	c.sender.SendMessage(MsgTypeThink, content)
}

func (c *pipelineCallback) OnComplete() {
	if c.task != nil && c.task.isCancelled() {
		c.traceSpan.finish(traceStatusCancelled, nil)
		return
	}
	c.traceSpan.finish(traceStatusSuccess, nil)
	slog.Info("rag pipeline: llm stream complete")
	if c.memory != nil {
		if answer := c.answerPrefix + c.answer.String(); answer != "" {
			answer += c.citations
			if err := c.memory.SaveMessage(c.ctx, c.conversationID, chat.NewAssistantMessage(answer)); err != nil {
				slog.Warn("rag memory: save assistant message failed", "conversationId", c.conversationID, "err", err)
			}
		}
	}
	if c.citations != "" {
		_ = c.sender.SendMessage(MsgTypeResponse, c.citations)
	}
	c.sender.SendFinish("", "")
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
