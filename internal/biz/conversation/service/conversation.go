package service

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"

	"gorm.io/gorm"
)

// ConversationService 会话业务服务。
type ConversationService struct {
	convRepo *repo.ConversationRepo
	msgRepo  *repo.MessageRepo
	fbRepo   *repo.FeedbackRepo
}

// NewConversationService 创建 ConversationService。
func NewConversationService(
	convRepo *repo.ConversationRepo,
	msgRepo *repo.MessageRepo,
	fbRepo *repo.FeedbackRepo,
) *ConversationService {
	return &ConversationService{
		convRepo: convRepo,
		msgRepo:  msgRepo,
		fbRepo:   fbRepo,
	}
}

// ListConversations 获取用户会话列表。
func (s *ConversationService) ListConversations(ctx context.Context, userID string, page, size int) ([]model.Conversation, int64, error) {
	return s.convRepo.ListByUser(ctx, userID, page, size)
}

// GetConversation 获取单个会话详情。
func (s *ConversationService) GetConversation(ctx context.Context, conversationID, userID string) (*model.Conversation, error) {
	return s.convRepo.FindByConversationID(ctx, conversationID, userID)
}

// UpdateTitle 更新会话标题。
func (s *ConversationService) UpdateTitle(ctx context.Context, conversationID, userID, title string) error {
	return s.convRepo.UpdateTitle(ctx, conversationID, userID, title)
}

// DeleteConversation 删除会话。
func (s *ConversationService) DeleteConversation(ctx context.Context, conversationID, userID string) error {
	return s.convRepo.SoftDelete(ctx, conversationID, userID)
}

// GetMessages 获取会话消息历史。
func (s *ConversationService) GetMessages(ctx context.Context, conversationID, userID string, limit int) ([]model.Message, error) {
	return s.msgRepo.LoadHistory(ctx, conversationID, userID, limit)
}

// CreateFeedback 创建消息反馈。
func (s *ConversationService) CreateFeedback(ctx context.Context, req struct {
	MessageID      string
	ConversationID string
	UserID         string
	Vote           int16
	Reason         string
	Comment        string
}) error {
	fb := &model.MessageFeedback{
		MessageID:      req.MessageID,
		ConversationID: req.ConversationID,
		UserID:         req.UserID,
		Vote:           req.Vote,
		Reason:         req.Reason,
		Comment:        req.Comment,
	}
	return s.fbRepo.Upsert(ctx, fb)
}

// DBMemoryStore 基于数据库的会话记忆存储，实现 rag.MemoryStore 接口。
type DBMemoryStore struct {
	db       *gorm.DB
	convRepo *repo.ConversationRepo
	msgRepo  *repo.MessageRepo
}

// NewDBMemoryStore 创建 DBMemoryStore。
func NewDBMemoryStore(database *gorm.DB, convRepo *repo.ConversationRepo, msgRepo *repo.MessageRepo) *DBMemoryStore {
	return &DBMemoryStore{db: database, convRepo: convRepo, msgRepo: msgRepo}
}

// LoadHistory 加载会话消息历史，转换为 chat.Message 格式。
func (s *DBMemoryStore) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	var conv model.Conversation
	err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ?", conversationID).First(&conv).Error
	if err != nil {
		return nil, fmt.Errorf("find conversation for history: %w", err)
	}
	msgs, err := s.msgRepo.LoadHistory(ctx, conversationID, conv.UserID, 100)
	if err != nil {
		return nil, err
	}
	result := make([]chat.Message, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, chat.Message{
			Role:             chat.Role(m.Role),
			Content:          m.Content,
			ThinkingContent:  m.ThinkingContent,
			ThinkingDuration: m.ThinkingDuration,
		})
	}
	return result, nil
}

// AppendMessage 追加消息到会话。
func (s *DBMemoryStore) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	var conv model.Conversation
	err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ?", conversationID).First(&conv).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return "", fmt.Errorf("find conversation to append: %w", err)
		}
		user := appctx.User(ctx)
		if user == nil || user.UserID == "" {
			return "", fmt.Errorf("create conversation: missing login user")
		}
		now := time.Now()
		conv = model.Conversation{
			ConversationID: conversationID,
			UserID:         user.UserID,
			Title:          conversationTitle(msg.Content),
			LastTime:       now,
		}
		conv.CreateTime = now
		conv.UpdateTime = now
		if err := s.convRepo.Create(ctx, &conv); err != nil {
			return "", fmt.Errorf("create conversation: %w", err)
		}
	}
	m := &model.Message{
		ConversationID:   conversationID,
		UserID:           conv.UserID,
		Role:             string(msg.Role),
		Content:          msg.Content,
		ThinkingContent:  msg.ThinkingContent,
		ThinkingDuration: msg.ThinkingDuration,
	}
	m.CreateTime = time.Now()
	if err := s.msgRepo.Create(ctx, m); err != nil {
		return "", fmt.Errorf("create message: %w", err)
	}
	_ = s.convRepo.TouchLastTime(ctx, conversationID)
	return m.ID, nil
}

func conversationTitle(content string) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return "新会话"
	}
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

// LoadConversation 加载会话信息。
func (s *DBMemoryStore) LoadConversation(ctx context.Context, conversationID string) (*rag.Conversation, error) {
	var conv model.Conversation
	err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ?", conversationID).First(&conv).Error
	if err != nil {
		return nil, fmt.Errorf("load conversation: %w", err)
	}
	return &rag.Conversation{
		ID:     conv.ConversationID,
		UserID: conv.UserID,
		Title:  conv.Title,
	}, nil
}

// UpdateTitle 更新会话标题。
func (s *DBMemoryStore) UpdateTitle(ctx context.Context, conversationID, title string) error {
	var conv model.Conversation
	if err := s.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ?", conversationID).First(&conv).Error; err != nil {
		return fmt.Errorf("find conversation to update title: %w", err)
	}
	return s.convRepo.UpdateTitle(ctx, conversationID, conv.UserID, title)
}
