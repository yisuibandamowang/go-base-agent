package dto

// CreateDocumentReq 创建文档请求。
type CreateDocumentReq struct {
	DocName         string `json:"docName" binding:"required"`
	FileURL         string `json:"fileUrl" binding:"required"`
	FileType        string `json:"fileType" binding:"required"`
	FileSize        int64  `json:"fileSize"`
	Enabled         *int16 `json:"enabled"`
	SourceType      string `json:"sourceType"`
	SourceLocation  string `json:"sourceLocation"`
	ScheduleEnabled int16  `json:"scheduleEnabled"`
	ScheduleCron    string `json:"scheduleCron"`
	ProcessMode     string `json:"processMode"`
	ChunkStrategy   string `json:"chunkStrategy"`
	ChunkConfig     string `json:"chunkConfig"`
	PipelineID      string `json:"pipelineId"`
}

// UpdateDocumentReq 更新文档请求。
type UpdateDocumentReq struct {
	DocName         *string `json:"docName"`
	Enabled         *int16  `json:"enabled"`
	SourceLocation  string  `json:"sourceLocation"`
	ScheduleEnabled *int16  `json:"scheduleEnabled"`
	ScheduleCron    string  `json:"scheduleCron"`
	ProcessMode     string  `json:"processMode"`
	ChunkStrategy   string  `json:"chunkStrategy"`
	ChunkConfig     string  `json:"chunkConfig"`
	PipelineID      string  `json:"pipelineId"`
}

// DocumentResp 文档响应。
type DocumentResp struct {
	ID              string `json:"id"`
	KbID            string `json:"kbId"`
	KbName          string `json:"kbName,omitempty"`
	DocName         string `json:"docName"`
	Enabled         bool   `json:"enabled"`
	ChunkCount      int    `json:"chunkCount"`
	FileURL         string `json:"fileUrl"`
	FileType        string `json:"fileType"`
	FileSize        int64  `json:"fileSize"`
	ProcessMode     string `json:"processMode"`
	Status          string `json:"status"`
	SourceType      string `json:"sourceType"`
	SourceLocation  string `json:"sourceLocation"`
	ScheduleEnabled int16  `json:"scheduleEnabled"`
	ScheduleCron    string `json:"scheduleCron"`
	ChunkStrategy   string `json:"chunkStrategy"`
	ChunkConfig     string `json:"chunkConfig"`
	PipelineID      string `json:"pipelineId"`
	CreatedBy       string `json:"createdBy"`
	UpdatedBy       string `json:"updatedBy"`
	CreateTime      string `json:"createTime"`
	UpdateTime      string `json:"updateTime"`
	ChunksEdited    *bool  `json:"chunksEdited,omitempty"`
}

// ChunkLogResp 文档分块日志响应。
type ChunkLogResp struct {
	ID              string `json:"id"`
	DocID           string `json:"docId"`
	Status          string `json:"status"`
	ProcessMode     string `json:"processMode"`
	ChunkStrategy   string `json:"chunkStrategy"`
	PipelineID      string `json:"pipelineId"`
	PipelineName    string `json:"pipelineName"`
	ExtractDuration int64  `json:"extractDuration"`
	ChunkDuration   int64  `json:"chunkDuration"`
	EmbedDuration   int64  `json:"embedDuration"`
	PersistDuration int64  `json:"persistDuration"`
	OtherDuration   int64  `json:"otherDuration"`
	TotalDuration   int64  `json:"totalDuration"`
	ChunkCount      int    `json:"chunkCount"`
	ErrorMessage    string `json:"errorMessage"`
	StartTime       string `json:"startTime"`
	EndTime         string `json:"endTime"`
	CreateTime      string `json:"createTime"`
	UpdateTime      string `json:"updateTime"`
}

// ChunkResp 分块响应。
type ChunkResp struct {
	ID          string `json:"id"`
	DocID       string `json:"docId"`
	KbID        string `json:"kbId"`
	ChunkIndex  int    `json:"chunkIndex"`
	Content     string `json:"content"`
	ContentHash string `json:"contentHash"`
	CharCount   int    `json:"charCount"`
	TokenCount  int    `json:"tokenCount"`
	Enabled     int16  `json:"enabled"`
	CreatedBy   string `json:"createdBy"`
	UpdatedBy   string `json:"updatedBy"`
	CreateTime  string `json:"createTime"`
	UpdateTime  string `json:"updateTime"`
}

// UpdateChunkReq 更新分块请求。
type UpdateChunkReq struct {
	Content string `json:"content" binding:"required"`
}

// CreateChunkReq 手工创建分块请求。
type CreateChunkReq struct {
	Content string `json:"content" binding:"required"`
	Index   *int   `json:"index"`
	ChunkID string `json:"chunkId"`
}

// BatchEnableChunksReq 批量启用/禁用分块请求。
type BatchEnableChunksReq struct {
	IDs      []string `json:"ids"`
	ChunkIDs []string `json:"chunkIds"`
	Enabled  int16    `json:"enabled"`
}

// ToggleChunkReq 单个分块启用/禁用请求。
type ToggleChunkReq struct {
	Enabled int16 `json:"enabled"`
}
