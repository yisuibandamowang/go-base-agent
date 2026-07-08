package rag

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/framework/sse"

	"github.com/gin-gonic/gin"
)

func newTestSSESender(t *testing.T) (*SSESender, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return NewSSESender(sse.NewSender(c)), w
}

func TestMetaPayload_JSON(t *testing.T) {
	p := MetaPayload{ConversationID: "conv-1", TaskID: "task-2"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	expected := `{"conversationId":"conv-1","taskId":"task-2"}`
	if string(data) != expected {
		t.Fatalf("unexpected JSON: %s", string(data))
	}
}

func TestMessageDelta_JSON(t *testing.T) {
	p := MessageDelta{Type: "response", Delta: "hello"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"type":"response"`) {
		t.Fatal("missing type field")
	}
	if !strings.Contains(string(data), `"delta":"hello"`) {
		t.Fatal("missing delta field")
	}
}

func TestCompletionPayload_JSON_OmitEmpty(t *testing.T) {
	p := CompletionPayload{MessageID: "msg-1"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "title") {
		t.Fatal("title should be omitted when empty")
	}
	if !strings.Contains(string(data), `"messageId":"msg-1"`) {
		t.Fatal("missing messageId")
	}
}

func TestCompletionPayload_JSON_WithTitle(t *testing.T) {
	p := CompletionPayload{MessageID: "msg-1", Title: "test title"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"title":"test title"`) {
		t.Fatal("missing title")
	}
}

func TestSendMeta(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendMeta("conv-1", "task-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: meta") {
		t.Fatal("missing event: meta")
	}
	if !strings.Contains(body, `"conversationId":"conv-1"`) {
		t.Fatal("missing conversationId in data")
	}
}

func TestSendMessage(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendMessage(MsgTypeResponse, "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing event: message")
	}
	if !strings.Contains(body, `"delta":"hello world"`) {
		t.Fatal("missing delta")
	}
}

func TestSendMessage_Think(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendMessage(MsgTypeThink, "thinking...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"type":"think"`) {
		t.Fatal("missing think type")
	}
}

func TestSendFinish(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendFinish("msg-123", "conversation title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: finish") {
		t.Fatal("missing event: finish")
	}
	if !strings.Contains(body, `"messageId":"msg-123"`) {
		t.Fatal("missing messageId")
	}
}

func TestSendDone(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendDone()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing event: done")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatal("missing [DONE] data")
	}
}

func TestSendReject(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendReject()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: reject") {
		t.Fatal("missing event: reject")
	}
	if !strings.Contains(body, MsgTypeResponse) {
		t.Fatal("missing response type")
	}
}

func TestSendCancel(t *testing.T) {
	s, w := newTestSSESender(t)
	err := s.SendCancel("msg-1", "title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: cancel") {
		t.Fatal("missing event: cancel")
	}
}

func TestSSESender_Close(t *testing.T) {
	s, _ := newTestSSESender(t)
	s.Close()
	err := s.SendMeta("a", "b")
	if err == nil {
		t.Fatal("expected error after close")
	}
}
