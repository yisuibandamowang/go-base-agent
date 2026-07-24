package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/mq"

	"gorm.io/gorm"
)

// KnowledgeBaseService 知识库业务逻辑层。
type KnowledgeBaseService struct {
	repo          *repo.KnowledgeBaseRepo
	auditRecorder *auditService.BizChangeLogService
	vecCleaner    vectorSpaceCleaner
	fileDeleter   knowledgeSpaceDeleter
	mqProducer    mq.Producer
	mqEnabled     bool
}

type vectorSpaceCleaner interface {
	EnsureVectorSpace(ctx context.Context, spec rag.VectorSpaceSpec) error
	DropVectorSpace(ctx context.Context, collectionName string) error
}

type knowledgeSpaceDeleter interface {
	DeleteKnowledgeSpace(ctx context.Context, collectionName string) error
}

// NewKnowledgeBaseService 创建 KnowledgeBaseService。
func NewKnowledgeBaseService(repo *repo.KnowledgeBaseRepo) *KnowledgeBaseService {
	return &KnowledgeBaseService{repo: repo}
}

// SetVectorStore 设置向量空间清理器。
func (s *KnowledgeBaseService) SetVectorStore(cleaner vectorSpaceCleaner) {
	s.vecCleaner = cleaner
}

// SetFileDeleter 设置知识空间文件删除器。
func (s *KnowledgeBaseService) SetFileDeleter(deleter knowledgeSpaceDeleter) {
	s.fileDeleter = deleter
}

// SetMQProducer 设置知识库删除 MQ 生产者。
func (s *KnowledgeBaseService) SetMQProducer(producer mq.Producer, enabled bool) {
	s.mqProducer = producer
	s.mqEnabled = enabled
}

// SetAuditRecorder 设置审计日志记录器。
func (s *KnowledgeBaseService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// Create 创建知识库，校验名称和 collection 唯一性。
func (s *KnowledgeBaseService) Create(ctx context.Context, req dto.CreateKnowledgeBaseReq, userID string) (*dto.KnowledgeBaseResp, error) {
	if err := s.checkNameUnique(ctx, req.Name, ""); err != nil {
		return nil, err
	}
	if err := s.checkCollectionUnique(ctx, req.CollectionName, ""); err != nil {
		return nil, err
	}

	kb := &model.KnowledgeBase{
		Name:           req.Name,
		EmbeddingModel: req.EmbeddingModel,
		CollectionName: req.CollectionName,
		CreatedBy:      userID,
	}
	kb.CreateTime = time.Now()
	kb.UpdateTime = time.Now()

	if err := s.repo.Create(ctx, kb); err != nil {
		return nil, fmt.Errorf("failed to create knowledge base: %w", err)
	}
	if s.vecCleaner != nil {
		if err := s.vecCleaner.EnsureVectorSpace(ctx, rag.VectorSpaceSpec{
			SpaceID: rag.VectorSpaceID{Name: req.CollectionName},
			Remark:  req.Name,
		}); err != nil {
			return nil, fmt.Errorf("failed to ensure vector space: %w", err)
		}
	}
	resp := toResp(kb)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeKnowledgeBase,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建知识库：" + resp.Name,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// Get 查询知识库详情。
func (s *KnowledgeBaseService) Get(ctx context.Context, id string) (*dto.KnowledgeBaseResp, error) {
	kb, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to get knowledge base: %w", err)
	}
	return toResp(kb), nil
}

// Update 更新知识库，校验唯一性。
func (s *KnowledgeBaseService) Update(ctx context.Context, id string, req dto.UpdateKnowledgeBaseReq, userID string) (*dto.KnowledgeBaseResp, error) {
	kb, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to find knowledge base for update: %w", err)
	}
	before := toResp(kb)

	if err := s.checkNameUnique(ctx, req.Name, id); err != nil {
		return nil, err
	}
	if err := s.checkCollectionUnique(ctx, req.CollectionName, id); err != nil {
		return nil, err
	}

	kb.Name = req.Name
	kb.EmbeddingModel = req.EmbeddingModel
	kb.CollectionName = req.CollectionName
	kb.UpdatedBy = userID
	kb.UpdateTime = time.Now()

	if err := s.repo.Update(ctx, kb); err != nil {
		return nil, fmt.Errorf("failed to update knowledge base: %w", err)
	}
	resp := toResp(kb)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeBase,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新知识库：" + resp.Name,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// Delete 软删除知识库。
func (s *KnowledgeBaseService) Delete(ctx context.Context, id string) error {
	kb, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return fmt.Errorf("failed to find knowledge base for delete: %w", err)
	}
	before := toResp(kb)
	docCount, err := s.repo.CountDocumentsByKBID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to count knowledge base documents: %w", err)
	}
	if docCount > 0 {
		return fmt.Errorf("当前知识库下还有文档，请删除文档")
	}
	if s.mqEnabled && s.mqProducer != nil {
		operator := "system"
		if user := appctx.User(ctx); user != nil && strings.TrimSpace(user.Username) != "" {
			operator = user.Username
		}
		event := KnowledgeBaseCleanupEvent{
			KBID:           id,
			CollectionName: kb.CollectionName,
			Operator:       operator,
		}
		body, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal knowledge base cleanup event: %w", err)
		}
		if _, err := s.mqProducer.SendInTransaction(ctx, mq.Message{
			Topic:   KnowledgeBaseCleanupTopic,
			Keys:    id,
			BizDesc: "知识库删除清理",
			Body:    body,
		}, func(txCtx context.Context, msg mq.Message) error {
			if err := s.repo.SoftDelete(txCtx, id); err != nil {
				return fmt.Errorf("failed to soft delete knowledge base: %w", err)
			}
			return nil
		}); err != nil {
			if restoreErr := s.repo.Restore(ctx, id); restoreErr != nil {
				return fmt.Errorf("failed to send knowledge base cleanup event: %w; rollback delete failed: %v", err, restoreErr)
			}
			return fmt.Errorf("failed to send knowledge base cleanup event: %w", err)
		}
	} else {
		if err := s.repo.SoftDelete(ctx, id); err != nil {
			return err
		}
		if err := s.cleanupPhysicalResources(ctx, KnowledgeBaseCleanupEvent{
			KBID:           id,
			CollectionName: kb.CollectionName,
			Operator:       "system",
		}); err != nil {
			return err
		}
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeBase,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除知识库：" + before.Name,
		BeforeSnapshot: before,
	})
	return nil
}

