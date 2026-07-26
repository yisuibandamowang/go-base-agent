package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"
	redislock "go-base-agent/internal/framework/lock"
	"go-base-agent/internal/framework/mq"
	"go-base-agent/internal/infra/chat"

	"gorm.io/gorm"
)

// ConversationService 会话业务服务。
type ConversationService struct {
	convRepo           *repo.ConversationRepo
	msgRepo            *repo.MessageRepo
	fbRepo             *repo.FeedbackRepo
	sumRepo            *repo.ConversationSummaryRepo
	feedbackMQProducer mq.Producer
	feedbackMQEnabled  bool
	titleMaxChars      int
}

// NewConversationService 创建 ConversationService。
func NewConversationService(
	convRepo *repo.ConversationRepo,
	msgRepo *repo.MessageRepo,
	fbRepo *repo.FeedbackRepo,
	sumRepo *repo.ConversationSummaryRepo,
) *ConversationService {
	return &ConversationService{
		convRepo:      convRepo,
		msgRepo:       msgRepo,
		fbRepo:        fbRepo,
		sumRepo:       sumRepo,
		titleMaxChars: 30,
	}
}

// SetTitleMaxChars 设置会话标题最大长度。
func (s *ConversationService) SetTitleMaxChars(maxChars int) {
	if maxChars > 0 {
		s.titleMaxChars = maxChars
	}
}

// SetFeedbackMQProducer 设置消息反馈 MQ 生产者。
func (s *ConversationService) SetFeedbackMQProducer(producer mq.Producer, enabled bool) {
	s.feedbackMQProducer = producer
	s.feedbackMQEnabled = enabled
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
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("会话名称不能为空")
	}
	maxChars := s.titleMaxChars
	if maxChars <= 0 {
		maxChars = 30
	}
	if len([]rune(title)) > maxChars {
		return fmt.Errorf("会话名称长度不能超过%d个字符", maxChars)
	}
	return s.convRepo.UpdateTitle(ctx, conversationID, userID, title)
}

// DeleteConversation 删除会话。
func (s *ConversationService) DeleteConversation(ctx context.Context, conversationID, userID string) error {
	if err := s.convRepo.SoftDelete(ctx, conversationID, userID); err != nil {
		return err
	}
	if s.msgRepo != nil {
		if err := s.msgRepo.SoftDeleteByConversationIDAndUserID(ctx, conversationID, userID); err != nil {
			return err
		}
	}
	if s.fbRepo != nil {
		if err := s.fbRepo.SoftDeleteByConversationIDAndUserID(ctx, conversationID, userID); err != nil {
			return err
		}
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
	if strings.TrimSpace(req.MessageID) == "" {
		return fmt.Errorf("消息ID不能为空")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return fmt.Errorf("用户ID不能为空")
	}
	if req.Vote != 1 && req.Vote != -1 {
		return fmt.Errorf("反馈值必须为 1 或 -1")
	}
	if s.publishFeedbackEvent(ctx, MessageFeedbackEvent{
		MessageID:  req.MessageID,
		UserID:     req.UserID,
		Vote:       req.Vote,
		Reason:     req.Reason,
		Comment:    req.Comment,
		SubmitTime: time.Now().UnixMilli(),
	}, "消息反馈") {
		return nil
	}
	return s.createFeedbackSync(ctx, req)
}

// DeleteFeedback 删除消息反馈。
func (s *ConversationService) DeleteFeedback(ctx context.Context, messageID, userID string) error {
	if s.publishFeedbackEvent(ctx, MessageFeedbackEvent{
		MessageID:  messageID,
		UserID:     userID,
		Cancelled:  true,
		SubmitTime: time.Now().UnixMilli(),
	}, "取消消息反馈") {
		return nil
	}
	if s.fbRepo == nil {
		return nil
	}
	msg, err := s.loadAssistantMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}
	return s.upsertCancelledFeedback(ctx, msg, userID, time.Now())
}

