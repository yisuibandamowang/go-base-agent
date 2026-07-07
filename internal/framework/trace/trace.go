package trace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ctxKey int

const keyTrace ctxKey = iota

type TraceNode struct {
	NodeID       string    `json:"nodeId"`
	NodeName     string    `json:"nodeName"`
	NodeType     string    `json:"nodeType"`
	ParentNodeID string    `json:"parentNodeId,omitempty"`
	Depth        int       `json:"depth"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
	DurationMs   int64     `json:"durationMs"`
}

type TraceContext struct {
	mu       sync.Mutex
	TraceID  string       `json:"traceId"`
	Name     string       `json:"name"`
	Status   string       `json:"status"`
	ErrorMsg string       `json:"errorMessage,omitempty"`
	Start    time.Time    `json:"startTime"`
	End      time.Time    `json:"endTime,omitempty"`
	Nodes    []*TraceNode `json:"nodes"`

	stack []string // 当前节点 ID 栈
}

func NewTraceContext(name string) *TraceContext {
	return &TraceContext{
		TraceID: uuid.New().String(),
		Name:    name,
		Status:  "RUNNING",
		Start:   time.Now(),
		Nodes:   make([]*TraceNode, 0),
		stack:   make([]string, 0),
	}
}

func WithTrace(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, keyTrace, tc)
}

func FromContext(ctx context.Context) *TraceContext {
	tc, _ := ctx.Value(keyTrace).(*TraceContext)
	return tc
}

func (tc *TraceContext) Finish(err error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.End = time.Now()
	if err != nil {
		tc.Status = "ERROR"
		tc.ErrorMsg = err.Error()
	} else {
		tc.Status = "SUCCESS"
	}
}

func (tc *TraceContext) startNode(name string, typ string) (string, int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if typ == "" {
		typ = "METHOD"
	}

	depth := len(tc.stack)
	parentID := ""
	if depth > 0 {
		parentID = tc.stack[depth-1]
	}

	nodeID := uuid.New().String()
	tc.stack = append(tc.stack, nodeID)

	node := &TraceNode{
		NodeID:       nodeID,
		NodeName:     name,
		NodeType:     typ,
		ParentNodeID: parentID,
		Depth:        depth,
		Status:       "RUNNING",
		StartTime:    time.Now(),
	}
	tc.Nodes = append(tc.Nodes, node)

	return nodeID, depth
}

func (tc *TraceContext) endNode(nodeID string, err error) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for _, n := range tc.Nodes {
		if n.NodeID == nodeID {
			n.EndTime = time.Now()
			n.DurationMs = n.EndTime.Sub(n.StartTime).Milliseconds()
			if err != nil {
				n.Status = "ERROR"
				n.ErrorMessage = err.Error()
			} else {
				n.Status = "SUCCESS"
			}
			break
		}
	}

	// pop stack
	for i := len(tc.stack) - 1; i >= 0; i-- {
		if tc.stack[i] == nodeID {
			tc.stack = append(tc.stack[:i], tc.stack[i+1:]...)
			break
		}
	}
}

func Traced[Req, Resp any](nodeName string, fn func(context.Context, Req) (Resp, error)) func(context.Context, Req) (Resp, error) {
	return func(ctx context.Context, req Req) (resp Resp, err error) {
		tc := FromContext(ctx)
		if tc == nil {
			return fn(ctx, req)
		}

		nodeID, _ := tc.startNode(nodeName, "METHOD")
		resp, err = fn(ctx, req)
		tc.endNode(nodeID, err)
		return resp, err
	}
}

func Traced2[Req, Resp any](nodeName string, nodeType string, fn func(context.Context, Req) (Resp, error)) func(context.Context, Req) (Resp, error) {
	return func(ctx context.Context, req Req) (resp Resp, err error) {
		tc := FromContext(ctx)
		if tc == nil {
			return fn(ctx, req)
		}

		nodeID, _ := tc.startNode(nodeName, nodeType)
		resp, err = fn(ctx, req)
		tc.endNode(nodeID, err)
		return resp, err
	}
}

func TracedVoid(nodeName string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) (err error) {
		tc := FromContext(ctx)
		if tc == nil {
			return fn(ctx)
		}

		nodeID, _ := tc.startNode(nodeName, "METHOD")
		err = fn(ctx)
		tc.endNode(nodeID, err)
		return err
	}
}

func (tc *TraceContext) DurationMs() int64 {
	if tc.End.IsZero() {
		return time.Since(tc.Start).Milliseconds()
	}
	return tc.End.Sub(tc.Start).Milliseconds()
}

func (tc *TraceContext) String() string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return fmt.Sprintf(
		"Trace(id=%s name=%s status=%s nodes=%d duration=%dms)",
		tc.TraceID, tc.Name, tc.Status, len(tc.Nodes), tc.DurationMs(),
	)
}
