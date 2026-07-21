package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"strings"
	"time"

	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"

	"gorm.io/gorm"
)

// PipelineService 是摄取流水线业务服务。
type PipelineService struct {
	repo          *repo.PipelineRepo
	db            *gorm.DB
	auditRecorder *auditService.BizChangeLogService
}

// NewPipelineService 创建 PipelineService。
func NewPipelineService(pipelineRepo *repo.PipelineRepo, database *gorm.DB) *PipelineService {
	return &PipelineService{repo: pipelineRepo, db: database}
}

// SetAuditRecorder 设置审计日志记录器。
func (s *PipelineService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// Create 创建摄取流水线。
func (s *PipelineService) Create(ctx context.Context, req dto.CreatePipelineReq, userID string) (*dto.PipelineResp, error) {
	pipeline := &model.IngestionPipeline{
		Name:        req.Name,
		Description: req.Description,
		CreatedBy:   userID,
		UpdatedBy:   userID,
	}
	if err := s.repo.Create(ctx, pipeline); err != nil {
		return nil, fmt.Errorf("创建摄取流水线失败: %w", err)
	}
	if err := s.repo.ReplaceNodes(ctx, pipeline.ID, toPipelineNodes(pipeline.ID, req.Nodes, userID)); err != nil {
		return nil, err
	}
	resp, err := s.Get(ctx, pipeline.ID)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeIngestionPipeline,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建摄取流水线：" + resp.Name,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// Update 更新摄取流水线。
func (s *PipelineService) Update(ctx context.Context, id string, req dto.UpdatePipelineReq, userID string) (*dto.PipelineResp, error) {
	before, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	pipeline, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Name != "" {
		pipeline.Name = req.Name
	}
	if req.Description != nil {
		pipeline.Description = *req.Description
	}
	pipeline.UpdatedBy = userID
	if err := s.repo.Update(ctx, pipeline); err != nil {
		return nil, err
	}
	if req.Nodes != nil {
		if err := s.repo.ReplaceNodes(ctx, pipeline.ID, toPipelineNodes(pipeline.ID, req.Nodes, userID)); err != nil {
			return nil, err
		}
	}
	resp, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeIngestionPipeline,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新摄取流水线：" + resp.Name,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// Get 查询摄取流水线详情。
func (s *PipelineService) Get(ctx context.Context, id string) (*dto.PipelineResp, error) {
	pipeline, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	nodes, err := s.repo.ListNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	return pipelineToResp(pipeline, nodes), nil
}

// List 分页查询摄取流水线。
func (s *PipelineService) List(ctx context.Context, page, size int, keyword string) ([]dto.PipelineResp, int64, error) {
	items, total, err := s.repo.List(ctx, page, size, keyword)
	if err != nil {
		return nil, 0, err
	}
	resp := make([]dto.PipelineResp, 0, len(items))
	for _, item := range items {
		nodes, err := s.repo.ListNodes(ctx, item.ID)
		if err != nil {
			return nil, 0, err
		}
		resp = append(resp, *pipelineToResp(&item, nodes))
	}
	return resp, total, nil
}

// Delete 删除摄取流水线。
func (s *PipelineService) Delete(ctx context.Context, id string) error {
	before, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeIngestionPipeline,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除摄取流水线：" + before.Name,
		BeforeSnapshot: before,
	})
	return nil
}

// DefinitionNodes 返回流水线节点定义。
func (s *PipelineService) DefinitionNodes(ctx context.Context, id string) ([]model.IngestionPipelineNode, error) {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.ListNodes(ctx, id)
}

// TaskService 是摄取任务业务服务。
type TaskService struct {
	repo          *repo.TaskRepo
	pipelineSvc   *PipelineService
	db            *gorm.DB
	executor      TaskExecutor
	auditRecorder *auditService.BizChangeLogService
}

// NewTaskService 创建 TaskService。
func NewTaskService(taskRepo *repo.TaskRepo, pipelineSvc *PipelineService, database *gorm.DB) *TaskService {
	return &TaskService{repo: taskRepo, pipelineSvc: pipelineSvc, db: database}
}

// TaskExecutor 执行实际的数据摄取和索引动作。
type TaskExecutor interface {
	ExecuteIngestionTask(ctx context.Context, req dto.CreateTaskReq) (int, error)
}

// PipelineTaskExecutor can return per-node execution results for a pipeline task.
type PipelineTaskExecutor interface {
	ExecuteIngestionPipelineTask(ctx context.Context, req dto.CreateTaskReq, nodes []model.IngestionPipelineNode) (TaskExecutionResult, error)
}

// TaskExecutionResult contains the outcome of a pipeline task.
type TaskExecutionResult struct {
	ChunkCount int
	Nodes      []TaskNodeExecutionResult
}

// TaskNodeExecutionResult contains the outcome of one pipeline node.
type TaskNodeExecutionResult struct {
	NodeID       string
	NodeType     string
	Status       string
	DurationMs   int64
	Message      string
	ErrorMessage string
	Output       map[string]any
}

// SetExecutor 设置摄取任务的实际执行器。
func (s *TaskService) SetExecutor(executor TaskExecutor) {
	s.executor = executor
}

// SetAuditRecorder 设置审计日志记录器。
func (s *TaskService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// Create 创建并执行摄取任务。
func (s *TaskService) Create(ctx context.Context, req dto.CreateTaskReq, userID string) (*dto.IngestionResultResp, error) {
	nodes, err := s.pipelineSvc.DefinitionNodes(ctx, req.PipelineID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	task := &model.IngestionTask{
		PipelineID:     req.PipelineID,
		SourceType:     normalizeSourceType(req.Source.Type),
		SourceLocation: req.Source.Location,
		SourceFileName: req.Source.FileName,
		Status:         "running",
		StartedAt:      &now,
		CreatedBy:      userID,
		UpdatedBy:      userID,
		MetadataJSON:   writeJSON(req.Metadata),
	}
	if task.SourceType == "" {
		task.SourceType = "url"
	}
	if err := s.repo.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("创建摄取任务失败: %w", err)
	}

	if s.executor == nil {
		err := fmt.Errorf("摄取任务执行器未配置")
		if updateErr := s.markTaskFailed(ctx, task, nodes, err.Error()); updateErr != nil {
			return nil, updateErr
		}
		s.recordAudit(ctx, auditService.RecordReq{
			BizType:       auditService.BizTypeIngestionTask,
			BizID:         task.ID,
			OperationType: auditService.OperationRun,
			ActionDesc:    "执行摄取任务：" + task.SourceFileName,
			AfterSnapshot: taskToResp(task),
			Success:       boolPtr(false),
			ErrorMessage:  err.Error(),
		})
		return nil, err
	}

	execStart := time.Now()
	execResult := TaskExecutionResult{}
	if pipelineExecutor, ok := s.executor.(PipelineTaskExecutor); ok {
		execResult, err = pipelineExecutor.ExecuteIngestionPipelineTask(ctx, req, nodes)
	} else {
		chunkCount, execErr := s.executor.ExecuteIngestionTask(ctx, req)
		err = execErr
		execResult = TaskExecutionResult{ChunkCount: chunkCount}
	}
	durationMs := time.Since(execStart).Milliseconds()
	if err != nil {
		msg := fmt.Sprintf("执行摄取任务失败: %v", err)
		if updateErr := s.markTaskFailed(ctx, task, nodes, msg); updateErr != nil {
			return nil, updateErr
		}
		s.recordAudit(ctx, auditService.RecordReq{
			BizType:       auditService.BizTypeIngestionTask,
			BizID:         task.ID,
			OperationType: auditService.OperationRun,
			ActionDesc:    "执行摄取任务：" + task.SourceFileName,
			AfterSnapshot: taskToResp(task),
			Success:       boolPtr(false),
			ErrorMessage:  msg,
		})
		return nil, fmt.Errorf("执行摄取任务失败: %w", err)
	}

	logs := make([]map[string]any, 0, len(nodes))
	nodeResultMap := make(map[string]TaskNodeExecutionResult, len(execResult.Nodes))
	for _, nodeResult := range execResult.Nodes {
		nodeResultMap[nodeResult.NodeID] = nodeResult
	}
	for idx, node := range nodes {
		nodeResult, ok := nodeResultMap[node.NodeID]
		if !ok {
			nodeResult = TaskNodeExecutionResult{
				NodeID:     node.NodeID,
				NodeType:   node.NodeType,
				Status:     "success",
				DurationMs: durationMs,
				Message:    "OK",
				Output:     map[string]any{"chunkCount": execResult.ChunkCount},
			}
		}
		status := firstNonEmpty(nodeResult.Status, "success")
		message := firstNonEmpty(nodeResult.Message, "OK")
		nodeDuration := nodeResult.DurationMs
		if nodeDuration == 0 {
			nodeDuration = durationMs
		}
		taskNode := newTaskNode(task, node, idx+1, status, nodeDuration, message, nodeResult.ErrorMessage, nodeResult.Output)
		if err := s.repo.CreateNode(ctx, taskNode); err != nil {
			return nil, fmt.Errorf("创建摄取任务节点失败: %w", err)
		}
		logs = append(logs, taskNodeLog(node, status == "success", message, nodeDuration))
	}
	completed := time.Now()
	task.Status = "completed"
	task.CompletedAt = &completed
	task.LogsJSON = writeJSON(logs)
	task.ChunkCount = execResult.ChunkCount
	if err := s.repo.Update(ctx, task); err != nil {
		return nil, fmt.Errorf("更新摄取任务失败: %w", err)
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeIngestionTask,
		BizID:         task.ID,
		OperationType: auditService.OperationRun,
		ActionDesc:    "执行摄取任务：" + task.SourceFileName,
		AfterSnapshot: taskToResp(task),
	})
	return &dto.IngestionResultResp{
		TaskID:     task.ID,
		PipelineID: task.PipelineID,
		Status:     task.Status,
		ChunkCount: task.ChunkCount,
		Message:    "OK",
	}, nil
}

func (s *TaskService) markTaskFailed(ctx context.Context, task *model.IngestionTask, nodes []model.IngestionPipelineNode, message string) error {
	logs := make([]map[string]any, 0, len(nodes))
	for idx, node := range nodes {
		status := "skipped"
		nodeMessage := "SKIPPED"
		errorMessage := ""
		if idx == 0 {
			status = "failed"
			nodeMessage = "FAILED"
			errorMessage = message
		}
		taskNode := newTaskNode(task, node, idx+1, status, 0, nodeMessage, errorMessage, nil)
		if err := s.repo.CreateNode(ctx, taskNode); err != nil {
			return fmt.Errorf("创建摄取任务节点失败: %w", err)
		}
		logMessage := nodeMessage
		if errorMessage != "" {
			logMessage = errorMessage
		}
		logs = append(logs, taskNodeLog(node, status == "success", logMessage, 0))
	}
	completed := time.Now()
	task.Status = "failed"
	task.CompletedAt = &completed
	task.ErrorMessage = message
	task.LogsJSON = writeJSON(logs)
	if err := s.repo.Update(ctx, task); err != nil {
		return fmt.Errorf("更新摄取任务失败: %w", err)
	}
	return nil
}

func (s *TaskService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

func (s *PipelineService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func newTaskNode(task *model.IngestionTask, node model.IngestionPipelineNode, order int, status string, durationMs int64, message, errorMessage string, output map[string]any) *model.IngestionTaskNode {
	return &model.IngestionTaskNode{
		TaskID:       task.ID,
		PipelineID:   task.PipelineID,
		NodeID:       node.NodeID,
		NodeType:     node.NodeType,
		NodeOrder:    order,
		Status:       status,
		DurationMs:   durationMs,
		Message:      message,
		ErrorMessage: errorMessage,
		OutputJSON:   writeJSON(output),
	}
}

func taskNodeLog(node model.IngestionPipelineNode, success bool, message string, durationMs int64) map[string]any {
	return map[string]any{
		"nodeId":     node.NodeID,
		"nodeType":   node.NodeType,
		"success":    success,
		"message":    message,
		"durationMs": durationMs,
	}
}

// Upload 上传文件并执行摄取任务。
func (s *TaskService) Upload(ctx context.Context, pipelineID string, header *multipart.FileHeader, userID string) (*dto.IngestionResultResp, error) {
	if header == nil {
		return nil, fmt.Errorf("文件不能为空")
	}
	return s.Create(ctx, dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source: dto.DocumentSourceReq{
			Type:     "file",
			Location: header.Filename,
			FileName: header.Filename,
		},
	}, userID)
}

// Get 查询摄取任务详情。
func (s *TaskService) Get(ctx context.Context, id string) (*dto.TaskResp, error) {
	task, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return taskToResp(task), nil
}

// List 分页查询摄取任务。
func (s *TaskService) List(ctx context.Context, page, size int, status string) ([]dto.TaskResp, int64, error) {
	items, total, err := s.repo.List(ctx, page, size, normalizeStatus(status))
	if err != nil {
		return nil, 0, err
	}
	resp := make([]dto.TaskResp, 0, len(items))
	for _, item := range items {
		resp = append(resp, *taskToResp(&item))
	}
	return resp, total, nil
}

// Nodes 查询摄取任务节点。
func (s *TaskService) Nodes(ctx context.Context, taskID string) ([]dto.TaskNodeResp, error) {
	nodes, err := s.repo.ListNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	resp := make([]dto.TaskNodeResp, 0, len(nodes))
	for _, node := range nodes {
		resp = append(resp, *taskNodeToResp(&node))
	}
	return resp, nil
}

func toPipelineNodes(pipelineID string, reqs []dto.PipelineNodeReq, userID string) []*model.IngestionPipelineNode {
	nodes := make([]*model.IngestionPipelineNode, 0, len(reqs))
	for _, req := range reqs {
		nodes = append(nodes, &model.IngestionPipelineNode{
			PipelineID:    pipelineID,
			NodeID:        req.NodeID,
			NodeType:      normalizeNodeType(req.NodeType),
			NextNodeID:    req.NextNodeID,
			SettingsJSON:  writeJSON(req.Settings),
			ConditionJSON: writeJSON(req.Condition),
			CreatedBy:     userID,
			UpdatedBy:     userID,
		})
	}
	return nodes
}

func pipelineToResp(pipeline *model.IngestionPipeline, nodes []model.IngestionPipelineNode) *dto.PipelineResp {
	resp := &dto.PipelineResp{
		ID:          pipeline.ID,
		Name:        pipeline.Name,
		Description: pipeline.Description,
		CreatedBy:   pipeline.CreatedBy,
		Nodes:       make([]dto.PipelineNodeResp, 0, len(nodes)),
		CreateTime:  pipeline.CreateTime,
		UpdateTime:  pipeline.UpdateTime,
	}
	for _, node := range nodes {
		resp.Nodes = append(resp.Nodes, dto.PipelineNodeResp{
			ID:         node.ID,
			NodeID:     node.NodeID,
			NodeType:   node.NodeType,
			Settings:   readMap(node.SettingsJSON),
			Condition:  readMap(node.ConditionJSON),
			NextNodeID: node.NextNodeID,
		})
	}
	return resp
}

func taskToResp(task *model.IngestionTask) *dto.TaskResp {
	return &dto.TaskResp{
		ID:             task.ID,
		PipelineID:     task.PipelineID,
		SourceType:     task.SourceType,
		SourceLocation: task.SourceLocation,
		SourceFileName: task.SourceFileName,
		Status:         task.Status,
		ChunkCount:     task.ChunkCount,
		ErrorMessage:   task.ErrorMessage,
		Logs:           readList(task.LogsJSON),
		Metadata:       readMap(task.MetadataJSON),
		StartedAt:      task.StartedAt,
		CompletedAt:    task.CompletedAt,
		CreatedBy:      task.CreatedBy,
		CreateTime:     task.CreateTime,
		UpdateTime:     task.UpdateTime,
	}
}

func taskNodeToResp(node *model.IngestionTaskNode) *dto.TaskNodeResp {
	return &dto.TaskNodeResp{
		ID:           node.ID,
		TaskID:       node.TaskID,
		PipelineID:   node.PipelineID,
		NodeID:       node.NodeID,
		NodeType:     node.NodeType,
		NodeOrder:    node.NodeOrder,
		Status:       node.Status,
		DurationMs:   node.DurationMs,
		Message:      node.Message,
		ErrorMessage: node.ErrorMessage,
		Output:       readMap(node.OutputJSON),
		CreateTime:   node.CreateTime,
		UpdateTime:   node.UpdateTime,
	}
}

func normalizeNodeType(nodeType string) string {
	v := strings.TrimSpace(strings.ToLower(nodeType))
	return strings.ReplaceAll(v, "-", "_")
}

func normalizeSourceType(sourceType string) string {
	v := strings.TrimSpace(strings.ToLower(sourceType))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "localfile", "local_file":
		return "file"
	default:
		return v
	}
}

func normalizeStatus(status string) string {
	v := strings.TrimSpace(strings.ToLower(status))
	return strings.ReplaceAll(v, "-", "_")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func readMap(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func readList(raw string) []map[string]any {
	if strings.TrimSpace(raw) == "" {
		return []map[string]any{}
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []map[string]any{}
	}
	return out
}
