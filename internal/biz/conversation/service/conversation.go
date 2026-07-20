package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	sumRepo  *repo.ConversationSummaryRepo
}

// NewConversationService 创建 ConversationService。
func NewConversationService(
	convRepo *repo.ConversationRepo,
	msgRepo *repo.MessageRepo,
	fbRepo *repo.FeedbackRepo,
	sumRepo *repo.ConversationSummaryRepo,
) *ConversationService {
	return &ConversationService{
		convRepo: convRepo,
		msgRepo:  msgRepo,
		fbRepo:   fbRepo,
		sumRepo:  sumRepo,
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
	if err := s.convRepo.SoftDelete(ctx, conversationID, userID); err != nil {
		return err
	}
	if s.sumRepo != nil {
		if err := s.sumRepo.DeleteByConversationID(ctx, conversationID, userID); err != nil {
			return err
		}
	}
	return nil
}

// GetMessages 获取会话消息历史。
func (s *ConversationService) GetMessages(ctx context.Context, conversationID, userID string, limit int) ([]model.Message, error) {
	return s.msgRepo.LoadHistory(ctx, conversationID, userID, limit)
}

// GetMessageVotes 获取消息反馈值映射。
func (s *ConversationService) GetMessageVotes(ctx context.Context, userID string, messageIDs []string) (map[string]int16, error) {
	if s.fbRepo == nil {
		return map[string]int16{}, nil
	}
	return s.fbRepo.ListVotesByMessageIDs(ctx, userID, messageIDs)
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

// DeleteFeedback 删除消息反馈。
func (s *ConversationService) DeleteFeedback(ctx context.Context, messageID, userID string) error {
	if s.fbRepo == nil {
		return nil
	}
	return s.fbRepo.DeleteByMessageIDAndUserID(ctx, messageID, userID)
}

// ConversationSummaryGenerator 生成会话摘要。
type ConversationSummaryGenerator interface {
	Generate(ctx context.Context, history []chat.Message, previousSummary string, maxChars int) (string, error)
}

// ConversationTitleGenerator 生成会话标题。
type ConversationTitleGenerator interface {
	Generate(ctx context.Context, question string) (string, error)
}

// DBMemoryStore 基于数据库的会话记忆存储，实现 rag.MemoryStore 接口。
type DBMemoryStore struct {
	db                *gorm.DB
	convRepo          *repo.ConversationRepo
	msgRepo           *repo.MessageRepo
	summaryRepo       *repo.ConversationSummaryRepo
	summaryGenerator  ConversationSummaryGenerator
	titleGenerator    ConversationTitleGenerator
	summaryEnabled    bool
	summaryStartTurns int
	summaryMaxChars   int
	titleMaxChars     int
}

// NewDBMemoryStore 创建 DBMemoryStore。
func NewDBMemoryStore(
	database *gorm.DB,
	convRepo *repo.ConversationRepo,
	msgRepo *repo.MessageRepo,
	summaryRepo *repo.ConversationSummaryRepo,
	summaryGenerator ConversationSummaryGenerator,
	summaryEnabled bool,
	summaryStartTurns int,
	summaryMaxChars int,
	titleMaxChars int,
) *DBMemoryStore {
	if summaryStartTurns <= 0 {
		summaryStartTurns = 3
	}
	if summaryMaxChars <= 0 {
		summaryMaxChars = 200
	}
	if titleMaxChars <= 0 {
		titleMaxChars = 30
	}
	return &DBMemoryStore{
		db:                database,
		convRepo:          convRepo,
		msgRepo:           msgRepo,
		summaryRepo:       summaryRepo,
		summaryGenerator:  summaryGenerator,
		summaryEnabled:    summaryEnabled,
		summaryStartTurns: summaryStartTurns,
		summaryMaxChars:   summaryMaxChars,
		titleMaxChars:     titleMaxChars,
	}
}

// SetTitleGenerator 设置会话标题生成器。
func (s *DBMemoryStore) SetTitleGenerator(generator ConversationTitleGenerator, maxChars ...int) {
	s.titleGenerator = generator
	if len(maxChars) > 0 && maxChars[0] > 0 {
		s.titleMaxChars = maxChars[0]
	}
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
	result := make([]chat.Message, 0, len(msgs)+1)
	if summary := s.loadLatestSummary(ctx, conversationID, conv.UserID); summary != nil && summary.Content != "" {
		result = append(result, chat.NewSystemMessage("历史摘要："+summary.Content))
	}
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
			Title:          s.generateConversationTitle(ctx, msg.Content),
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
	s.maybeUpdateSummary(ctx, conversationID, &conv, m, msg)
	return m.ID, nil
}

func (s *DBMemoryStore) generateConversationTitle(ctx context.Context, content string) string {
	if s != nil && s.titleGenerator != nil {
		if title, err := s.titleGenerator.Generate(ctx, content); err == nil && strings.TrimSpace(title) != "" {
			return strings.TrimSpace(title)
		} else if err != nil {
			slog.Warn("generate conversation title failed", "err", err)
		}
	}
	maxChars := 30
	if s != nil && s.titleMaxChars > 0 {
		maxChars = s.titleMaxChars
	}
	return conversationTitle(content, maxChars)
}

func conversationTitle(content string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 30
	}
	runes := []rune(content)
	if len(runes) == 0 {
		return "新会话"
	}
	if len(runes) > maxChars {
		runes = runes[:maxChars]
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

func (s *DBMemoryStore) loadLatestSummary(ctx context.Context, conversationID, userID string) *model.ConversationSummary {
	if s.summaryRepo == nil {
		return nil
	}
	summary, err := s.summaryRepo.FindLatestByConversationID(ctx, conversationID, userID)
	if err != nil {
		slog.Warn("load conversation summary failed", "conversationId", conversationID, "err", err)
		return nil
	}
	return summary
}

func (s *DBMemoryStore) maybeUpdateSummary(ctx context.Context, conversationID string, conv *model.Conversation, saved *model.Message, incoming chat.Message) {
	if s.summaryRepo == nil || s.summaryGenerator == nil || !s.summaryEnabled || conv == nil || saved == nil {
		return
	}
	if incoming.Role != chat.RoleAssistant {
		return
	}
	userCount, err := s.msgRepo.CountUserMessages(ctx, conversationID, conv.UserID)
	if err != nil {
		slog.Warn("count conversation messages failed", "conversationId", conversationID, "err", err)
		return
	}
	if int(userCount) < s.summaryStartTurns {
		return
	}

	var previous string
	var history []model.Message
	if latest := s.loadLatestSummary(ctx, conversationID, conv.UserID); latest != nil {
		previous = latest.Content
		history, err = s.msgRepo.LoadHistorySince(ctx, conversationID, conv.UserID, latest.LastMessageID)
	} else {
		history, err = s.msgRepo.LoadHistory(ctx, conversationID, conv.UserID, 0)
	}
	if err != nil {
		slog.Warn("load conversation history for summary failed", "conversationId", conversationID, "err", err)
		return
	}
	if len(history) == 0 {
		return
	}

	summary, err := s.summaryGenerator.Generate(ctx, toChatMessages(history), previous, s.summaryMaxChars)
	if err != nil {
		slog.Warn("generate conversation summary failed", "conversationId", conversationID, "err", err)
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	record := &model.ConversationSummary{
		ConversationID: conversationID,
		UserID:         conv.UserID,
		LastMessageID:  saved.ID,
		Content:        summary,
	}
	record.CreateTime = time.Now()
	record.UpdateTime = time.Now()
	if err := s.summaryRepo.Create(ctx, record); err != nil {
		slog.Warn("save conversation summary failed", "conversationId", conversationID, "err", err)
	}
}

func toChatMessages(msgs []model.Message) []chat.Message {
	result := make([]chat.Message, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, chat.Message{
			Role:             chat.Role(m.Role),
			Content:          m.Content,
			ThinkingContent:  m.ThinkingContent,
			ThinkingDuration: m.ThinkingDuration,
		})
	}
	return result
}
