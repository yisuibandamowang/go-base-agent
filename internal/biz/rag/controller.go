package rag

import (
	"context"
	"net/http"
	"strconv"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/snowflake"
	"go-base-agent/internal/framework/sse"

	"github.com/gin-gonic/gin"
)

// Service defines the RAG chat service interface.
type Service interface {
	StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender)
	StopTask(taskID string)
}

// Controller handles RAG chat HTTP endpoints.
// Aligns with Java RAGChatController.
type Controller struct {
	svc Service
}

// NewController creates a new RAG chat controller.
func NewController(svc Service) *Controller {
	return &Controller{svc: svc}
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

	s := sse.NewSender(c)
	sender := NewSSESender(s)

	if err := sender.SendMeta(conversationID, taskID); err != nil {
		return
	}

	// Use a detached context so the pipeline can finish after SSE transport cleanup,
	// while preserving request-scoped identity for conversation memory.
	ctx := detachedRequestContext(c.Request.Context())
	ctl.svc.StreamChat(ctx, question, conversationID, taskID, deepThinking, sender)
}

// Stop handles POST /rag/v3/stop — cancel a running task.
func (ctl *Controller) Stop(c *gin.Context) {
	taskID := c.Query("taskId")
	if taskID == "" {
		c.JSON(http.StatusOK, gin.H{"code": "A000001", "message": "taskId不能为空"})
		return
	}
	ctl.svc.StopTask(taskID)
	c.JSON(http.StatusOK, gin.H{"code": "0", "message": "success"})
}

func detachedRequestContext(reqCtx context.Context) context.Context {
	ctx := context.Background()
	if traceID := appctx.TraceID(reqCtx); traceID != "" {
		ctx = appctx.WithTraceID(ctx, traceID)
	}
	if user := appctx.User(reqCtx); user != nil {
		ctx = appctx.WithUser(ctx, user)
	}
	return ctx
}