// SubmitFeedbackByEvent 异步处理反馈事件。
func (s *ConversationService) SubmitFeedbackByEvent(ctx context.Context, event MessageFeedbackEvent) error {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" {
		return fmt.Errorf("消息ID不能为空")
	}
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return fmt.Errorf("用户ID不能为空")
	}
	msg, err := s.loadAssistantMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}
	if event.Cancelled {
		if s.fbRepo == nil {
			return nil
		}
		return s.upsertCancelledFeedback(ctx, msg, userID, feedbackEventTime(event.SubmitTime))
	}
	if event.Vote != 1 && event.Vote != -1 {
		return fmt.Errorf("反馈值必须为 1 或 -1")
	}
	if s.fbRepo == nil {
		return fmt.Errorf("feedback repo is nil")
	}
	fb := &model.MessageFeedback{
		MessageID:      messageID,
		ConversationID: msg.ConversationID,
		UserID:         userID,
		Vote:           event.Vote,
		Reason:         event.Reason,
		Comment:        event.Comment,
	}
	return s.upsertActiveFeedback(ctx, fb, feedbackEventTime(event.SubmitTime))
}

func (s *ConversationService) createFeedbackSync(ctx context.Context, req struct {
	MessageID      string
	ConversationID string
	UserID         string
	Vote           int16
	Reason         string
	Comment        string
}) error {
	msg, err := s.loadAssistantMessage(ctx, req.MessageID, req.UserID)
	if err != nil {
		return err
	}
	if s.fbRepo == nil {
		return fmt.Errorf("feedback repo is nil")
	}
	fb := &model.MessageFeedback{
		MessageID:      req.MessageID,
		ConversationID: msg.ConversationID,
		UserID:         req.UserID,
		Vote:           req.Vote,
		Reason:         req.Reason,
		Comment:        req.Comment,
	}
	return s.upsertActiveFeedback(ctx, fb, time.Now())
}

func (s *ConversationService) upsertActiveFeedback(ctx context.Context, fb *model.MessageFeedback, ts time.Time) error {
	if s.fbRepo == nil {
		return fmt.Errorf("feedback repo is nil")
	}
	fb.CreateTime = ts
	fb.UpdateTime = ts
	return s.fbRepo.UpsertActive(ctx, fb)
}

func (s *ConversationService) upsertCancelledFeedback(ctx context.Context, msg *model.Message, userID string, ts time.Time) error {
	if s.fbRepo == nil {
		return nil
	}
	fb := &model.MessageFeedback{
		MessageID:      msg.ID,
		ConversationID: msg.ConversationID,
		UserID:         userID,
	}
	fb.CreateTime = ts
	fb.UpdateTime = ts
	return s.fbRepo.UpsertCancelled(ctx, fb)
}

func feedbackEventTime(submitTime int64) time.Time {
	if submitTime <= 0 {
		return time.Now()
	}
	return time.UnixMilli(submitTime)
}

func (s *ConversationService) loadAssistantMessage(ctx context.Context, messageID, userID string) (*model.Message, error) {
	if s.msgRepo == nil {
		return nil, fmt.Errorf("message repo is nil")
	}
	msg, err := s.msgRepo.FindByIDAndUserID(ctx, messageID, userID)
	if err != nil {
		return nil, fmt.Errorf("消息不存在: %w", err)
	}
	if !strings.EqualFold(msg.Role, string(chat.RoleAssistant)) {
		return nil, fmt.Errorf("仅支持对助手消息反馈")
	}
	return msg, nil
}

func (s *ConversationService) publishFeedbackEvent(ctx context.Context, event MessageFeedbackEvent, bizDesc string) bool {
	if !s.feedbackMQEnabled || s.feedbackMQProducer == nil {
		return false
	}
	body, err := json.Marshal(event)
	if err != nil {
		slog.Warn("failed to marshal feedback event, fallback to sync", "err", err)
		return false
	}
	if _, err := s.feedbackMQProducer.Send(ctx, mq.Message{
		Topic:   MessageFeedbackTopic,
		Keys:    event.UserID + ":" + event.MessageID,
		BizDesc: bizDesc,
		Body:    body,
	}); err != nil {
		slog.Warn("failed to send feedback event, fallback to sync", "err", err)
		return false
	}
	return true
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
	historyKeepTurns  int
	summaryLock       *redislock.RedisLock
	summaryLockTTL    time.Duration
	summaryTaskRunner func(func())
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
	historyKeepTurns ...int,
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
	keepTurns := 0
	if len(historyKeepTurns) > 0 && historyKeepTurns[0] > 0 {
		keepTurns = historyKeepTurns[0]
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
		historyKeepTurns:  keepTurns,
		summaryLockTTL:    30 * time.Second,
	}
}

