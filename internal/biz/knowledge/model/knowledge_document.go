package model

import "go-base-agent/internal/framework/db"

// KnowledgeDocument 对应 t_knowledge_document 表。
type KnowledgeDocument struct {
	db.BaseModel

	KbID               string `gorm:"column:kb_id;type:varchar(20);not null;index:idx_kb_id" json:"kbId"`
	DocName            string `gorm:"column:doc_name;type:varchar(256);not null" json:"docName"`
	Enabled            int16  `gorm:"column:enabled;type:smallint;default:1" json:"enabled"`
	ChunkCount         int    `gorm:"column:chunk_count;default:0" json:"chunkCount"`
	FileURL            string `gorm:"column:file_url;type:varchar(1024);not null" json:"fileUrl"`
	FileType           string `gorm:"column:file_type;type:varchar(16);not null" json:"fileType"`
	FileSize           int64  `gorm:"column:file_size;type:bigint" json:"fileSize"`
	ProcessMode        string `gorm:"column:process_mode;type:varchar(16);default:chunk" json:"processMode"`
	Status             string `gorm:"column:status;type:varchar(16);default:pending" json:"status"`
	SourceType         string `gorm:"column:source_type;type:varchar(16)" json:"sourceType"`
	SourceLocation     string `gorm:"column:source_location;type:varchar(1024)" json:"sourceLocation"`
	CanonicalSourceKey string `gorm:"column:canonical_source_key;type:varchar(256)" json:"canonicalSourceKey"`
	SourceRootKey      string `gorm:"column:source_root_key;type:varchar(256)" json:"sourceRootKey"`
	SourceParentKey    string `gorm:"column:source_parent_key;type:varchar(256)" json:"sourceParentKey"`
	SourceContentHash  string `gorm:"column:source_content_hash;type:varchar(64)" json:"sourceContentHash"`
	SourceNodeType     string `gorm:"column:source_node_type;type:varchar(16)" json:"sourceNodeType"`
	ScheduleEnabled    int16  `gorm:"column:schedule_enabled;type:smallint" json:"scheduleEnabled"`
	ScheduleCron       string `gorm:"column:schedule_cron;type:varchar(64)" json:"scheduleCron"`
	ChunkStrategy      string `gorm:"column:chunk_strategy;type:varchar(32)" json:"chunkStrategy"`
	ChunkConfig        string `gorm:"column:chunk_config;type:jsonb" json:"chunkConfig"`
	PipelineID         string `gorm:"column:pipeline_id;type:varchar(20)" json:"pipelineId"`
	CreatedBy          string `gorm:"column:created_by;type:varchar(20);not null" json:"createdBy"`
	UpdatedBy          string `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (KnowledgeDocument) TableName() string {
	return "t_knowledge_document"
}

// KnowledgeChunk 对应 t_knowledge_chunk 表。
type KnowledgeChunk struct {
	db.BaseModel

	KbID              string `gorm:"column:kb_id;type:varchar(20);not null" json:"kbId"`
	DocID             string `gorm:"column:doc_id;type:varchar(20);not null;index:idx_doc_id" json:"docId"`
	ChunkIndex        int    `gorm:"column:chunk_index;not null" json:"chunkIndex"`
	Content           string `gorm:"column:content;type:text;not null" json:"content"`
	ContentHash       string `gorm:"column:content_hash;type:varchar(64)" json:"contentHash"`
	CharCount         int    `gorm:"column:char_count" json:"charCount"`
	TokenCount        int    `gorm:"column:token_count" json:"tokenCount"`
	SourceVersion     string `gorm:"column:source_version;type:varchar(64)" json:"sourceVersion"`
	SourceHash        string `gorm:"column:source_hash;type:varchar(64)" json:"sourceHash"`
	ChunkConfigHash   string `gorm:"column:chunk_config_hash;type:varchar(64)" json:"chunkConfigHash"`
	BlockIndex        int    `gorm:"column:block_index" json:"blockIndex"`
	BlockType         string `gorm:"column:block_type;type:varchar(64)" json:"blockType"`
	SourceStartOffset int    `gorm:"column:source_start_offset" json:"sourceStartOffset"`
	SourceEndOffset   int    `gorm:"column:source_end_offset" json:"sourceEndOffset"`
	CoreStartOffset   int    `gorm:"column:core_start_offset" json:"coreStartOffset"`
	CoreEndOffset     int    `gorm:"column:core_end_offset" json:"coreEndOffset"`
	Enabled           int16  `gorm:"column:enabled;type:smallint;default:1" json:"enabled"`
	CreatedBy         string `gorm:"column:created_by;type:varchar(20);not null" json:"createdBy"`
	UpdatedBy         string `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (KnowledgeChunk) TableName() string {
	return "t_knowledge_chunk"
}
