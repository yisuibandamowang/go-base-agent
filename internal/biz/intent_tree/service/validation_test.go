package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"

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

func TestIntentService_CreateNodeDefaultsEnabledToOneLikeJava(t *testing.T) {
	svc := newIntentValidationService(t)
	created, err := svc.CreateNode(context.Background(), dto.CreateIntentReq{
		IntentCode: "member.default-enabled",
		Name:       "默认启用节点",
		Level:      1,
	}, "user-1")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if created.Enabled != 1 {
		t.Fatalf("expected enabled to default to 1, got %d", created.Enabled)
	}
}

func TestIntentService_CreateNodePreservesExplicitDisabledState(t *testing.T) {
	svc := newIntentValidationService(t)
	var req dto.CreateIntentReq
	if err := json.Unmarshal([]byte(`{"intentCode":"member.disabled","name":"禁用节点","level":1,"enabled":0}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	created, err := svc.CreateNode(context.Background(), req, "user-1")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if created.Enabled != 0 {
		t.Fatalf("expected explicit disabled state to remain 0, got %d", created.Enabled)
	}
}

func TestIntentService_UpdateNodeIgnoresImmutableJavaFields(t *testing.T) {
	svc := newIntentValidationService(t)
	created, err := svc.CreateNode(context.Background(), dto.CreateIntentReq{
		IntentCode:     "member.query",
		Name:           "会员查询",
		Level:          1,
		McpToolID:      "tool-a",
		Enabled:        1,
		EnabledSet:     true,
		CollectionName: "",
	}, "user-1")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	newCode := "member.query.updated"
	newKbID := "kb-2"
	newToolID := "tool-b"
	newName := "会员查询2"
	updated, err := svc.UpdateNode(context.Background(), created.ID, dto.UpdateIntentReq{
		IntentCode: &newCode,
		KbID:       &newKbID,
		McpToolID:  &newToolID,
		Name:       &newName,
	}, "user-1")
	if err != nil {
		t.Fatalf("update node: %v", err)
	}
	if updated.IntentCode != "member.query" {
		t.Fatalf("expected intentCode to remain immutable, got %s", updated.IntentCode)
	}
	if updated.KbID != "" {
		t.Fatalf("expected kbId to remain immutable, got %s", updated.KbID)
	}
	if updated.McpToolID != "tool-a" {
		t.Fatalf("expected mcpToolId to remain immutable, got %s", updated.McpToolID)
	}
	if updated.Name != newName {
		t.Fatalf("expected mutable fields to still update, got %s", updated.Name)
	}
}

func TestIntentService_GetTreeOrdersSiblingsLikeJava(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	root := &model.IntentNode{BaseModel: db.BaseModel{ID: "root"}, IntentCode: "root", Name: "根", Level: 0, Enabled: 1, SortOrder: 0}
	childB := &model.IntentNode{BaseModel: db.BaseModel{ID: "child-b"}, IntentCode: "child-b", Name: "B", Level: 1, ParentCode: "root", Enabled: 1, SortOrder: 0}
	childA := &model.IntentNode{BaseModel: db.BaseModel{ID: "child-a"}, IntentCode: "child-a", Name: "A", Level: 1, ParentCode: "root", Enabled: 1, SortOrder: 0}
	if err := gdb.Create(root).Error; err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if err := gdb.Create(childB).Error; err != nil {
		t.Fatalf("seed childB: %v", err)
	}
	if err := gdb.Create(childA).Error; err != nil {
		t.Fatalf("seed childA: %v", err)
	}

	tree, err := svc.GetTree(context.Background())
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("expected one root, got %d", len(tree))
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("expected two children, got %d", len(tree[0].Children))
	}
	if tree[0].Children[0].ID != "child-a" || tree[0].Children[1].ID != "child-b" {
		t.Fatalf("expected Java-like child order by sortOrder then id, got [%s,%s]", tree[0].Children[0].ID, tree[0].Children[1].ID)
	}
}

func TestIntentService_InitFromFactorySeedsJavaDefaultIntentTree(t *testing.T) {
	svc, gdb := newIntentValidationServiceWithDB(t)
	if err := gdb.Create(&knowledgeModel.KnowledgeBase{BaseModel: db.BaseModel{ID: "1997855927072321537"}, Name: "集团知识库", EmbeddingModel: "qwen3.6:latest", CollectionName: "group-collection"}).Error; err != nil {
		t.Fatalf("seed group kb: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeBase{BaseModel: db.BaseModel{ID: "1997857139737882625"}, Name: "业务知识库", EmbeddingModel: "qwen3.6:latest", CollectionName: "biz-collection"}).Error; err != nil {
		t.Fatalf("seed biz kb: %v", err)
	}
	method := reflect.ValueOf(svc).MethodByName("InitFromFactory")
	if !method.IsValid() {
		t.Fatalf("expected InitFromFactory to exist on IntentService")
	}

	results := method.Call([]reflect.Value{reflect.ValueOf(context.Background())})
	if len(results) != 2 {
		t.Fatalf("expected two return values, got %d", len(results))
	}
	if !results[1].IsNil() {
		t.Fatalf("expected no error return, got %v", results[1].Interface())
	}
	created := int(results[0].Int())
	if created != 18 {
		t.Fatalf("expected 18 default nodes, got %d", created)
	}

	tree, err := svc.GetTree(context.Background())
	if err != nil {
		t.Fatalf("get tree after init: %v", err)
	}
	if len(tree) != 4 {
		t.Fatalf("expected 4 root nodes, got %d", len(tree))
	}
	rootIDs := []string{tree[0].IntentCode, tree[1].IntentCode, tree[2].IntentCode, tree[3].IntentCode}
	if strings.Join(rootIDs, ",") != "group,biz,sales,sys" {
		t.Fatalf("expected Java default root order, got %v", rootIDs)
	}
	if len(tree[0].Children) != 3 || tree[0].Children[2].IntentCode != "group-finance" {
		t.Fatalf("expected group tree to include finance branch, got %+v", tree[0].Children)
	}
	if len(tree[1].Children) != 2 || tree[1].Children[0].IntentCode != "biz-oa" || tree[1].Children[1].IntentCode != "biz-ins" {
		t.Fatalf("expected biz tree to include oa/ins branches, got %+v", tree[1].Children)
	}
	if len(tree[3].Children) != 2 || tree[3].Children[0].IntentCode != "sys-welcome" || tree[3].Children[1].IntentCode != "sys-about-bot" {
		t.Fatalf("expected sys tree to include assistant branches, got %+v", tree[3].Children)
	}
	var count int64
	if err := gdb.Model(&model.IntentNode{}).Count(&count).Error; err != nil {
		t.Fatalf("count seeded nodes: %v", err)
	}
	if count != 18 {
		t.Fatalf("expected 18 rows in DB, got %d", count)
	}

	results = method.Call([]reflect.Value{reflect.ValueOf(context.Background())})
	if !results[1].IsNil() {
		t.Fatalf("expected no error return on duplicate init, got %v", results[1].Interface())
	}
	if secondCreated := int(results[0].Int()); secondCreated != 0 {
		t.Fatalf("expected duplicate init to create 0 nodes, got %d", secondCreated)
	}
	if err := gdb.Model(&model.IntentNode{}).Count(&count).Error; err != nil {
		t.Fatalf("count duplicate seeded nodes: %v", err)
	}
	if count != 18 {
		t.Fatalf("expected duplicate init to keep 18 rows, got %d", count)
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
