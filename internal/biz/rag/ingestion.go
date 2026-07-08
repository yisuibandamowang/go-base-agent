package rag

import (
	"context"
	"fmt"
	"log/slog"
)

// IngestionNodeType enumerates pipeline node types.
// Aligns with Java IngestionNodeType.
type IngestionNodeType string

const (
	NodeFetcher  IngestionNodeType = "FETCHER"
	NodeParser   IngestionNodeType = "PARSER"
	NodeEnhancer IngestionNodeType = "ENHANCER"
	NodeChunker  IngestionNodeType = "CHUNKER"
	NodeEnricher IngestionNodeType = "ENRICHER"
	NodeIndexer  IngestionNodeType = "INDEXER"
)

// IngestionContext carries the state through the ingestion pipeline.
// Aligns with Java IngestionContext.
type IngestionContext struct {
	RawBytes []byte
	MimeType string
	RawText  string
	Document *ParsedDocument
	Chunks   []VectorChunk
	Metadata map[string]string
}

// NodeConfig defines a pipeline node's configuration.
type NodeConfig struct {
	NodeID     string
	NodeType   IngestionNodeType
	NextNodeID string
	Enabled    bool
}

// PipelineDefinition defines an ingestion pipeline.
type PipelineDefinition struct {
	ID    string
	Name  string
	Nodes []NodeConfig
}

// NodeResult is the result of executing a pipeline node.
type NodeResult struct {
	Success        bool
	ShouldContinue bool
	ErrorMessage   string
}

// IngestionNode is a single step in the ingestion pipeline.
// Aligns with Java IngestionNode.
type IngestionNode interface {
	NodeType() IngestionNodeType
	Execute(ctx context.Context, nodeCtx *IngestionContext, config NodeConfig) NodeResult
}

// IngestionEngine executes a document ingestion pipeline.
// Aligns with Java IngestionEngine.
type IngestionEngine struct {
	nodes map[IngestionNodeType]IngestionNode
}

// NewIngestionEngine creates a new ingestion engine.
func NewIngestionEngine(nodes []IngestionNode) *IngestionEngine {
	m := make(map[IngestionNodeType]IngestionNode, len(nodes))
	for _, n := range nodes {
		m[n.NodeType()] = n
	}
	return &IngestionEngine{nodes: m}
}

// Execute runs the pipeline against the given context.
func (e *IngestionEngine) Execute(ctx context.Context, ctx2 *IngestionContext, pipeline PipelineDefinition) error {
	nodeMap := make(map[string]NodeConfig, len(pipeline.Nodes))
	var startNode *NodeConfig
	for _, n := range pipeline.Nodes {
		nc := n
		nodeMap[n.NodeID] = nc
		if startNode == nil {
			startNode = &nc
		}
	}
	if startNode == nil {
		return fmt.Errorf("pipeline %s has no nodes", pipeline.ID)
	}

	visited := make(map[string]bool)
	current := startNode

	for current != nil {
		if visited[current.NodeID] {
			return fmt.Errorf("circular pipeline at node %s", current.NodeID)
		}
		visited[current.NodeID] = true

		if !current.Enabled {
			if current.NextNodeID != "" {
				next, ok := nodeMap[current.NextNodeID]
				if ok {
					current = &next
				} else {
					current = nil
				}
			} else {
				current = nil
			}
			continue
		}

		node, ok := e.nodes[current.NodeType]
		if !ok {
			return fmt.Errorf("no implementation for node type %s", current.NodeType)
		}

		result := node.Execute(ctx, ctx2, *current)
		if !result.Success {
			return fmt.Errorf("node %s failed: %s", current.NodeID, result.ErrorMessage)
		}
		if !result.ShouldContinue {
			return nil
		}

		if current.NextNodeID != "" {
			next, ok := nodeMap[current.NextNodeID]
			if ok {
				current = &next
			} else {
				current = nil
			}
		} else {
			current = nil
		}
	}

	return nil
}

// NoopIngestionNode implements all node types as no-ops.
type NoopIngestionNode struct {
	typ IngestionNodeType
}

func (n *NoopIngestionNode) NodeType() IngestionNodeType { return n.typ }
func (n *NoopIngestionNode) Execute(ctx context.Context, nodeCtx *IngestionContext, config NodeConfig) NodeResult {
	slog.Info("ingestion noop", "nodeId", config.NodeID, "type", n.typ)
	return NodeResult{Success: true, ShouldContinue: true}
}
