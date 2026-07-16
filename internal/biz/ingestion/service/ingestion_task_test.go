package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeTaskExecutor struct {
	req        dto.CreateTaskReq
	chunkCount int
	err        error
}

func (f *fakeTaskExecutor) ExecuteIngestionTask(ctx context.Context, req dto.CreateTaskReq) (int, error) {
	f.req = req
	return f.chunkCount, f.err
}

func TestTaskService_CreateExecutesInjectedExecutor(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	executor := &fakeTaskExecutor{chunkCount: 3}
	taskSvc.SetExecutor(executor)

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source: dto.DocumentSourceReq{
			Type:     "file",
			Location: "doc-1",
			FileName: "会员Agent说明.md",
		},
		Metadata: map[string]any{"docId": "doc-1"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if resp.Status != "completed" || resp.ChunkCount != 3 {
		t.Fatalf("unexpected task response: %+v", resp)
	}
	if executor.req.Metadata["docId"] != "doc-1" {
		t.Fatalf("executor did not receive original request: %+v", executor.req)
	}

	nodes, err := taskSvc.Nodes(t.Context(), resp.TaskID)
	if err != nil {
		t.Fatalf("list task nodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Status != "success" || nodes[1].Status != "success" {
		t.Fatalf("unexpected task nodes: %+v", nodes)
	}
}

func TestTaskService_CreateMarksTaskFailedWhenExecutorFails(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(&fakeTaskExecutor{err: errors.New("embed failed")})

	_, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1"},
	}, "user-1")
	if err == nil {
		t.Fatal("expected executor error")
	}

	tasks, _, listErr := taskSvc.List(t.Context(), 1, 10, "failed")
	if listErr != nil {
		t.Fatalf("list failed tasks: %v", listErr)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" || tasks[0].ErrorMessage == "" {
		t.Fatalf("expected failed task with error message, got %+v", tasks)
	}
	nodes, nodeErr := taskSvc.Nodes(t.Context(), tasks[0].ID)
	if nodeErr != nil {
		t.Fatalf("list task nodes: %v", nodeErr)
	}
	if len(nodes) == 0 || nodes[0].Status != "failed" {
		t.Fatalf("expected failed task node, got %+v", nodes)
	}
}

func TestTaskService_CreateRecordsAuditLogs(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	if err := gdb.AutoMigrate(&auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate audit table: %v", err)
	}
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	taskSvc.SetExecutor(&fakeTaskExecutor{chunkCount: 2})

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})
	successResp, err := taskSvc.Create(ctx, dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source: dto.DocumentSourceReq{
			Type:     "file",
			Location: "doc-1",
			FileName: "会员Agent说明.md",
		},
		Metadata: map[string]any{"docId": "doc-1"},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create successful task: %v", err)
	}

	taskSvc.SetExecutor(&fakeTaskExecutor{err: errors.New("embed failed")})
	_, failedErr := taskSvc.Create(ctx, dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-2", FileName: "失败文档.md"},
	}, "admin-1")
	if failedErr == nil {
		t.Fatal("expected failed task error")
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ?", auditService.BizTypeIngestionTask).Order("create_time ASC").Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].BizId != successResp.TaskID || logs[0].OperationType != auditService.OperationRun ||
		!logs[0].Success || !strings.Contains(logs[0].AfterSnapshot, `"status":"completed"`) ||
		!strings.Contains(logs[0].AfterSnapshot, `"chunkCount":2`) {
		t.Fatalf("unexpected success audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationRun || logs[1].Success ||
		!strings.Contains(logs[1].ErrorMessage, "embed failed") ||
		!strings.Contains(logs[1].AfterSnapshot, `"status":"failed"`) {
		t.Fatalf("unexpected failed audit log: %+v", logs[1])
	}
}

func setupTaskServiceTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&model.IngestionPipeline{},
		&model.IngestionPipelineNode{},
		&model.IngestionTask{},
		&model.IngestionTaskNode{},
	); err != nil {
		t.Fatalf("migrate ingestion tables: %v", err)
	}
	pipeline := &model.IngestionPipeline{Name: "默认流水线", CreatedBy: "user-1", UpdatedBy: "user-1"}
	if err := gdb.Create(pipeline).Error; err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	nodes := []*model.IngestionPipelineNode{
		{PipelineID: pipeline.ID, NodeID: "fetch", NodeType: "fetcher", CreatedBy: "user-1", UpdatedBy: "user-1"},
		{PipelineID: pipeline.ID, NodeID: "index", NodeType: "indexer", CreatedBy: "user-1", UpdatedBy: "user-1"},
	}
	for _, node := range nodes {
		if err := gdb.Create(node).Error; err != nil {
			t.Fatalf("create pipeline node: %v", err)
		}
	}
	return gdb, pipeline.ID
}
