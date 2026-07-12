package dto

import "time"

// PipelineNodeReq 是流水线节点请求。
type PipelineNodeReq struct {
	NodeID     string         `json:"nodeId"`
	NodeType   string         `json:"nodeType"`
	Settings   map[string]any `json:"settings"`
	Condition  map[string]any `json:"condition"`
	NextNodeID string         `json:"nextNodeId"`
}

// CreatePipelineReq 是创建流水线请求。
type CreatePipelineReq struct {
	Name        string            `json:"name" binding:"required"`
	Description string            `json:"description"`
	Nodes       []PipelineNodeReq `json:"nodes"`
}

// UpdatePipelineReq 是更新流水线请求。
type UpdatePipelineReq struct {
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Nodes       []PipelineNodeReq `json:"nodes"`
}

// PipelineNodeResp 是流水线节点响应。
type PipelineNodeResp struct {
	ID         string         `json:"id"`
	NodeID     string         `json:"nodeId"`
	NodeType   string         `json:"nodeType"`
	Settings   map[string]any `json:"settings,omitempty"`
	Condition  map[string]any `json:"condition,omitempty"`
	NextNodeID string         `json:"nextNodeId,omitempty"`
}

// PipelineResp 是流水线响应。
type PipelineResp struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	CreatedBy   string             `json:"createdBy"`
	Nodes       []PipelineNodeResp `json:"nodes"`
	CreateTime  time.Time          `json:"createTime"`
	UpdateTime  time.Time          `json:"updateTime"`
}

// DocumentSourceReq 是摄取任务文档来源请求。
type DocumentSourceReq struct {
	Type        string            `json:"type"`
	Location    string            `json:"location"`
	FileName    string            `json:"fileName"`
	Credentials map[string]string `json:"credentials"`
}

// CreateTaskReq 是创建摄取任务请求。
type CreateTaskReq struct {
	PipelineID    string            `json:"pipelineId" binding:"required"`
	Source        DocumentSourceReq `json:"source" binding:"required"`
	Metadata      map[string]any    `json:"metadata"`
	VectorSpaceID any               `json:"vectorSpaceId"`
}

// IngestionResultResp 是摄取任务执行结果。
type IngestionResultResp struct {
	TaskID     string `json:"taskId"`
	PipelineID string `json:"pipelineId"`
	Status     string `json:"status"`
	ChunkCount int    `json:"chunkCount"`
	Message    string `json:"message"`
}

// TaskResp 是摄取任务响应。
type TaskResp struct {
	ID             string           `json:"id"`
	PipelineID     string           `json:"pipelineId"`
	SourceType     string           `json:"sourceType"`
	SourceLocation string           `json:"sourceLocation"`
	SourceFileName string           `json:"sourceFileName"`
	Status         string           `json:"status"`
	ChunkCount     int              `json:"chunkCount"`
	ErrorMessage   string           `json:"errorMessage"`
	Logs           []map[string]any `json:"logs"`
	Metadata       map[string]any   `json:"metadata"`
	StartedAt      *time.Time       `json:"startedAt"`
	CompletedAt    *time.Time       `json:"completedAt"`
	CreatedBy      string           `json:"createdBy"`
	CreateTime     time.Time        `json:"createTime"`
	UpdateTime     time.Time        `json:"updateTime"`
}

// TaskNodeResp 是摄取任务节点响应。
type TaskNodeResp struct {
	ID           string         `json:"id"`
	TaskID       string         `json:"taskId"`
	PipelineID   string         `json:"pipelineId"`
	NodeID       string         `json:"nodeId"`
	NodeType     string         `json:"nodeType"`
	NodeOrder    int            `json:"nodeOrder"`
	Status       string         `json:"status"`
	DurationMs   int64          `json:"durationMs"`
	Message      string         `json:"message"`
	ErrorMessage string         `json:"errorMessage"`
	Output       map[string]any `json:"output,omitempty"`
	CreateTime   time.Time      `json:"createTime"`
	UpdateTime   time.Time      `json:"updateTime"`
}
