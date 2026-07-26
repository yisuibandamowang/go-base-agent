package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// PipelineRepo 是摄取流水线数据访问层。
type PipelineRepo struct {
	db *gorm.DB
}

// NewPipelineRepo 创建 PipelineRepo。
func NewPipelineRepo(database *gorm.DB) *PipelineRepo {
	return &PipelineRepo{db: database}
}

// WithDB 返回绑定指定数据库会话的 PipelineRepo。
func (r *PipelineRepo) WithDB(database *gorm.DB) *PipelineRepo {
	return &PipelineRepo{db: database}
}

// Create 创建流水线。
func (r *PipelineRepo) Create(ctx context.Context, pipeline *model.IngestionPipeline) error {
	return r.db.WithContext(ctx).Create(pipeline).Error
}

// Update 更新流水线。
func (r *PipelineRepo) Update(ctx context.Context, pipeline *model.IngestionPipeline) error {
	result := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(pipeline).Where("id = ?", pipeline.ID).
		Select("name", "description", "updated_by", "update_time").
		Updates(pipeline)
	if result.Error != nil {
		return fmt.Errorf("update ingestion pipeline: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// FindByID 按 ID 查询流水线。
func (r *PipelineRepo) FindByID(ctx context.Context, id string) (*model.IngestionPipeline, error) {
	var pipeline model.IngestionPipeline
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&pipeline).Error
	if err != nil {
		return nil, fmt.Errorf("find ingestion pipeline: %w", err)
	}
	return &pipeline, nil
}

// ExistsActiveName 查询未删除流水线中是否存在同名记录。
func (r *PipelineRepo) ExistsActiveName(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(&model.IngestionPipeline{}).
		Where("name = ?", strings.TrimSpace(name)).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("count ingestion pipeline by name: %w", err)
	}
	return count > 0, nil
}

// List 分页查询流水线。
func (r *PipelineRepo) List(ctx context.Context, page, size int, keyword string) ([]model.IngestionPipeline, int64, error) {
	var (
		items []model.IngestionPipeline
		total int64
	)
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.IngestionPipeline{})
	if keyword != "" {
		query = query.Where("name LIKE ?", "%"+keyword+"%")
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count ingestion pipelines: %w", err)
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("update_time DESC").Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list ingestion pipelines: %w", err)
	}
	return items, total, nil
}

// SoftDelete 软删除流水线。
func (r *PipelineRepo) SoftDelete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.IngestionPipeline{}).Where("id = ? AND deleted = 0", id).
			Updates(map[string]any{"deleted": 1, "update_time": time.Now()}).Error; err != nil {
			return fmt.Errorf("delete ingestion pipeline: %w", err)
		}
		if err := tx.Model(&model.IngestionPipelineNode{}).Where("pipeline_id = ? AND deleted = 0", id).
			Updates(map[string]any{"deleted": 1, "update_time": time.Now()}).Error; err != nil {
			return fmt.Errorf("delete ingestion pipeline nodes: %w", err)
		}
		return nil
	})
}

// ReplaceNodes 替换流水线节点。
func (r *PipelineRepo) ReplaceNodes(ctx context.Context, pipelineID string, nodes []*model.IngestionPipelineNode) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pipeline_id = ?", pipelineID).Delete(&model.IngestionPipelineNode{}).Error; err != nil {
			return fmt.Errorf("delete old ingestion nodes: %w", err)
		}
		if len(nodes) == 0 {
			return nil
		}
		if err := tx.Create(&nodes).Error; err != nil {
			return fmt.Errorf("create ingestion nodes: %w", err)
		}
		return nil
	})
}

// ListNodes 查询流水线节点。
func (r *PipelineRepo) ListNodes(ctx context.Context, pipelineID string) ([]model.IngestionPipelineNode, error) {
	var nodes []model.IngestionPipelineNode
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("pipeline_id = ?", pipelineID).
		Order("create_time ASC, id ASC").
		Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("list ingestion pipeline nodes: %w", err)
	}
	return nodes, nil
}

// TaskRepo 是摄取任务数据访问层。
type TaskRepo struct {
	db *gorm.DB
}

// NewTaskRepo 创建 TaskRepo。
func NewTaskRepo(database *gorm.DB) *TaskRepo {
	return &TaskRepo{db: database}
}

// Create 创建任务。
func (r *TaskRepo) Create(ctx context.Context, task *model.IngestionTask) error {
	return r.db.WithContext(ctx).Create(task).Error
}

// Update 更新任务。
func (r *TaskRepo) Update(ctx context.Context, task *model.IngestionTask) error {
	return r.db.WithContext(ctx).Model(task).
		Select("status", "chunk_count", "error_message", "logs_json", "metadata_json", "completed_at", "updated_by", "update_time").
		Updates(task).Error
}

// FindByID 按 ID 查询任务。
func (r *TaskRepo) FindByID(ctx context.Context, id string) (*model.IngestionTask, error) {
	var task model.IngestionTask
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&task).Error
	if err != nil {
		return nil, fmt.Errorf("find ingestion task: %w", err)
	}
	return &task, nil
}

// List 分页查询任务。
func (r *TaskRepo) List(ctx context.Context, page, size int, status string) ([]model.IngestionTask, int64, error) {
	var (
		items []model.IngestionTask
		total int64
	)
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.IngestionTask{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count ingestion tasks: %w", err)
	}
	if err := query.Scopes(db.Paginate(page, size)).Order("create_time DESC").Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list ingestion tasks: %w", err)
	}
	return items, total, nil
}

// CreateNode 创建任务节点。
func (r *TaskRepo) CreateNode(ctx context.Context, node *model.IngestionTaskNode) error {
	return r.db.WithContext(ctx).Create(node).Error
}

// ListNodes 查询任务节点。
func (r *TaskRepo) ListNodes(ctx context.Context, taskID string) ([]model.IngestionTaskNode, error) {
	var nodes []model.IngestionTaskNode
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("task_id = ?", taskID).
		Order("node_order ASC, create_time ASC, id ASC").
		Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("list ingestion task nodes: %w", err)
	}
	return nodes, nil
}
