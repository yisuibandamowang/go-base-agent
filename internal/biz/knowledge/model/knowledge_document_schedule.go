package model

import (
	"time"

	"go-base-agent/internal/framework/snowflake"

	"gorm.io/gorm"
)

// KnowledgeDocumentSchedule 对应 t_knowledge_document_schedule 表。
type KnowledgeDocumentSchedule struct {
	ID              string     `gorm:"column:id;primaryKey;type:varchar(20)" json:"id"`
	DocID           string     `gorm:"column:doc_id;type:varchar(20);not null;uniqueIndex:uk_doc_id" json:"docId"`
	KbID            string     `gorm:"column:kb_id;type:varchar(20);not null" json:"kbId"`
	CronExpr        string     `gorm:"column:cron_expr;type:varchar(64)" json:"cronExpr"`
	Enabled         int16      `gorm:"column:enabled;type:smallint;default:0" json:"enabled"`
	NextRunTime     *time.Time `gorm:"column:next_run_time" json:"nextRunTime"`
	LastRunTime     *time.Time `gorm:"column:last_run_time" json:"lastRunTime"`
	LastSuccessTime *time.Time `gorm:"column:last_success_time" json:"lastSuccessTime"`
	LastStatus      string     `gorm:"column:last_status;type:varchar(16)" json:"lastStatus"`
	LastError       string     `gorm:"column:last_error;type:varchar(512)" json:"lastError"`
	LastETag        string     `gorm:"column:last_etag;type:varchar(256)" json:"lastEtag"`
	LastModified    string     `gorm:"column:last_modified;type:varchar(256)" json:"lastModified"`
	LastContentHash string     `gorm:"column:last_content_hash;type:varchar(128)" json:"lastContentHash"`
	LockOwner       string     `gorm:"column:lock_owner;type:varchar(128)" json:"lockOwner"`
	LockUntil       *time.Time `gorm:"column:lock_until" json:"lockUntil"`
	CreateTime      time.Time  `gorm:"column:create_time;autoCreateTime" json:"createTime"`
	UpdateTime      time.Time  `gorm:"column:update_time;autoUpdateTime" json:"updateTime"`
}

// TableName 返回文档定时刷新任务表名。
func (KnowledgeDocumentSchedule) TableName() string {
	return "t_knowledge_document_schedule"
}

// BeforeCreate 生成雪花 ID。
func (s *KnowledgeDocumentSchedule) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = snowflake.NextIDStr()
	}
	return nil
}

// KnowledgeDocumentScheduleExec 对应 t_knowledge_document_schedule_exec 表。
type KnowledgeDocumentScheduleExec struct {
	ID           string     `gorm:"column:id;primaryKey;type:varchar(20)" json:"id"`
	ScheduleID   string     `gorm:"column:schedule_id;type:varchar(20);not null" json:"scheduleId"`
	DocID        string     `gorm:"column:doc_id;type:varchar(20);not null" json:"docId"`
	KbID         string     `gorm:"column:kb_id;type:varchar(20);not null" json:"kbId"`
	Status       string     `gorm:"column:status;type:varchar(16);not null" json:"status"`
	Message      string     `gorm:"column:message;type:varchar(512)" json:"message"`
	StartTime    *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime      *time.Time `gorm:"column:end_time" json:"endTime"`
	FileName     string     `gorm:"column:file_name;type:varchar(512)" json:"fileName"`
	FileSize     int64      `gorm:"column:file_size;type:bigint" json:"fileSize"`
	ContentHash  string     `gorm:"column:content_hash;type:varchar(128)" json:"contentHash"`
	ETag         string     `gorm:"column:etag;type:varchar(256)" json:"etag"`
	LastModified string     `gorm:"column:last_modified;type:varchar(256)" json:"lastModified"`
	CreateTime   time.Time  `gorm:"column:create_time;autoCreateTime" json:"createTime"`
	UpdateTime   time.Time  `gorm:"column:update_time;autoUpdateTime" json:"updateTime"`
}

// TableName 返回文档定时刷新执行记录表名。
func (KnowledgeDocumentScheduleExec) TableName() string {
	return "t_knowledge_document_schedule_exec"
}

// BeforeCreate 生成雪花 ID。
func (e *KnowledgeDocumentScheduleExec) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = snowflake.NextIDStr()
	}
	return nil
}
