package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentModel "go-base-agent/internal/biz/agent/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// AgentRepo 智能体数据访问层。
type AgentRepo struct {
	db *gorm.DB
}

// NewAgentRepo 创建 AgentRepo。
func NewAgentRepo(database *gorm.DB) *AgentRepo {
	return &AgentRepo{db: database}
}

// Transaction 执行事务。
func (r *AgentRepo) Transaction(ctx context.Context, fn func(tx *AgentRepo) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&AgentRepo{db: tx})
	})
}

// ListProfiles 查询全部智能体。
func (r *AgentRepo) ListProfiles(ctx context.Context) ([]agentModel.AgentProfile, error) {
	var items []agentModel.AgentProfile
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	return items, nil
}

// FindProfileByID 根据 ID 查询智能体。
func (r *AgentRepo) FindProfileByID(ctx context.Context, id string) (*agentModel.AgentProfile, error) {
	var item agentModel.AgentProfile
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("id = ?", id).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find agent profile: %w", err)
	}
	return &item, nil
}

// FindBuiltinProfile 查询内置智能体。
func (r *AgentRepo) FindBuiltinProfile(ctx context.Context) (*agentModel.AgentProfile, error) {
	var item agentModel.AgentProfile
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("builtin = 1").Order("create_time ASC, id ASC").First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find builtin agent profile: %w", err)
	}
	return &item, nil
}

// ProfileNameExists 判断名称是否已存在。
func (r *AgentRepo) ProfileNameExists(ctx context.Context, name, excludeID string) (bool, error) {
	query := r.db.WithContext(ctx).Model(&agentModel.AgentProfile{}).Scopes(db.NotDeletedScope()).
		Where("name = ?", name)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("count agent profile name: %w", err)
	}
	return count > 0, nil
}

// CreateProfile 创建智能体。
func (r *AgentRepo) CreateProfile(ctx context.Context, profile *agentModel.AgentProfile) error {
	if err := r.db.WithContext(ctx).Create(profile).Error; err != nil {
		return fmt.Errorf("create agent profile: %w", err)
	}
	return nil
}

// UpdateProfile 更新智能体。
func (r *AgentRepo) UpdateProfile(ctx context.Context, id string, values map[string]any) error {
	values["update_time"] = time.Now()
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentProfile{}).
		Where("id = ? AND deleted = 0", id).
		Updates(values).Error; err != nil {
		return fmt.Errorf("update agent profile: %w", err)
	}
	return nil
}

// SoftDeleteProfile 软删除智能体。
func (r *AgentRepo) SoftDeleteProfile(ctx context.Context, profile *agentModel.AgentProfile) error {
	if err := db.SoftDelete(r.db.WithContext(ctx), profile); err != nil {
		return fmt.Errorf("soft delete agent profile: %w", err)
	}
	return nil
}

// ClearActiveProfiles 清空全部激活态。
func (r *AgentRepo) ClearActiveProfiles(ctx context.Context) error {
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentProfile{}).
		Where("deleted = 0 AND active = 1").
		Updates(map[string]any{"active": 0, "update_time": time.Now()}).Error; err != nil {
		return fmt.Errorf("clear active agent profiles: %w", err)
	}
	return nil
}

// SetActiveProfile 设置激活态。
func (r *AgentRepo) SetActiveProfile(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentProfile{}).
		Where("id = ? AND deleted = 0", id).
		Updates(map[string]any{"active": 1, "update_time": time.Now()}).Error; err != nil {
		return fmt.Errorf("set active agent profile: %w", err)
	}
	return nil
}

// PromptRef 仅投影 agent_id 和 slot_key。
type PromptRef struct {
	AgentID string `gorm:"column:agent_id"`
	SlotKey string `gorm:"column:slot_key"`
}

// ListConfiguredPromptRefs 查询已配置槽位。
func (r *AgentRepo) ListConfiguredPromptRefs(ctx context.Context) ([]PromptRef, error) {
	var items []PromptRef
	if err := r.db.WithContext(ctx).Table("t_agent_prompt").
		Select("agent_id, slot_key").
		Where("deleted = 0 AND content IS NOT NULL AND content <> ''").
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list configured agent prompt refs: %w", err)
	}
	return items, nil
}

// ListPromptsByAgentID 查询指定智能体全部提示词。
func (r *AgentRepo) ListPromptsByAgentID(ctx context.Context, agentID string) ([]agentModel.AgentPrompt, error) {
	var items []agentModel.AgentPrompt
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("agent_id = ?", agentID).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list agent prompts: %w", err)
	}
	return items, nil
}

// FindPromptBySlot 查询指定槽位提示词。
func (r *AgentRepo) FindPromptBySlot(ctx context.Context, agentID, slotKey string) (*agentModel.AgentPrompt, error) {
	var item agentModel.AgentPrompt
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("agent_id = ? AND slot_key = ?", agentID, slotKey).
		First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find agent prompt: %w", err)
	}
	return &item, nil
}

// PromptExists 判断槽位记录是否存在。
func (r *AgentRepo) PromptExists(ctx context.Context, agentID, slotKey string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentPrompt{}).Scopes(db.NotDeletedScope()).
		Where("agent_id = ? AND slot_key = ?", agentID, slotKey).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("count agent prompt: %w", err)
	}
	return count > 0, nil
}

// CreatePrompt 创建提示词。
func (r *AgentRepo) CreatePrompt(ctx context.Context, prompt *agentModel.AgentPrompt) error {
	if err := r.db.WithContext(ctx).Create(prompt).Error; err != nil {
		return fmt.Errorf("create agent prompt: %w", err)
	}
	return nil
}

// UpdatePromptContent 更新提示词内容。
func (r *AgentRepo) UpdatePromptContent(ctx context.Context, agentID, slotKey string, content *string) error {
	var contentValue any
	if content != nil {
		contentValue = *content
	}
	values := map[string]any{
		"content":     contentValue,
		"update_time": time.Now(),
	}
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentPrompt{}).
		Where("agent_id = ? AND slot_key = ? AND deleted = 0", agentID, slotKey).
		Updates(values).Error; err != nil {
		return fmt.Errorf("update agent prompt content: %w", err)
	}
	return nil
}

// SoftDeletePromptsByAgentID 软删除指定智能体的全部提示词。
func (r *AgentRepo) SoftDeletePromptsByAgentID(ctx context.Context, agentID string) error {
	if err := r.db.WithContext(ctx).Model(&agentModel.AgentPrompt{}).
		Where("agent_id = ? AND deleted = 0", agentID).
		Updates(map[string]any{"deleted": 1, "update_time": time.Now()}).Error; err != nil {
		return fmt.Errorf("soft delete agent prompts: %w", err)
	}
	return nil
}

// FindPromptByAgentAndSlot 根据 agent 和 slot 查询单条提示词。
func (r *AgentRepo) FindPromptByAgentAndSlot(ctx context.Context, agentID, slotKey string) (*agentModel.AgentPrompt, error) {
	return r.FindPromptBySlot(ctx, agentID, slotKey)
}
