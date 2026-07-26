package rag

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// IngestionNodeType enumerates pipeline node types.
// Aligns with Java IngestionNodeType.
type IngestionNodeType string

const (
	NodeFetcher  IngestionNodeType = "fetcher"
	NodeParser   IngestionNodeType = "parser"
	NodeEnhancer IngestionNodeType = "enhancer"
	NodeChunker  IngestionNodeType = "chunker"
	NodeEnricher IngestionNodeType = "enricher"
	NodeIndexer  IngestionNodeType = "indexer"
)

// NormalizeIngestionNodeType normalizes Java-compatible node type values.
func NormalizeIngestionNodeType(value IngestionNodeType) IngestionNodeType {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(string(value))), "-", "_")
	switch normalized {
	case "fetcher":
		return NodeFetcher
	case "parser":
		return NodeParser
	case "enhancer":
		return NodeEnhancer
	case "chunker":
		return NodeChunker
	case "enricher":
		return NodeEnricher
	case "indexer":
		return NodeIndexer
	default:
		return IngestionNodeType(normalized)
	}
}

// IngestionContext carries the state through the ingestion pipeline.
// Aligns with Java IngestionContext.
type IngestionContext struct {
	TaskID           string
	PipelineID       string
	Source           *DocumentSource
	RawBytes         []byte
	MimeType         string
	RawText          string
	Document         *ParsedDocument
	Chunks           []VectorChunk
	EnhancedText     string
	Keywords         []string
	Questions        []string
	Metadata         map[string]any
	VectorSpaceID    string
	Status           IngestionStatus
	Logs             []NodeLog
	ErrorMessage     string
	SkipIndexerWrite bool
	Assets           []AssetRef
}

// DocumentSource describes the source document flowing through ingestion.
type DocumentSource struct {
	Type        string
	Location    string
	FileName    string
	Credentials map[string]string
}

// IngestionStatus describes the current pipeline execution status.
type IngestionStatus string

const (
	IngestionStatusPending   IngestionStatus = "pending"
	IngestionStatusRunning   IngestionStatus = "running"
	IngestionStatusCompleted IngestionStatus = "completed"
	IngestionStatusFailed    IngestionStatus = "failed"
)

// NodeLog captures one ingestion node execution log.
type NodeLog struct {
	NodeID       string
	NodeType     IngestionNodeType
	Message      string
	DurationMs   int64
	Success      bool
	ErrorMessage string
	Output       map[string]any
}

// NodeConfig defines a pipeline node's configuration.
type NodeConfig struct {
	NodeID     string
	NodeType   IngestionNodeType
	Settings   map[string]any
	Condition  any
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
	Message        string
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
	nodes              map[IngestionNodeType]IngestionNode
	conditionEvaluator *ConditionEvaluator
}

// NewIngestionEngine creates a new ingestion engine.
func NewIngestionEngine(nodes []IngestionNode) *IngestionEngine {
	m := make(map[IngestionNodeType]IngestionNode, len(nodes))
	for _, n := range nodes {
		m[NormalizeIngestionNodeType(n.NodeType())] = n
	}
	return &IngestionEngine{
		nodes:              m,
		conditionEvaluator: NewConditionEvaluator(),
	}
}

// Execute runs the pipeline against the given context.
func (e *IngestionEngine) Execute(ctx context.Context, ctx2 *IngestionContext, pipeline PipelineDefinition) error {
	if ctx2 == nil {
		return fmt.Errorf("ingestion context is nil")
	}
	if ctx2.Logs == nil {
		ctx2.Logs = make([]NodeLog, 0, len(pipeline.Nodes))
	}
	ctx2.PipelineID = pipeline.ID
	ctx2.Status = IngestionStatusRunning

	nodeMap := make(map[string]NodeConfig, len(pipeline.Nodes))
	referenced := make(map[string]struct{}, len(pipeline.Nodes))
	for _, n := range pipeline.Nodes {
		if _, exists := nodeMap[n.NodeID]; exists {
			return fmt.Errorf("duplicate pipeline node %s", n.NodeID)
		}
		nodeMap[n.NodeID] = n
		if n.NextNodeID != "" {
			referenced[n.NextNodeID] = struct{}{}
		}
	}
	startNode := findStartNode(pipeline.Nodes, referenced)
	if startNode == nil {
		return fmt.Errorf("pipeline %s has no nodes", pipeline.ID)
	}

	visited := make(map[string]bool)
	current := *startNode

	for {
		if visited[current.NodeID] {
			return fmt.Errorf("circular pipeline at node %s", current.NodeID)
		}
		visited[current.NodeID] = true

		if !current.Enabled {
			ctx2.appendNodeLog(current, NodeResult{Success: true, ShouldContinue: true, Message: "Skipped: DISABLED"}, 0)
			next, err := nextNode(nodeMap, current)
			if err != nil {
				ctx2.fail(err)
				return err
			}
			if next == nil {
				ctx2.Status = IngestionStatusCompleted
				return nil
			}
			current = *next
			continue
		}
		if e.conditionEvaluator != nil && !e.conditionEvaluator.Evaluate(ctx2, current.Condition) {
			slog.Info("ingestion node skipped by condition", "nodeId", current.NodeID, "nodeType", current.NodeType)
			ctx2.appendNodeLog(current, NodeResult{Success: true, ShouldContinue: true, Message: "Skipped: 条件未满足"}, 0)
			next, err := nextNode(nodeMap, current)
			if err != nil {
				ctx2.fail(err)
				return err
			}
			if next == nil {
				ctx2.Status = IngestionStatusCompleted
				return nil
			}
			current = *next
			continue
		}

		nodeType := NormalizeIngestionNodeType(current.NodeType)
		node, ok := e.nodes[nodeType]
		if !ok {
			err := fmt.Errorf("no implementation for node type %s", current.NodeType)
			ctx2.fail(err)
			return err
		}

		start := time.Now()
		result := node.Execute(ctx, ctx2, current)
		ctx2.appendNodeLog(current, result, time.Since(start).Milliseconds())
		if !result.Success {
			err := fmt.Errorf("node %s failed: %s", current.NodeID, nodeResultMessage(result))
			ctx2.fail(err)
			return err
		}
		if !result.ShouldContinue {
			ctx2.Status = IngestionStatusCompleted
			return nil
		}

		next, err := nextNode(nodeMap, current)
		if err != nil {
			ctx2.fail(err)
			return err
		}
		if next == nil {
			ctx2.Status = IngestionStatusCompleted
			return nil
		}
		current = *next
	}
}

