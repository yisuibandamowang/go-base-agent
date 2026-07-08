package model

import "go-base-agent/internal/framework/db"

// User 对应 t_user 表。
type User struct {
	db.BaseModel

	Username string `gorm:"column:username;type:varchar(64);not null;uniqueIndex:uk_user_username" json:"username"`
	Password string `gorm:"column:password;type:varchar(128);not null" json:"-"`
	Role     string `gorm:"column:role;type:varchar(32);not null" json:"role"`
	Avatar   string `gorm:"column:avatar;type:varchar(128)" json:"avatar"`
}

func (User) TableName() string {
	return "t_user"
}
