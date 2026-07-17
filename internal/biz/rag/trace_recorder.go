package rag

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/snowflake"

	"gorm.io/gorm"
)

const (
	traceStatusRunning   = "RUNNING"
	traceStatusSuccess   = "SUCCESS"
	traceStatusError     = "ERROR"
	traceStatusCancelled = "CANCELLED"
)

// TraceRunRecord maps to t_rag_trace_run.
type TraceRunRecord struct {
	ID             string     `gorm:"column:id;primaryKey"`
	TraceID        string     `gorm:"column:trace_id;uniqueIndex"`
	TraceName      string     `gorm:"column:trace_name"`
	EntryMethod    string     `gorm:"column:entry_method"`
	ConversationID string     `gorm:"column:conversation_id"`
	TaskID         string     `gorm:"column:task_id"`
	UserID         string     `gorm:"column:user_id"`
	Status         string     `gorm:"column:status"`
	ErrorMessage   string     `gorm:"column:error_message"`
	StartTime      *time.Time `gorm:"column:start_time"`
	EndTime        *time.Time `gorm:"column:end_time"`
	DurationMs     int64      `gorm:"column:duration_ms"`
	ExtraData      string     `gorm:"column:extra_data"`
	CreateTime     time.Time  `gorm:"column:create_time"`
	UpdateTime     time.Time  `gorm:"column:update_time"`
	Deleted        int16      `gorm:"column:deleted"`
}

func (TraceRunRecord) TableName() string { return "t_rag_trace_run" }

// TraceNodeRecord maps to t_rag_trace_node.
type TraceNodeRecord struct {
	ID           string     `gorm:"column:id;primaryKey"`
	TraceID      string     `gorm:"column:trace_id;uniqueIndex:uk_run_node"`
	NodeID       string     `gorm:"column:node_id;uniqueIndex:uk_run_node"`
	ParentNodeID string     `gorm:"column:parent_node_id"`
	Depth        int        `gorm:"column:depth"`
	NodeType     string     `gorm:"column:node_type"`
	NodeName     string     `gorm:"column:node_name"`
	ClassName    string     `gorm:"column:class_name"`
	MethodName   string     `gorm:"column:method_name"`
	Status       string     `gorm:"column:status"`
	ErrorMessage string     `gorm:"column:error_message"`
	StartTime    *time.Time `gorm:"column:start_time"`
	EndTime      *time.Time `gorm:"column:end_time"`
	DurationMs   int64      `gorm:"column:duration_ms"`
	ExtraData    string     `gorm:"column:extra_data"`
	CreateTime   time.Time  `gorm:"column:create_time"`
	UpdateTime   time.Time  `gorm:"column:update_time"`
	Deleted      int16      `gorm:"column:deleted"`
}

func (TraceNodeRecord) TableName() string { return "t_rag_trace_node" }

// TraceRecorder persists RAG trace runs and nodes.
type TraceRecorder interface {
	StartRun(ctx context.Context, conversationID, taskID string) (*TraceRunRecord, error)
	FinishRun(ctx context.Context, traceID, status string, err error) error
	StartNode(ctx context.Context, traceID, parentNodeID, nodeName, nodeType string, depth int) (*TraceNodeRecord, error)
	FinishNode(ctx context.Context, traceID, nodeID, status string, err error) error
}

// DBTraceRecorder stores trace records in PostgreSQL-compatible tables.
type DBTraceRecorder struct {
	db             *gorm.DB
	maxErrorLength int
}

// NewDBTraceRecorder creates a database-backed trace recorder.
func NewDBTraceRecorder(db *gorm.DB, maxErrorLength int) *DBTraceRecorder {
	if maxErrorLength <= 0 {
		maxErrorLength = 1000
	}
	return &DBTraceRecorder{db: db, maxErrorLength: maxErrorLength}
}

