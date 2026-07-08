package rag

import (
	"context"
	"testing"
)

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