func (c *IngestionContext) appendNodeLog(config NodeConfig, result NodeResult, durationMs int64) {
	if c == nil {
		return
	}
	c.Logs = append(c.Logs, NodeLog{
		NodeID:       config.NodeID,
		NodeType:     NormalizeIngestionNodeType(config.NodeType),
		Message:      nodeResultMessage(result),
		DurationMs:   durationMs,
		Success:      result.Success,
		ErrorMessage: result.ErrorMessage,
		Output:       extractIngestionNodeOutput(c, config),
	})
}

func nodeResultMessage(result NodeResult) string {
	if strings.TrimSpace(result.Message) != "" {
		return result.Message
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		return result.ErrorMessage
	}
	return "OK"
}

func (c *IngestionContext) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.Status = IngestionStatusFailed
	c.ErrorMessage = err.Error()
}

func extractIngestionNodeOutput(ctx *IngestionContext, config NodeConfig) map[string]any {
	if ctx == nil {
		return map[string]any{}
	}
	switch NormalizeIngestionNodeType(config.NodeType) {
	case NodeFetcher:
		output := map[string]any{
			"mimeType":       ctx.MimeType,
			"rawBytesLength": len(ctx.RawBytes),
		}
		if len(ctx.RawBytes) > 0 {
			output["rawBytesBase64"] = base64.StdEncoding.EncodeToString(ctx.RawBytes)
		}
		if ctx.Source != nil {
			output["source"] = map[string]any{
				"type":     ctx.Source.Type,
				"location": ctx.Source.Location,
				"fileName": ctx.Source.FileName,
			}
		}
		return output
	case NodeParser:
		return map[string]any{
			"mimeType": ctx.MimeType,
			"rawText":  ctx.RawText,
			"document": ctx.Document,
		}
	case NodeEnhancer:
		return map[string]any{
			"enhancedText": ctx.EnhancedText,
			"keywords":     ctx.Keywords,
			"questions":    ctx.Questions,
			"metadata":     ctx.Metadata,
		}
	case NodeChunker, NodeEnricher:
		return map[string]any{
			"chunkCount": len(ctx.Chunks),
			"chunks":     ctx.Chunks,
		}
	case NodeIndexer:
		return map[string]any{
			"settings":   config.Settings,
			"chunkCount": len(ctx.Chunks),
			"chunks":     ctx.Chunks,
		}
	default:
		return map[string]any{
			"mimeType":     ctx.MimeType,
			"rawText":      ctx.RawText,
			"enhancedText": ctx.EnhancedText,
			"keywords":     ctx.Keywords,
			"questions":    ctx.Questions,
			"metadata":     ctx.Metadata,
			"chunks":       ctx.Chunks,
		}
	}
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

func findStartNode(nodes []NodeConfig, referenced map[string]struct{}) *NodeConfig {
	for i := range nodes {
		if _, ok := referenced[nodes[i].NodeID]; !ok {
			return &nodes[i]
		}
	}
	return nil
}

func nextNode(nodeMap map[string]NodeConfig, current NodeConfig) (*NodeConfig, error) {
	if current.NextNodeID == "" {
		return nil, nil
	}
	next, ok := nodeMap[current.NextNodeID]
	if !ok {
		return nil, fmt.Errorf("missing next node %s referenced by %s", current.NextNodeID, current.NodeID)
	}
	return &next, nil
}
