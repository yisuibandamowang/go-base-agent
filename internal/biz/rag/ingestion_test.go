package rag

import (
	"context"
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
