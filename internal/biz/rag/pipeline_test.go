package rag

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-base-agent/internal/infra/chat"
)

type fakeLLMService struct {
	chatFn   func(ctx context.Context, req chat.Request) (string, error)
	streamFn func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error)
}

func (f *fakeLLMService) Chat(ctx context.Context, req chat.Request) (string, error) {
	if f.chatFn != nil {
		return f.chatFn(ctx, req)
	}
	return "response", nil
}
func (f *fakeLLMService) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return f.Chat(ctx, req)
}
func (f *fakeLLMService) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	if f.streamFn != nil {
		return f.streamFn(ctx, req, cb)
	}
	return &fakeHandle{}, nil
}

type fakeHandle struct{}

func (f *fakeHandle) Cancel() {}
func (f *fakeHandle) Wait()   {}

type blockingHandle struct {
	once      sync.Once
	cancelled chan struct{}
}

func newBlockingHandle() *blockingHandle {
	return &blockingHandle{cancelled: make(chan struct{})}
}

func (h *blockingHandle) Cancel() {
	h.once.Do(func() {
		close(h.cancelled)
	})
}

func (h *blockingHandle) Wait() {
	<-h.cancelled
}

type recordingMemoryService struct {
	history      []chat.Message
	saved        []chat.Message
	savedIDs     []string
	conversation *Conversation
}

func (m *recordingMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return m.history, nil
}

func (m *recordingMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	m.saved = append(m.saved, msg)
	return nil
}

func (m *recordingMemoryService) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	m.saved = append(m.saved, msg)
	id := "msg-" + strconv.Itoa(len(m.saved))
	m.savedIDs = append(m.savedIDs, id)
	return id, nil
}

func (m *recordingMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return m.conversation, nil
}

type titleAwareMemoryService struct {
	recordingMemoryService
	conversation      *Conversation
	missingBeforeSave bool
}

func (m *titleAwareMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	if m.missingBeforeSave && len(m.saved) == 0 {
		return nil, errors.New("conversation not found")
	}
	return m.conversation, nil
}

type staticRetriever struct {
	chunks []RetrievedChunk
	err    error
}

func (r staticRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	return r.chunks, r.err
}

type recordingRetriever struct {
	question string
	queries  []string
	chunks   []RetrievedChunk
}

func (r *recordingRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	r.question = question
	r.queries = append(r.queries, question)
	return r.chunks, nil
}

type recordingRewriter struct {
	history      []chat.Message
	lastQuestion string
	result       string
	subQuestions []string
}

func (r *recordingRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	r.history = append([]chat.Message(nil), history...)
	r.lastQuestion = question
	return &RewriteResult{RewrittenQuestion: r.result, SubQuestions: r.subQuestions}, nil
}

type staticIntentResolutionService struct {
	subIntents []SubQuestionIntent
}

func (s staticIntentResolutionService) ResolveQuestions(ctx context.Context, questions []string) ([]SubQuestionIntent, error) {
	return s.subIntents, nil
}

type recordingIntentAwareRetriever struct {
	contexts []SearchContext
	chunks   []RetrievedChunk
}

func (r *recordingIntentAwareRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	return r.chunks, nil
}

func (r *recordingIntentAwareRetriever) RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	r.contexts = append(r.contexts, sc)
	return r.chunks, nil
}

func testRetriever() Retriever {
	return staticRetriever{chunks: []RetrievedChunk{{ID: "chunk-1", Text: "知识库片段", Score: 0.9}}}
}

func citationRetriever() Retriever {
	return staticRetriever{chunks: []RetrievedChunk{{
		ID:    "chunk-1",
		Text:  "助手支持错误排查能力。",
		Score: 0.9,
		Metadata: map[string]string{
			"kb_name":    "go 语言知识库",
			"doc_name":   "会员Agent说明.md",
			"page_start": "1",
			"line_start": "12",
			"line_end":   "16",
			"source_url": "https://example.com/member-agent.md",
		},
	}}}
}

type staticMcpContextProvider struct {
	context string
	err     error
}

func (p staticMcpContextProvider) BuildContext(ctx context.Context, question string) (string, error) {
	return p.context, p.err
}

