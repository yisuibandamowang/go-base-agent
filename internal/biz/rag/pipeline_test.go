package rag

import (
	"context"
	"errors"
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
	history []chat.Message
	saved   []chat.Message
}

func (m *recordingMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return m.history, nil
}

func (m *recordingMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	m.saved = append(m.saved, msg)
	return nil
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
	chunks   []RetrievedChunk
}

func (r *recordingRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	r.question = question
	return r.chunks, nil
}

type recordingRewriter struct {
	history []chat.Message
	result  string
}

func (r *recordingRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	r.history = append([]chat.Message(nil), history...)
	return &RewriteResult{RewrittenQuestion: r.result}, nil
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

func TestPipeline_StreamChat_ThinkingCallback(t *testing.T) {
	done := make(chan struct{})
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
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
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
