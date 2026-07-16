package repo

import (
	"context"
	"time"

	"go-base-agent/internal/biz/knowledge/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeDocumentScheduleRepo 文档定时刷新数据访问层。
type KnowledgeDocumentScheduleRepo struct {
	gdb *gorm.DB
}

// NewKnowledgeDocumentScheduleRepo 创建 KnowledgeDocumentScheduleRepo。
func NewKnowledgeDocumentScheduleRepo(gdb *gorm.DB) *KnowledgeDocumentScheduleRepo {
	return &KnowledgeDocumentScheduleRepo{gdb: gdb}
}

// UpsertByDocID 按 doc_id 创建或更新定时刷新任务。
func (r *KnowledgeDocumentScheduleRepo) UpsertByDocID(ctx context.Context, schedule *model.KnowledgeDocumentSchedule) error {
	return r.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "doc_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"kb_id",
			"cron_expr",
			"enabled",
			"next_run_time",
			"update_time",
		}),
	}).Create(schedule).Error
}

// UpdateByID 更新定时刷新任务字段。
func (r *KnowledgeDocumentScheduleRepo) UpdateByID(ctx context.Context, id string, updates map[string]any) error {
	return r.gdb.WithContext(ctx).Model(&model.KnowledgeDocumentSchedule{}).
		Where("id = ?", id).
		Updates(updates).Error
}

// ClaimDue 获取并锁定到期任务。
func (r *KnowledgeDocumentScheduleRepo) ClaimDue(ctx context.Context, now time.Time, limit int, owner string, lockUntil time.Time) ([]model.KnowledgeDocumentSchedule, error) {
	if limit <= 0 {
		limit = 20
	}
	var candidates []model.KnowledgeDocumentSchedule
	if err := r.gdb.WithContext(ctx).
		Where("enabled = 1 AND next_run_time IS NOT NULL AND next_run_time <= ?", now).
		Where("lock_until IS NULL OR lock_until < ?", now).
		Order("next_run_time ASC").
		Limit(limit).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]model.KnowledgeDocumentSchedule, 0, len(candidates))
	for _, schedule := range candidates {
		result := r.gdb.WithContext(ctx).Model(&model.KnowledgeDocumentSchedule{}).
			Where("id = ? AND enabled = 1", schedule.ID).
			Where("next_run_time IS NOT NULL AND next_run_time <= ?", now).
			Where("lock_until IS NULL OR lock_until < ?", now).
			Updates(map[string]any{
				"lock_owner":  owner,
				"lock_until":  lockUntil,
				"update_time": now,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			schedule.LockOwner = owner
			schedule.LockUntil = &lockUntil
			claimed = append(claimed, schedule)
		}
	}
	return claimed, nil
}

// CreateExec 创建定时刷新执行记录。
func (r *KnowledgeDocumentScheduleRepo) CreateExec(ctx context.Context, exec *model.KnowledgeDocumentScheduleExec) error {
	return r.gdb.WithContext(ctx).Create(exec).Error
}

// DeleteByDocID 根据文档 ID 删除定时任务。
func (r *KnowledgeDocumentScheduleRepo) DeleteByDocID(ctx context.Context, docID string) error {
	return r.gdb.WithContext(ctx).Where("doc_id = ?", docID).Delete(&model.KnowledgeDocumentSchedule{}).Error
}
