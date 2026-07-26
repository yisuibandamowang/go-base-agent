package service

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
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

type fakePipelineTaskExecutor struct {
	result TaskExecutionResult
	err    error
}

func (f fakePipelineTaskExecutor) ExecuteIngestionTask(ctx context.Context, req dto.CreateTaskReq) (int, error) {
	return f.result.ChunkCount, f.err
}

func (f fakePipelineTaskExecutor) ExecuteIngestionPipelineTask(ctx context.Context, req dto.CreateTaskReq, nodes []model.IngestionPipelineNode) (TaskExecutionResult, error) {
	return f.result, f.err
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

func TestTaskService_UploadPassesFileContentToExecutor(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	executor := &fakeTaskExecutor{chunkCount: 1}
	taskSvc.SetExecutor(executor)

	resp, err := taskSvc.Upload(t.Context(), pipelineID, newTestUploadFileHeader(t, "会员说明.md", "text/markdown", []byte("# 会员说明")), "user-1")
	if err != nil {
		t.Fatalf("upload task: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("unexpected upload response: %+v", resp)
	}

	if string(executor.req.RawBytes) != "# 会员说明" {
		t.Fatalf("expected upload raw bytes to be passed to executor, got %q", string(executor.req.RawBytes))
	}
	if !strings.HasPrefix(executor.req.MimeType, "text/markdown") {
		t.Fatalf("expected upload mime type to be passed to executor, got %q", executor.req.MimeType)
	}
}

func TestTaskService_CreatePersistsPipelineAwareNodeResults(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(fakePipelineTaskExecutor{result: TaskExecutionResult{
		ChunkCount: 4,
		Nodes: []TaskNodeExecutionResult{
			{NodeID: "fetch", NodeType: "fetcher", Status: "success", Message: "fetched", Output: map[string]any{"bytes": 12}},
			{NodeID: "index", NodeType: "indexer", Status: "success", Message: "indexed", Output: map[string]any{"chunkCount": 4}},
		},
	}})

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1", FileName: "doc.md"},
		Metadata:   map[string]any{"docId": "doc-1"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if resp.ChunkCount != 4 || resp.Status != "completed" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	nodes, err := taskSvc.Nodes(t.Context(), resp.TaskID)
	if err != nil {
		t.Fatalf("list task nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected two nodes, got %+v", nodes)
	}
	if nodes[0].NodeID != "fetch" || nodes[0].Message != "fetched" || nodes[0].Output["bytes"] == nil {
		t.Fatalf("unexpected fetch node: %+v", nodes[0])
	}
	if nodes[1].NodeID != "index" || nodes[1].Message != "indexed" || nodes[1].Output["chunkCount"] == nil {
		t.Fatalf("unexpected index node: %+v", nodes[1])
	}
}

func TestTaskService_MergesExecutionMetadataIntoTask(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(fakePipelineTaskExecutor{result: TaskExecutionResult{
		ChunkCount: 2,
		Metadata:   map[string]any{"keywords": []string{"会员", "积分"}},
	}})

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1", FileName: "doc.md"},
		Metadata:   map[string]any{"docId": "doc-1"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task, err := taskSvc.Get(t.Context(), resp.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Metadata["docId"] != "doc-1" {
		t.Fatalf("expected original metadata to be kept, got %+v", task.Metadata)
	}
	keywords, ok := task.Metadata["keywords"].([]any)
	if !ok || len(keywords) != 2 || keywords[0] != "会员" {
		t.Fatalf("expected execution metadata to be merged, got %+v", task.Metadata)
	}
}

func TestTaskService_AssignsTaskNodeOrderFromPipelineLinks(t *testing.T) {
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
	pipeline := &model.IngestionPipeline{Name: "逆序流水线", CreatedBy: "user-1", UpdatedBy: "user-1"}
	if err := gdb.Create(pipeline).Error; err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	for _, node := range []*model.IngestionPipelineNode{
		{PipelineID: pipeline.ID, NodeID: "index", NodeType: "indexer", CreatedBy: "user-1", UpdatedBy: "user-1"},
		{PipelineID: pipeline.ID, NodeID: "fetch", NodeType: "fetcher", NextNodeID: "index", CreatedBy: "user-1", UpdatedBy: "user-1"},
	} {
		if err := gdb.Create(node).Error; err != nil {
			t.Fatalf("create pipeline node: %v", err)
		}
	}
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(fakePipelineTaskExecutor{result: TaskExecutionResult{
		ChunkCount: 1,
		Nodes: []TaskNodeExecutionResult{
			{NodeID: "fetch", NodeType: "fetcher", Status: "success", Message: "fetched"},
			{NodeID: "index", NodeType: "indexer", Status: "success", Message: "indexed"},
		},
	}})

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipeline.ID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1", FileName: "doc.md"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	nodes, err := taskSvc.Nodes(t.Context(), resp.TaskID)
	if err != nil {
		t.Fatalf("list task nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected two task nodes, got %+v", nodes)
	}
	if nodes[0].NodeID != "fetch" || nodes[0].NodeOrder != 1 || nodes[1].NodeID != "index" || nodes[1].NodeOrder != 2 {
		t.Fatalf("expected task nodes ordered by nextNodeId chain, got %+v", nodes)
	}
}

func TestTaskService_MarksSkippedNodeFromSkippedMessage(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(fakePipelineTaskExecutor{result: TaskExecutionResult{
		ChunkCount: 1,
		Nodes: []TaskNodeExecutionResult{
			{NodeID: "fetch", NodeType: "fetcher", Status: "SUCCESS", Message: "Skipped: condition not matched"},
			{NodeID: "index", NodeType: "indexer", Status: "success", Message: "indexed"},
		},
	}})

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1", FileName: "doc.md"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	nodes, err := taskSvc.Nodes(t.Context(), resp.TaskID)
	if err != nil {
		t.Fatalf("list task nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected two task nodes, got %+v", nodes)
	}
	if nodes[0].Status != "skipped" {
		t.Fatalf("expected skipped node status, got %+v", nodes[0])
	}
	if nodes[1].Status != "success" {
		t.Fatalf("expected normalized success node status, got %+v", nodes[1])
	}
}

func TestTaskService_TruncatesLargeNodeOutput(t *testing.T) {
	const maxOutputJSONSize = 1024 * 1024
	gdb, pipelineID := setupTaskServiceTestDB(t)
	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	taskSvc.SetExecutor(fakePipelineTaskExecutor{result: TaskExecutionResult{
		ChunkCount: 1,
		Nodes: []TaskNodeExecutionResult{
			{NodeID: "fetch", NodeType: "fetcher", Status: "success", Message: "fetched", Output: map[string]any{"payload": strings.Repeat("x", 1024*1024+256)}},
		},
	}})

	resp, err := taskSvc.Create(t.Context(), dto.CreateTaskReq{
		PipelineID: pipelineID,
		Source:     dto.DocumentSourceReq{Type: "file", Location: "doc-1", FileName: "doc.md"},
	}, "user-1")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	var node model.IngestionTaskNode
	if err := gdb.Where("task_id = ? AND node_id = ?", resp.TaskID, "fetch").First(&node).Error; err != nil {
		t.Fatalf("load task node: %v", err)
	}
	if len(node.OutputJSON) > maxOutputJSONSize {
		t.Fatalf("expected output json to be truncated to <= %d, got %d", maxOutputJSONSize, len(node.OutputJSON))
	}
	if !strings.Contains(node.OutputJSON, "输出过大，已截断") {
		t.Fatalf("expected truncation marker, got suffix %q", node.OutputJSON[len(node.OutputJSON)-80:])
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

func TestTaskService_CreateValidatesRequiredSourceFields(t *testing.T) {
	tests := []struct {
		name    string
		req     dto.CreateTaskReq
		wantErr string
	}{
		{
			name:    "missing pipeline id",
			req:     dto.CreateTaskReq{Source: dto.DocumentSourceReq{Type: "file", Location: "doc-1"}},
			wantErr: "必须传流水线ID",
		},
		{
			name:    "missing source",
			req:     dto.CreateTaskReq{PipelineID: "missing-source-pipeline"},
			wantErr: "文档来源不能为空",
		},
		{
			name:    "missing source type",
			req:     dto.CreateTaskReq{PipelineID: "missing-source-type-pipeline", Source: dto.DocumentSourceReq{Location: "doc-1"}},
			wantErr: "文档来源类型不能为空",
		},
		{
			name:    "unknown source type",
			req:     dto.CreateTaskReq{PipelineID: "unknown-source-type-pipeline", Source: dto.DocumentSourceReq{Type: "s3", Location: "doc-1"}},
			wantErr: "未知文档来源类型: s3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, pipelineID := setupTaskServiceTestDB(t)
			tt.req.PipelineID = strings.Replace(tt.req.PipelineID, "missing-source-pipeline", pipelineID, 1)
			tt.req.PipelineID = strings.Replace(tt.req.PipelineID, "missing-source-type-pipeline", pipelineID, 1)
			tt.req.PipelineID = strings.Replace(tt.req.PipelineID, "unknown-source-type-pipeline", pipelineID, 1)

			taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
			taskSvc.SetExecutor(&fakeTaskExecutor{chunkCount: 1})
			_, err := taskSvc.Create(t.Context(), tt.req, "user-1")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %q, got %v", tt.wantErr, err)
			}
			var count int64
			if err := gdb.Model(&model.IngestionTask{}).Count(&count).Error; err != nil {
				t.Fatalf("count tasks: %v", err)
			}
			if count != 0 {
				t.Fatalf("expected invalid task not to be created, got %d", count)
			}
		})
	}
}

func TestTaskService_NormalizesTaskAndNodeFieldsOnResponse(t *testing.T) {
	gdb, pipelineID := setupTaskServiceTestDB(t)
	task := &model.IngestionTask{
		PipelineID:     pipelineID,
		SourceType:     "LOCAL-FILE",
		SourceLocation: "doc-1",
		SourceFileName: "doc.md",
		Status:         "COMPLETED",
		CreatedBy:      "user-1",
		UpdatedBy:      "user-1",
	}
	if err := gdb.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := gdb.Create(&model.IngestionTaskNode{
		TaskID:     task.ID,
		PipelineID: pipelineID,
		NodeID:     "fetch",
		NodeType:   "FETCHER",
		NodeOrder:  1,
		Status:     "SUCCESS",
	}).Error; err != nil {
		t.Fatalf("create task node: %v", err)
	}

	taskSvc := NewTaskService(repo.NewTaskRepo(gdb), NewPipelineService(repo.NewPipelineRepo(gdb), gdb), gdb)
	resp, err := taskSvc.Get(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if resp.SourceType != "file" || resp.Status != "completed" {
		t.Fatalf("expected normalized task response, got %+v", resp)
	}
	nodes, err := taskSvc.Nodes(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("list task nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeType != "fetcher" || nodes[0].Status != "success" {
		t.Fatalf("expected normalized task node response, got %+v", nodes)
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

func newTestUploadFileHeader(t *testing.T, filename, contentType string, data []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create upload part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write upload part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest("POST", "/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(int64(len(data)) + 1024); err != nil {
		t.Fatalf("parse multipart form: %v", err)
	}
	files := req.MultipartForm.File["file"]
	if len(files) != 1 {
		t.Fatalf("expected one upload file, got %d", len(files))
	}
	return files[0]
}
