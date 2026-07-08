package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"

	"gorm.io/gorm"
)

// KnowledgeBaseService 知识库业务逻辑层。
type KnowledgeBaseService struct {
	repo *repo.KnowledgeBaseRepo
}

// NewKnowledgeBaseService 创建 KnowledgeBaseService。
func NewKnowledgeBaseService(repo *repo.KnowledgeBaseRepo) *KnowledgeBaseService {
	return &KnowledgeBaseService{repo: repo}
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
	return toResp(kb), nil
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
	return toResp(kb), nil
}

// Delete 软删除知识库。
func (s *KnowledgeBaseService) Delete(ctx context.Context, id string) error {
	_, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return fmt.Errorf("failed to find knowledge base for delete: %w", err)
	}
	return s.repo.SoftDelete(ctx, id)
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
