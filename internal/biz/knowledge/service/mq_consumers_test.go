package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go-base-agent/internal/framework/mq"
)

func TestRegisterKnowledgeBaseCleanupConsumerDropsVectorSpace(t *testing.T) {
	consumer := mq.NewNoopConsumer()
	vecStore := &capturingVectorSpaceCleaner{}
	kbSvc := NewKnowledgeBaseService(nil)
	kbSvc.SetVectorStore(vecStore)

	if err := RegisterKnowledgeBaseCleanupConsumer(consumer, kbSvc); err != nil {
		t.Fatalf("register cleanup consumer: %v", err)
	}
	body, err := json.Marshal(KnowledgeBaseCleanupEvent{
		KBID:           "kb-1",
		CollectionName: "kb_cleanup_event",
		Operator:       "operator-1",
	})
	if err != nil {
		t.Fatalf("marshal cleanup event: %v", err)
	}

	if err := consumer.DispatchForTest(context.Background(), KnowledgeBaseCleanupTopic, mq.Message{Body: body}); err != nil {
		t.Fatalf("dispatch cleanup event: %v", err)
	}
	if len(vecStore.dropCalls) != 1 || vecStore.dropCalls[0] != "kb_cleanup_event" {
		t.Fatalf("expected cleanup consumer to drop kb_cleanup_event, got %+v", vecStore.dropCalls)
	}
}

func TestRegisterKnowledgeBaseCleanupConsumerDeletesKnowledgeSpaceFiles(t *testing.T) {
	consumer := mq.NewNoopConsumer()
	fileDeleter := &capturingKnowledgeSpaceDeleter{}
	kbSvc := NewKnowledgeBaseService(nil)
	kbSvc.SetFileDeleter(fileDeleter)

	if err := RegisterKnowledgeBaseCleanupConsumer(consumer, kbSvc); err != nil {
		t.Fatalf("register cleanup consumer: %v", err)
	}
	body, err := json.Marshal(KnowledgeBaseCleanupEvent{
		KBID:           "kb-1",
		CollectionName: "kb_cleanup_event",
		Operator:       "operator-1",
	})
	if err != nil {
		t.Fatalf("marshal cleanup event: %v", err)
	}

	if err := consumer.DispatchForTest(context.Background(), KnowledgeBaseCleanupTopic, mq.Message{Body: body}); err != nil {
		t.Fatalf("dispatch cleanup event: %v", err)
	}
	if len(fileDeleter.deleteCalls) != 1 || fileDeleter.deleteCalls[0] != "kb_cleanup_event" {
		t.Fatalf("expected cleanup consumer to delete kb_cleanup_event, got %+v", fileDeleter.deleteCalls)
	}
}

func TestRegisterKnowledgeDocumentChunkConsumerRejectsInvalidPayload(t *testing.T) {
	consumer := mq.NewNoopConsumer()
	if err := RegisterKnowledgeDocumentChunkConsumer(consumer, &DocumentService{}); err != nil {
		t.Fatalf("register chunk consumer: %v", err)
	}

	err := consumer.DispatchForTest(context.Background(), KnowledgeDocumentChunkTopic, mq.Message{Body: []byte("{")})
	if err == nil || !strings.Contains(err.Error(), "decode knowledge document chunk event") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

type capturingKnowledgeSpaceDeleter struct {
	deleteCalls []string
}

func (d *capturingKnowledgeSpaceDeleter) DeleteKnowledgeSpace(_ context.Context, collectionName string) error {
	d.deleteCalls = append(d.deleteCalls, collectionName)
	return nil
}
