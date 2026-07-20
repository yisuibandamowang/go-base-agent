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

	if err := engine.Execute(context.Background(), &IngestionContext{}, pipeline); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "n2" {
		t.Fatalf("expected skipped fetcher and executed parser, got %+v", calls)
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
