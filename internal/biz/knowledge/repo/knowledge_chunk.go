package repo

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeChunkRepo 知识库分块数据访问层。
type KnowledgeChunkRepo struct {
	gdb *gorm.DB
}

// NewKnowledgeChunkRepo 创建 KnowledgeChunkRepo。
func NewKnowledgeChunkRepo(gdb *gorm.DB) *KnowledgeChunkRepo {
	return &KnowledgeChunkRepo{gdb: gdb}
}

// Create 创建单个分块。
func (r *KnowledgeChunkRepo) Create(ctx context.Context, chunk *model.KnowledgeChunk) error {
	return r.gdb.WithContext(ctx).Create(chunk).Error
}

// BatchCreate 批量创建分块。
func (r *KnowledgeChunkRepo) BatchCreate(ctx context.Context, chunks []*model.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return r.gdb.WithContext(ctx).Create(chunks).Error
}

// FindByID 根据 ID 查询分块（仅未删除记录）。
func (r *KnowledgeChunkRepo) FindByID(ctx context.Context, id string) (*model.KnowledgeChunk, error) {
	var chunk model.KnowledgeChunk
	err := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&chunk).Error
	if err != nil {
		return nil, err
	}
	return &chunk, nil
}

// ListByDoc 按文档 ID 分页查询分块列表。
func (r *KnowledgeChunkRepo) ListByDoc(ctx context.Context, docID string, page, size int, enabled *int16) ([]model.KnowledgeChunk, int64, error) {
	var (
		records []model.KnowledgeChunk
		total   int64
	)
	query := r.gdb.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.KnowledgeChunk{}).Where("doc_id = ?", docID)
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("chunk_index ASC").Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// FindEditedDocIDs 查询存在手工编辑分块的文档 ID。
func (r *KnowledgeChunkRepo) FindEditedDocIDs(ctx context.Context, docIDs []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(docIDs) == 0 {
		return result, nil
	}
	var rows []model.KnowledgeChunk
	err := r.gdb.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Select("doc_id", "create_time", "update_time").
		Where("deleted = 0 AND doc_id IN ?", docIDs).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("find edited doc ids: %w", err)
	}
	for _, row := range rows {
		if row.UpdateTime.After(row.CreateTime.Add(time.Second)) {
			result[row.DocID] = true
		}
	}
	return result, nil
}

// Update 更新分块内容。
func (r *KnowledgeChunkRepo) Update(ctx context.Context, chunk *model.KnowledgeChunk) error {
	result := r.gdb.WithContext(ctx).Model(chunk).
		Select("content", "content_hash", "char_count", "token_count", "source_version", "source_hash", "chunk_config_hash", "block_index", "block_type", "source_start_offset", "source_end_offset", "core_start_offset", "core_end_offset", "updated_by", "update_time").
		Updates(chunk)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete 软删除分块。
func (r *KnowledgeChunkRepo) SoftDelete(ctx context.Context, id string) error {
	var chunk model.KnowledgeChunk
	chunk.ID = id
	return db.SoftDelete(r.gdb.WithContext(ctx), &chunk)
}

// DeleteByDocID 软删除指定文档的所有分块（用于重新入库前清理）。
func (r *KnowledgeChunkRepo) DeleteByDocID(ctx context.Context, docID string) error {
	return r.gdb.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Where("doc_id = ? AND deleted = 0", docID).
		Updates(map[string]interface{}{
			"deleted":     1,
			"update_time": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error
}

// UpdateEnabled 切换单个分块的启用状态。
func (r *KnowledgeChunkRepo) UpdateEnabled(ctx context.Context, id string, enabled int16) error {
	result := r.gdb.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Where("id = ? AND deleted = 0", id).
		Updates(map[string]interface{}{
			"enabled":     enabled,
			"update_time": gorm.Expr("CURRENT_TIMESTAMP"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// BatchUpdateEnabled 批量切换分块启用状态。
func (r *KnowledgeChunkRepo) BatchUpdateEnabled(ctx context.Context, docID string, ids []string, enabled int16) error {
	if len(ids) == 0 {
		return nil
	}
	return r.gdb.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Where("doc_id = ? AND id IN ? AND deleted = 0", docID, ids).
		Updates(map[string]interface{}{
			"enabled":     enabled,
			"update_time": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error
}

// UpsertChunks 批量 upsert 分块（用于重新入库场景，按 doc_id + chunk_index 冲突时更新）。
func (r *KnowledgeChunkRepo) UpsertChunks(ctx context.Context, chunks []*model.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return r.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "doc_id"}, {Name: "chunk_index"}, {Name: "deleted"}},
		DoUpdates: clause.AssignmentColumns([]string{"content", "content_hash", "char_count", "token_count", "source_version", "source_hash", "chunk_config_hash", "block_index", "block_type", "source_start_offset", "source_end_offset", "core_start_offset", "core_end_offset", "enabled", "updated_by", "update_time", "deleted"}),
	}).Create(chunks).Error
}
