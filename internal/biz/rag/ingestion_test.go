package rag

import (
	"context"
	"strings"
	"testing"
)

type recordingIngestionNode struct {
	typ    IngestionNodeType
	calls  *[]string
	result NodeResult
}

func (n *recordingIngestionNode) NodeType() IngestionNodeType { return n.typ }

func (n *recordingIngestionNode) Execute(ctx context.Context, nodeCtx *IngestionContext, config NodeConfig) NodeResult {
	*n.calls = append(*n.calls, config.NodeID)
	if n.result == (NodeResult{}) {
		return NodeResult{Success: true, ShouldContinue: true}
	}
	return n.result
}

func TestIngestionEngine_Basic(t *testing.T) {
	nodes := []IngestionNode{
		&NoopIngestionNode{typ: NodeFetcher},
		&NoopIngestionNode{typ: NodeParser},
	}

	engine := NewIngestionEngine(nodes)

	pipeline := PipelineDefinition{
		ID:   "test-pipeline",
		Name: "test",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, NextNodeID: "n2", Enabled: true},
			{NodeID: "n2", NodeType: NodeParser, Enabled: true},
		},
	}

	ctx := &IngestionContext{}
	err := engine.Execute(context.Background(), ctx, pipeline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIngestionEngine_DisabledNode(t *testing.T) {
	nodes := []IngestionNode{
		&NoopIngestionNode{typ: NodeFetcher},
	}

	engine := NewIngestionEngine(nodes)

	pipeline := PipelineDefinition{
		ID: "test",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, Enabled: false},
		},
	}

	err := engine.Execute(context.Background(), &IngestionContext{}, pipeline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIngestionEngine_EmptyPipeline(t *testing.T) {
	engine := NewIngestionEngine(nil)
	err := engine.Execute(context.Background(), &IngestionContext{}, PipelineDefinition{ID: "empty"})
	if err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}

func TestIngestionEngine_StartNodeSkipsReferencedNodeOrder(t *testing.T) {
	calls := make([]string, 0, 2)
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: NodeFetcher, calls: &calls},
		&recordingIngestionNode{typ: NodeParser, calls: &calls},
	}

	engine := NewIngestionEngine(nodes)
	pipeline := PipelineDefinition{
		ID: "ordered",
		Nodes: []NodeConfig{
			{NodeID: "n2", NodeType: NodeParser, Enabled: true},
			{NodeID: "n1", NodeType: NodeFetcher, NextNodeID: "n2", Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), &IngestionContext{}, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "n1" || calls[1] != "n2" {
		t.Fatalf("unexpected execution order: %+v", calls)
	}
}

func TestIngestionEngine_NormalizesJavaNodeTypeValues(t *testing.T) {
	calls := make([]string, 0, 2)
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: IngestionNodeType("FETCHER"), calls: &calls},
		&recordingIngestionNode{typ: IngestionNodeType("parser"), calls: &calls},
	}

	engine := NewIngestionEngine(nodes)
	pipeline := PipelineDefinition{
		ID: "normalize",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: IngestionNodeType("fetcher"), NextNodeID: "n2", Enabled: true},
			{NodeID: "n2", NodeType: IngestionNodeType("PARSER"), Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), &IngestionContext{}, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "n1" || calls[1] != "n2" {
		t.Fatalf("unexpected execution order: %+v", calls)
	}
	runCtx := &IngestionContext{}
	if err := engine.Execute(context.Background(), runCtx, pipeline); err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}
	if runCtx.Status != IngestionStatusCompleted {
		t.Fatalf("expected completed status, got %q", runCtx.Status)
	}
	if len(runCtx.Logs) != 2 || runCtx.Logs[0].NodeType != NodeFetcher || runCtx.Logs[1].NodeType != NodeParser {
		t.Fatalf("unexpected node logs: %+v", runCtx.Logs)
	}
}

func TestIngestionEngine_ConditionFalseSkipsNodeAndContinues(t *testing.T) {
	calls := make([]string, 0, 2)
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: NodeFetcher, calls: &calls},
		&recordingIngestionNode{typ: NodeParser, calls: &calls},
	}

	engine := NewIngestionEngine(nodes)
	pipeline := PipelineDefinition{
		ID: "condition",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, Condition: false, NextNodeID: "n2", Enabled: true},
			{NodeID: "n2", NodeType: NodeParser, Enabled: true},
		},
	}

	runCtx := &IngestionContext{}
	if err := engine.Execute(context.Background(), runCtx, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "n2" {
		t.Fatalf("expected skipped fetcher and executed parser, got %+v", calls)
	}
	if len(runCtx.Logs) != 2 {
		t.Fatalf("expected skipped and executed node logs, got %+v", runCtx.Logs)
	}
	if runCtx.Logs[0].Message != "Skipped: 条件未满足" || runCtx.Logs[0].ErrorMessage != "" || !runCtx.Logs[0].Success {
		t.Fatalf("expected Java-style skipped log, got %+v", runCtx.Logs[0])
	}
}

