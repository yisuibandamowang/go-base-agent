package handler

import (
	"net/http"
	"strconv"

	"go-base-agent/internal/biz/conversation/dto"
	"go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/service"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/middleware"

	"github.com/gin-gonic/gin"
)

// ConversationHandler 会话 HTTP 处理层。
type ConversationHandler struct {
	svc *service.ConversationService
}

// NewConversationHandler 创建 ConversationHandler。
func NewConversationHandler(svc *service.ConversationService) *ConversationHandler {
	return &ConversationHandler{svc: svc}
}

// List GET /api/ragent/conversations
func (h *ConversationHandler) List(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	page, size := 1, 0
	paged := wantsPaged(c)
	if paged {
		page, size = paginationParams(c)
	}
	convs, total, err := h.svc.ListConversations(c.Request.Context(), user.UserID, page, size)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	records := make([]dto.ConversationResp, 0, len(convs))
	for _, conv := range convs {
		records = append(records, dto.ConversationResp{
			ID:             conv.ID,
			ConversationID: conv.ConversationID,
			Title:          conv.Title,
			LastTime:       conv.LastTime,
			CreateTime:     conv.CreateTime,
		})
	}
	if paged {
		c.JSON(http.StatusOK, convention.Success(convention.NewPageResp(records, total, page, size)))
		return
	}
	c.JSON(http.StatusOK, convention.Success(records))
}

// Get GET /api/ragent/conversations/:conversationId
func (h *ConversationHandler) Get(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	conversationID := c.Param("conversationId")
	conv, err := h.svc.GetConversation(c.Request.Context(), conversationID, user.UserID)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	resp := dto.ConversationResp{
		ID:             conv.ID,
		ConversationID: conv.ConversationID,
		Title:          conv.Title,
		LastTime:       conv.LastTime,
		CreateTime:     conv.CreateTime,
	}
	c.JSON(http.StatusOK, convention.Success(resp))
}

// UpdateTitle PUT /api/ragent/conversations/:conversationId/title
func (h *ConversationHandler) UpdateTitle(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	conversationID := c.Param("conversationId")
	var req dto.UpdateTitleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	if err := h.svc.UpdateTitle(c.Request.Context(), conversationID, user.UserID, req.Title); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// Delete DELETE /api/ragent/conversations/:conversationId
func (h *ConversationHandler) Delete(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	conversationID := c.Param("conversationId")
	if err := h.svc.DeleteConversation(c.Request.Context(), conversationID, user.UserID); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// Messages GET /api/ragent/conversations/:conversationId/messages
func (h *ConversationHandler) Messages(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	conversationID := c.Param("conversationId")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "0"))
	msgs, err := h.svc.GetMessages(c.Request.Context(), conversationID, user.UserID, limit)
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	records := make([]dto.MessageResp, 0, len(msgs))
	votes, err := h.svc.GetMessageVotes(c.Request.Context(), user.UserID, messageIDs(msgs))
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	for _, m := range msgs {
		var vote *int16
		if v, ok := votes[m.ID]; ok {
			vv := v
			vote = &vv
		}
		records = append(records, dto.MessageResp{
			ID:               m.ID,
			ConversationID:   m.ConversationID,
			Role:             m.Role,
			Content:          m.Content,
			ThinkingContent:  m.ThinkingContent,
			ThinkingDuration: m.ThinkingDuration,
			Vote:             vote,
			CreateTime:       m.CreateTime,
		})
	}
	c.JSON(http.StatusOK, convention.Success(records))
}

// SubmitFeedback POST /api/ragent/conversations/feedback
func (h *ConversationHandler) SubmitFeedback(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	var req dto.FeedbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "参数校验失败: "+err.Error()))
		return
	}
	if req.MessageID == "" {
		req.MessageID = c.Param("messageId")
	}
	if req.MessageID == "" {
		c.JSON(http.StatusOK, convention.Failure("A000001", "messageId不能为空"))
		return
	}
	err := h.svc.CreateFeedback(c.Request.Context(), struct {
		MessageID      string
		ConversationID string
		UserID         string
		Vote           int16
		Reason         string
		Comment        string
	}{
		MessageID:      req.MessageID,
		ConversationID: req.ConversationID,
		UserID:         user.UserID,
		Vote:           req.Vote,
		Reason:         req.Reason,
		Comment:        req.Comment,
	})
	if err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

// DeleteFeedback DELETE /api/ragent/conversations/messages/:messageId/feedback
func (h *ConversationHandler) DeleteFeedback(c *gin.Context) {
	user := middleware.GetLoginUser(c)
	if user == nil {
		c.JSON(http.StatusOK, convention.Failure("A000001", "未登录"))
		return
	}
	messageID := c.Param("messageId")
	if messageID == "" {
		c.JSON(http.StatusOK, convention.Failure("A000001", "messageId不能为空"))
		return
	}
	if err := h.svc.DeleteFeedback(c.Request.Context(), messageID, user.UserID); err != nil {
		c.JSON(http.StatusOK, convention.Failure("B000001", err.Error()))
		return
	}
	c.JSON(http.StatusOK, convention.Success[any](nil))
}

func paginationParams(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(c.DefaultQuery("current", c.DefaultQuery("page", "1")))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(c.DefaultQuery("size", "10"))
	if err != nil || size < 1 {
		size = 10
	}
	return page, size
}

func wantsPaged(c *gin.Context) bool {
	v := c.Query("paged")
	if v == "" {
		v = c.Query("pageMode")
	}
	return v == "true" || v == "1" || v == "page"
}

func messageIDs(msgs []model.Message) []string {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		ids = append(ids, msg.ID)
	}
	return ids
}
