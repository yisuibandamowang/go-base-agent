package rag

import (
	"context"
	"errors"
	"reflect"
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

type contextSensitiveMemoryService struct {
	recordingMemoryService
}

func (m *contextSensitiveMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.recordingMemoryService.SaveMessage(ctx, conversationID, msg)
}

func (m *contextSensitiveMemoryService) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return m.recordingMemoryService.AppendMessage(ctx, conversationID, msg)
}

func (m *contextSensitiveMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.recordingMemoryService.LoadConversation(ctx, conversationID)
}

type timeoutMemoryService struct {
	started chan struct{}
	err     error
}

func (m *timeoutMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	if m.started != nil {
		close(m.started)
	}
	<-ctx.Done()
	m.err = ctx.Err()
	return nil, ctx.Err()
}

func (m *timeoutMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	return nil
}

func (m *timeoutMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return nil, nil
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

type cancelingRetriever struct {
	cancel context.CancelFunc
}

func (r cancelingRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if r.cancel != nil {
		r.cancel()
	}
	return nil, nil
}

type recordingRetriever struct {
	question string
	queries  []string
	topKs    []int
	chunks   []RetrievedChunk
	err      error
}

func (r *recordingRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	r.question = question
	r.queries = append(r.queries, question)
	r.topKs = append(r.topKs, topK)
	return r.chunks, r.err
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

type fakeAnswerCacheManager struct {
	loadKey    string
	saveKey    string
	saveAnswer CachedAnswer
	saveTTL    time.Duration
	answer     *CachedAnswer
	answers    map[string]CachedAnswer
	hit        bool
}

func (m *fakeAnswerCacheManager) LoadAnswer(ctx context.Context, key string) (*CachedAnswer, bool, error) {
	m.loadKey = key
	if m.answers != nil {
		answer, ok := m.answers[key]
		if !ok {
			return nil, false, nil
		}
		return &answer, true, nil
	}
	if !m.hit || m.answer == nil {
		return nil, false, nil
	}
	cp := *m.answer
	return &cp, true, nil
}

func (m *fakeAnswerCacheManager) SaveAnswer(ctx context.Context, key string, answer CachedAnswer, ttl time.Duration) error {
	m.saveKey = key
	m.saveAnswer = answer
	m.saveTTL = ttl
	return nil
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

type contextSensitiveTraceRecorder struct {
	finishRunErr  error
	finishNodeErr []error
	finishStatus  string
}

func (r *contextSensitiveTraceRecorder) StartRun(ctx context.Context, conversationID, taskID string) (*TraceRunRecord, error) {
	return &TraceRunRecord{TraceID: "trace-1"}, nil
}

func (r *contextSensitiveTraceRecorder) FinishRun(ctx context.Context, traceID, status string, err error) error {
	r.finishStatus = status
	if ctxErr := ctx.Err(); ctxErr != nil {
		r.finishRunErr = ctxErr
		return ctxErr
	}
	return nil
}

func (r *contextSensitiveTraceRecorder) StartNode(ctx context.Context, traceID, parentNodeID, nodeName, nodeType string, depth int) (*TraceNodeRecord, error) {
	return &TraceNodeRecord{NodeID: nodeName + "-node"}, nil
}

func (r *contextSensitiveTraceRecorder) FinishNode(ctx context.Context, traceID, nodeID, status string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		r.finishNodeErr = append(r.finishNodeErr, ctxErr)
		return ctxErr
	}
	return nil
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

func TestPipeline_StreamChat_UsesConfiguredDefaultTopK(t *testing.T) {
	done := make(chan struct{})
	retriever := &recordingRetriever{chunks: []RetrievedChunk{{ID: "chunk-1", Text: "知识库片段", Score: 0.9}}}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("hello")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, retriever, &NoopMemoryService{})
	p.SetDefaultTopK(15)
	p.StreamChat(context.Background(), "test", "conv-1", "task-1", false, s)

	<-done
	if len(retriever.topKs) == 0 || retriever.topKs[0] != 15 {
		t.Fatalf("expected default topK 15, got %+v", retriever.topKs)
	}
}

func TestPipeline_StreamChat_UsesConfiguredTimeout(t *testing.T) {
	mem := &timeoutMemoryService{started: make(chan struct{})}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), mem)
	p.SetStreamTimeout(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.StreamChat(ctx, "test", "conv-1", "task-1", false, s)
		close(done)
	}()

	select {
	case <-mem.started:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not enter memory load")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not finish after configured timeout")
	}
	if mem.err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", mem.err)
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

