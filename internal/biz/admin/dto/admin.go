package dto

import "time"

// DashboardResp 仪表盘统计响应。
type DashboardResp struct {
	KnowledgeBaseCount int64 `json:"knowledgeBaseCount"`
	DocumentCount      int64 `json:"documentCount"`
	ChunkCount         int64 `json:"chunkCount"`
	UserCount          int64 `json:"userCount"`
	ConversationCount  int64 `json:"conversationCount"`
	MessageCount       int64 `json:"messageCount"`
	VectorCount        int64 `json:"vectorCount"`
}

// TraceRunResp 链路追踪运行记录响应。
type TraceRunResp struct {
	ID             string     `json:"id"`
	TraceID        string     `json:"traceId"`
	TraceName      string     `json:"traceName"`
	ConversationID string     `json:"conversationId"`
	TaskID         string     `json:"taskId"`
	UserID         string     `json:"userId"`
	Status         string     `json:"status"`
	ErrorMessage   string     `json:"errorMessage"`
	StartTime      *time.Time `json:"startTime"`
	EndTime        *time.Time `json:"endTime"`
	DurationMs     int64      `json:"durationMs"`
}

// TraceDetailResp 链路详情（含节点树）。
type TraceDetailResp struct {
	Run   *TraceRunResp   `json:"run"`
	Nodes []TraceNodeResp `json:"nodes"`
}

// TraceNodeResp 链路追踪节点响应。
type TraceNodeResp struct {
	ID           string     `json:"id"`
	TraceID      string     `json:"traceId"`
	NodeID       string     `json:"nodeId"`
	ParentNodeID string     `json:"parentNodeId"`
	Depth        int        `json:"depth"`
	NodeType     string     `json:"nodeType"`
	NodeName     string     `json:"nodeName"`
	Status       string     `json:"status"`
	ErrorMessage string     `json:"errorMessage"`
	StartTime    *time.Time `json:"startTime"`
	EndTime      *time.Time `json:"endTime"`
	DurationMs   int64      `json:"durationMs"`
}

// SampleQuestionResp 示例问题响应。
type SampleQuestionResp struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Question    string    `json:"question"`
	CreateTime  time.Time `json:"createTime"`
	UpdateTime  time.Time `json:"updateTime"`
}

// CreateSampleQuestionReq 创建示例问题请求。
type CreateSampleQuestionReq struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Question    string `json:"question" binding:"required"`
}

// UpdateSampleQuestionReq 更新示例问题请求。
type UpdateSampleQuestionReq struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Question    *string `json:"question"`
}

// CreateUserReq 管理员创建用户。
type CreateUserReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"`
	Avatar   string `json:"avatar"`
}

// UpdateUserReq 管理员更新用户。
type UpdateUserReq struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	Role     *string `json:"role"`
	Avatar   *string `json:"avatar"`
}

// UserResp 用户响应。
type UserResp struct {
	ID         string    `json:"id"`
	Username   string    `json:"username"`
	Role       string    `json:"role"`
	Avatar     string    `json:"avatar"`
	CreateTime time.Time `json:"createTime"`
	UpdateTime time.Time `json:"updateTime"`
}

// PerformanceResp RAG 性能统计响应。
type PerformanceResp struct {
	AvgLatencyMs int64   `json:"avgLatencyMs"`
	SuccessRate  float64 `json:"successRate"`
	ErrorRate    float64 `json:"errorRate"`
	TotalTraces  int64   `json:"totalTraces"`
}

// TrendsResp 趋势数据响应。
type TrendsResp struct {
	Metric      string        `json:"metric"`
	Window      string        `json:"window"`
	Granularity string        `json:"granularity"`
	Series      []TrendSeries `json:"series"`
}

// TrendSeries 趋势序列。
type TrendSeries struct {
	Name string       `json:"name"`
	Data []TrendPoint `json:"data"`
}

// TrendPoint 趋势数据点。
type TrendPoint struct {
	Ts    int64   `json:"ts"`
	Value float64 `json:"value"`
}
