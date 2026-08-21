package model

import (
	"time"

	"go-base-agent/internal/framework/db"
)

// Conversation 对应 t_conversation 表。
type Conversation struct {
	db.BaseModel

	ConversationID string    `gorm:"column:conversation_id;type:varchar(20);not null;uniqueIndex:uk_conversation_user" json:"conversationId"`
	UserID         string    `gorm:"column:user_id;type:varchar(20);not null;uniqueIndex:uk_conversation_user" json:"userId"`
	Title          string    `gorm:"column:title;type:varchar(128);not null" json:"title"`
	LastTime       time.Time `gorm:"column:last_time" json:"lastTime"`
}

func (Conversation) TableName() string {
	return "t_conversation"
}

// Message 对应 t_message 表。
type Message struct {
	db.BaseModel

	ConversationID   string `gorm:"column:conversation_id;type:varchar(20);not null;index:idx_conversation_user_time" json:"conversationId"`
	UserID           string `gorm:"column:user_id;type:varchar(20);not null;index:idx_conversation_user_time" json:"userId"`
	Role             string `gorm:"column:role;type:varchar(16);not null" json:"role"`
	Content          string `gorm:"column:content;type:text;not null" json:"content"`
	ThinkingContent  string `gorm:"column:thinking_content;type:text" json:"thinkingContent"`
	ThinkingDuration int    `gorm:"column:thinking_duration;type:integer" json:"thinkingDuration"`
	// Sources 回答来源（JSONB 文档级来源列表，命中知识库的助手消息携带）。
	Sources string `gorm:"column:sources;type:jsonb" json:"sources"`
	// RetrievedChunks 推荐追问使用的 grounding 片段（JSONB）。
	RetrievedChunks string `gorm:"column:retrieved_chunks;type:jsonb" json:"-"`
	// RecommendedQuestions 推荐追问结果（JSONB）；空数组表示已生成但无结果。
	RecommendedQuestions string `gorm:"column:recommended_questions;type:jsonb" json:"-"`
	ReplyToMessageID     string `gorm:"column:reply_to_message_id;type:varchar(20)" json:"replyToMessageId,omitempty"`
	MessageStatus        string `gorm:"column:message_status;type:varchar(16);default:NORMAL" json:"messageStatus,omitempty"`
}

func (Message) TableName() string {
	return "t_message"
}

// ConversationSummary 对应 t_conversation_summary 表。
type ConversationSummary struct {
	db.BaseModel

	ConversationID string `gorm:"column:conversation_id;type:varchar(20);not null;index:idx_conv_user" json:"conversationId"`
	UserID         string `gorm:"column:user_id;type:varchar(20);not null;index:idx_conv_user" json:"userId"`
	LastMessageID  string `gorm:"column:last_message_id;type:varchar(20);not null" json:"lastMessageId"`
	Content        string `gorm:"column:content;type:text;not null" json:"content"`
}

func (ConversationSummary) TableName() string {
	return "t_conversation_summary"
}

// MessageFeedback 对应 t_message_feedback 表。
type MessageFeedback struct {
	db.BaseModel

	MessageID      string `gorm:"column:message_id;type:varchar(20);not null;uniqueIndex:uk_msg_user" json:"messageId"`
	ConversationID string `gorm:"column:conversation_id;type:varchar(20);not null;index:idx_conversation_id" json:"conversationId"`
	UserID         string `gorm:"column:user_id;type:varchar(20);not null;uniqueIndex:uk_msg_user;index:idx_user_id" json:"userId"`
	Vote           int16  `gorm:"column:vote;type:smallint;not null" json:"vote"`
	Reason         string `gorm:"column:reason;type:varchar(255)" json:"reason"`
	Comment        string `gorm:"column:comment;type:varchar(1024)" json:"comment"`
}

func (MessageFeedback) TableName() string {
	return "t_message_feedback"
}