func TestPipeline_StreamChat_PersistsCitationsWithAssistantMessage(t *testing.T) {
	mem := &recordingMemoryService{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("当前助手支持错误排查能力。")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, citationRetriever(), mem)
	p.StreamChat(context.Background(), "助手支持什么能力", "conv-1", "task-1", false, s)

	if len(mem.saved) != 2 {
		t.Fatalf("expected user and assistant messages to be saved, got %d", len(mem.saved))
	}
	content := mem.saved[1].Content
	for _, want := range []string{"当前助手支持错误排查能力。", "依据：", "会员Agent说明.md", "https://example.com/member-agent.md"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected persisted assistant content to contain %q, got: %s", want, content)
		}
	}
}

func TestPipeline_StreamChat_IgnoresQuestionOnlyCachedAnswerBeforeRetrieval(t *testing.T) {
	mem := &recordingMemoryService{}
	retriever := &recordingRetriever{chunks: []RetrievedChunk{{
		ID:    "fresh-chunk",
		Text:  "新的准确证据",
		Score: 0.9,
		Metadata: map[string]string{
			"doc_id":      "fresh-doc",
			"chunk_index": "1",
			"doc_name":    "iOS整体链路.md",
		},
	}}}
	question := "会员服务的ios整体链路是什么样的?"
	staleKey := buildAnswerCacheKey(question, false, []string{"member"})
	cache := &fakeAnswerCacheManager{
		answers: map[string]CachedAnswer{
			staleKey: {
				Content:   "旧的低质量缓存答案",
				Citations: "\n\n依据：\n1. 知识库：会员知识库；文档：《会员说明.md》\n",
			},
		},
	}
	llmCalled := false
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			llmCalled = true
			cb.OnContent("新的高质量答案")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, retriever, mem)
	p.SetAnswerCache(cache, true, time.Hour)
	p.StreamChat(context.Background(), question, "conv-1", "task-1", false, s)

	body := w.Body.String()
	if len(retriever.queries) == 0 {
		t.Fatal("expected retrieval to run before answer cache lookup")
	}
	if !llmCalled {
		t.Fatal("expected stale question-only cache to be ignored and llm to run")
	}
	if strings.Contains(body, "旧的低质量缓存答案") {
		t.Fatalf("expected stale cached answer to be ignored, got: %s", body)
	}
	if !strings.Contains(body, "新的高质量答案") {
		t.Fatalf("expected generated answer, got: %s", body)
	}
}

func TestPipeline_StreamChat_StreamsCachedAnswerAfterSameEvidenceRetrieved(t *testing.T) {
	mem := &recordingMemoryService{}
	chunks := []RetrievedChunk{{
		ID:    "chunk-1",
		Text:  "准确证据",
		Score: 0.9,
		Metadata: map[string]string{
			"doc_id":      "doc-1",
			"chunk_index": "1",
			"doc_name":    "会员说明.md",
		},
	}}
	key := buildAnswerCacheKey("会员服务链路是什么", false, answerCacheEvidenceKeys(chunks))
	retriever := &recordingRetriever{chunks: chunks}
	cache := &fakeAnswerCacheManager{
		answers: map[string]CachedAnswer{
			key: {
				Content:   "缓存答案",
				Citations: "\n\n依据：\n1. 知识库：会员知识库；文档：《会员说明.md》\n",
			},
		},
	}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			t.Fatal("llm should be skipped when the same evidence cache hits")
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, retriever, mem)
	p.SetAnswerCache(cache, true, time.Hour)
	p.StreamChat(context.Background(), "会员服务链路是什么", "conv-1", "task-1", false, s)

	body := w.Body.String()
	if len(retriever.queries) == 0 {
		t.Fatal("expected retrieval to run before cache hit")
	}
	for _, want := range []string{"缓存答案", "依据：", "会员说明.md", "event: finish", "event: done"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected cached SSE response to contain %q, got: %s", want, body)
		}
	}
	if len(mem.saved) != 2 {
		t.Fatalf("expected cached turn to save user and assistant messages, got %d", len(mem.saved))
	}
	if mem.saved[1].Role != chat.RoleAssistant || !strings.Contains(mem.saved[1].Content, "缓存答案") || !strings.Contains(mem.saved[1].Content, "依据：") {
		t.Fatalf("unexpected cached assistant message: %+v", mem.saved[1])
	}
}

