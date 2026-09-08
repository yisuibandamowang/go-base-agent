package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/idempotent"
	"go-base-agent/internal/framework/mq"

	"gorm.io/gorm"
)

// consumeGuardOptions 消费幂等守卫选项。
type consumeGuardOptions struct {
	guard *idempotent.ConsumeGuard
	ttl   time.Duration
}

// ConsumeGuardOption 消费幂等守卫选项函数。
type ConsumeGuardOption func(*consumeGuardOptions)

// WithConsumeGuard 注入三态消费幂等守卫（对齐 Java @IdempotentConsume）。
func WithConsumeGuard(guard *idempotent.ConsumeGuard) ConsumeGuardOption {
	return func(o *consumeGuardOptions) {
		o.guard = guard
	}
}

// WithConsumeKeyTimeout 设置幂等键保留时长（默认 1 小时，对齐 Java keyTimeout 默认值）。
func WithConsumeKeyTimeout(ttl time.Duration) ConsumeGuardOption {
	return func(o *consumeGuardOptions) {
		o.ttl = ttl
	}
}

// RegisterKnowledgeDocumentChunkConsumer 注册文档分块消费者。
// 分块是最重的重复消费风险点：可选接入三态消费幂等——消费中触发延迟重试、
// 已完成直接跳过、处理失败删除标记允许重投。
func RegisterKnowledgeDocumentChunkConsumer(consumer mq.Consumer, svc *DocumentService, opts ...ConsumeGuardOption) error {
	if consumer == nil {
		return fmt.Errorf("mq consumer is nil")
	}
	if svc == nil {
		return fmt.Errorf("document service is nil")
	}
	guardOpts := consumeGuardOptions{ttl: time.Hour}
	for _, opt := range opts {
		opt(&guardOpts)
	}
	return consumer.Subscribe(KnowledgeDocumentChunkTopic, KnowledgeDocumentChunkConsumerGroup, func(ctx context.Context, msg mq.Message) error {
		var event KnowledgeDocumentChunkEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			return fmt.Errorf("decode knowledge document chunk event: %w", err)
		}
		if strings.TrimSpace(event.DocID) == "" {
			return fmt.Errorf("knowledge document chunk event docId is empty")
		}
		if guardOpts.guard == nil {
			return executeChunkEvent(ctx, svc, event)
		}
		key := "knowledge:document:chunk:" + event.DocID
		if err := guardOpts.guard.TryBegin(ctx, key, guardOpts.ttl); err != nil {
			if errors.Is(err, idempotent.ErrConsumed) {
				return nil
			}
			return err
		}
		if err := executeChunkEvent(ctx, svc, event); err != nil {
			_ = guardOpts.guard.Fail(ctx, key)
			return err
		}
		return guardOpts.guard.Complete(ctx, key, guardOpts.ttl)
	})
}

func executeChunkEvent(ctx context.Context, svc *DocumentService, event KnowledgeDocumentChunkEvent) error {
	opCtx := ctx
	if strings.TrimSpace(event.Operator) != "" {
		opCtx = appctx.WithUser(ctx, &appctx.LoginUser{Username: event.Operator})
	}
	return svc.executeChunk(opCtx, event.DocID)
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
