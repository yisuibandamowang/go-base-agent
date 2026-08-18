package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/biz/rag"

	"github.com/gin-gonic/gin"
)

func TestGraphHandlerReturnsGraphView(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &recordingGraphService{
		view: rag.GraphView{
			Nodes: []rag.GraphViewNode{{ID: "n1", Name: "报销流程", Type: "process", Description: "提交申请"}},
			Edges: []rag.GraphViewEdge{{ID: "e1", Source: "n1", Target: "n2", Label: "包含", Description: "关系"}},
		},
	}
	h := NewGraphHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/admin/kg/graph", h.Graph)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/kg/graph?entity=报销&collection=kb&doc=doc-1&depth=3&limit=50", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if svc.entity != "报销" || svc.collection != "kb" || svc.doc != "doc-1" || svc.depth != 3 || svc.limit != 50 {
		t.Fatalf("unexpected service args: %+v", svc)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected graph data, got %s", w.Body.String())
	}
	if nodes, ok := data["nodes"].([]any); !ok || len(nodes) != 1 {
		t.Fatalf("unexpected nodes: %s", w.Body.String())
	}
}

func TestGraphHandlerReturnsLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &recordingGraphService{labels: []string{"报销流程", "费用制度"}}
	h := NewGraphHandler(svc)
	r := gin.New()
	r.GET("/api/ragent/admin/kg/labels", h.Labels)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/admin/kg/labels?keyword=报销&limit=20", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if svc.keyword != "报销" || svc.labelLimit != 20 {
		t.Fatalf("unexpected label args: %+v", svc)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	labels, ok := resp["data"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "报销流程" {
		t.Fatalf("unexpected labels response: %s", w.Body.String())
	}
}

type recordingGraphService struct {
	view       rag.GraphView
	labels     []string
	entity     string
	collection string
	doc        string
	depth      int
	limit      int
	keyword    string
	labelLimit int
}

func (r *recordingGraphService) GetGraph(_ context.Context, entity, collection, doc string, depth, limit int) (rag.GraphView, error) {
	r.entity = entity
	r.collection = collection
	r.doc = doc
	r.depth = depth
	r.limit = limit
	return r.view, nil
}

func (r *recordingGraphService) SearchEntities(_ context.Context, keyword string, limit int) ([]string, error) {
	r.keyword = keyword
	r.labelLimit = limit
	return r.labels, nil
}
