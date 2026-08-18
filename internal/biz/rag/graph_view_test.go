package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestLightRagClientFetchGraphAndLabels(t *testing.T) {
	var graphQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/graphs":
			graphQuery = r.URL.Query()
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}, "edges": []any{}, "is_truncated": false})
		case r.Method == http.MethodGet && r.URL.Path == "/graph/label/search":
			if r.URL.Query().Get("q") != "报销" {
				t.Fatalf("unexpected label keyword: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]string{{"label": "报销流程"}, {"name": "费用制度"}})
		case r.Method == http.MethodGet && r.URL.Path == "/graph/label/popular":
			_ = json.NewEncoder(w).Encode([]string{"热门实体"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLightRagClient(server.URL, "", nil, 0)
	raw, err := client.FetchGraph(context.Background(), "财务", 2, 120)
	if err != nil {
		t.Fatalf("fetch graph: %v", err)
	}
	if !strings.Contains(string(raw), `"nodes"`) {
		t.Fatalf("unexpected graph payload: %s", raw)
	}
	if graphQuery.Get("label") != "财务" || graphQuery.Get("max_depth") != "2" || graphQuery.Get("max_nodes") != "120" {
		t.Fatalf("unexpected graph query: %v", graphQuery)
	}

	labels, err := client.FetchLabels(context.Background(), "报销", 20)
	if err != nil {
		t.Fatalf("fetch search labels: %v", err)
	}
	if got, want := labels, []string{"报销流程", "费用制度"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels mismatch: got %v want %v", got, want)
	}
	labels, err = client.FetchLabels(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("fetch popular labels: %v", err)
	}
	if got, want := labels, []string{"热门实体"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("popular labels mismatch: got %v want %v", got, want)
	}
}

func TestGraphQueryServiceMapsAndFiltersGraphView(t *testing.T) {
	client := &recordingGraphViewClient{payload: []byte(`{
		"is_truncated": false,
		"nodes": [
			{"id":"n1","labels":["Fallback"],"properties":{"entity_id":"报销流程","entity_type":"process","description":"提交申请<SEP>提交申请","file_path":"kb_1954071234567890100.txt"}},
			{"id":"n2","labels":["费用制度"],"properties":{"entity_type":"policy","description":"制度说明","file_path":"kb_1954071234567890100.txt"}},
			{"id":"n3","labels":["别库"],"properties":{"entity_id":"别库实体","file_path":"kb_hr_1954071234567890200.txt"}}
		],
		"edges": [
			{"id":"e1","source":"n1","target":"n2","type":"RELATED","properties":{"keywords":"包含<SEP>包含<SEP>适用","description":"关系一<SEP>关系二"}},
			{"id":"e2","source":"n1","target":"n3","type":"DROP","properties":{"keywords":"丢弃"}}
		]
	}`)}
	service := NewGraphQueryService(client)

	view, err := service.GetGraph(context.Background(), "报销", "kb", "", 2, 10)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if client.label != "报销" || client.maxDepth != 2 || client.maxNodes != 1000 {
		t.Fatalf("unexpected fetch args: %+v", client)
	}
	if len(view.Nodes) != 2 || len(view.Edges) != 1 {
		t.Fatalf("unexpected graph view: %+v", view)
	}
	if view.Nodes[0].Name != "报销流程" || view.Nodes[0].Description != "提交申请" {
		t.Fatalf("unexpected first node: %+v", view.Nodes[0])
	}
	if view.Nodes[1].Name != "费用制度" {
		t.Fatalf("expected label fallback, got %+v", view.Nodes[1])
	}
	if view.Edges[0].Label != "包含 / 适用" || view.Edges[0].Description != "关系一\n关系二" {
		t.Fatalf("unexpected edge: %+v", view.Edges[0])
	}
}

func TestGraphQueryServiceRequiresEnabledClient(t *testing.T) {
	service := NewGraphQueryService(nil)

	_, err := service.GetGraph(context.Background(), "", "", "", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "知识图谱通道未启用") {
		t.Fatalf("expected disabled graph error, got %v", err)
	}
}

type recordingGraphViewClient struct {
	payload  []byte
	err      error
	label    string
	maxDepth int
	maxNodes int
}

func (r *recordingGraphViewClient) FetchGraph(_ context.Context, label string, maxDepth, maxNodes int) ([]byte, error) {
	r.label = label
	r.maxDepth = maxDepth
	r.maxNodes = maxNodes
	if r.err != nil {
		return nil, r.err
	}
	return r.payload, nil
}

func (r *recordingGraphViewClient) FetchLabels(context.Context, string, int) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return []string{"报销流程"}, nil
}

func TestGraphQueryServicePropagatesClientErrors(t *testing.T) {
	service := NewGraphQueryService(&recordingGraphViewClient{err: errors.New("down")})

	_, err := service.GetGraph(context.Background(), "", "", "", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "fetch graph") {
		t.Fatalf("expected fetch graph error, got %v", err)
	}
}
