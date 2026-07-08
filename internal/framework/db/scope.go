package db

import (
	"gorm.io/gorm"
)

// Paginate 通用分页 Scope，对齐 convention.PageResp 的分页语义。
// page 从 1 开始，size 为每页条数。
func Paginate(page, size int) func(*gorm.DB) *gorm.DB {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size
	return func(db *gorm.DB) *gorm.DB {
		return db.Offset(offset).Limit(size)
	}
}
