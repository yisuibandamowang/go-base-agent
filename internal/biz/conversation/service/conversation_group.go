package service

import (
	"context"
	"errors"
	"time"

	"go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"

	"gorm.io/gorm"
)

// ConversationGroupService provides grouped conversation query helpers.
type ConversationGroupService struct {
	convRepo    *repo.ConversationRepo
	msgRepo     *repo.MessageRepo
	summaryRepo *repo.ConversationSummaryRepo
}

// NewConversationGroupService creates a ConversationGroupService.
func NewConversationGroupService(convRepo *repo.ConversationRepo, msgRepo *repo.MessageRepo, summaryRepo *repo.ConversationSummaryRepo) *ConversationGroupService {
	return &ConversationGroupService{
		convRepo:    convRepo,
		msgRepo:     msgRepo,
		summaryRepo: summaryRepo,
	}
}

// ListLatestUserOnlyMessages returns the latest user messages in descending time order.
func (s *ConversationGroupService) ListLatestUserOnlyMessages(ctx context.Context, conversationID, userID string, limit int) ([]model.Message, error) {
	if s == nil || s.msgRepo == nil {
		return []model.Message{}, nil
	}
	return s.msgRepo.ListLatestUserOnlyMessages(ctx, conversationID, userID, limit)
}

// ListMessagesBetweenIDs returns user and assistant messages in the open ID interval.
func (s *ConversationGroupService) ListMessagesBetweenIDs(ctx context.Context, conversationID, userID, afterID, beforeID string) ([]model.Message, error) {
	if s == nil || s.msgRepo == nil {
		return []model.Message{}, nil
	}
	return s.msgRepo.ListMessagesBetweenIDs(ctx, conversationID, userID, afterID, beforeID)
}

// FindMaxMessageIDAtOrBefore returns the largest message ID at or before the given time.
func (s *ConversationGroupService) FindMaxMessageIDAtOrBefore(ctx context.Context, conversationID, userID string, at time.Time) (string, error) {
	if s == nil || s.msgRepo == nil {
		return "", nil
	}
	return s.msgRepo.FindMaxMessageIDAtOrBefore(ctx, conversationID, userID, at)
}

// CountUserMessages counts user messages in the conversation.
func (s *ConversationGroupService) CountUserMessages(ctx context.Context, conversationID, userID string) (int64, error) {
	if s == nil || s.msgRepo == nil {
		return 0, nil
	}
	return s.msgRepo.CountUserMessages(ctx, conversationID, userID)
}

// FindLatestSummary returns the latest summary for a conversation.
func (s *ConversationGroupService) FindLatestSummary(ctx context.Context, conversationID, userID string) (*model.ConversationSummary, error) {
	if s == nil || s.summaryRepo == nil {
		return nil, nil
	}
	summary, err := s.summaryRepo.FindLatestByConversationID(ctx, conversationID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return summary, err
}

// FindConversation returns a conversation by conversation ID and user ID.
func (s *ConversationGroupService) FindConversation(ctx context.Context, conversationID, userID string) (*model.Conversation, error) {
	if s == nil || s.convRepo == nil {
		return nil, nil
	}
	conv, err := s.convRepo.FindByConversationID(ctx, conversationID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return conv, err
}
