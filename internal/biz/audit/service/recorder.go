package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	auditModel "go-base-agent/internal/biz/audit/model"
	appctx "go-base-agent/internal/framework/context"
)

const (
	// BizTypeUser 用户业务对象。
	BizTypeUser = "USER"
	// BizTypeSampleQuestion 示例问题业务对象。
	BizTypeSampleQuestion = "SAMPLE_QUESTION"
	// BizTypeKnowledgeBase 知识库业务对象。
	BizTypeKnowledgeBase = "KNOWLEDGE_BASE"
	// BizTypeKnowledgeDocument 知识库文档业务对象。
	BizTypeKnowledgeDocument = "KNOWLEDGE_DOCUMENT"
	// BizTypeKnowledgeChunk 知识库分块业务对象。
	BizTypeKnowledgeChunk = "KNOWLEDGE_CHUNK"
	// BizTypeIngestionPipeline 摄取流水线业务对象。
	BizTypeIngestionPipeline = "INGESTION_PIPELINE"
	// BizTypeIngestionTask 摄取任务业务对象。
	BizTypeIngestionTask = "INGESTION_TASK"
	// BizTypeIntentTree 意图树业务对象。
	BizTypeIntentTree = "INTENT_TREE"
	// BizTypeQueryTermMapping 查询词映射业务对象。
	BizTypeQueryTermMapping = "QUERY_TERM_MAPPING"
)

const (
	// OperationCreate 创建操作。
	OperationCreate = "CREATE"
	// OperationUpdate 更新操作。
	OperationUpdate = "UPDATE"
	// OperationDelete 删除操作。
	OperationDelete = "DELETE"
	// OperationEnable 启用操作。
	OperationEnable = "ENABLE"
	// OperationDisable 禁用操作。
	OperationDisable = "DISABLE"
	// OperationRun 执行操作。
	OperationRun = "RUN"
)

// RecordReq 业务变更审计记录请求。
type RecordReq struct {
	BizType        string
	BizID          string
	OperationType  string
	ActionDesc     string
	BeforeSnapshot any
	AfterSnapshot  any
	ChangeDiff     any
	Success        *bool
	ErrorMessage   string
	ClassName      string
	MethodName     string
	IP             string
	UserAgent      string
}

// Record 写入业务变更审计日志。
func (s *BizChangeLogService) Record(ctx context.Context, req RecordReq) error {
	success := true
	if req.Success != nil {
		success = *req.Success
	}
	user := appctx.User(ctx)
	operatorID := "SYSTEM"
	operatorName := ""
	operatorRole := ""
	if user != nil {
		operatorID = firstNonEmpty(user.UserID, operatorID)
		operatorName = user.Username
		operatorRole = user.Role
	}

	item := &auditModel.BizChangeLog{
		BizType:        limit(req.BizType, 64),
		BizId:          limit(firstNonEmpty(req.BizID, "UNKNOWN"), 64),
		OperationType:  limit(req.OperationType, 32),
		ActionDesc:     limit(req.ActionDesc, 512),
		BeforeSnapshot: mustJSON(req.BeforeSnapshot),
		AfterSnapshot:  mustJSON(req.AfterSnapshot),
		ChangeDiff:     mustJSON(req.ChangeDiff),
		OperatorID:     limit(operatorID, 64),
		OperatorName:   limit(operatorName, 128),
		OperatorRole:   limit(operatorRole, 64),
		Success:        success,
		ErrorMessage:   limit(req.ErrorMessage, 512),
		ClassName:      limit(req.ClassName, 255),
		MethodName:     limit(req.MethodName, 255),
		IP:             limit(req.IP, 64),
		UserAgent:      limit(req.UserAgent, 512),
	}
	if strings.TrimSpace(item.BizType) == "" {
		return fmt.Errorf("biz type is empty")
	}
	if strings.TrimSpace(item.OperationType) == "" {
		return fmt.Errorf("operation type is empty")
	}
	return s.repo.Create(ctx, item)
}

func mustJSON(value any) string {
	if value == nil {
		return "null"
	}
	if raw, ok := value.(string); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return `""`
		}
		if json.Valid([]byte(trimmed)) {
			return trimmed
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return `""`
		}
		return string(data)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func limit(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength]
}
