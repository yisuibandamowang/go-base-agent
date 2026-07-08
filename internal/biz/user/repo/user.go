package repo

import (
	"context"
	"fmt"

	"go-base-agent/internal/biz/user/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// UserRepo 用户数据访问层。
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo 创建 UserRepo。
func NewUserRepo(database *gorm.DB) *UserRepo {
	return &UserRepo{db: database}
}

// FindByUsername 根据用户名查找用户。
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, fmt.Errorf("find user by username: %w", err)
	}
	return &user, nil
}

// FindByID 根据 ID 查找用户。
func (r *UserRepo) FindByID(ctx context.Context, id string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("id = ?", id).First(&user).Error
	if err != nil {
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &user, nil
}
