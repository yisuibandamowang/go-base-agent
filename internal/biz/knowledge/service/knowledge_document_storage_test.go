package service

import (
	"context"
	"errors"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"
)

type collectionHintFileReader struct {
	wantCollection string
	data           []byte
	readCalled     bool
	collectionSeen []string
}

func (r *collectionHintFileReader) Read(docID string) ([]byte, error) {
	r.readCalled = true
	return nil, errors.New("legacy read should not be used")
}

func (r *collectionHintFileReader) ReadWithCollection(_ context.Context, collectionName, docID string) ([]byte, error) {
	r.collectionSeen = append(r.collectionSeen, collectionName+":"+docID)
	if collectionName != r.wantCollection {
		return nil, errors.New("collection miss")
	}
	return append([]byte(nil), r.data...), nil
}

type missingKnowledgeBaseRepo struct{}

func (missingKnowledgeBaseRepo) FindByID(context.Context, string) (*knowledgeModel.KnowledgeBase, error) {
	return nil, errors.New("knowledge base missing")
}

func TestReadDocumentBytesUsesUploadFileURLCollectionHint(t *testing.T) {
	reader := &collectionHintFileReader{
		wantCollection: "kb_collection",
		data:           []byte("hello"),
	}
	svc := &DocumentService{
		fileStore: reader,
		kbRepo:    missingKnowledgeBaseRepo{},
	}
	doc := &knowledgeModel.KnowledgeDocument{
		BaseModel: db.BaseModel{ID: "doc-1"},
		KbID:      "kb-1",
		DocName:   "doc.md",
		FileURL:   "upload://kb_collection/doc.md",
	}

	data, err := svc.readDocumentBytes(context.Background(), doc)
	if err != nil {
		t.Fatalf("read document bytes: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected hello, got %q", string(data))
	}
	if reader.readCalled {
		t.Fatal("expected legacy read path to be skipped when collection hint is present")
	}
	if len(reader.collectionSeen) != 1 || reader.collectionSeen[0] != "kb_collection:doc-1" {
		t.Fatalf("unexpected collection read calls: %#v", reader.collectionSeen)
	}
}
