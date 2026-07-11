package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appctx "go-base-agent/internal/framework/context"

	"github.com/gin-gonic/gin"
)

type contextCaptureService struct {
	user *appctx.LoginUser
}

func (s *contextCaptureService) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	s.user = appctx.User(ctx)
	sender.SendFinish("", "")
	sender.SendDone()
	sender.Close()
}

func (s *contextCaptureService) StopTask(taskID string) {}

func TestController_Chat_MissingQuestion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctl := NewController(&StubService{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/rag/v3/chat", nil)

	ctl.Chat(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "A000001") {
		t.Fatal("expected error code for missing question")
	}
}

func TestController_Chat_SSEStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctl := NewController(&StubService{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=hello&conversationId=conv-1&deepThinking=true", nil)
	c.Request = req

	ctl.Chat(c)

	body := w.Body.String()

	if !strings.Contains(body, "event: meta") {
		t.Fatal("missing meta event")
	}
	if !strings.Contains(body, `"conversationId":"conv-1"`) {
		t.Fatal("missing conversationId in meta")
	}

	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
	if !strings.Contains(body, "stub: hello") {
		t.Fatal("missing stub response content")
	}

	if !strings.Contains(body, "event: finish") {
		t.Fatal("missing finish event")
	}

	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatal("missing [DONE] marker")
	}
}

func TestController_Chat_AutoConversationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctl := NewController(&StubService{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=test", nil)

	ctl.Chat(c)

	body := w.Body.String()
	if !strings.Contains(body, "event: meta") {
		t.Fatal("missing meta event")
	}
	// conversationId should be generated (snowflake, not empty)
	if !strings.Contains(body, `"conversationId":"`) {
		t.Fatal("missing conversationId")
	}
}

func TestController_Chat_PreservesLoginUserContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &contextCaptureService{}
	ctl := NewController(svc)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=test&conversationId=conv-1", nil)
	req = req.WithContext(appctx.WithUser(req.Context(), &appctx.LoginUser{UserID: "user-1"}))
	c.Request = req

	ctl.Chat(c)

	if svc.user == nil || svc.user.UserID != "user-1" {
		t.Fatalf("expected login user context to be preserved, got %+v", svc.user)
	}
}

func TestController_Stop_MissingTaskID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctl := NewController(&StubService{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/rag/v3/stop", nil)

	ctl.Stop(c)
	body := w.Body.String()
	if !strings.Contains(body, "A000001") {
		t.Fatal("expected error code for missing taskId")
	}
}

func TestController_Stop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctl := NewController(&StubService{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/rag/v3/stop?taskId=task-1", nil)

	ctl.Stop(c)
	body := w.Body.String()
	if !strings.Contains(body, `"code":"0"`) {
		t.Fatal("expected success code")
	}
}
