package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-base-agent/internal/biz/admin/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// AdminRepo 管理后台数据访问层。
type AdminRepo struct {
	db *gorm.DB
}

// NewAdminRepo 创建 AdminRepo。
func NewAdminRepo(database *gorm.DB) *AdminRepo {
	return &AdminRepo{db: database}
}

// DashboardStats 系统仪表盘统计数据。
type DashboardStats struct {
	KnowledgeBaseCount int64 `json:"knowledgeBaseCount"`
	DocumentCount      int64 `json:"documentCount"`
	ChunkCount         int64 `json:"chunkCount"`
	UserCount          int64 `json:"userCount"`
	ConversationCount  int64 `json:"conversationCount"`
	MessageCount       int64 `json:"messageCount"`
	VectorCount        int64 `json:"vectorCount"`
}

// GetDashboard 查询各项统计数。
func (r *AdminRepo) GetDashboard(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}

	tables := map[string]*int64{
		"t_knowledge_base":     &stats.KnowledgeBaseCount,
		"t_knowledge_document": &stats.DocumentCount,
		"t_knowledge_chunk":    &stats.ChunkCount,
		"t_user":               &stats.UserCount,
		"t_conversation":       &stats.ConversationCount,
		"t_message":            &stats.MessageCount,
		"t_knowledge_vector":   &stats.VectorCount,
	}

	for table, dest := range tables {
		if err := r.db.WithContext(ctx).Table(table).
			Where("deleted = 0").Count(dest).Error; err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
	}
	return stats, nil
}

// TraceRun 链路追踪运行记录（从 t_rag_trace_run 查询）。
type TraceRun struct {
	ID             string     `gorm:"column:id" json:"id"`
	TraceID        string     `gorm:"column:trace_id" json:"traceId"`
	TraceName      string     `gorm:"column:trace_name" json:"traceName"`
	EntryMethod    string     `gorm:"column:entry_method" json:"entryMethod"`
	ConversationID string     `gorm:"column:conversation_id" json:"conversationId"`
	TaskID         string     `gorm:"column:task_id" json:"taskId"`
	UserID         string     `gorm:"column:user_id" json:"userId"`
	Status         string     `gorm:"column:status" json:"status"`
	ErrorMessage   string     `gorm:"column:error_message" json:"errorMessage"`
	StartTime      *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime        *time.Time `gorm:"column:end_time" json:"endTime"`
	DurationMs     int64      `gorm:"column:duration_ms" json:"durationMs"`
	ExtraData      string     `gorm:"column:extra_data" json:"extraData"`
}

// TraceRunFilter 链路追踪运行记录查询条件。
type TraceRunFilter struct {
	TraceID        string
	ConversationID string
	TaskID         string
	Status         string
}

// TraceNode 链路追踪节点（从 t_rag_trace_node 查询）。
type TraceNode struct {
	ID           string     `gorm:"column:id" json:"id"`
	TraceID      string     `gorm:"column:trace_id" json:"traceId"`
	NodeID       string     `gorm:"column:node_id" json:"nodeId"`
	ParentNodeID string     `gorm:"column:parent_node_id" json:"parentNodeId"`
	Depth        int        `gorm:"column:depth" json:"depth"`
	NodeType     string     `gorm:"column:node_type" json:"nodeType"`
	NodeName     string     `gorm:"column:node_name" json:"nodeName"`
	ClassName    string     `gorm:"column:class_name" json:"className"`
	MethodName   string     `gorm:"column:method_name" json:"methodName"`
	Status       string     `gorm:"column:status" json:"status"`
	ErrorMessage string     `gorm:"column:error_message" json:"errorMessage"`
	StartTime    *time.Time `gorm:"column:start_time" json:"startTime"`
	EndTime      *time.Time `gorm:"column:end_time" json:"endTime"`
	DurationMs   int64      `gorm:"column:duration_ms" json:"durationMs"`
}

// ListTraceRuns 分页查询链路追踪运行记录。
func (r *AdminRepo) ListTraceRuns(ctx context.Context, page, size int, filter TraceRunFilter) ([]TraceRun, int64, error) {
	var (
		runs  []TraceRun
		total int64
	)
	query := r.db.WithContext(ctx).Table("t_rag_trace_run").Where("deleted = 0")
	query = applyTraceRunFilter(query, filter)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count trace runs: %w", err)
	}
	err := query.Limit(size).Offset((page - 1) * size).
		Order("start_time DESC, id DESC").
		Find(&runs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list trace runs: %w", err)
	}
	return runs, total, nil
}