func TestPipeline_StreamChat_Basic(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("hello")
				cb.OnContent(" world")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	go p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
	if !strings.Contains(body, `"delta":"hello"`) {
		t.Fatal("missing content")
	}
	if !strings.Contains(body, `"delta":" world"`) {
		t.Fatal("missing content")
	}
	if !strings.Contains(body, "event: finish") {
		t.Fatal("missing finish event")
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event")
	}
}

func TestPipeline_StreamChat_SavesConversationTurn(t *testing.T) {
	mem := &recordingMemoryService{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("hello")
			cb.OnContent(" world")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	p.StreamChat(context.Background(), "question", "conv-1", "task-1", false, s)

	if len(mem.saved) != 2 {
		t.Fatalf("expected user and assistant messages to be saved, got %d", len(mem.saved))
	}
	if mem.saved[0].Role != chat.RoleUser || mem.saved[0].Content != "question" {
		t.Fatalf("unexpected user message: %+v", mem.saved[0])
	}
	if mem.saved[1].Role != chat.RoleAssistant || mem.saved[1].Content != "hello world" {
		t.Fatalf("unexpected assistant message: %+v", mem.saved[1])
	}
}

func TestPipeline_StreamChat_ChunksResponseByConfiguredSize(t *testing.T) {
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("abcdef")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	p.SetMessageChunkSize(3)
	p.StreamChat(context.Background(), "question", "conv-1", "task-1", false, s)

	body := w.Body.String()
	if count := strings.Count(body, `"delta":"abc"`); count != 1 {
		t.Fatalf("expected one abc chunk, got %d in: %s", count, body)
	}
	if count := strings.Count(body, `"delta":"def"`); count != 1 {
		t.Fatalf("expected one def chunk, got %d in: %s", count, body)
	}
	if strings.Contains(body, `"delta":"abcdef"`) {
		t.Fatalf("expected response to be chunked, got: %s", body)
	}
}

func TestPipeline_StreamChat_FinishEventIncludesAssistantMessageID(t *testing.T) {
	mem := &recordingMemoryService{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("hello")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	p.StreamChat(context.Background(), "question", "conv-1", "task-1", false, s)

	body := w.Body.String()
	if !strings.Contains(body, `"messageId":"msg-2"`) {
		t.Fatalf("expected finish event to include saved assistant message id, got: %s", body)
	}
}

func TestPipeline_StreamChat_RewritesWithConversationHistory(t *testing.T) {
	history := []chat.Message{
		chat.NewUserMessage("会员Agent支持哪些能力？"),
		chat.NewAssistantMessage("会员Agent支持错误排查和权益查询。"),
	}
	mem := &recordingMemoryService{history: history}
	rewriter := &recordingRewriter{result: "会员Agent 错误排查能力"}
	retriever := &recordingRetriever{chunks: []RetrievedChunk{{ID: "chunk-1", Text: "错误排查能力说明", Score: 0.9}}}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), rewriter, retriever, mem)
	p.StreamChat(context.Background(), "这个怎么用？", "conv-1", "task-1", false, s)

	if len(rewriter.history) != len(history) {
		t.Fatalf("expected rewriter to receive %d history messages, got %d", len(history), len(rewriter.history))
	}
	if retriever.question != "会员Agent 错误排查能力" {
		t.Fatalf("expected retriever to use rewritten question, got %q", retriever.question)
	}
}

func TestPipeline_StreamChat_RetrievesSubQuestionsAndDeduplicatesChunks(t *testing.T) {
	rewriter := &recordingRewriter{
		result:       "会员Agent能力",
		subQuestions: []string{"会员Agent错误排查", "会员Agent权益查询"},
	}
	retriever := &recordingRetriever{chunks: []RetrievedChunk{
		{ID: "chunk-1", Text: "错误排查能力说明", Score: 0.9},
		{ID: "chunk-1", Text: "错误排查能力说明", Score: 0.8},
	}}
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), rewriter, retriever, &NoopMemoryService{})
	p.StreamChat(context.Background(), "会员Agent支持什么？", "conv-1", "task-1", false, s)

	wantQueries := []string{"会员Agent能力", "会员Agent错误排查", "会员Agent权益查询"}
	if strings.Join(retriever.queries, "|") != strings.Join(wantQueries, "|") {
		t.Fatalf("expected retrieve queries %v, got %v", wantQueries, retriever.queries)
	}
	content := capturedReq.Messages[len(capturedReq.Messages)-1].Content
	if count := strings.Count(content, "错误排查能力说明"); count != 1 {
		t.Fatalf("expected duplicate chunks to appear once in prompt, got %d occurrences in: %s", count, content)
	}
	if !strings.Contains(content, "<questions>\n1. 会员Agent错误排查\n2. 会员Agent权益查询\n</questions>") {
		t.Fatalf("expected prompt to include structured sub questions, got: %s", content)
	}
}

