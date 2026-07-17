package repo

import (
	"context"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// KnowledgeDocumentRepo 知识库文档数据访问层。
type KnowledgeDocumentRepo struct {
	gdb *gorm.DB
}

// NewKnowledgeDocumentRepo 创建 KnowledgeDocumentRepo。
func NewKnowledgeDocumentRepo(gdb *gorm.DB) *KnowledgeDocumentRepo {
	return &KnowledgeDocumentRepo{gdb: gdb}
}

// Create 创建文档记录。
func (r *KnowledgeDocumentRepo) Create(ctx context.Context, doc *model.KnowledgeDocument) error {
	return r.gdb.WithContext(ctx).Create(doc).Error
}

// FindByID 根据 ID 查询文档（仅未删除记录）。
func (r *KnowledgeDocumentRepo) FindByID(ctx context.Context, id string) (*model.KnowledgeDocument, error) {
	var doc model.KnowledgeDocument
	err := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&doc).Error
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// Update 更新文档字段。
func (r *KnowledgeDocumentRepo) Update(ctx context.Context, doc *model.KnowledgeDocument) error {
	result := r.gdb.WithContext(ctx).Model(doc).
		Select("doc_name", "enabled", "chunk_strategy", "chunk_config", "updated_by", "update_time").
		Updates(doc)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete 软删除文档。
func (r *KnowledgeDocumentRepo) SoftDelete(ctx context.Context, id string) error {
	var doc model.KnowledgeDocument
	doc.ID = id
	return db.SoftDelete(r.gdb.WithContext(ctx), &doc)
}

// ListByKB 按知识库 ID 分页查询文档列表。
func (r *KnowledgeDocumentRepo) ListByKB(ctx context.Context, kbID string, page, size int) ([]model.KnowledgeDocument, int64, error) {
	var (
		records []model.KnowledgeDocument
		total   int64
	)
	query := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeDocument{}).Where("kb_id = ?", kbID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("create_time DESC").Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// UpdateStatus 更新文档状态。
func (r *KnowledgeDocumentRepo) UpdateStatus(ctx context.Context, id, status string) error {
	return r.gdb.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ?", id).
		Update("status", status).Error
}

// UpdateChunkCount 更新分块数量。
func (r *KnowledgeDocumentRepo) UpdateChunkCount(ctx context.Context, id string, count int) error {
	return r.gdb.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"chunk_count": count,
			"status":      "success",
		}).Error
}

// SearchDocs 按文档名称模糊搜索。
func (r *KnowledgeDocumentRepo) SearchDocs(ctx context.Context, keyword string, page, size int) ([]model.KnowledgeDocument, int64, error) {
	var (
		records []model.KnowledgeDocument
		total   int64
	)
	countQuery := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeDocument{})
	if keyword != "" {
		countQuery = countQuery.Where("LOWER(doc_name) LIKE LOWER(?)", "%"+keyword+"%")
	}
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	listQuery := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeDocument{})
	if keyword != "" {
		listQuery = listQuery.Where("LOWER(doc_name) LIKE LOWER(?)", "%"+keyword+"%")
	}
	if err := listQuery.Scopes(db.Paginate(page, size)).Order("update_time DESC").Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}