// SetSummaryLock 设置摘要压缩锁。
func (s *DBMemoryStore) SetSummaryLock(summaryLock *redislock.RedisLock, ttl time.Duration) {
	s.summaryLock = summaryLock
	if ttl > 0 {
		s.summaryLockTTL = ttl
	}
}

// SetSummaryTaskRunner 设置摘要压缩执行器。
func (s *DBMemoryStore) SetSummaryTaskRunner(runner func(func())) {
	s.summaryTaskRunner = runner
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
	historyLimit := 100
	if s.historyKeepTurns > 0 {
		historyLimit = s.historyKeepTurns * 2
	}
	msgs, err := s.msgRepo.LoadLatestHistory(ctx, conversationID, conv.UserID, historyLimit)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return []chat.Message{}, nil
	}
	result := make([]chat.Message, 0, len(msgs)+1)
	if summary := s.loadLatestSummary(ctx, conversationID, conv.UserID); summary != nil && summary.Content != "" {
		result = append(result, chat.NewSystemMessage(s.decorateSummary(summary.Content)))
	}
	for _, m := range normalizeMemoryMessages(msgs) {
		result = append(result, chat.Message{
			Role:             chat.Role(m.Role),
			Content:          m.Content,
			ThinkingContent:  m.ThinkingContent,
			ThinkingDuration: m.ThinkingDuration,
		})
	}
	return result, nil
}

func normalizeMemoryMessages(msgs []model.Message) []model.Message {
	if len(msgs) == 0 {
		return nil
	}
	filtered := make([]model.Message, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if !strings.EqualFold(msg.Role, string(chat.RoleUser)) && !strings.EqualFold(msg.Role, string(chat.RoleAssistant)) {
			continue
		}
		filtered = append(filtered, msg)
	}
	start := 0
	for start < len(filtered) && strings.EqualFold(filtered[start].Role, string(chat.RoleAssistant)) {
		start++
	}
	if start >= len(filtered) {
		return nil
	}
	return filtered[start:]
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

	taskCtx := context.WithoutCancel(ctx)
	runner := s.summaryTaskRunner
	if runner == nil {
		runner = func(fn func()) {
			fn()
		}
	}
	runner(func() {
		if err := s.runSummaryCompression(taskCtx, conversationID, conv); err != nil {
			slog.Warn("summary compression failed", "conversationId", conversationID, "err", err)
		}
	})
}

func (s *DBMemoryStore) loadHistoryForSummary(ctx context.Context, conversationID string, conv *model.Conversation) ([]model.Message, string, string, bool) {
	if conv == nil {
		return nil, "", "", false
	}
	if s.historyKeepTurns > 0 {
		return s.loadWindowedHistoryForSummary(ctx, conversationID, conv.UserID)
	}

	var previous string
	var history []model.Message
	var err error
	if latest := s.loadLatestSummary(ctx, conversationID, conv.UserID); latest != nil {
		previous = latest.Content
		history, err = s.msgRepo.LoadHistorySince(ctx, conversationID, conv.UserID, latest.LastMessageID)
	} else {
		history, err = s.msgRepo.LoadHistory(ctx, conversationID, conv.UserID, 0)
	}
	if err != nil {
		slog.Warn("load conversation history for summary failed", "conversationId", conversationID, "err", err)
		return nil, "", "", false
	}
	lastMessageID := resolveLastMessageID(history)
	return history, previous, lastMessageID, lastMessageID != ""
}

func (s *DBMemoryStore) runSummaryCompression(ctx context.Context, conversationID string, conv *model.Conversation) error {
	if s.summaryLock != nil {
		lockKey := "ragent:memory:summary:" + strings.TrimSpace(conv.UserID) + ":" + strings.TrimSpace(conversationID)
		ttl := s.summaryLockTTL
		if ttl <= 0 {
			ttl = 30 * time.Second
		}
		return s.summaryLock.RunWithLock(ctx, lockKey, ttl, func() error {
			return s.doSummaryCompression(ctx, conversationID, conv)
		})
	}
	return s.doSummaryCompression(ctx, conversationID, conv)
}