func TestPipeline_StreamChat_AppendsCitationsAndLinks(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("当前助手支持错误排查能力。")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, citationRetriever(), &NoopMemoryService{})
	go p.StreamChat(context.Background(), "助手支持什么能力", "conv-1", "task-1", false, s)

	<-done

	body := w.Body.String()
	answer := "当前助手支持错误排查能力。"
	evidence := "依据："
	source := "知识库：go 语言知识库；文档：《会员Agent说明.md》第1页，第12-16行"
	link := "[会员Agent说明.md](https://example.com/member-agent.md)"
	for _, want := range []string{answer, evidence, source, "链接：", link} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response to contain %q, got: %s", want, body)
		}
	}
	if strings.Index(body, answer) > strings.Index(body, evidence) {
		t.Fatalf("expected citations after answer, got: %s", body)
	}
}

func TestFormatCitationSourceDoesNotExposeDocumentIDAsName(t *testing.T) {
	source := formatCitationSource(map[string]string{
		"kb_name": "go 语言知识库",
		"doc_id":  "2075677715192614912",
	})

	if source != "" {
		t.Fatalf("expected no citation source without document name, got %q", source)
	}
}

func TestWithChunkSourcesGroupsByDocumentAndOrdersWithinDocByIndex(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:   "a3",
			Text: "A-idx3正文",
			Metadata: map[string]string{
				"doc_id":      "docA",
				"doc_name":    "员工手册.pdf",
				"chunk_index": "3",
			},
		},
		{
			ID:   "b0",
			Text: "B-idx0正文",
			Metadata: map[string]string{
				"doc_id":      "docB",
				"doc_name":    "报销政策.md",
				"chunk_index": "0",
			},
		},
		{
			ID:   "a1",
			Text: "A-idx1正文",
			Metadata: map[string]string{
				"doc_id":      "docA",
				"doc_name":    "员工手册旧标题.md",
				"chunk_index": "1",
			},
		},
		{
			ID:   "a-no-index",
			Text: "A-无索引正文",
			Metadata: map[string]string{
				"doc_id":   "docA",
				"doc_name": "员工手册无索引标题.md",
			},
		},
		{ID: "x0", Text: "孤块正文"},
	}

	result := withChunkSources(chunks, "fallback")

	if strings.Index(result, "A-idx1正文") > strings.Index(result, "A-idx3正文") {
		t.Fatalf("expected chunks in the same document to be ordered by chunk_index, got: %s", result)
	}
	if strings.Index(result, "A-idx3正文") > strings.Index(result, "A-无索引正文") {
		t.Fatalf("expected chunks without chunk_index to be ordered after indexed chunks, got: %s", result)
	}
	if strings.Index(result, "A-idx3正文") > strings.Index(result, "B-idx0正文") {
		t.Fatalf("expected document groups to keep first-hit relevance order, got: %s", result)
	}
	if strings.Index(result, "B-idx0正文") > strings.Index(result, "孤块正文") {
		t.Fatalf("expected chunks without doc_id to remain as their own late group, got: %s", result)
	}
	if !strings.Contains(result, `source="员工手册"`) || !strings.Contains(result, `source="报销政策"`) {
		t.Fatalf("expected source anchors without extensions, got: %s", result)
	}
	if strings.Contains(result, "员工手册.pdf") {
		t.Fatalf("expected source title to strip extension, got: %s", result)
	}
	if strings.Contains(result, "员工手册旧标题") {
		t.Fatalf("expected source title to come from first-hit chunk before index sorting, got: %s", result)
	}
	if !strings.Contains(result, "<content>\n孤块正文\n</content>") {
		t.Fatalf("expected chunk without doc_id to render without source, got: %s", result)
	}
}