// StartRun inserts a RUNNING trace run.
func (r *DBTraceRecorder) StartRun(ctx context.Context, conversationID, taskID string) (*TraceRunRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("trace recorder db is nil")
	}
	now := time.Now()
	run := &TraceRunRecord{
		ID:             snowflake.NextIDStr(),
		TraceID:        snowflake.NextIDStr(),
		TraceName:      "rag-stream-chat",
		EntryMethod:    "GET /rag/v3/chat",
		ConversationID: conversationID,
		TaskID:         taskID,
		UserID:         traceUserID(ctx),
		Status:         traceStatusRunning,
		StartTime:      &now,
		CreateTime:     now,
		UpdateTime:     now,
	}
	if err := r.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, fmt.Errorf("start trace run: %w", err)
	}
	return run, nil
}

// FinishRun updates a trace run terminal state.
func (r *DBTraceRecorder) FinishRun(ctx context.Context, traceID, status string, err error) error {
	if r == nil || r.db == nil || strings.TrimSpace(traceID) == "" {
		return nil
	}
	now := time.Now()
	var current TraceRunRecord
	_ = r.db.WithContext(ctx).Table("t_rag_trace_run").
		Select("start_time").Where("trace_id = ?", traceID).First(&current).Error
	updates := map[string]any{
		"status":      status,
		"end_time":    now,
		"update_time": now,
		"duration_ms": durationSince(current.StartTime, now),
	}
	if err != nil {
		updates["error_message"] = r.truncateError(err)
	}
	return r.db.WithContext(ctx).Table("t_rag_trace_run").Where("trace_id = ?", traceID).Updates(updates).Error
}

// StartNode inserts a RUNNING trace node.
func (r *DBTraceRecorder) StartNode(ctx context.Context, traceID, parentNodeID, nodeName, nodeType string, depth int) (*TraceNodeRecord, error) {
	if r == nil || r.db == nil || strings.TrimSpace(traceID) == "" {
		return nil, fmt.Errorf("trace recorder is not ready")
	}
	now := time.Now()
	nodeID := snowflake.NextIDStr()
	node := &TraceNodeRecord{
		ID:           snowflake.NextIDStr(),
		TraceID:      traceID,
		NodeID:       nodeID,
		ParentNodeID: parentNodeID,
		Depth:        depth,
		NodeType:     nodeType,
		NodeName:     nodeName,
		Status:       traceStatusRunning,
		StartTime:    &now,
		CreateTime:   now,
		UpdateTime:   now,
	}
	if err := r.db.WithContext(ctx).Create(node).Error; err != nil {
		return nil, fmt.Errorf("start trace node: %w", err)
	}
	return node, nil
}

// FinishNode updates a trace node terminal state.
func (r *DBTraceRecorder) FinishNode(ctx context.Context, traceID, nodeID, status string, err error) error {
	if r == nil || r.db == nil || strings.TrimSpace(traceID) == "" || strings.TrimSpace(nodeID) == "" {
		return nil
	}
	now := time.Now()
	var current TraceNodeRecord
	_ = r.db.WithContext(ctx).Table("t_rag_trace_node").
		Select("start_time").Where("trace_id = ? AND node_id = ?", traceID, nodeID).First(&current).Error
	updates := map[string]any{
		"status":      status,
		"end_time":    now,
		"update_time": now,
		"duration_ms": durationSince(current.StartTime, now),
	}
	if err != nil {
		updates["error_message"] = r.truncateError(err)
	}
	return r.db.WithContext(ctx).Table("t_rag_trace_node").
		Where("trace_id = ? AND node_id = ?", traceID, nodeID).
		Updates(updates).Error
}

func (r *DBTraceRecorder) truncateError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len([]rune(msg)) <= r.maxErrorLength {
		return msg
	}
	return string([]rune(msg)[:r.maxErrorLength])
}

func traceUserID(ctx context.Context) string {
	user := appctx.User(ctx)
	if user == nil {
		return ""
	}
	return strings.TrimSpace(user.UserID)
}

func durationSince(start *time.Time, end time.Time) int64 {
	if start == nil || start.IsZero() {
		return 0
	}
	return end.Sub(*start).Milliseconds()
}

type traceSpan struct {
	ctx      context.Context
	recorder TraceRecorder
	traceID  string
	nodeID   string
	once     sync.Once
}

func (s *traceSpan) finish(status string, err error) {
	if s == nil || s.recorder == nil || s.nodeID == "" {
		return
	}
	s.once.Do(func() {
		_ = s.recorder.FinishNode(s.ctx, s.traceID, s.nodeID, status, err)
	})
}