func applyTraceRunFilter(query *gorm.DB, filter TraceRunFilter) *gorm.DB {
	if traceID := strings.TrimSpace(filter.TraceID); traceID != "" {
		query = query.Where("trace_id = ?", traceID)
	}
	if conversationID := strings.TrimSpace(filter.ConversationID); conversationID != "" {
		query = query.Where("conversation_id = ?", conversationID)
	}
	if taskID := strings.TrimSpace(filter.TaskID); taskID != "" {
		query = query.Where("task_id = ?", taskID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	return query
}

// GetTraceNodes 根据 trace_id 查询节点。
func (r *AdminRepo) GetTraceNodes(ctx context.Context, traceID string) ([]TraceNode, error) {
	var nodes []TraceNode
	err := r.db.WithContext(ctx).Table("t_rag_trace_node").
		Where("trace_id = ? AND deleted = 0", traceID).
		Order("start_time ASC, id ASC").
		Find(&nodes).Error
	if err != nil {
		return nil, fmt.Errorf("get trace nodes: %w", err)
	}
	return nodes, nil
}

// GetTraceRun 根据 trace_id 查询单条运行记录。
func (r *AdminRepo) GetTraceRun(ctx context.Context, traceID string) (*TraceRun, error) {
	var run TraceRun
	err := r.db.WithContext(ctx).Table("t_rag_trace_run").
		Where("trace_id = ? AND deleted = 0", traceID).
		First(&run).Error
	if err != nil {
		return nil, fmt.Errorf("get trace run: %w", err)
	}
	return &run, nil
}

// SampleQuestionRepo 示例问题数据访问层。
type SampleQuestionRepo struct {
	db *gorm.DB
}

// NewSampleQuestionRepo 创建 SampleQuestionRepo。
func NewSampleQuestionRepo(database *gorm.DB) *SampleQuestionRepo {
	return &SampleQuestionRepo{db: database}
}

// Create 创建示例问题。
func (r *SampleQuestionRepo) Create(ctx context.Context, sq *model.SampleQuestion) error {
	return r.db.WithContext(ctx).Create(sq).Error
}

// List 分页查询示例问题。
func (r *SampleQuestionRepo) List(ctx context.Context, page, size int, keyword string) ([]model.SampleQuestion, int64, error) {
	var (
		items []model.SampleQuestion
		total int64
	)
	query := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Model(&model.SampleQuestion{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("title LIKE ? OR description LIKE ? OR question LIKE ?", like, like, like)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count sample questions: %w", err)
	}
	err := query.Scopes(db.Paginate(page, size)).Order("update_time DESC").Find(&items).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list sample questions: %w", err)
	}
	return items, total, nil
}

// ListRandom 随机查询示例问题。
func (r *SampleQuestionRepo) ListRandom(ctx context.Context, limit int) ([]model.SampleQuestion, error) {
	var items []model.SampleQuestion
	if limit < 1 {
		limit = 3
	}
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Order("RANDOM()").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list random sample questions: %w", err)
	}
	return items, nil
}

// FindByID 根据 ID 查询。
func (r *SampleQuestionRepo) FindByID(ctx context.Context, id string) (*model.SampleQuestion, error) {
	var sq model.SampleQuestion
	err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).Where("id = ?", id).First(&sq).Error
	if err != nil {
		return nil, fmt.Errorf("find sample question: %w", err)
	}
	return &sq, nil
}

// Update 更新示例问题。
func (r *SampleQuestionRepo) Update(ctx context.Context, sq *model.SampleQuestion) error {
	return r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Model(sq).Where("id = ?", sq.ID).
		Select("*").Omit("id", "create_time").
		Updates(sq).Error
}

// SoftDelete 软删除。
func (r *SampleQuestionRepo) SoftDelete(ctx context.Context, id string) error {
	var sq model.SampleQuestion
	sq.ID = id
	return db.SoftDelete(r.db.WithContext(ctx), &sq)
}