func (s *KnowledgeBaseService) cleanupPhysicalResources(ctx context.Context, event KnowledgeBaseCleanupEvent) error {
	if strings.TrimSpace(event.CollectionName) == "" {
		return nil
	}
	var firstErr error
	if s.vecCleaner != nil {
		if err := s.vecCleaner.DropVectorSpace(ctx, event.CollectionName); err != nil {
			firstErr = fmt.Errorf("failed to drop vector space: %w", err)
		}
	}
	if s.fileDeleter != nil {
		if err := s.fileDeleter.DeleteKnowledgeSpace(ctx, event.CollectionName); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to delete knowledge space files: %w", err)
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

// List 分页查询知识库列表。
func (s *KnowledgeBaseService) List(ctx context.Context, page, size int) ([]dto.KnowledgeBaseResp, int64, error) {
	records, total, err := s.repo.List(ctx, page, size)
	if err != nil {
		return nil, 0, err
	}
	resps := make([]dto.KnowledgeBaseResp, 0, len(records))
	for i := range records {
		resps = append(resps, *toResp(&records[i]))
	}
	return resps, total, nil
}

func (s *KnowledgeBaseService) checkNameUnique(ctx context.Context, name string, excludeID string) error {
	exists, err := s.repo.ExistsByName(ctx, name, excludeID)
	if err != nil {
		return fmt.Errorf("failed to check name uniqueness: %w", err)
	}
	if exists {
		return fmt.Errorf("知识库名称已存在")
	}
	return nil
}

func (s *KnowledgeBaseService) checkCollectionUnique(ctx context.Context, collectionName string, excludeID string) error {
	exists, err := s.repo.ExistsByCollectionName(ctx, collectionName, excludeID)
	if err != nil {
		return fmt.Errorf("failed to check collection uniqueness: %w", err)
	}
	if exists {
		return fmt.Errorf("collection 名称已存在")
	}
	return nil
}

func toResp(kb *model.KnowledgeBase) *dto.KnowledgeBaseResp {
	return &dto.KnowledgeBaseResp{
		ID:             kb.ID,
		Name:           kb.Name,
		EmbeddingModel: kb.EmbeddingModel,
		CollectionName: kb.CollectionName,
		CreatedBy:      kb.CreatedBy,
		UpdatedBy:      kb.UpdatedBy,
		CreateTime:     kb.CreateTime.Format(time.RFC3339),
		UpdateTime:     kb.UpdateTime.Format(time.RFC3339),
	}
}

func (s *KnowledgeBaseService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}
