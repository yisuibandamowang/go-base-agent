package rag

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/idempotent"
	"go-base-agent/internal/framework/snowflake"
	"go-base-agent/internal/framework/sse"

	"github.com/gin-gonic/gin"
)

// Service defines the RAG chat service interface.
type Service interface {
	StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender)
	StopTask(taskID, requester string) error
}

// Controller handles RAG chat HTTP endpoints.
// Aligns with Java RAGChatController.
type Controller struct {
	svc   Service
	guard *idempotent.Guard
	// evalEnabled 评测模式旁路幂等提交：评测工具高频重复提问会被幂等锁拦截。
	// 对齐 Java IdempotentSubmitAspect 的 app.eval.enabled 开关。
	evalEnabled bool
}

// NewController creates a new RAG chat controller.
func NewController(svc Service) *Controller {
	return &Controller{svc: svc}
}

// SetIdempotentGuard configures an optional idempotency guard for chat and stop routes.
func (ctl *Controller) SetIdempotentGuard(guard *idempotent.Guard) {
	ctl.guard = guard
}

// SetEvalEnabled toggles the idempotency bypass for evaluation mode.
func (ctl *Controller) SetEvalEnabled(enabled bool) {
	ctl.evalEnabled = enabled
}

// Chat handles GET /rag/v3/chat — SSE streaming chat.
func (ctl *Controller) Chat(c *gin.Context) {
	question := c.Query("question")
	if question == "" {
		c.JSON(http.StatusOK, gin.H{"code": "A000001", "message": "问题不能为空"})
		return
	}

	conversationID := c.Query("conversationId")
	if conversationID == "" {
		conversationID = snowflake.NextIDStr()
	}

	deepThinking := false
	if dt := c.Query("deepThinking"); dt != "" {
		deepThinking, _ = strconv.ParseBool(dt)
	}

	taskID := snowflake.NextIDStr()
	chatLockKey := chatSubmitLockKey(c, conversationID)
	if !ctl.acquireSubmitLock(c, chatLockKey, 5*time.Minute, "当前会话处理中，请稍后再发起新的对话") {
		return
	}
	defer ctl.releaseSubmitLock(chatLockKey)

	s := sse.NewSender(c)
	sender := NewSSESender(s)

	if err := sender.SendMeta(conversationID, taskID); err != nil {
		return
	}

	// Use a detached context so the pipeline can finish after SSE transport cleanup,
	// while preserving request-scoped identity for conversation memory.
	ctx := detachedRequestContext(c.Request.Context())
	if repoPath := strings.TrimSpace(c.Query("codeRepoPath")); repoPath != "" {
		ctx = appctx.WithCodeRepoPath(ctx, repoPath)
	}
	ctl.svc.StreamChat(ctx, question, conversationID, taskID, deepThinking, sender)
}

// Stop handles POST /rag/v3/stop — cancel a running task.
// taskId 是时间有序可预测的雪花 ID，不是访问凭证：必须比对 Redis 属主后才能取消。
func (ctl *Controller) Stop(c *gin.Context) {
	taskID := c.Query("taskId")
	if taskID == "" {
		c.JSON(http.StatusOK, gin.H{"code": "A000001", "message": "taskId不能为空"})
		return
	}
	stopLockKey := stopSubmitLockKey(c, taskID)
	if !ctl.acquireSubmitLock(c, stopLockKey, 5*time.Minute, "您操作太快，请稍后再试") {
		return
	}
	defer ctl.releaseSubmitLock(stopLockKey)
	requester := ""
	if user := appctx.User(c.Request.Context()); user != nil {
		requester = strings.TrimSpace(user.UserID)
	}
	if err := ctl.svc.StopTask(taskID, requester); err != nil {
		c.JSON(http.StatusOK, gin.H{"code": "A000001", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

func (ctl *Controller) acquireSubmitLock(c *gin.Context, key string, ttl time.Duration, duplicateMessage string) bool {
	if ctl.guard == nil || ctl.evalEnabled {
		return true
	}
	ok, err := ctl.guard.Check(c.Request.Context(), key, ttl)
	if err != nil {
		return true
	}
	if !ok {
		c.JSON(http.StatusOK, gin.H{"code": "A000001", "message": duplicateMessage})
		return false
	}
	return true
}

func (ctl *Controller) releaseSubmitLock(key string) {
	if ctl.guard == nil || ctl.evalEnabled {
		return
	}
	_ = ctl.guard.Clear(context.Background(), key)
}

func chatSubmitLockKey(c *gin.Context, conversationID string) string {
	userID := "anonymous"
	if user := appctx.User(c.Request.Context()); user != nil && strings.TrimSpace(user.UserID) != "" {
		userID = strings.TrimSpace(user.UserID)
	}
	conversationKey := strings.TrimSpace(conversationID)
	if conversationKey == "" {
		conversationKey = "new"
	}
	return "rag:chat:submit:" + userID + ":" + conversationKey
}

func stopSubmitLockKey(c *gin.Context, taskID string) string {
	userID := "anonymous"
	if user := appctx.User(c.Request.Context()); user != nil && strings.TrimSpace(user.UserID) != "" {
		userID = strings.TrimSpace(user.UserID)
	}
	return "rag:stop:submit:" + userID + ":" + strings.TrimSpace(taskID)
}

func detachedRequestContext(reqCtx context.Context) context.Context {
	ctx := context.Background()
	if traceID := appctx.TraceID(reqCtx); traceID != "" {
		ctx = appctx.WithTraceID(ctx, traceID)
	}
	if user := appctx.User(reqCtx); user != nil {
		ctx = appctx.WithUser(ctx, user)
	}
	if tenant := appctx.Tenant(reqCtx); tenant != nil {
		ctx = appctx.WithTenant(ctx, tenant)
	}
	if repoPath := appctx.CodeRepoPath(reqCtx); strings.TrimSpace(repoPath) != "" {
		ctx = appctx.WithCodeRepoPath(ctx, strings.TrimSpace(repoPath))
	}
	return ctx
}
