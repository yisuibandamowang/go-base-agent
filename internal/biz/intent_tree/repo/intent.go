package repo

import (
	"context"
	"fmt"
	"time"

	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// IntentRepo 意图树数据访问层。
type IntentRepo struct {
	db *gorm.DB
}

// NewIntentRepo 创建 IntentRepo。
func NewIntentRepo(database *gorm.DB) *IntentRepo {
	return &IntentRepo{db: database}
}

// Create 创建意图节点。
func (r *IntentRepo) Create(ctx context.Context, node *model.IntentNode) error {
	return r.db.WithContext(ctx).Create(node).Error
}

// FindByID 根据 ID 查询节点。
func (r *IntentRepo) FindByID(ctx context.Context, id string) (*model.IntentNode, error) {
	var node model.IntentNode
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("id = ?", id).First(&node).Error
	if err != nil {
		return nil, fmt.Errorf("find intent node: %w", err)
	}
	return &node, nil
}

// FindByIntentCode 根据 intent_code 查询节点。
func (r *IntentRepo) FindByIntentCode(ctx context.Context, intentCode string) (*model.IntentNode, error) {
	var node model.IntentNode
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("intent_code = ?", intentCode).First(&node).Error
	if err != nil {
		return nil, fmt.Errorf("find intent node by code: %w", err)
	}
	return &node, nil
}

// ListByParent 查询指定父节点下的子节点列表。
func (r *IntentRepo) ListByParent(ctx context.Context, parentCode string) ([]model.IntentNode, error) {
	var nodes []model.IntentNode
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope())
	if parentCode == "" {
		query = query.Where("parent_code IS NULL OR parent_code = ''")
	} else {
		query = query.Where("parent_code = ?", parentCode)
	}
	err := query.Order("sort_order ASC, create_time ASC").Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("list intent nodes by parent: %w", err)
	}
	return nodes, nil
}

// ListAll 查询所有意图节点，按 level + sort_order 排序。
func (r *IntentRepo) ListAll(ctx context.Context) ([]model.IntentNode, error) {
	var nodes []model.IntentNode
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Order("level ASC, sort_order ASC, create_time ASC").
		Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("list all intent nodes: %w", err)
	}
	return nodes, nil
}

// ListAllPage 分页查询所有节点。
func (r *IntentRepo) ListAllPage(ctx context.Context, page, size int) ([]model.IntentNode, int64, error) {
	var (
		nodes []model.IntentNode
		total int64
	)
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.IntentNode{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count intent nodes: %w", err)
	}
	err := query.Scopes(db.Paginate(page, size)).
		Order("level ASC, sort_order ASC, create_time ASC").
		Find(&nodes).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list intent nodes page: %w", err)
	}
	return nodes, total, nil
}

// Update 更新意图节点。
func (r *IntentRepo) Update(ctx context.Context, node *model.IntentNode) error {
	result := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(node).Where("id = ?", node.ID).
		Select("*").Omit("id", "create_time").
		Updates(node)
	if result.Error != nil {
		return fmt.Errorf("update intent node: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete 软删除意图节点。
func (r *IntentRepo) SoftDelete(ctx context.Context, id string) error {
	var node model.IntentNode
	node.ID = id
	if err := db.SoftDelete(r.db.WithContext(ctx), &node); err != nil {
		return fmt.Errorf("soft delete intent node: %w", err)
	}
	return nil
}

// UpdateEnabled 切换节点的启用状态。
func (r *IntentRepo) UpdateEnabled(ctx context.Context, id string, enabled int16) error {
	result := r.db.WithContext(ctx).Model(&model.IntentNode{}).
		Where("id = ? AND deleted = 0", id).
		Updates(map[string]interface{}{
			"enabled":     enabled,
			"update_time": time.Now(),
		})
	if result.Error != nil {
		return fmt.Errorf("update intent enabled: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// TermMappingRepo 关键词映射数据访问层。
type TermMappingRepo struct {
	db *gorm.DB
}

// NewTermMappingRepo 创建 TermMappingRepo。
func NewTermMappingRepo(database *gorm.DB) *TermMappingRepo {
	return &TermMappingRepo{db: database}
}

// Create 创建映射。
func (r *TermMappingRepo) Create(ctx context.Context, m *model.QueryTermMapping) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// FindByID 根据 ID 查询映射。
func (r *TermMappingRepo) FindByID(ctx context.Context, id string) (*model.QueryTermMapping, error) {
	var m model.QueryTermMapping
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&m).Error
	if err != nil {
		return nil, fmt.Errorf("find term mapping by id: %w", err)
	}
	return &m, nil
}

// Update 更新映射。
func (r *TermMappingRepo) Update(ctx context.Context, m *model.QueryTermMapping) error {
	return r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(m).Where("id = ?", m.ID).
		Select("*").Omit("id", "create_time").
		Updates(m).Error
}

// SoftDelete 软删除映射。
func (r *TermMappingRepo) SoftDelete(ctx context.Context, id string) error {
	var m model.QueryTermMapping
	m.ID = id
	return db.SoftDelete(r.db.WithContext(ctx), &m)
}

// ListByDomain 按领域查询映射列表。
func (r *TermMappingRepo) ListByDomain(ctx context.Context, domain string, page, size int) ([]model.QueryTermMapping, int64, error) {
	var (
		mappings []model.QueryTermMapping
		total    int64
	)
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.QueryTermMapping{})
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count term mappings: %w", err)
	}
	err := query.Scopes(db.Paginate(page, size)).
		Order("priority ASC, create_time DESC").
		Find(&mappings).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list term mappings: %w", err)
	}
	return mappings, total, nil
}

// FindBySourceTerm 根据源词查找映射。
func (r *TermMappingRepo) FindBySourceTerm(ctx context.Context, domain, sourceTerm string) (*model.QueryTermMapping, error) {
	var m model.QueryTermMapping
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("source_term = ? AND enabled = 1", sourceTerm)
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	err := query.Order("priority ASC").First(&m).Error
	if err != nil {
		return nil, fmt.Errorf("find term mapping: %w", err)
	}
	return &m, nil
}
