package service

import (
	"context"
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

func TestPipelineService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}

	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.Create(ctx, dto.CreatePipelineReq{
		Name:        "默认摄取流水线",
		Description: "用于文档入库",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher", NextNodeID: "index"},
			{NodeID: "index", NodeType: "indexer"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	updatedDesc := "用于会员文档入库"
	updated, err := svc.Update(ctx, created.ID, dto.UpdatePipelineReq{
		Name:        "会员摄取流水线",
		Description: &updatedDesc,
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher", NextNodeID: "chunk"},
			{NodeID: "chunk", NodeType: "chunker", NextNodeID: "index"},
			{NodeID: "index", NodeType: "indexer"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("update pipeline: %v", err)
	}

	if err := svc.Delete(ctx, updated.ID); err != nil {
		t.Fatalf("delete pipeline: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeIngestionPipeline, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "默认摄取流水线") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "默认摄取流水线") ||
		!strings.Contains(logs[1].AfterSnapshot, "会员摄取流水线") ||
		!strings.Contains(logs[1].AfterSnapshot, "chunk") {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, "会员摄取流水线") {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}

func TestPipelineService_CreateRejectsDuplicateName(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	ctx := context.Background()

	if _, err := svc.Create(ctx, dto.CreatePipelineReq{Name: "默认摄取流水线"}, "admin-1"); err != nil {
		t.Fatalf("create first pipeline: %v", err)
	}
	_, err = svc.Create(ctx, dto.CreatePipelineReq{Name: "默认摄取流水线"}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "流水线名称已存在") {
		t.Fatalf("expected duplicate pipeline name rejection, got %v", err)
	}

	var count int64
	if err := gdb.Model(&model.IngestionPipeline{}).Where("name = ? AND deleted = 0", "默认摄取流水线").Count(&count).Error; err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one active pipeline with duplicate name, got %d", count)
	}
}

func TestPipelineService_RejectsUnknownNodeType(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	ctx := context.Background()

	_, err = svc.Create(ctx, dto.CreatePipelineReq{
		Name: "非法节点流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "magic", NodeType: "magic"},
		},
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "未知节点类型: magic") {
		t.Fatalf("expected unknown node type rejection on create, got %v", err)
	}
	var count int64
	if err := gdb.Model(&model.IngestionPipeline{}).Where("name = ? AND deleted = 0", "非法节点流水线").Count(&count).Error; err != nil {
		t.Fatalf("count failed pipeline: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected invalid pipeline not to be created, got %d", count)
	}

	created, err := svc.Create(ctx, dto.CreatePipelineReq{
		Name: "默认摄取流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "FETCHER"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create valid pipeline: %v", err)
	}
	if created.Nodes[0].NodeType != "fetcher" {
		t.Fatalf("expected node type normalized to fetcher, got %+v", created.Nodes[0])
	}

	_, err = svc.Update(ctx, created.ID, dto.UpdatePipelineReq{
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "magic", NodeType: "magic"},
		},
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "未知节点类型: magic") {
		t.Fatalf("expected unknown node type rejection on update, got %v", err)
	}
}

func TestPipelineService_NormalizesNodeTypeOnResponse(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	pipeline := &model.IngestionPipeline{Name: "历史流水线", CreatedBy: "admin-1", UpdatedBy: "admin-1"}
	if err := gdb.Create(pipeline).Error; err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if err := gdb.Create(&model.IngestionPipelineNode{
		PipelineID: pipeline.ID,
		NodeID:     "fetch",
		NodeType:   "FETCHER",
		CreatedBy:  "admin-1",
		UpdatedBy:  "admin-1",
	}).Error; err != nil {
		t.Fatalf("create pipeline node: %v", err)
	}

	resp, err := NewPipelineService(repo.NewPipelineRepo(gdb), gdb).Get(context.Background(), pipeline.ID)
	if err != nil {
		t.Fatalf("get pipeline: %v", err)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].NodeType != "fetcher" {
		t.Fatalf("expected normalized node type response, got %+v", resp.Nodes)
	}
}

func TestPipelineService_RollsBackPipelineWhenNodeReplaceFails(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	if err := gdb.Exec(`
CREATE TRIGGER fail_ingestion_node_insert
BEFORE INSERT ON t_ingestion_pipeline_node
WHEN NEW.node_id = 'boom'
BEGIN
	SELECT RAISE(ABORT, 'node insert failed');
END;
`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	ctx := context.Background()

	_, err = svc.Create(ctx, dto.CreatePipelineReq{
		Name: "失败流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "boom", NodeType: "fetcher"},
		},
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "create ingestion nodes") {
		t.Fatalf("expected node insert failure on create, got %v", err)
	}
	var count int64
	if err := gdb.Model(&model.IngestionPipeline{}).Where("name = ? AND deleted = 0", "失败流水线").Count(&count).Error; err != nil {
		t.Fatalf("count failed pipeline: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected failed create to roll back pipeline, got %d", count)
	}

	created, err := svc.Create(ctx, dto.CreatePipelineReq{
		Name: "默认摄取流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create valid pipeline: %v", err)
	}
	_, err = svc.Update(ctx, created.ID, dto.UpdatePipelineReq{
		Name: "更新后流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "boom", NodeType: "parser"},
		},
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "create ingestion nodes") {
		t.Fatalf("expected node insert failure on update, got %v", err)
	}
	reloaded, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload pipeline: %v", err)
	}
	if reloaded.Name != "默认摄取流水线" {
		t.Fatalf("expected failed update to roll back name, got %q", reloaded.Name)
	}
	if len(reloaded.Nodes) != 1 || reloaded.Nodes[0].NodeID != "fetch" {
		t.Fatalf("expected failed update to keep old nodes, got %+v", reloaded.Nodes)
	}
}

func TestPipelineService_ReplacesNodesPhysically(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	if err := gdb.Exec(`CREATE UNIQUE INDEX uk_ingestion_pipeline_node ON t_ingestion_pipeline_node (pipeline_id, node_id, deleted)`).Error; err != nil {
		t.Fatalf("create node unique index: %v", err)
	}
	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	ctx := context.Background()

	created, err := svc.Create(ctx, dto.CreatePipelineReq{
		Name: "默认摄取流水线",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	for i, nodeType := range []string{"parser", "indexer"} {
		if _, err := svc.Update(ctx, created.ID, dto.UpdatePipelineReq{
			Nodes: []dto.PipelineNodeReq{
				{NodeID: "fetch", NodeType: nodeType},
			},
		}, "admin-1"); err != nil {
			t.Fatalf("update pipeline nodes round %d: %v", i+1, err)
		}
	}

	var total int64
	if err := gdb.Model(&model.IngestionPipelineNode{}).Where("pipeline_id = ?", created.ID).Count(&total).Error; err != nil {
		t.Fatalf("count all nodes: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected old nodes to be physically replaced, got %d rows", total)
	}
	reloaded, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reload pipeline: %v", err)
	}
	if len(reloaded.Nodes) != 1 || reloaded.Nodes[0].NodeType != "indexer" {
		t.Fatalf("expected latest node only, got %+v", reloaded.Nodes)
	}
}
