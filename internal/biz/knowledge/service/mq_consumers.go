package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/mq"

	"gorm.io/gorm"
)

// RegisterKnowledgeDocumentChunkConsumer 注册文档分块消费者。
func RegisterKnowledgeDocumentChunkConsumer(consumer mq.Consumer, svc *DocumentService) error {
	if consumer == nil {
		return fmt.Errorf("mq consumer is nil")
	}
	if svc == nil {
		return fmt.Errorf("document service is nil")
	}
	return consumer.Subscribe(KnowledgeDocumentChunkTopic, KnowledgeDocumentChunkConsumerGroup, func(ctx context.Context, msg mq.Message) error {
		var event KnowledgeDocumentChunkEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			return fmt.Errorf("decode knowledge document chunk event: %w", err)
		}
		if strings.TrimSpace(event.DocID) == "" {
			return fmt.Errorf("knowledge document chunk event docId is empty")
		}
		opCtx := ctx
		if strings.TrimSpace(event.Operator) != "" {
			opCtx = appctx.WithUser(ctx, &appctx.LoginUser{Username: event.Operator})
		}
		return svc.executeChunk(opCtx, event.DocID)
	})
}

// RegisterKnowledgeBaseCleanupConsumer 注册知识库清理消费者。
func RegisterKnowledgeBaseCleanupConsumer(consumer mq.Consumer, svc *KnowledgeBaseService) error {
	if consumer == nil {
		return fmt.Errorf("mq consumer is nil")
	}
	if svc == nil {
		return fmt.Errorf("knowledge base service is nil")
	}
	return consumer.Subscribe(KnowledgeBaseCleanupTopic, KnowledgeBaseCleanupConsumerGroup, func(ctx context.Context, msg mq.Message) error {
		var event KnowledgeBaseCleanupEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			return fmt.Errorf("decode knowledge base cleanup event: %w", err)
		}
		return svc.cleanupPhysicalResources(ctx, event)
	})
}

// CheckChunkTransaction 回查文档分块事务是否可提交。
func (s *DocumentService) CheckChunkTransaction(ctx context.Context, msg mq.Message) (bool, error) {
	if s == nil || s.docRepo == nil {
		return false, fmt.Errorf("document repo is nil")
	}
	var event KnowledgeDocumentChunkEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return false, fmt.Errorf("decode knowledge document chunk event: %w", err)
	}
	if strings.TrimSpace(event.DocID) == "" {
		return false, fmt.Errorf("knowledge document chunk event docId is empty")
	}
	doc, err := s.docRepo.FindByID(ctx, event.DocID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load document for transaction check: %w", err)
	}
	return strings.EqualFold(strings.TrimSpace(doc.Status), "running"), nil
}

// CheckCleanupTransaction 回查知识库删除清理事务是否可提交。
func (s *KnowledgeBaseService) CheckCleanupTransaction(ctx context.Context, msg mq.Message) (bool, error) {
	if s == nil || s.repo == nil {
		return false, fmt.Errorf("knowledge base repo is nil")
	}
	var event KnowledgeBaseCleanupEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return false, fmt.Errorf("decode knowledge base cleanup event: %w", err)
	}
	if strings.TrimSpace(event.KBID) == "" {
		return false, fmt.Errorf("knowledge base cleanup event kbId is empty")
	}
	_, err := s.repo.FindByID(ctx, event.KBID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("load knowledge base for transaction check: %w", err)
	}
	return false, nil
}