func TestIngestionEngine_JavaStyleStringConditionExecutesNode(t *testing.T) {
	calls := make([]string, 0, 2)
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: NodeFetcher, calls: &calls},
		&recordingIngestionNode{typ: NodeParser, calls: &calls},
	}

	engine := NewIngestionEngine(nodes)
	runCtx := &IngestionContext{RawText: "会员 Agent 支持权益查询"}
	pipeline := PipelineDefinition{
		ID: "spel-condition",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, Condition: "#ctx.rawText.contains('会员')", NextNodeID: "n2", Enabled: true},
			{NodeID: "n2", NodeType: NodeParser, Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), runCtx, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "n1" || calls[1] != "n2" {
		t.Fatalf("expected java-style string condition to execute both nodes, got %+v", calls)
	}
}

func TestConditionEvaluator_JavaStyleStringExpressions(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := &IngestionContext{
		RawText:  "会员 Agent 支持权益查询",
		MimeType: "text/markdown",
		Metadata: map[string]any{"domain": "membership"},
	}

	for _, condition := range []string{
		"rawText != null",
		"#ctx.rawText.contains('会员')",
		"#ctx.metadata['domain'] == 'membership'",
		"mimeType == 'text/markdown'",
	} {
		if !evaluator.Evaluate(ctx, condition) {
			t.Fatalf("expected condition %q to be true", condition)
		}
	}
	if evaluator.Evaluate(ctx, "#ctx.rawText.contains('支付')") {
		t.Fatal("expected non-matching contains condition to be false")
	}
}

func TestIngestionEngine_FetcherLogIncludesRawBytesBase64(t *testing.T) {
	nodes := []IngestionNode{
		&NoopIngestionNode{typ: NodeFetcher},
	}
	engine := NewIngestionEngine(nodes)
	runCtx := &IngestionContext{RawBytes: []byte("hello")}
	pipeline := PipelineDefinition{
		ID: "fetcher-output",
		Nodes: []NodeConfig{
			{NodeID: "fetch", NodeType: NodeFetcher, Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), runCtx, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runCtx.Logs) != 1 {
		t.Fatalf("expected one node log, got %+v", runCtx.Logs)
	}
	if got := runCtx.Logs[0].Output["rawBytesBase64"]; got != "aGVsbG8=" {
		t.Fatalf("expected rawBytesBase64 to match Java output, got %+v", runCtx.Logs[0].Output)
	}
}

func TestIngestionEngine_MissingNextNodeFails(t *testing.T) {
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: NodeFetcher, calls: &[]string{}},
	}

	engine := NewIngestionEngine(nodes)
	pipeline := PipelineDefinition{
		ID: "broken",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, NextNodeID: "missing", Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), &IngestionContext{}, pipeline); err == nil {
		t.Fatal("expected error for missing next node")
	}
}

func TestIngestionEngine_CycleFails(t *testing.T) {
	nodes := []IngestionNode{
		&recordingIngestionNode{typ: NodeFetcher, calls: &[]string{}},
	}

	engine := NewIngestionEngine(nodes)
	pipeline := PipelineDefinition{
		ID: "cycle",
		Nodes: []NodeConfig{
			{NodeID: "n1", NodeType: NodeFetcher, NextNodeID: "n2", Enabled: true},
			{NodeID: "n2", NodeType: NodeFetcher, NextNodeID: "n1", Enabled: true},
		},
	}

	if err := engine.Execute(context.Background(), &IngestionContext{}, pipeline); err == nil {
		t.Fatal("expected error for cycle")
	}
}

func TestNoopIngestionNode(t *testing.T) {
	n := &NoopIngestionNode{typ: NodeFetcher}
	if n.NodeType() != NodeFetcher {
		t.Fatal("unexpected type")
	}

	result := n.Execute(context.Background(), &IngestionContext{}, NodeConfig{NodeID: "n1"})
	if !result.Success || !result.ShouldContinue {
		t.Fatal("noop should succeed and continue")
	}
}

