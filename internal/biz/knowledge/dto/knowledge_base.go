package dto

// CreateKnowledgeBaseReq 创建知识库请求。
type CreateKnowledgeBaseReq struct {
	Name           string `json:"name" binding:"required"`
	EmbeddingModel string `json:"embeddingModel" binding:"required"`
	CollectionName string `json:"collectionName" binding:"required"`
}

// UpdateKnowledgeBaseReq 更新知识库请求。
type UpdateKnowledgeBaseReq struct {
	Name           string `json:"name"`
	EmbeddingModel string `json:"embeddingModel"`
	// CollectionName 保留历史 Go 请求兼容；更新时按 Java 语义忽略。
	CollectionName string `json:"collectionName"`
}

// KnowledgeBaseResp 知识库响应。
type KnowledgeBaseResp struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	EmbeddingModel string `json:"embeddingModel"`
	CollectionName string `json:"collectionName"`
	DocumentCount  int64  `json:"documentCount"`
	CreatedBy      string `json:"createdBy"`
	UpdatedBy      string `json:"updatedBy"`
	CreateTime     string `json:"createTime"`
	UpdateTime     string `json:"updateTime"`
}