func TestPipeline_StreamChat_SavesCompletedAnswerToCache(t *testing.T) {
	cache := &fakeAnswerCacheManager{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("稳定答案")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, citationRetriever(), &NoopMemoryService{})
	p.SetAnswerCache(cache, true, 30*time.Minute)
	p.StreamChat(context.Background(), "助手支持什么能力", "conv-1", "task-1", false, s)

	if cache.saveKey == "" {
		t.Fatal("expected completed answer to be saved to cache")
	}
	if cache.saveAnswer.Content != "稳定答案" {
		t.Fatalf("expected answer content to be cached, got %+v", cache.saveAnswer)
	}
	if !strings.Contains(cache.saveAnswer.Citations, "依据：") || !strings.Contains(cache.saveAnswer.Citations, "会员Agent说明.md") {
		t.Fatalf("expected citations to be cached, got %+v", cache.saveAnswer)
	}
	if cache.saveTTL != 30*time.Minute {
		t.Fatalf("expected configured cache ttl, got %s", cache.saveTTL)
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

func TestPipeline_StreamChat_UsesOnlyFinalEvidenceChunksForPromptAndCitations(t *testing.T) {
	done := make(chan struct{})
	var prompt string
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			if len(req.Messages) > 0 {
				prompt = req.Messages[len(req.Messages)-1].Content
			}
			go func() {
				cb.OnContent("已根据高相关证据回答。")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}
	chunks := []RetrievedChunk{
		{
			ID:    "high-1",
			Text:  "高相关正文1",
			Score: 0.9,
			Metadata: map[string]string{
				"kb_name":     "会员知识库",
				"doc_id":      "doc-high-1",
				"doc_name":    "会员服务架构.md",
				"chunk_index": "1",
			},
		},
		{
			ID:    "high-2",
			Text:  "高相关正文2",
			Score: 0.8,
			Metadata: map[string]string{
				"kb_name":     "会员知识库",
				"doc_id":      "doc-high-2",
				"doc_name":    "iOS 整体链路.md",
				"chunk_index": "2",
			},
		},
		{
			ID:    "low-1",
			Text:  "低相关收银台正文",
			Score: 0.1,
			Metadata: map[string]string{
				"kb_name":     "go 语言知识库",
				"doc_id":      "doc-low",
				"doc_name":    "收银台诊断工具当前支持能力.md",
				"chunk_index": "3",
			},
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, staticRetriever{chunks: chunks}, &NoopMemoryService{})
	p.SetDefaultTopK(2)
	go p.StreamChat(context.Background(), "会员服务的ios整体链路是什么样的?", "conv-1", "task-1", false, s)

	<-done

	if strings.Contains(prompt, "低相关收银台正文") {
		t.Fatalf("expected low ranked chunk to be excluded from prompt, got: %s", prompt)
	}
	body := w.Body.String()
	if strings.Contains(body, "收银台诊断工具当前支持能力.md") {
		t.Fatalf("expected low ranked chunk citation to be excluded, got: %s", body)
	}
	if !strings.Contains(body, "会员服务架构.md") || !strings.Contains(body, "iOS 整体链路.md") {
		t.Fatalf("expected high ranked citations to remain, got: %s", body)
	}
}

func TestSelectFinalEvidenceChunksSortsDeterministically(t *testing.T) {
	chunks := []RetrievedChunk{
		{ID: "c", Score: 0.8, Metadata: map[string]string{"doc_id": "doc-b", "chunk_index": "2"}},
		{ID: "b", Score: 0.9, Metadata: map[string]string{"doc_id": "doc-b", "chunk_index": "2"}},
		{ID: "a", Score: 0.9, Metadata: map[string]string{"doc_id": "doc-a", "chunk_index": "3"}},
		{ID: "d", Score: 0.9, Metadata: map[string]string{"doc_id": "doc-a", "chunk_index": "1"}},
	}

	got := selectFinalEvidenceChunks(chunks, "会员服务说明", 3)

	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %+v", got)
	}
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	want := []string{"d", "a", "b"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("unexpected deterministic evidence order: got %v, want %v", ids, want)
	}
}

func TestSelectFinalEvidenceChunksFiltersLexicalNoise(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:    "noise",
			Text:  "iOS 客户端展示收银台诊断入口。",
			Score: 0.99,
			Metadata: map[string]string{
				"kb_name":  "go 语言知识库",
				"doc_name": "收银台诊断工具使用手册.md",
			},
		},
		{
			ID:    "member",
			Text:  "该页面暂无具体正文。",
			Score: 0.4,
			Metadata: map[string]string{
				"kb_name":  "会员知识库",
				"doc_name": "iOS 整体链路.md",
			},
		},
	}

	got := selectFinalEvidenceChunks(chunks, "会员服务的ios整体链路是什么样的?", 2)

	if len(got) != 1 {
		t.Fatalf("expected only lexical related evidence, got %+v", got)
	}
	if got[0].ID != "member" {
		t.Fatalf("expected member evidence to remain, got %+v", got)
	}
}

