package model

import "go-base-agent/internal/framework/db"

// KnowledgeBase 对应 t_knowledge_base 表。
type KnowledgeBase struct {
	db.BaseModel

	Name           string `gorm:"column:name;type:varchar(128);not null" json:"name"`
	EmbeddingModel string `gorm:"column:embedding_model;type:varchar(64);not null" json:"embeddingModel"`
	CollectionName string `gorm:"column:collection_name;type:varchar(64);not null;uniqueIndex:uk_collection_name" json:"collectionName"`
	CreatedBy      string `gorm:"column:created_by;type:varchar(20);not null" json:"createdBy"`
	UpdatedBy      string `gorm:"column:updated_by;type:varchar(20)" json:"updatedBy"`
}

func (KnowledgeBase) TableName() string {
	return "t_knowledge_base"
}
