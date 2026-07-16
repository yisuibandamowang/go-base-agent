package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/idempotent"

	"github.com/gin-gonic/gin"
)

type contextCaptureService struct {
	user   *appctx.LoginUser
	tenant *appctx.TenantContext
}

func (s *contextCaptureService) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	s.user = appctx.User(ctx)
	s.tenant = appctx.Tenant(ctx)
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

func TestController_Chat_PreservesTenantContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &contextCaptureService{}
	ctl := NewController(svc)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=test&conversationId=conv-1", nil)
	req = req.WithContext(appctx.WithTenant(req.Context(), &appctx.TenantContext{TenantID: "tenant-1", Domain: "membership"}))
	c.Request = req

	ctl.Chat(c)

	if svc.tenant == nil || svc.tenant.TenantID != "tenant-1" {
		t.Fatalf("expected tenant context to be preserved, got %+v", svc.tenant)
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

func TestController_Chat_IdempotentBlocksDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctl := NewController(&blockingRagService{
		chatStarted: make(chan struct{}, 1),
		chatRelease: make(chan struct{}),
	})
	ctl.SetIdempotentGuard(idempotent.New(rdb))
	svc := ctl.svc.(*blockingRagService)

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	req1 := httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=hello", nil)
	req1 = req1.WithContext(appctx.WithUser(req1.Context(), &appctx.LoginUser{UserID: "user-1"}))
	c1.Request = req1

	done1 := make(chan struct{})
	go func() {
		ctl.Chat(c1)
		close(done1)
	}()

	select {
	case <-svc.chatStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first chat request to start")
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	req2 := httptest.NewRequest(http.MethodGet, "/rag/v3/chat?question=hello", nil)
	req2 = req2.WithContext(appctx.WithUser(req2.Context(), &appctx.LoginUser{UserID: "user-1"}))
	c2.Request = req2

	ctl.Chat(c2)
	if !strings.Contains(w2.Body.String(), "当前会话处理中") {
		t.Fatalf("expected duplicate chat to be blocked, got %s", w2.Body.String())
	}

	close(svc.chatRelease)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first chat request to finish")
	}
}

func TestController_Stop_IdempotentBlocksDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctl := NewController(&blockingRagService{
		stopStarted: make(chan struct{}, 1),
		stopRelease: make(chan struct{}),
	})
	ctl.SetIdempotentGuard(idempotent.New(rdb))
	svc := ctl.svc.(*blockingRagService)

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	req1 := httptest.NewRequest(http.MethodPost, "/rag/v3/stop?taskId=task-1", nil)
	req1 = req1.WithContext(appctx.WithUser(req1.Context(), &appctx.LoginUser{UserID: "user-1"}))
	c1.Request = req1

	done1 := make(chan struct{})
	go func() {
		ctl.Stop(c1)
		close(done1)
	}()

	select {
	case <-svc.stopStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first stop request to start")
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	req2 := httptest.NewRequest(http.MethodPost, "/rag/v3/stop?taskId=task-1", nil)
	req2 = req2.WithContext(appctx.WithUser(req2.Context(), &appctx.LoginUser{UserID: "user-1"}))
	c2.Request = req2

	ctl.Stop(c2)
	if !strings.Contains(w2.Body.String(), "您操作太快") {
		t.Fatalf("expected duplicate stop to be blocked, got %s", w2.Body.String())
	}

	close(svc.stopRelease)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first stop request to finish")
	}
}

type blockingRagService struct {
	chatStarted chan struct{}
	chatRelease chan struct{}
	stopStarted chan struct{}
	stopRelease chan struct{}
}

func (s *blockingRagService) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	if s.chatStarted != nil {
		select {
		case s.chatStarted <- struct{}{}:
		default:
		}
	}
	if s.chatRelease != nil {
		<-s.chatRelease
	}
	sender.SendFinish("", "")
	sender.SendDone()
	sender.Close()
}

func (s *blockingRagService) StopTask(taskID string) {
	if s.stopStarted != nil {
		select {
		case s.stopStarted <- struct{}{}:
		default:
		}
	}
	if s.stopRelease != nil {
		<-s.stopRelease
	}
}
