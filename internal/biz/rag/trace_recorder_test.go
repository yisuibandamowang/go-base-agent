package rag

import (
	"context"
	"testing"

	"go-base-agent/internal/infra/chat"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPipelineStreamChatRecordsTraceRunAndNodes(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&TraceRunRecord{}, &TraceNodeRecord{}); err != nil {
		t.Fatalf("migrate trace tables: %v", err)
	}

	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent("hello")
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}
	sender, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder(), &NoopRewriter{}, testRetriever(), &NoopMemoryService{})
	p.SetTraceRecorder(NewDBTraceRecorder(gdb, 1000))

	p.StreamChat(context.Background(), "question", "conv-1", "task-1", false, sender)

	var run TraceRunRecord
	if err := gdb.Table("t_rag_trace_run").First(&run).Error; err != nil {
		t.Fatalf("expected trace run: %v", err)
	}
	if run.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS trace run, got %+v", run)
	}
	if run.ConversationID != "conv-1" || run.TaskID != "task-1" {
		t.Fatalf("unexpected trace identifiers: %+v", run)
	}

	var nodes []TraceNodeRecord
	if err := gdb.Table("t_rag_trace_node").Order("start_time ASC").Find(&nodes).Error; err != nil {
		t.Fatalf("list trace nodes: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least two trace nodes, got %d", len(nodes))
	}
	if !hasTraceNode(nodes, "retrieve") {
		t.Fatalf("expected retrieve node, got %+v", nodes)
	}
	if !hasTraceNode(nodes, "llm-stream") {
		t.Fatalf("expected llm-stream node, got %+v", nodes)
	}
}

func hasTraceNode(nodes []TraceNodeRecord, name string) bool {
	for _, node := range nodes {
		if node.NodeName == name && node.Status == "SUCCESS" {
			return true
		}
	}
	return false
}