func TestWithChunkSourcesJoinsSameDocumentChunksWithSingleNewline(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:   "c1",
			Text: "第一块正文",
			Metadata: map[string]string{
				"doc_id":      "docC",
				"doc_name":    "说明.txt",
				"chunk_index": "1",
			},
		},
		{
			ID:   "c2",
			Text: "第二块正文",
			Metadata: map[string]string{
				"doc_id":      "docC",
				"doc_name":    "说明.txt",
				"chunk_index": "2",
			},
		},
	}

	result := withChunkSources(chunks, "fallback")

	if !strings.Contains(result, "第一块正文\n第二块正文") {
		t.Fatalf("expected same-document chunks to be joined with a single newline, got: %s", result)
	}
}

func TestWithChunkSourcesPreservesChunkTrailingNewlines(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:   "c1",
			Text: "第一块正文\n",
			Metadata: map[string]string{
				"doc_id":      "docC",
				"doc_name":    "说明.txt",
				"chunk_index": "1",
			},
		},
		{
			ID:   "c2",
			Text: "第二块正文",
			Metadata: map[string]string{
				"doc_id":      "docC",
				"doc_name":    "说明.txt",
				"chunk_index": "2",
			},
		},
	}

	result := withChunkSources(chunks, "fallback")

	if !strings.Contains(result, "第一块正文\n\n第二块正文") {
		t.Fatalf("expected chunk body newlines to be preserved before joining, got: %s", result)
	}
}