// TestConditionEvaluatorFailClosedOnMalformedStructures 验证未知或畸形条件结构 fail-closed：
// 放行畸形条件会让节点绕过配置意图执行，宁可漏执行不可错执行。
// 对齐 Java 修复：未知或畸形节点条件结构 fail-closed。
func TestConditionEvaluatorFailClosedOnMalformedStructures(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := &IngestionContext{}

	// 未知键名（应为 field 却写成 fields）
	if evaluator.Evaluate(ctx, map[string]any{"fields": "rawText", "operator": "eq", "value": "x"}) {
		t.Fatal("unknown object condition should fail closed")
	}
	// any 不是数组
	if evaluator.Evaluate(ctx, map[string]any{"any": "not-an-array"}) {
		t.Fatal("non-array any should fail closed")
	}
	// any 为空数组
	if evaluator.Evaluate(ctx, map[string]any{"any": []any{}}) {
		t.Fatal("empty any array should fail closed")
	}
	// all 不是数组
	if evaluator.Evaluate(ctx, map[string]any{"all": "not-an-array"}) {
		t.Fatal("non-array all should fail closed")
	}
	// not 包内含畸形结构（not 取反后畸形应得 false 的反例：not(malformed)=not(false)=true 是错的，
	// Java 语义是 not 内部先按 fail-closed 求值为 false，再取反为 true；此处畸形在 not 内层同样先判 false）
	inner := evaluator.Evaluate(ctx, map[string]any{"fields": "rawText"})
	if inner {
		t.Fatal("malformed inner condition should fail closed before negation")
	}
	// 未知类型（数字等）
	if evaluator.Evaluate(ctx, 42) {
		t.Fatal("unknown condition type should fail closed")
	}
	// 合法结构不受影响
	if !evaluator.Evaluate(ctx, nil) {
		t.Fatal("nil condition should allow execution")
	}
	if !evaluator.Evaluate(ctx, true) {
		t.Fatal("boolean true should allow execution")
	}
}

// TestConditionEvaluatorInvalidNumericComparisonSafeFalse 验证非法数值条件安全返回 false。
// 对齐 Java 修复：让非法数值条件安全返回 false。
func TestConditionEvaluatorInvalidNumericComparisonSafeFalse(t *testing.T) {
	evaluator := NewConditionEvaluator()
	ctx := &IngestionContext{}

	// 数值字段与非数值比较：不 panic，安全返回 false
	result := evaluator.Evaluate(ctx, map[string]any{
		"field": "keywords", "operator": "gt", "value": "not-a-number",
	})
	if result {
		t.Fatal("invalid numeric comparison should safely return false")
	}

	// 两边都不是数值
	result = evaluator.Evaluate(ctx, map[string]any{
		"field": "rawText", "operator": "gte", "value": "abc",
	})
	if result {
		t.Fatal("non-numeric gte comparison should safely return false")
	}
}

// TestIngestionEngineRejectsMultipleStartNodes 验证多起点流水线在执行前失败，
// 而非静默取第一个起始节点导致其余分支永不执行。
// 对齐 Java 修复：多起点流水线配置执行前失败。
func TestIngestionEngineRejectsMultipleStartNodes(t *testing.T) {
	engine := NewIngestionEngine(nil)
	pipeline := PipelineDefinition{
		ID: "multi-start",
		Nodes: []NodeConfig{
			{NodeID: "fetcher-a", NodeType: NodeFetcher, Enabled: true},
			{NodeID: "fetcher-b", NodeType: NodeFetcher, Enabled: true},
			{NodeID: "parser", NodeType: NodeParser, Enabled: true, NextNodeID: ""},
		},
	}

	err := engine.Execute(context.Background(), &IngestionContext{}, pipeline)
	if err == nil {
		t.Fatal("expected multiple start nodes to fail before execution")
	}
	if !strings.Contains(err.Error(), "multiple start nodes") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "fetcher-a") || !strings.Contains(err.Error(), "fetcher-b") {
		t.Fatalf("error should list all start nodes: %v", err)
	}
}

// TestIngestionEngineRejectsNoStartNode 验证无起始节点（成环）同样报错。
func TestIngestionEngineRejectsNoStartNode(t *testing.T) {
	engine := NewIngestionEngine(nil)
	pipeline := PipelineDefinition{
		ID: "all-referenced",
		Nodes: []NodeConfig{
			{NodeID: "a", NodeType: NodeFetcher, Enabled: true, NextNodeID: "b"},
			{NodeID: "b", NodeType: NodeParser, Enabled: true, NextNodeID: "a"},
		},
	}

	err := engine.Execute(context.Background(), &IngestionContext{}, pipeline)
	if err == nil {
		t.Fatal("expected cyclic pipeline without start node to fail")
	}
	if !strings.Contains(err.Error(), "no start node") {
		t.Fatalf("unexpected error: %v", err)
	}
}
