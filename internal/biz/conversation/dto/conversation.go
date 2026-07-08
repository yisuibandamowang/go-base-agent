package dto

import "time"

// ConversationResp 会话信息响应。
type ConversationResp struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Title          string    `json:"title"`
	LastTime       time.Time `json:"lastTime"`
	CreateTime     time.Time `json:"createTime"`
}

// MessageResp 消息响应。
type MessageResp struct {
	ID               string    `json:"id"`
	ConversationID   string    `json:"conversationId"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	ThinkingContent  string    `json:"thinkingContent,omitempty"`
	ThinkingDuration int       `json:"thinkingDuration,omitempty"`
	CreateTime       time.Time `json:"createTime"`
}

// UpdateTitleReq 更新会话标题请求。
type UpdateTitleReq struct {
	Title string `json:"title" binding:"required"`
}

// FeedbackReq 消息反馈请求。
type FeedbackReq struct {
	MessageID      string `json:"messageId" binding:"required"`
	ConversationID string `json:"conversationId" binding:"required"`
	Vote           int16  `json:"vote" binding:"required"`
	Reason         string `json:"reason"`
	Comment        string `json:"comment"`
}