func TestPipeline_StreamChat_NoRetrievedChunksPrefixesReasonThenGuidesWithLLM(t *testing.T) {
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("你可以补充更具体的产品名称或文档关键词。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, &NoopRetriever{}, &NoopMemoryService{})
	p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	body := w.Body.String()
	reason := "检索失败原因：知识库中未检索到相关内容"
	guidance := "你可以补充更具体的产品名称或文档关键词。"
	if !strings.Contains(body, reason) {
		t.Fatalf("expected retrieval reason, got: %s", body)
	}
	if !strings.Contains(body, guidance) {
		t.Fatalf("expected model guidance, got: %s", body)
	}
	if strings.Index(body, reason) > strings.Index(body, guidance) {
		t.Fatalf("expected retrieval reason before model guidance, got: %s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event")
	}
}

func TestPipeline_StreamChat_RetrieveErrorPrefixesSpecificReasonThenGuidesWithLLM(t *testing.T) {
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("请检查知识库连接或稍后重试。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, staticRetriever{err: errors.New("database timeout")}, &NoopMemoryService{})
	p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	body := w.Body.String()
	reason := "检索失败原因：知识库检索执行失败：database timeout"
	guidance := "请检查知识库连接或稍后重试。"
	if !strings.Contains(body, reason) {
		t.Fatalf("expected specific retrieval error, got: %s", body)
	}
	if !strings.Contains(body, guidance) {
		t.Fatalf("expected model guidance, got: %s", body)
	}
	if strings.Index(body, reason) > strings.Index(body, guidance) {
		t.Fatalf("expected retrieval reason before model guidance, got: %s", body)
	}
}

func TestPipeline_StreamChat_SystemOnlyIntentSkipsRetrieval(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnContent("我是系统助手。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}
	retriever := staticRetriever{err: errors.New("retrieval should be skipped")}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, retriever, &NoopMemoryService{})
	p.SetIntentResolver(staticIntentResolutionService{subIntents: []SubQuestionIntent{{
		SubQuestion: "你是谁",
		NodeScores: []NodeScore{{
			Node: IntentNode{
				ID:             "system-intro",
				Kind:           IntentKindSystem,
				PromptTemplate: "你是一个公司内部助手，请直接介绍自己。",
			},
			Score: 0.95,
		}},
	}}})

	p.StreamChat(context.Background(), "你是谁", "conv-1", "task-1", false, s)

	if len(capturedReq.Messages) < 2 {
		t.Fatalf("expected system-only intent to call LLM, got %d messages", len(capturedReq.Messages))
	}
	if capturedReq.Messages[0].Content != "你是一个公司内部助手，请直接介绍自己。" {
		t.Fatalf("expected system-only prompt template, got: %s", capturedReq.Messages[0].Content)
	}
	if capturedReq.Messages[len(capturedReq.Messages)-1].Content != "你是谁" {
		t.Fatalf("expected original question as user message, got: %s", capturedReq.Messages[len(capturedReq.Messages)-1].Content)
	}
	if strings.Contains(w.Body.String(), "检索失败原因") {
		t.Fatalf("expected system-only flow not to use retrieval fallback, got: %s", w.Body.String())
	}
}

func TestPipeline_StreamChat_SystemOnlyIntentUsesDefaultPromptWhenTemplateMissing(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnContent("默认系统回复。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, staticRetriever{err: errors.New("retrieval should be skipped")}, &NoopMemoryService{})
	p.SetIntentResolver(staticIntentResolutionService{subIntents: []SubQuestionIntent{{
		SubQuestion: "你好",
		NodeScores: []NodeScore{{
			Node: IntentNode{
				ID:   "system-greeting",
				Kind: IntentKindSystem,
			},
			Score: 0.9,
		}},
	}}})

	p.StreamChat(context.Background(), "你好", "conv-1", "task-1", false, s)

	if len(capturedReq.Messages) == 0 {
		t.Fatal("expected LLM to be called for system-only intent")
	}
	if capturedReq.Messages[0].Role != chat.RoleSystem || capturedReq.Messages[0].Content == "" {
		t.Fatalf("expected default system prompt, got %+v", capturedReq.Messages[0])
	}
	if capturedReq.Messages[len(capturedReq.Messages)-1].Content != "你好" {
		t.Fatalf("expected original question as user message, got: %s", capturedReq.Messages[len(capturedReq.Messages)-1].Content)
	}
}

func TestPipeline_StreamChat_SystemOnlyIntentUsesPreferredLLMWhenConfigured(t *testing.T) {
	done := make(chan struct{})
	strong := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("云端系统回复")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}
	preferred := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("本地系统回复")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(strong, NewDefaultPromptBuilder(), &NoopRewriter{}, staticRetriever{err: errors.New("retrieval should be skipped")}, &NoopMemoryService{})
	p.SetPreferredLLMService(preferred)
	p.SetIntentResolver(staticIntentResolutionService{subIntents: []SubQuestionIntent{{
		SubQuestion: "你是谁",
		NodeScores: []NodeScore{{
			Node: IntentNode{
				ID:             "system-intro",
				Kind:           IntentKindSystem,
				PromptTemplate: "你是系统助手。",
			},
			Score: 0.95,
		}},
	}}})

	p.StreamChat(context.Background(), "你是谁", "conv-1", "task-1", false, s)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for preferred llm response")
	}
	body := w.Body.String()
	if !strings.Contains(body, "本地系统回复") {
		t.Fatalf("expected preferred llm to handle system-only response, got: %s", body)
	}
	if strings.Contains(body, "云端系统回复") {
		t.Fatalf("expected strong llm not to handle system-only response, got: %s", body)
	}
}

func TestPipeline_StreamChat_NewConversationFinishEventIncludesConversationTitle(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("你好")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	mem := &titleAwareMemoryService{
		conversation:      &Conversation{ID: "conv-1", UserID: "user-1", Title: "会员咨询"},
		missingBeforeSave: true,
	}
	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	p.StreamChat(context.Background(), "会员怎么查？", "conv-1", "task-1", false, s)

	<-done
	body := w.Body.String()
	if !strings.Contains(body, `"title":"会员咨询"`) {
		t.Fatalf("expected finish event to include conversation title, got: %s", body)
	}
}

func TestPipeline_StreamChat_UsesStrongLLMWhenKbContextExists(t *testing.T) {
	done := make(chan struct{})
	strong := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("云端RAG回答")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}
	preferred := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("本地RAG回答")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	mem := &recordingMemoryService{}
	s, w := newTestSSESender(t)
	p := NewPipeline(strong, NewDefaultPromptBuilder(), &NoopRewriter{}, staticRetriever{chunks: []RetrievedChunk{{ID: "chunk-1", Text: "知识库片段", Score: 0.9}}}, mem)
	p.SetPreferredLLMService(preferred)
	p.StreamChat(context.Background(), "当前会员等级是什么", "conv-1", "task-1", false, s)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for strong llm response")
	}
	body := w.Body.String()
	if !strings.Contains(body, "云端RAG回答") {
		t.Fatalf("expected strong llm for kb-backed response, got: %s", body)
	}
	if strings.Contains(body, "本地RAG回答") {
		t.Fatalf("expected preferred llm not to handle kb-backed response, got: %s", body)
	}
	if len(mem.saved) == 0 {
		t.Fatal("expected conversation messages to be saved")
	}
}