func (s *DBMemoryStore) doSummaryCompression(ctx context.Context, conversationID string, conv *model.Conversation) error {
	history, previous, lastMessageID, ok := s.loadHistoryForSummary(ctx, conversationID, conv)
	if !ok || len(history) == 0 {
		return nil
	}

	summary, err := s.summaryGenerator.Generate(ctx, toChatMessages(history), previous, s.summaryMaxChars)
	if err != nil {
		return fmt.Errorf("generate conversation summary: %w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil
	}
	record := &model.ConversationSummary{
		ConversationID: conversationID,
		UserID:         conv.UserID,
		LastMessageID:  lastMessageID,
		Content:        summary,
	}
	record.CreateTime = time.Now()
	record.UpdateTime = time.Now()
	if err := s.summaryRepo.Create(ctx, record); err != nil {
		return fmt.Errorf("save conversation summary: %w", err)
	}
	return nil
}

func (s *DBMemoryStore) loadWindowedHistoryForSummary(ctx context.Context, conversationID, userID string) ([]model.Message, string, string, bool) {
	latest := s.loadLatestSummary(ctx, conversationID, userID)
	latestUserTurns, err := s.msgRepo.ListLatestUserOnlyMessages(ctx, conversationID, userID, s.historyKeepTurns)
	if err != nil {
		slog.Warn("load latest user turns for summary failed", "conversationId", conversationID, "err", err)
		return nil, "", "", false
	}
	if len(latestUserTurns) == 0 {
		return nil, "", "", false
	}

	historyStartID := latestUserTurns[len(latestUserTurns)-1].ID
	if historyStartID == "" {
		return nil, "", "", false
	}

	var previous string
	var afterID string
	if latest != nil {
		previous = latest.Content
		afterID = s.resolveSummaryStartID(ctx, conversationID, userID, latest)
	}
	if afterID != "" && compareMessageID(afterID, historyStartID) >= 0 {
		return nil, previous, "", false
	}

	summaryCutoffID := latestUserTurns[(len(latestUserTurns)-1)/2].ID
	if summaryCutoffID == "" {
		return nil, previous, "", false
	}

	history, err := s.msgRepo.ListMessagesBetweenIDs(ctx, conversationID, userID, afterID, summaryCutoffID)
	if err != nil {
		slog.Warn("load windowed history for summary failed", "conversationId", conversationID, "err", err)
		return nil, previous, "", false
	}
	lastMessageID := resolveLastMessageID(history)
	return history, previous, lastMessageID, lastMessageID != ""
}

func (s *DBMemoryStore) resolveSummaryStartID(ctx context.Context, conversationID, userID string, summary *model.ConversationSummary) string {
	if summary == nil {
		return ""
	}
	if summary.LastMessageID != "" {
		return summary.LastMessageID
	}
	at := summary.UpdateTime
	if at.IsZero() {
		at = summary.CreateTime
	}
	id, err := s.msgRepo.FindMaxMessageIDAtOrBefore(ctx, conversationID, userID, at)
	if err != nil {
		slog.Warn("resolve summary start id failed", "conversationId", conversationID, "err", err)
		return ""
	}
	return id
}

func resolveLastMessageID(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ID != "" {
			return messages[i].ID
		}
	}
	return ""
}

func compareMessageID(left, right string) int {
	leftNum, leftErr := strconv.ParseInt(left, 10, 64)
	rightNum, rightErr := strconv.ParseInt(right, 10, 64)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNum > rightNum:
			return 1
		case leftNum < rightNum:
			return -1
		default:
			return 0
		}
	}
	return strings.Compare(left, right)
}

func (s *DBMemoryStore) decorateSummary(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	loader := rag.NewPromptLoader("")
	wrapped, err := loader.Render("conversation_summary_wrapper.txt", map[string]any{
		"Content": content,
	})
	if err != nil {
		slog.Warn("render conversation summary wrapper failed", "err", err)
		return "历史摘要：" + content
	}
	return wrapped
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
