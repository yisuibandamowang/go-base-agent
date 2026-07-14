package repo

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/audit/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// BizChangeLogRepo 业务变更审计日志数据访问层。
type BizChangeLogRepo struct {
	db *gorm.DB
}

// NewBizChangeLogRepo 创建 BizChangeLogRepo。
func NewBizChangeLogRepo(database *gorm.DB) *BizChangeLogRepo {
	return &BizChangeLogRepo{db: database}
}

// BizChangeLogQuery 变更日志查询条件。
type BizChangeLogQuery struct {
	BizType       string
	BizId         string
	OperationType string
	OperatorID    string
	OperatorName  string
	Success       *bool
	BeginTime     *time.Time
	EndTime       *time.Time
}

// List 分页查询审计日志。
func (r *BizChangeLogRepo) List(ctx context.Context, query BizChangeLogQuery, page, size int) ([]model.BizChangeLog, int64, error) {
	var (
		items []model.BizChangeLog
		total int64
	)
	dbQuery := r.db.WithContext(ctx).Model(&model.BizChangeLog{})
	if query.BizType != "" {
		dbQuery = dbQuery.Where("biz_type = ?", query.BizType)
	}
	if query.BizId != "" {
		dbQuery = dbQuery.Where("biz_id LIKE ?", "%"+query.BizId+"%")
	}
	if query.OperationType != "" {
		dbQuery = dbQuery.Where("operation_type = ?", query.OperationType)
	}
	if query.OperatorID != "" {
		dbQuery = dbQuery.Where("operator_id = ?", query.OperatorID)
	}
	if query.OperatorName != "" {
		dbQuery = dbQuery.Where("operator_name LIKE ?", "%"+query.OperatorName+"%")
	}
	if query.Success != nil {
		dbQuery = dbQuery.Where("success = ?", *query.Success)
	}
	if query.BeginTime != nil {
		dbQuery = dbQuery.Where("create_time >= ?", *query.BeginTime)
	}
	if query.EndTime != nil {
		dbQuery = dbQuery.Where("create_time <= ?", *query.EndTime)
	}

	if err := dbQuery.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count biz change logs: %w", err)
	}
	if err := dbQuery.Order("create_time DESC").
		Scopes(db.Paginate(page, size)).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list biz change logs: %w", err)
	}
	return items, total, nil
}

// FindByID 根据 ID 查询单条日志。
func (r *BizChangeLogRepo) FindByID(ctx context.Context, id string) (*model.BizChangeLog, error) {
	var item model.BizChangeLog
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&item).Error; err != nil {
		return nil, fmt.Errorf("find biz change log: %w", err)
	}
	return &item, nil
}