func TestPipeline_StreamChat_PassesSubIntentsToIntentAwareRetriever(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("intent aware answer")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}
	retriever := &recordingIntentAwareRetriever{
		chunks: []RetrievedChunk{{ID: "chunk-1", Text: "知识库片段", Score: 0.9}},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, retriever, &NoopMemoryService{})
	p.SetIntentResolver(staticIntentResolutionService{subIntents: []SubQuestionIntent{{
		SubQuestion: "会员积分怎么查",
		NodeScores: []NodeScore{{
			Node:  IntentNode{ID: "leaf-kb", CollectionName: "member_kb", TopK: 6, Kind: IntentKindKB},
			Score: 0.92,
		}},
	}}})

	p.StreamChat(context.Background(), "会员积分怎么查", "conv-1", "task-1", false, s)

	<-done
	if len(retriever.contexts) == 0 {
		t.Fatal("expected intent-aware retriever to be called")
	}
	got := retriever.contexts[0]
	if len(got.Intents) != 1 || len(got.Intents[0].NodeScores) != 1 {
		t.Fatalf("expected sub intents to be passed to retriever, got %+v", got.Intents)
	}
	if got.Intents[0].NodeScores[0].Node.CollectionName != "member_kb" {
		t.Fatalf("expected kb collection to be preserved, got %+v", got.Intents[0].NodeScores[0].Node)
	}
}

func TestPipeline_StreamChat_ExistingTitledConversationDoesNotResendTitle(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("继续回答。")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	mem := &titleAwareMemoryService{
		conversation: &Conversation{ID: "conv-1", UserID: "user-1", Title: "已有标题"},
	}
	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	p.StreamChat(context.Background(), "继续", "conv-1", "task-1", false, s)

	<-done
	body := w.Body.String()
	if strings.Contains(body, `"title"`) {
		t.Fatalf("expected existing titled conversation not to resend title, got: %s", body)
	}
}

func TestPipeline_StopTaskCancelsStreamAndClosesSender(t *testing.T) {
	handleCh := make(chan *blockingHandle, 1)
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("partial")
			handle := newBlockingHandle()
			handleCh <- handle
			return handle, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	done := make(chan struct{})
	go func() {
		p.StreamChat(context.Background(), "test", "conv-1", "task-stop", false, s)
		close(done)
	}()

	var handle *blockingHandle
	select {
	case handle = <-handleCh:
	case <-time.After(time.Second):
		t.Fatal("expected stream handle to be created")
	}

	p.StopTask("task-stop")

	select {
	case <-handle.cancelled:
	case <-time.After(time.Second):
		handle.Cancel()
		t.Fatal("expected StopTask to cancel stream handle")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		handle.Cancel()
		t.Fatal("expected StreamChat to return after StopTask")
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: cancel") {
		t.Fatalf("missing cancel event, body: %s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("missing done event, body: %s", body)
	}
	if !s.IsClosed() {
		t.Fatal("expected sender to be closed after StopTask")
	}
}

func TestPipeline_StopTaskCancelEventIncludesAssistantMessageID(t *testing.T) {
	handleCh := make(chan *blockingHandle, 1)
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("partial")
			handle := newBlockingHandle()
			handleCh <- handle
			return handle, nil
		},
	}

	mem := &recordingMemoryService{}
	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	done := make(chan struct{})
	go func() {
		p.StreamChat(context.Background(), "test", "conv-1", "task-stop", false, s)
		close(done)
	}()

	var handle *blockingHandle
	select {
	case handle = <-handleCh:
	case <-time.After(time.Second):
		t.Fatal("expected stream handle to be created")
	}

	p.StopTask("task-stop")

	select {
	case <-handle.cancelled:
	case <-time.After(time.Second):
		handle.Cancel()
		t.Fatal("expected StopTask to cancel stream handle")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		handle.Cancel()
		t.Fatal("expected StreamChat to return after StopTask")
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: cancel") {
		t.Fatalf("missing cancel event, body: %s", body)
	}
	if !strings.Contains(body, `"messageId":"msg-2"`) {
		t.Fatalf("expected cancel event to include assistant message id, got: %s", body)
	}
	if len(mem.saved) != 2 {
		t.Fatalf("expected user and partial assistant messages to be saved, got %d", len(mem.saved))
	}
}

