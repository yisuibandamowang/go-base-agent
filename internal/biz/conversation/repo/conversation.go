package repo

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// ConversationRepo 会话数据访问层。
type ConversationRepo struct {
	db *gorm.DB
}

// NewConversationRepo 创建 ConversationRepo。
func NewConversationRepo(database *gorm.DB) *ConversationRepo {
	return &ConversationRepo{db: database}
}

// Create 创建会话。
func (r *ConversationRepo) Create(ctx context.Context, conversation *model.Conversation) error {
	return r.db.WithContext(ctx).Create(conversation).Error
}

// FindByConversationID 根据会话ID和用户ID查找会话。
func (r *ConversationRepo) FindByConversationID(ctx context.Context, conversationID, userID string) (*model.Conversation, error) {
	var conv model.Conversation
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		First(&conv).Error
	if err != nil {
		return nil, fmt.Errorf("find conversation: %w", err)
	}
	return &conv, nil
}

// ListByUser 分页查询用户的会话列表，按 last_time 降序。
func (r *ConversationRepo) ListByUser(ctx context.Context, userID string, page, size int) ([]model.Conversation, int64, error) {
	var convs []model.Conversation
	var total int64

	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("user_id = ?", userID)

	if err := query.Model(&model.Conversation{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count conversations: %w", err)
	}

	err := query.Scopes(db.Paginate(page, size)).
		Order("last_time DESC").
		Find(&convs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list conversations: %w", err)
	}
	return convs, total, nil
}

// UpdateTitle 更新会话标题。
func (r *ConversationRepo) UpdateTitle(ctx context.Context, conversationID, userID, title string) error {
	return r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(&model.Conversation{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Updates(map[string]interface{}{
			"title":       title,
			"update_time": time.Now(),
		}).Error
}

// TouchLastTime 更新会话最后活跃时间。
func (r *ConversationRepo) TouchLastTime(ctx context.Context, conversationID string) error {
	return r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(&model.Conversation{}).
		Where("conversation_id = ?", conversationID).
		Update("last_time", time.Now()).Error
}

// SoftDelete 软删除会话。
func (r *ConversationRepo) SoftDelete(ctx context.Context, conversationID, userID string) error {
	var conv model.Conversation
	return db.SoftDelete(r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID), &conv)
}

// MessageRepo 消息数据访问层。
type MessageRepo struct {
	db *gorm.DB
}

// NewMessageRepo 创建 MessageRepo。
func NewMessageRepo(database *gorm.DB) *MessageRepo {
	return &MessageRepo{db: database}
}

// Create 创建消息。
func (r *MessageRepo) Create(ctx context.Context, msg *model.Message) error {
	return r.db.WithContext(ctx).Create(msg).Error
}

// LoadHistory 加载会话消息历史。
func (r *MessageRepo) LoadHistory(ctx context.Context, conversationID, userID string, limit int) ([]model.Message, error) {
	var msgs []model.Message
	q := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Order("create_time ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("load message history: %w", err)
	}
	return msgs, nil
}

// LoadHistorySince 加载指定消息ID之后的消息。
func (r *MessageRepo) LoadHistorySince(ctx context.Context, conversationID, userID, sinceID string) ([]model.Message, error) {
	var msgs []model.Message
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ? AND id > ?", conversationID, userID, sinceID).
		Order("create_time ASC").
		Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("load history since: %w", err)
	}
	return msgs, nil
}

// CountUserMessages 统计会话中的用户消息数量。
func (r *MessageRepo) CountUserMessages(ctx context.Context, conversationID, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(&model.Message{}).
		Where("conversation_id = ? AND user_id = ? AND role = ?", conversationID, userID, "user").
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count user messages: %w", err)
	}
	return count, nil
}

// FeedbackRepo 消息反馈数据访问层。
type FeedbackRepo struct {
	db *gorm.DB
}

// NewFeedbackRepo 创建 FeedbackRepo。
func NewFeedbackRepo(database *gorm.DB) *FeedbackRepo {
	return &FeedbackRepo{db: database}
}

// Upsert 创建或更新反馈。
func (r *FeedbackRepo) Upsert(ctx context.Context, fb *model.MessageFeedback) error {
	return r.db.WithContext(ctx).
		Where("message_id = ? AND user_id = ?", fb.MessageID, fb.UserID).
		Assign(map[string]interface{}{
			"vote":        fb.Vote,
			"reason":      fb.Reason,
			"comment":     fb.Comment,
			"update_time": time.Now(),
		}).
		FirstOrCreate(fb).Error
}

// ListVotesByMessageIDs 查询指定消息的反馈值。
func (r *FeedbackRepo) ListVotesByMessageIDs(ctx context.Context, userID string, messageIDs []string) (map[string]int16, error) {
	if len(messageIDs) == 0 {
		return map[string]int16{}, nil
	}
	var records []model.MessageFeedback
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("user_id = ? AND message_id IN ?", userID, messageIDs).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list feedback votes: %w", err)
	}
	result := make(map[string]int16, len(records))
	for _, record := range records {
		result[record.MessageID] = record.Vote
	}
	return result, nil
}

// DeleteByMessageIDAndUserID 删除消息反馈。
func (r *FeedbackRepo) DeleteByMessageIDAndUserID(ctx context.Context, messageID, userID string) error {
	var fb model.MessageFeedback
	return db.SoftDelete(r.db.WithContext(ctx).
		Where("message_id = ? AND user_id = ?", messageID, userID), &fb)
}

// ConversationSummaryRepo 会话摘要数据访问层。
type ConversationSummaryRepo struct {
	db *gorm.DB
}

// NewConversationSummaryRepo 创建 ConversationSummaryRepo。
func NewConversationSummaryRepo(database *gorm.DB) *ConversationSummaryRepo {
	return &ConversationSummaryRepo{db: database}
}

// Create 创建会话摘要。
func (r *ConversationSummaryRepo) Create(ctx context.Context, summary *model.ConversationSummary) error {
	return r.db.WithContext(ctx).Create(summary).Error
}

// FindLatestByConversationID 获取最新摘要。
func (r *ConversationSummaryRepo) FindLatestByConversationID(ctx context.Context, conversationID, userID string) (*model.ConversationSummary, error) {
	var summary model.ConversationSummary
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Order("create_time DESC").
		First(&summary).Error
	if err != nil {
		return nil, fmt.Errorf("find latest summary: %w", err)
	}
	return &summary, nil
}

// DeleteByConversationID 删除会话摘要。
func (r *ConversationSummaryRepo) DeleteByConversationID(ctx context.Context, conversationID, userID string) error {
	var summary model.ConversationSummary
	return db.SoftDelete(r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID), &summary)
}
