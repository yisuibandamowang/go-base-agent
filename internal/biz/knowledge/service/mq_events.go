package service

const (
	// KnowledgeDocumentChunkTopic 是文档分块任务主题。
	KnowledgeDocumentChunkTopic = "knowledge-document-chunk_topic"
	// KnowledgeDocumentChunkConsumerGroup 是文档分块任务消费组。
	KnowledgeDocumentChunkConsumerGroup = "knowledge-document-chunk_cg"
	// KnowledgeBaseCleanupTopic 是知识库物理资源清理主题。
	KnowledgeBaseCleanupTopic = "knowledge-base-cleanup_topic"
	// KnowledgeBaseCleanupConsumerGroup 是知识库物理资源清理消费组。
	KnowledgeBaseCleanupConsumerGroup = "knowledge-base-cleanup_cg"
)

// KnowledgeDocumentChunkEvent 对齐 Java KnowledgeDocumentChunkEvent。
type KnowledgeDocumentChunkEvent struct {
	DocID    string `json:"docId"`
	Operator string `json:"operator"`
}

// KnowledgeBaseCleanupEvent 对齐 Java KnowledgeBaseCleanupEvent。
type KnowledgeBaseCleanupEvent struct {
	KBID           string `json:"kbId"`
	CollectionName string `json:"collectionName"`
	Operator       string `json:"operator"`
}