func TestPipeline_StreamChat_DeepThinking(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	p.StreamChat(context.Background(), "test", "conv-1", "task-1", true, s)

	if capturedReq.Thinking == nil || !*capturedReq.Thinking {
		t.Fatal("expected thinking=true in request")
	}
}

func TestPipeline_StreamChat_DisablesThinkingWhenDeepThinkingOff(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	if capturedReq.Thinking == nil || *capturedReq.Thinking {
		t.Fatal("expected thinking=false in request")
	}
}

func TestPipeline_StreamChat_Messages(t *testing.T) {
	var capturedReq chat.Request
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			go func() {
				cb.OnContent("ok")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	go p.StreamChat(context.Background(), "hello world", "conv-1", "task-1", false, s)

	<-done

	if len(capturedReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedReq.Messages))
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
}

func TestPipeline_StreamChat_IncludesMcpContext(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	p.SetMcpContextProvider(staticMcpContextProvider{context: "工具：member_profile\n结果：用户为金卡会员。"})
	p.StreamChat(context.Background(), "当前会员等级是什么", "conv-1", "task-1", false, s)

	if len(capturedReq.Messages) < 2 {
		t.Fatalf("expected prompt messages, got %d", len(capturedReq.Messages))
	}
	content := capturedReq.Messages[len(capturedReq.Messages)-1].Content
	if !strings.Contains(content, "MCP工具结果") {
		t.Fatalf("expected prompt to include MCP context section, got: %s", content)
	}
	if !strings.Contains(content, "用户为金卡会员") {
		t.Fatalf("expected prompt to include MCP tool result, got: %s", content)
	}
}

func TestPipeline_StreamChat_AllowsMcpOnlyContextWithoutRetrievedChunks(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnContent("北京今日晴。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, &NoopRetriever{}, &NoopMemoryService{})
	p.SetMcpContextProvider(staticMcpContextProvider{context: "<data>\n工具：weather_query\n北京 今日晴\n</data>"})
	p.StreamChat(context.Background(), "查询天气", "conv-1", "task-1", false, s)

	if len(capturedReq.Messages) == 0 {
		t.Fatal("expected LLM to be called for MCP-only context")
	}
	content := capturedReq.Messages[len(capturedReq.Messages)-1].Content
	if !strings.Contains(content, "<tool-data>\n<data>\n工具：weather_query\n北京 今日晴\n</data>\n</tool-data>") {
		t.Fatalf("expected prompt to include MCP-only tool-data, got: %s", content)
	}
	if strings.Contains(content, "<documents>") {
		t.Fatalf("expected MCP-only prompt not to include documents, got: %s", content)
	}
	if strings.Contains(w.Body.String(), "检索失败原因") {
		t.Fatalf("expected MCP-only flow not to use retrieval fallback, got: %s", w.Body.String())
	}
}

func TestPipeline_StreamChat_ThinkingCallback(t *testing.T) {
	done := make(chan struct{})
	mem := &recordingMemoryService{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnThinking("let me think...")
				cb.OnContent("answer")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	go p.StreamChat(context.Background(), "test", "conv-1", "task-1", true, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, `"type":"think"`) {
		t.Fatal("missing think type")
	}
	if !strings.Contains(body, `"delta":"let me think..."`) {
		t.Fatal("missing thinking content")
	}
	if !strings.Contains(body, `"type":"response"`) {
		t.Fatal("missing response type")
	}
	if len(mem.saved) != 2 {
		t.Fatalf("expected user and assistant messages to be saved, got %d", len(mem.saved))
	}
	assistant := mem.saved[1]
	if assistant.ThinkingContent != "let me think..." {
		t.Fatalf("expected thinking content to be saved, got: %+v", assistant)
	}
	if assistant.ThinkingDuration < 1 {
		t.Fatalf("expected thinking duration to be at least 1 second, got: %+v", assistant)
	}
}

func TestPipeline_StreamChat_Error(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnError(nil)
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	go p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: finish") {
		t.Fatalf("missing finish event on error, body: %s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event on error")
	}
}
