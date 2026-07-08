package db

import (
	"time"

	"go-base-agent/internal/framework/snowflake"

	"gorm.io/gorm"
)

type BaseModel struct {
	ID         string    `gorm:"column:id;primaryKey;type:varchar(20)" json:"id"`
	Deleted    int16     `gorm:"column:deleted;type:smallint;default:0" json:"deleted"`
	CreateTime time.Time `gorm:"column:create_time;autoCreateTime" json:"createTime"`
	UpdateTime time.Time `gorm:"column:update_time;autoUpdateTime" json:"updateTime"`
}

// BeforeCreate 生成雪花 ID。
func (b *BaseModel) BeforeCreate(_ *gorm.DB) error {
	if b.ID == "" {
		b.ID = snowflake.NextIDStr()
	}
	return nil
}

// NotDeletedScope 过滤未删除记录（deleted = 0）。
func NotDeletedScope() func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("deleted = 0")
	}
}

// SoftDelete 将 deleted 标记为 1，同时更新 update_time。
func SoftDelete[T any](db *gorm.DB, model *T) error {
	return db.Model(model).Updates(map[string]interface{}{
		"deleted":     1,
		"update_time": time.Now(),
	}).Error
}
