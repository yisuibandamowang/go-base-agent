package rag

import (
	"context"
	"errors"
	"testing"
)

func TestGraphSyncingVectorStoreIndexesDocumentChunksAfterDelegateSuccess(t *testing.T) {
	delegate := &recordingGraphSyncVectorStore{}
	syncClient := &recordingGraphSyncClient{insertErr: errors.New("boom")}
	store := NewGraphSyncingVectorStore(delegate, syncClient)

	err := store.IndexDocumentChunks(context.Background(), "kb", "doc-1", []VectorChunk{
		{ChunkID: "c1", Content: "第一段"},
		{ChunkID: "c2", Content: "第二段"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !delegate.indexCalled {
		t.Fatal("expected delegate index to be called")
	}
	if syncClient.insertText != "第一段\n\n第二段" {
		t.Fatalf("unexpected insert text: %q", syncClient.insertText)
	}
	if syncClient.fileSource != "kb_doc-1" {
		t.Fatalf("unexpected file source: %q", syncClient.fileSource)
	}
}

func TestGraphSyncingVectorStoreDeletesDocumentVectorsAfterDelegateSuccess(t *testing.T) {
	delegate := &recordingGraphSyncVectorStore{}
	syncClient := &recordingGraphSyncClient{deleteErr: errors.New("boom")}
	store := NewGraphSyncingVectorStore(delegate, syncClient)

	err := store.DeleteDocumentVectors(context.Background(), "kb", "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !delegate.deleteDocumentCalled {
		t.Fatal("expected delegate delete to be called")
	}
	if syncClient.deletedDocID != "doc-1" {
		t.Fatalf("unexpected deleted doc id: %q", syncClient.deletedDocID)
	}
}

func TestGraphSyncingVectorStoreDropsVectorSpaceAndSyncsGraphCollection(t *testing.T) {
	delegate := &recordingGraphSyncVectorStore{}
	syncClient := &recordingGraphSyncClient{}
	store := NewGraphSyncingVectorStore(delegate, syncClient)

	err := store.DropVectorSpace(context.Background(), "kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !delegate.dropVectorSpaceCalled {
		t.Fatal("expected delegate drop to be called")
	}
	if syncClient.deletedCollection != "kb" {
		t.Fatalf("unexpected deleted collection: %q", syncClient.deletedCollection)
	}
}

func TestGraphSyncingVectorStoreSkipsSyncWhenDelegateFails(t *testing.T) {
	delegate := &recordingGraphSyncVectorStore{indexErr: errors.New("delegate failed")}
	syncClient := &recordingGraphSyncClient{}
	store := NewGraphSyncingVectorStore(delegate, syncClient)

	err := store.IndexDocumentChunks(context.Background(), "kb", "doc-1", []VectorChunk{{ChunkID: "c1", Content: "正文"}})
	if err == nil {
		t.Fatal("expected delegate error")
	}
	if syncClient.insertText != "" {
		t.Fatalf("expected sync to be skipped, got %q", syncClient.insertText)
	}
}

type recordingGraphSyncVectorStore struct {
	indexCalled           bool
	deleteDocumentCalled  bool
	dropVectorSpaceCalled bool
	indexErr              error
	deleteErr             error
}

func (r *recordingGraphSyncVectorStore) IndexDocumentChunks(context.Context, string, string, []VectorChunk) error {
	r.indexCalled = true
	return r.indexErr
}

func (r *recordingGraphSyncVectorStore) UpdateChunk(context.Context, string, string, VectorChunk) error {
	return nil
}

func (r *recordingGraphSyncVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	r.deleteDocumentCalled = true
	return r.deleteErr
}

func (r *recordingGraphSyncVectorStore) DeleteChunkByID(context.Context, string, string) error {
	return nil
}

func (r *recordingGraphSyncVectorStore) DeleteChunksByIDs(context.Context, string, []string) error {
	return nil
}

func (r *recordingGraphSyncVectorStore) Search(context.Context, string, []float32, int) ([]VectorChunk, error) {
	return nil, nil
}

func (r *recordingGraphSyncVectorStore) EnsureVectorSpace(context.Context, VectorSpaceSpec) error {
	return nil
}

func (r *recordingGraphSyncVectorStore) VectorSpaceExists(context.Context, VectorSpaceID) (bool, error) {
	return true, nil
}

func (r *recordingGraphSyncVectorStore) DropVectorSpace(context.Context, string) error {
	r.dropVectorSpaceCalled = true
	return nil
}

type recordingGraphSyncClient struct {
	insertText        string
	fileSource        string
	deletedDocID      string
	deletedCollection string
	insertErr         error
	deleteErr         error
}

func (r *recordingGraphSyncClient) InsertText(_ context.Context, text, fileSource string) error {
	r.insertText = text
	r.fileSource = fileSource
	return r.insertErr
}

func (r *recordingGraphSyncClient) DeleteByDoc(_ context.Context, docID string) error {
	r.deletedDocID = docID
	return r.deleteErr
}

func (r *recordingGraphSyncClient) DeleteByCollection(_ context.Context, collectionName string) error {
	r.deletedCollection = collectionName
	return r.deleteErr
}

func TestGraphSyncingVectorStoreKeepsDelegatedSearchAndAdmin(t *testing.T) {
	delegate := &recordingGraphSyncVectorStore{}
	store := NewGraphSyncingVectorStore(delegate, nil)

	if _, err := store.Search(context.Background(), "kb", nil, 3); err != nil {
		t.Fatalf("unexpected search error: %v", err)
	}
	if err := store.EnsureVectorSpace(context.Background(), VectorSpaceSpec{SpaceID: VectorSpaceID{Name: "kb"}}); err != nil {
		t.Fatalf("unexpected ensure error: %v", err)
	}
	exists, err := store.VectorSpaceExists(context.Background(), VectorSpaceID{Name: "kb"})
	if err != nil {
		t.Fatalf("unexpected exists error: %v", err)
	}
	if !exists {
		t.Fatal("expected delegated exists result")
	}
	if err := store.DropVectorSpace(context.Background(), "kb"); err != nil {
		t.Fatalf("unexpected drop error: %v", err)
	}
}

var _ GraphSyncClient = (*recordingGraphSyncClient)(nil)
