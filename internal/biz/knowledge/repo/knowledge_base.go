package repo

import (
	"context"
	"fmt"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// KnowledgeBaseRepo 知识库数据访问层。
type KnowledgeBaseRepo struct {
	gdb *gorm.DB
}

// NewKnowledgeBaseRepo 创建 KnowledgeBaseRepo。
func NewKnowledgeBaseRepo(gdb *gorm.DB) *KnowledgeBaseRepo {
	return &KnowledgeBaseRepo{gdb: gdb}
}

// Create 创建知识库。
func (r *KnowledgeBaseRepo) Create(ctx context.Context, kb *model.KnowledgeBase) error {
	return r.gdb.WithContext(ctx).Create(kb).Error
}

// FindByID 根据 ID 查询知识库（仅未删除记录）。
func (r *KnowledgeBaseRepo) FindByID(ctx context.Context, id string) (*model.KnowledgeBase, error) {
	var kb model.KnowledgeBase
	err := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&kb).Error
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

// Update 更新知识库字段（name、embedding_model、collection_name、updated_by、update_time）。
func (r *KnowledgeBaseRepo) Update(ctx context.Context, kb *model.KnowledgeBase) error {
	result := r.gdb.WithContext(ctx).Model(kb).
		Select("name", "embedding_model", "collection_name", "updated_by", "update_time").
		Updates(kb)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete 软删除知识库（deleted = 1）。
func (r *KnowledgeBaseRepo) SoftDelete(ctx context.Context, id string) error {
	var kb model.KnowledgeBase
	kb.ID = id
	return db.SoftDelete(r.gdb.WithContext(ctx), &kb)
}

// List 分页查询知识库列表（仅未删除记录）。
func (r *KnowledgeBaseRepo) List(ctx context.Context, page, size int) ([]model.KnowledgeBase, int64, error) {
	var (
		records []model.KnowledgeBase
		total   int64
	)
	query := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeBase{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count knowledge bases: %w", err)
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("create_time DESC").Find(&records).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list knowledge bases: %w", err)
	}
	return records, total, nil
}

// ExistsByName 检查名称是否已存在（排除指定 ID 用于更新时的唯一性校验）。
func (r *KnowledgeBaseRepo) ExistsByName(ctx context.Context, name string, excludeID string) (bool, error) {
	var count int64
	query := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeBase{}).Where("name = ?", name)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ExistsByCollectionName 检查 collection_name 是否已存在（排除指定 ID）。
func (r *KnowledgeBaseRepo) ExistsByCollectionName(ctx context.Context, collectionName string, excludeID string) (bool, error) {
	var count int64
	query := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeBase{}).Where("collection_name = ?", collectionName)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
