package model

import (
	"time"

	"go-base-agent/internal/framework/snowflake"

	"gorm.io/gorm"
)

// BizChangeLog 对应 t_biz_change_log 表。
type BizChangeLog struct {
	ID             string    `gorm:"column:id;primaryKey;type:varchar(20)" json:"id"`
	BizType        string    `gorm:"column:biz_type;type:varchar(64);not null" json:"bizType"`
	BizId          string    `gorm:"column:biz_id;type:varchar(64);not null" json:"bizId"`
	OperationType  string    `gorm:"column:operation_type;type:varchar(32);not null" json:"operationType"`
	ActionDesc     string    `gorm:"column:action_desc;type:varchar(512);not null" json:"actionDesc"`
	BeforeSnapshot string    `gorm:"column:before_snapshot;type:jsonb" json:"beforeSnapshot"`
	AfterSnapshot  string    `gorm:"column:after_snapshot;type:jsonb" json:"afterSnapshot"`
	ChangeDiff     string    `gorm:"column:change_diff;type:jsonb" json:"changeDiff"`
	OperatorID     string    `gorm:"column:operator_id;type:varchar(64)" json:"operatorId"`
	OperatorName   string    `gorm:"column:operator_name;type:varchar(128)" json:"operatorName"`
	OperatorRole   string    `gorm:"column:operator_role;type:varchar(64)" json:"operatorRole"`
	Success        bool      `gorm:"column:success;not null" json:"success"`
	ErrorMessage   string    `gorm:"column:error_message;type:varchar(512)" json:"errorMessage"`
	ClassName      string    `gorm:"column:class_name;type:varchar(255)" json:"className"`
	MethodName     string    `gorm:"column:method_name;type:varchar(255)" json:"methodName"`
	IP             string    `gorm:"column:ip;type:varchar(64)" json:"ip"`
	UserAgent      string    `gorm:"column:user_agent;type:varchar(512)" json:"userAgent"`
	CreateTime     time.Time `gorm:"column:create_time;autoCreateTime" json:"createTime"`
}

// TableName 返回数据库表名。
func (BizChangeLog) TableName() string {
	return "t_biz_change_log"
}

// BeforeCreate 在插入前生成雪花 ID。
func (b *BizChangeLog) BeforeCreate(_ *gorm.DB) error {
	if b.ID == "" {
		b.ID = snowflake.NextIDStr()
	}
	return nil
}
