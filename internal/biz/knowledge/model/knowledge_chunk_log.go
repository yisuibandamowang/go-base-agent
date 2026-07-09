package model

import (
	"time"

	"go-base-agent/internal/framework/snowflake"

	"gorm.io/gorm"
)

// KnowledgeDocumentChunkLog 对应 t_knowledge_document_chunk_log 表。
// 注意：该表没有 deleted 字段，不使用 BaseModel。
type KnowledgeDocumentChunkLog struct {
	ID              string     `gorm:"column:id;primaryKey;type:varchar(20)" json:"id"`
	DocID           string     `gorm:"column:doc_id;type:varchar(20);not null;index:idx_doc_id_log" json:"docId"`
	Status          string     `gorm:"column:status;type:varchar(16);not null" json:"status"`
	ProcessMode     string     `gorm:"column:process_mode;type:varchar(16)" json:"processMode"`
	ChunkStrategy   string     `gorm:"column:chunk_strategy;type:varchar(16)" json:"chunkStrategy"`
	PipelineID      string     `gorm:"column:pipeline_id;type:varchar(20)" json:"pipelineId"`
	ExtractDuration int64      `gorm:"column:extract_duration;type:bigint" json:"extractDuration"`
	ChunkDuration   int64      `gorm:"column:chunk_duration;type:bigint" json:"chunkDuration"`
	EmbedDuration   int64      `gorm:"column:embed_duration;type:bigint" json:"embedDuration"`
	PersistDuration int64      `gorm:"column:persist_duration;type:bigint" json:"persistDuration"`
	TotalDuration   int64      `gorm:"column:total_duration;type:bigint" json:"totalDuration"`
	ChunkCount      int        `gorm:"column:chunk_count;type:integer" json:"chunkCount"`
	ErrorMessage    string     `gorm:"column:error_message;type:text" json:"errorMessage"`
	StartTime       *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime         *time.Time `gorm:"column:end_time" json:"endTime"`
	CreateTime      time.Time  `gorm:"column:create_time;autoCreateTime" json:"createTime"`
	UpdateTime      time.Time  `gorm:"column:update_time;autoUpdateTime" json:"updateTime"`
}

func (KnowledgeDocumentChunkLog) TableName() string {
	return "t_knowledge_document_chunk_log"
}

// BeforeCreate generates snowflake ID.
func (l *KnowledgeDocumentChunkLog) BeforeCreate(_ *gorm.DB) error {
	if l.ID == "" {
		l.ID = snowflake.NextIDStr()
	}
	return nil
}
