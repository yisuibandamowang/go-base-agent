package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GraphView is the frontend-friendly graph visualization payload.
type GraphView struct {
	Nodes     []GraphViewNode `json:"nodes"`
	Edges     []GraphViewEdge `json:"edges"`
	Truncated bool            `json:"truncated"`
}

// GraphViewNode represents one graph entity node.
type GraphViewNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// GraphViewEdge represents one graph relation edge.
type GraphViewEdge struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// GraphViewClient fetches graph visualization data from graph backends.
type GraphViewClient interface {
	FetchGraph(ctx context.Context, label string, maxDepth, maxNodes int) ([]byte, error)
	FetchLabels(ctx context.Context, keyword string, limit int) ([]string, error)
}

// GraphQueryService maps LightRAG graph payloads into admin visualization views.
type GraphQueryService struct {
	client GraphViewClient
}

// NewGraphQueryService creates a graph query service.
func NewGraphQueryService(client GraphViewClient) *GraphQueryService {
	return &GraphQueryService{client: client}
}

// GetGraph fetches and filters graph data for the admin visualization page.
func (s *GraphQueryService) GetGraph(ctx context.Context, entity, collection, doc string, depth, limit int) (GraphView, error) {
	if s == nil || s.client == nil {
		return GraphView{}, fmt.Errorf("知识图谱通道未启用（rag.graph.type=none）")
	}
	maxDepth := depth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	maxNodes := limit
	if maxNodes <= 0 {
		maxNodes = 200
	}
	if maxNodes > 1000 {
		maxNodes = 1000
	}
	label := strings.TrimSpace(entity)
	if label == "" {
		label = "*"
	}
	token := ""
	if strings.TrimSpace(doc) != "" {
		token = strings.TrimSpace(doc)
	} else if strings.TrimSpace(collection) != "" {
		token = strings.TrimSpace(collection) + "_"
	}
	fetchNodes := maxNodes
	if token != "" {
		fetchNodes = 1000
	}
	payload, err := s.client.FetchGraph(ctx, label, maxDepth, fetchNodes)
	if err != nil {
		return GraphView{}, fmt.Errorf("fetch graph: %w", err)
	}
	return mapGraphView(payload, token, maxNodes)
}

// SearchEntities returns graph entity labels for admin search boxes.
func (s *GraphQueryService) SearchEntities(ctx context.Context, keyword string, limit int) ([]string, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("知识图谱通道未启用（rag.graph.type=none）")
	}
	return s.client.FetchLabels(ctx, keyword, limit)
}

type rawGraphView struct {
	Nodes       []rawGraphNode `json:"nodes"`
	Edges       []rawGraphEdge `json:"edges"`
	IsTruncated bool           `json:"is_truncated"`
}

type rawGraphNode struct {
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Properties map[string]any `json:"properties"`
}

type rawGraphEdge struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

func mapGraphView(payload []byte, token string, limit int) (GraphView, error) {
	var raw rawGraphView
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &raw); err != nil {
			return GraphView{}, fmt.Errorf("unmarshal graph payload: %w", err)
		}
	}
	nodes := make([]GraphViewNode, 0, len(raw.Nodes))
	kept := make(map[string]struct{}, len(raw.Nodes))
	truncated := raw.IsTruncated
	for _, node := range raw.Nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			continue
		}
		if token != "" && !graphViewNodeMatches(node.Properties, token) {
			continue
		}
		if limit > 0 && len(nodes) >= limit {
			truncated = true
			break
		}
		name := graphStringProp(node.Properties, "entity_id")
		if name == "" && len(node.Labels) > 0 {
			name = strings.TrimSpace(node.Labels[0])
		}
		if name == "" {
			name = id
		}
		kept[id] = struct{}{}
		nodes = append(nodes, GraphViewNode{
			ID:          id,
			Name:        name,
			Type:        graphStringProp(node.Properties, "entity_type"),
			Description: cleanGraphMerged(graphStringProp(node.Properties, "description"), "\n"),
		})
	}
	edges := make([]GraphViewEdge, 0, len(raw.Edges))
	for _, edge := range raw.Edges {
		source := strings.TrimSpace(edge.Source)
		target := strings.TrimSpace(edge.Target)
		if source == "" || target == "" {
			continue
		}
		if _, ok := kept[source]; !ok {
			continue
		}
		if _, ok := kept[target]; !ok {
			continue
		}
		label := cleanGraphMerged(graphStringProp(edge.Properties, "keywords"), " / ")
		if label == "" {
			label = strings.TrimSpace(edge.Type)
		}
		id := strings.TrimSpace(edge.ID)
		if id == "" {
			id = source + "-" + target
		}
		edges = append(edges, GraphViewEdge{
			ID:          id,
			Source:      source,
			Target:      target,
			Label:       label,
			Description: cleanGraphMerged(graphStringProp(edge.Properties, "description"), "\n"),
		})
	}
	return GraphView{Nodes: nodes, Edges: edges, Truncated: truncated}, nil
}

func graphViewNodeMatches(props map[string]any, token string) bool {
	filePath := graphStringProp(props, "file_path")
	if filePath == "" {
		return false
	}
	if strings.HasSuffix(token, "_") {
		source := ParseGraphFileSource(filePath)
		return source != nil && strings.EqualFold(source.CollectionName, strings.TrimSuffix(token, "_"))
	}
	return strings.Contains(filePath, token)
}

func graphStringProp(props map[string]any, key string) string {
	if len(props) == 0 {
		return ""
	}
	value, ok := props[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func cleanGraphMerged(raw, joiner string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	seen := make(map[string]struct{})
	parts := strings.Split(raw, "<SEP>")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		piece := strings.TrimSpace(part)
		if piece == "" {
			continue
		}
		if _, ok := seen[piece]; ok {
			continue
		}
		seen[piece] = struct{}{}
		result = append(result, piece)
	}
	return strings.Join(result, joiner)
}
