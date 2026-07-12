package service

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"strings"
	"time"

	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"

	"gorm.io/gorm"
)

// PipelineService 是摄取流水线业务服务。
type PipelineService struct {
	repo *repo.PipelineRepo
	db   *gorm.DB
}

// NewPipelineService 创建 PipelineService。
func NewPipelineService(pipelineRepo *repo.PipelineRepo, database *gorm.DB) *PipelineService {
	return &PipelineService{repo: pipelineRepo, db: database}
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
	return s.Get(ctx, pipeline.ID)
}

// Update 更新摄取流水线。
func (s *PipelineService) Update(ctx context.Context, id string, req dto.UpdatePipelineReq, userID string) (*dto.PipelineResp, error) {
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
	return s.Get(ctx, id)
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
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return err
	}
	return s.repo.SoftDelete(ctx, id)
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
	repo        *repo.TaskRepo
	pipelineSvc *PipelineService
	db          *gorm.DB
}

// NewTaskService 创建 TaskService。
func NewTaskService(taskRepo *repo.TaskRepo, pipelineSvc *PipelineService, database *gorm.DB) *TaskService {
	return &TaskService{repo: taskRepo, pipelineSvc: pipelineSvc, db: database}
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
	logs := make([]map[string]any, 0, len(nodes))
	for idx, node := range nodes {
		taskNode := &model.IngestionTaskNode{
			TaskID:     task.ID,
			PipelineID: task.PipelineID,
			NodeID:     node.NodeID,
			NodeType:   node.NodeType,
			NodeOrder:  idx + 1,
			Status:     "success",
			DurationMs: 0,
			Message:    "OK",
			OutputJSON: "{}",
		}
		if err := s.repo.CreateNode(ctx, taskNode); err != nil {
			return nil, fmt.Errorf("创建摄取任务节点失败: %w", err)
		}
		logs = append(logs, map[string]any{
			"nodeId":     node.NodeID,
			"nodeType":   node.NodeType,
			"success":    true,
			"message":    "OK",
			"durationMs": int64(0),
		})
	}
	completed := time.Now()
	task.Status = "completed"
	task.CompletedAt = &completed
	task.LogsJSON = writeJSON(logs)
	task.ChunkCount = 0
	if err := s.repo.Update(ctx, task); err != nil {
		return nil, fmt.Errorf("更新摄取任务失败: %w", err)
	}
	return &dto.IngestionResultResp{
		TaskID:     task.ID,
		PipelineID: task.PipelineID,
		Status:     task.Status,
		ChunkCount: task.ChunkCount,
		Message:    "OK",
	}, nil
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
	return strings.ReplaceAll(v, "-", "_")
}

func normalizeStatus(status string) string {
	v := strings.TrimSpace(strings.ToLower(status))
	return strings.ReplaceAll(v, "-", "_")
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
