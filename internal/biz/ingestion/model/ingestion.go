package model

import (
	"time"

	"go-base-agent/internal/framework/db"
)

// IngestionPipeline 对应 t_ingestion_pipeline 表。
type IngestionPipeline struct {
	db.BaseModel

	Name        string `gorm:"column:name;type:varchar(100);not null" json:"name"`
	Description string `gorm:"column:description;type:text" json:"description"`
	CreatedBy   string `gorm:"column:created_by;type:varchar(20)" json:"createdBy"`
	UpdatedBy   string `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (IngestionPipeline) TableName() string {
	return "t_ingestion_pipeline"
}

// IngestionPipelineNode 对应 t_ingestion_pipeline_node 表。
type IngestionPipelineNode struct {
	db.BaseModel

	PipelineID    string `gorm:"column:pipeline_id;type:varchar(20);not null;index:idx_ingestion_pipeline_node_pipeline" json:"pipelineId"`
	NodeID        string `gorm:"column:node_id;type:varchar(20);not null" json:"nodeId"`
	NodeType      string `gorm:"column:node_type;type:varchar(16);not null" json:"nodeType"`
	NextNodeID    string `gorm:"column:next_node_id;type:varchar(20)" json:"nextNodeId"`
	SettingsJSON  string `gorm:"column:settings_json;type:jsonb" json:"settingsJson"`
	ConditionJSON string `gorm:"column:condition_json;type:jsonb" json:"conditionJson"`
	CreatedBy     string `gorm:"column:created_by;type:varchar(20)" json:"createdBy"`
	UpdatedBy     string `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (IngestionPipelineNode) TableName() string {
	return "t_ingestion_pipeline_node"
}

// IngestionTask 对应 t_ingestion_task 表。
type IngestionTask struct {
	db.BaseModel

	PipelineID     string     `gorm:"column:pipeline_id;type:varchar(20);not null;index:idx_ingestion_task_pipeline" json:"pipelineId"`
	SourceType     string     `gorm:"column:source_type;type:varchar(20);not null" json:"sourceType"`
	SourceLocation string     `gorm:"column:source_location;type:text" json:"sourceLocation"`
	SourceFileName string     `gorm:"column:source_file_name;type:varchar(255)" json:"sourceFileName"`
	Status         string     `gorm:"column:status;type:varchar(16);not null;index:idx_ingestion_task_status" json:"status"`
	ChunkCount     int        `gorm:"column:chunk_count;default:0" json:"chunkCount"`
	ErrorMessage   string     `gorm:"column:error_message;type:text" json:"errorMessage"`
	LogsJSON       string     `gorm:"column:logs_json;type:jsonb" json:"logsJson"`
	MetadataJSON   string     `gorm:"column:metadata_json;type:jsonb" json:"metadataJson"`
	StartedAt      *time.Time `gorm:"column:started_at" json:"startedAt"`
	CompletedAt    *time.Time `gorm:"column:completed_at" json:"completedAt"`
	CreatedBy      string     `gorm:"column:created_by;type:varchar(20)" json:"createdBy"`
	UpdatedBy      string     `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (IngestionTask) TableName() string {
	return "t_ingestion_task"
}

// IngestionTaskNode 对应 t_ingestion_task_node 表。
type IngestionTaskNode struct {
	db.BaseModel

	TaskID       string `gorm:"column:task_id;type:varchar(20);not null;index:idx_ingestion_task_node_task" json:"taskId"`
	PipelineID   string `gorm:"column:pipeline_id;type:varchar(20);not null;index:idx_ingestion_task_node_pipeline" json:"pipelineId"`
	NodeID       string `gorm:"column:node_id;type:varchar(20);not null" json:"nodeId"`
	NodeType     string `gorm:"column:node_type;type:varchar(16);not null" json:"nodeType"`
	NodeOrder    int    `gorm:"column:node_order;not null;default:0" json:"nodeOrder"`
	Status       string `gorm:"column:status;type:varchar(16);not null;index:idx_ingestion_task_node_status" json:"status"`
	DurationMs   int64  `gorm:"column:duration_ms;not null;default:0" json:"durationMs"`
	Message      string `gorm:"column:message;type:text" json:"message"`
	ErrorMessage string `gorm:"column:error_message;type:text" json:"errorMessage"`
	OutputJSON   string `gorm:"column:output_json;type:text" json:"outputJson"`
}

func (IngestionTaskNode) TableName() string {
	return "t_ingestion_task_node"
}
