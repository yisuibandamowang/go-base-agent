package service

import (
	"context"
	"errors"
	"testing"

	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"

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
