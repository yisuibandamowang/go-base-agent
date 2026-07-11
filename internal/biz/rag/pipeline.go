package rag

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go-base-agent/internal/infra/chat"
)

// Pipeline orchestrates the RAG chat flow.
// Aligns with Java StreamChatPipeline.
type Pipeline struct {
	llm      chat.LLMService
	prompt   PromptBuilder
	rewrite  QueryRewriter
	retrieve Retriever
	memory   MemoryService
}

// NewPipeline creates a new RAG pipeline.
func NewPipeline(llm chat.LLMService, prompt PromptBuilder, rewrite QueryRewriter, retrieve Retriever, memory MemoryService) *Pipeline {
	return &Pipeline{llm: llm, prompt: prompt, rewrite: rewrite, retrieve: retrieve, memory: memory}
}

// StreamChat implements Service.StreamChat.
func (p *Pipeline) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	// Timeout context: the entire pipeline should complete within 120s
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

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

	result, err := p.rewrite.Rewrite(ctx, question, nil)
	q := question
	if err != nil {
		slog.Warn("rag: rewrite failed", "err", err)
	} else if result != nil && result.RewrittenQuestion != "" {
		slog.Info("rag: query rewritten", "from", question, "to", result.RewrittenQuestion)
		q = result.RewrittenQuestion
	}

	history, _ := p.memory.LoadHistory(ctx, conversationID)

	chunks, err := p.retrieve.Retrieve(ctx, q, 10)
	var kbCtx string
	if err != nil {
		slog.Warn("rag: retrieve failed", "err", err)
		p.streamRetrievalFallback(ctx, conversationID, question, sender, "检索失败原因：知识库检索执行失败："+err.Error())
		return
	} else if len(chunks) > 0 {
		slog.Info("rag: chunks retrieved", "count", len(chunks))
		for _, c := range chunks {
			kbCtx += c.Text + "\n"
		}
	} else {
		slog.Warn("rag: no chunks found for question", "question", runeLimit(q, 50))
		p.streamRetrievalFallback(ctx, conversationID, question, sender, "检索失败原因：知识库中未检索到相关内容，已完成向量检索但没有召回与问题相关的文档片段。")
		return
	}

	var thinkingVal *bool
	if deepThinking {
		v := true
		thinkingVal = &v
	}

	req := p.prompt.Build(PromptContext{
		Question:  q,
		History:   history,
		KbContext: withChunkSources(chunks, kbCtx),
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
	}
	if ctx.Err() != nil {
		slog.Info("rag pipeline: cancelled before llm call", "err", ctx.Err())
		sender.SendFinish("", "")
		sender.SendDone()
		sender.Close()
		return
	}
	handle, err := p.llm.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: stream chat failed", "err", err)
		cb.OnError(err)
		return
	}
	slog.Info("rag pipeline: llm stream started")
	handle.Wait()
}

func (p *Pipeline) streamRetrievalFallback(ctx context.Context, conversationID, question string, sender *SSESender, reason string) {
	if err := p.memory.SaveMessage(ctx, conversationID, chat.NewUserMessage(question)); err != nil {
		slog.Warn("rag memory: save user message failed", "conversationId", conversationID, "err", err)
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
	}
	handle, err := p.llm.StreamChat(ctx, req, cb)
	if err != nil {
		slog.Error("rag pipeline: retrieval fallback stream failed", "err", err)
		cb.OnError(err)
		return
	}
	handle.Wait()
}

// StopTask implements Service.StopTask.
func (p *Pipeline) StopTask(taskID string) {
	slog.Info("rag pipeline: stop task", "taskId", taskID)
}

// pipelineCallback converts LLM StreamCallback events to SSE events.
type pipelineCallback struct {
	ctx            context.Context
	conversationID string
	memory         MemoryService
	sender         *SSESender
	buf            []byte
	answer         strings.Builder
	answerPrefix   string
	citations      string
}

func (c *pipelineCallback) OnContent(content string) {
	slog.Info("rag pipeline: llm content chunk", "len", len(content))
	c.answer.WriteString(content)
	c.sender.SendMessage(MsgTypeResponse, content)
}

func (c *pipelineCallback) OnThinking(content string) {
	slog.Info("rag pipeline: llm thinking chunk", "len", len(content))
	c.sender.SendMessage(MsgTypeThink, content)
}

func (c *pipelineCallback) OnComplete() {
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
	slog.Error("rag pipeline: llm error", "err", err)
	// Ignore send errors — client may have already disconnected
	_ = c.sender.SendFinish("", "")
	_ = c.sender.SendDone()
	c.sender.Close()
}

func runeLimit(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func withChunkSources(chunks []RetrievedChunk, fallback string) string {
	if len(chunks) == 0 {
		return fallback
	}
	var b strings.Builder
	for i, chunk := range chunks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[片段")
		b.WriteString(strconv.Itoa(i + 1))
		if source := formatCitationSource(chunk.Metadata); source != "" {
			b.WriteString("，来源：")
			b.WriteString(source)
		}
		b.WriteString("]\n")
		b.WriteString(chunk.Text)
		b.WriteString("\n")
	}
	return b.String()
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