func TestSelectFinalEvidenceChunksPrefersIntentCollections(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:    "global-noise",
			Text:  "会员服务 iOS 收银台诊断说明。",
			Score: 0.99,
			Metadata: map[string]string{
				"collection_name": "goknowladge",
				"kb_name":         "go 语言知识库",
				"doc_name":        "收银台诊断工具使用手册.md",
			},
		},
		{
			ID:    "intent-member",
			Text:  "会员服务架构目录。",
			Score: 0.4,
			Metadata: map[string]string{
				"collection_name": "member",
				"kb_name":         "会员知识库",
				"doc_name":        "会员服务架构.md",
			},
		},
	}

	got := selectFinalEvidenceChunks(chunks, "会员服务的ios整体链路是什么样的?", 5, []string{"member"})

	if len(got) != 1 {
		t.Fatalf("expected only preferred collection evidence, got %+v", got)
	}
	if got[0].ID != "intent-member" {
		t.Fatalf("expected intent collection evidence to remain, got %+v", got)
	}
}

func TestSelectFinalEvidenceChunksPrefersMetadataAnchorCollections(t *testing.T) {
	chunks := []RetrievedChunk{
		{
			ID:    "global-noise",
			Text:  "会员服务 iOS 收银台诊断说明。",
			Score: 0.99,
			Metadata: map[string]string{
				"collection_name": "goknowladge",
				"kb_name":         "go 语言知识库",
				"doc_name":        "收银台诊断工具使用手册.md",
			},
		},
		{
			ID:    "member",
			Text:  "会员服务架构目录。",
			Score: 0.4,
			Metadata: map[string]string{
				"collection_name": "member",
				"kb_name":         "会员知识库",
				"doc_name":        "会员服务架构.md",
			},
		},
	}

	got := selectFinalEvidenceChunks(chunks, "会员服务的ios整体链路是什么样的?", 5)

	if len(got) != 1 {
		t.Fatalf("expected metadata anchored collection evidence only, got %+v", got)
	}
	if got[0].ID != "member" {
		t.Fatalf("expected member metadata anchored evidence to remain, got %+v", got)
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

func TestPipeline_StreamChat_PersistsFallbackAndTraceAfterContextCancelled(t *testing.T) {
	parentCtx, cancel := context.WithCancel(context.Background())
	mem := &contextSensitiveMemoryService{}
	trace := &contextSensitiveTraceRecorder{}
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, cancelingRetriever{cancel: cancel}, mem)
	p.SetTraceRecorder(trace)
	p.StreamChat(parentCtx, "扶摇 tag 去重的线上问题是怎么导致的？", "conv-1", "task-1", false, s)

	if len(mem.saved) != 1 {
		t.Fatalf("expected fallback user message to be saved after cancellation, got %d", len(mem.saved))
	}
	if mem.saved[0].Role != chat.RoleUser {
		t.Fatalf("expected saved user message, got %+v", mem.saved[0])
	}
	if trace.finishRunErr != nil {
		t.Fatalf("expected trace run finish to ignore request cancellation, got %v", trace.finishRunErr)
	}
	if len(trace.finishNodeErr) != 0 {
		t.Fatalf("expected trace node finish to ignore request cancellation, got %v", trace.finishNodeErr)
	}
	if trace.finishStatus != traceStatusSuccess {
		t.Fatalf("expected trace run SUCCESS, got %q", trace.finishStatus)
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
