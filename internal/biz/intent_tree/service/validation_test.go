package service

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newIntentValidationService(t *testing.T) *IntentService {
	svc, _ := newIntentValidationServiceWithDB(t)
	return svc
}

func newIntentValidationServiceWithDB(t *testing.T) (*IntentService, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IntentNode{}, &knowledgeModel.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	return NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb), gdb
}

func TestIntentService_CreateNodeRejectsDuplicateIntentCodeLikeJava(t *testing.T) {
	svc := newIntentValidationService(t)
	ctx := context.Background()
	if _, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.query",
		Name:       "会员查询",
		Level:      1,
	}, "user-1"); err != nil {
		t.Fatalf("create first node: %v", err)
	}

	_, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.query",
		Name:       "重复节点",
		Level:      1,
	}, "user-1")
	if err == nil || !strings.Contains(err.Error(), "意图标识已存在: member.query") {
		t.Fatalf("expected duplicate intentCode error, got %v", err)
	}
}

func TestIntentService_RejectsNonPositiveTopKLikeJava(t *testing.T) {
	svc := newIntentValidationService(t)
	ctx := context.Background()
	if _, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.invalid-topk",
		Name:       "非法 TopK",
		Level:      1,
		TopK:       0,
		TopKSet:    true,
	}, "user-1"); err == nil || !strings.Contains(err.Error(), "节点级 TopK 必须大于 0") {
		t.Fatalf("expected create topK validation error, got %v", err)
	}

	created, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.valid-topk",
		Name:       "合法 TopK",
		Level:      1,
	}, "user-1")
	if err != nil {
		t.Fatalf("create valid node without explicit topK: %v", err)
	}
	invalidTopK := 0
	if _, err := svc.UpdateNode(ctx, created.ID, dto.UpdateIntentReq{
		TopK: &invalidTopK,
	}, "user-1"); err == nil || !strings.Contains(err.Error(), "节点级 TopK 必须大于 0") {
		t.Fatalf("expected update topK validation error, got %v", err)
	}
}

func TestIntentService_CreateTopicKBNodeRequiresKBIDLikeJava(t *testing.T) {
	svc := newIntentValidationService(t)
	_, err := svc.CreateNode(context.Background(), dto.CreateIntentReq{
		IntentCode: "member.topic",
		Name:       "会员主题",
		Level:      2,
		Kind:       0,
	}, "user-1")
	if err == nil || !strings.Contains(err.Error(), "TOPIC级别的RAG检索节点必须指定目标知识库") {
		t.Fatalf("expected topic KB node kbId validation error, got %v", err)
	}
}

func TestIntentService_CreateNodeResolvesCollectionNameFromKBIDLikeJava(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	kb := &knowledgeModel.KnowledgeBase{
		Name:           "会员知识库",
		EmbeddingModel: "emb-1",
		CollectionName: "member_collection",
		CreatedBy:      "tester",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}

	created, err := svc.CreateNode(context.Background(), dto.CreateIntentReq{
		KbID:           kb.ID,
		IntentCode:     "member.topic.with-kb",
		Name:           "会员主题",
		Level:          2,
		Kind:           0,
		CollectionName: "wrong_collection",
	}, "user-1")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if created.CollectionName != "member_collection" {
		t.Fatalf("expected collectionName from knowledge base, got %s", created.CollectionName)
	}
}

func TestIntentService_BatchDisableRejectsEnabledDescendantsNotSelectedLikeJava(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	parent := &model.IntentNode{IntentCode: "member", Name: "会员", Level: 0, Enabled: 1}
	child := &model.IntentNode{IntentCode: "member.points", Name: "积分", Level: 1, ParentCode: "member", Enabled: 1}
	if err := gdb.Create(parent).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := gdb.Create(child).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}
	err := svc.BatchToggleNodes(context.Background(), []string{parent.ID}, 0)
	if err == nil || !strings.Contains(err.Error(), "批量停用失败：节点 [会员] 存在已启用的子节点未包含在本次操作中") {
		t.Fatalf("expected batch disable descendant validation error, got %v", err)
	}
	var stored model.IntentNode
	if err := gdb.First(&stored, "id = ?", parent.ID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if stored.Enabled != 1 {
		t.Fatalf("expected parent to remain enabled, got %d", stored.Enabled)
	}
}

func TestIntentService_BatchDeleteRejectsEnabledDescendantsNotSelectedLikeJava(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	parent := &model.IntentNode{IntentCode: "member", Name: "会员", Level: 0, Enabled: 1}
	child := &model.IntentNode{IntentCode: "member.points", Name: "积分", Level: 1, ParentCode: "member", Enabled: 1}
	if err := gdb.Create(parent).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := gdb.Create(child).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}

	err := svc.BatchDeleteNodes(context.Background(), []string{parent.ID})
	if err == nil || !strings.Contains(err.Error(), "批量删除失败：节点 [会员] 存在已启用的子节点未包含在本次操作中") {
		t.Fatalf("expected batch delete descendant validation error, got %v", err)
	}
	var stored model.IntentNode
	if err := gdb.First(&stored, "id = ?", parent.ID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if stored.Deleted != 0 {
		t.Fatalf("expected parent to remain active, got deleted=%d", stored.Deleted)
	}
}

func TestIntentService_BatchDeleteRejectsDisabledDescendantsNotSelectedLikeJava(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	parent := &model.IntentNode{IntentCode: "member", Name: "会员", Level: 0, Enabled: 1}
	child := &model.IntentNode{IntentCode: "member.points", Name: "积分", Level: 1, ParentCode: "member", Enabled: 0}
	if err := gdb.Create(parent).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := gdb.Create(child).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if err := gdb.Model(&model.IntentNode{}).Where("id = ?", child.ID).UpdateColumn("enabled", 0).Error; err != nil {
		t.Fatalf("disable child: %v", err)
	}

	err := svc.BatchDeleteNodes(context.Background(), []string{parent.ID})
	if err == nil || !strings.Contains(err.Error(), "批量删除失败：节点 [会员] 未包含全量子节点") {
		t.Fatalf("expected batch delete full subtree validation error, got %v", err)
	}
	var stored model.IntentNode
	if err := gdb.First(&stored, "id = ?", parent.ID).Error; err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if stored.Deleted != 0 {
		t.Fatalf("expected parent to remain active, got deleted=%d", stored.Deleted)
	}
}
