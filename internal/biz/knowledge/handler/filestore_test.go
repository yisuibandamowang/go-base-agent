package handler

import (
	"context"
	"errors"
	"testing"

	"go-base-agent/internal/framework/config"
)

type failingFileBackend struct {
	err error
}

func (f failingFileBackend) Put(ctx context.Context, collectionName, docID, name string, data []byte) error {
	return f.err
}

func (f failingFileBackend) Get(ctx context.Context, collectionName, docID string) (*storedFile, bool, error) {
	return nil, false, f.err
}

func (f failingFileBackend) Read(ctx context.Context, collectionName, docID string) ([]byte, error) {
	return nil, f.err
}

func (f failingFileBackend) Delete(ctx context.Context, collectionName, docID string) error {
	return f.err
}

func (f failingFileBackend) DeleteKnowledgeSpace(ctx context.Context, collectionName string) error {
	return f.err
}

func TestNewConfiguredFileStoreFallsBackToMemoryWhenRustFSMissing(t *testing.T) {
	store, err := NewConfiguredFileStore(config.RustFSConfig{})
	if err != nil {
		t.Fatalf("expected memory fallback, got error: %v", err)
	}

	if err := store.PutWithContext(context.Background(), "doc-1", "a.md", []byte("hello")); err != nil {
		t.Fatalf("put memory file: %v", err)
	}
	data, err := store.Read("doc-1")
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected hello, got %q", string(data))
	}
}

func TestFileStorePutWithContextReturnsBackendError(t *testing.T) {
	expected := errors.New("object storage unavailable")
	store := &FileStore{backend: failingFileBackend{err: expected}}

	err := store.PutWithContext(context.Background(), "doc-1", "a.md", []byte("hello"))
	if !errors.Is(err, expected) {
		t.Fatalf("expected backend error, got %v", err)
	}
}

func TestFileStoreGetReturnsBackendErrorAsMiss(t *testing.T) {
	store := &FileStore{backend: failingFileBackend{err: errors.New("boom")}}

	if _, ok := store.Get("doc-1"); ok {
		t.Fatal("expected failed backend get to be treated as a miss")
	}
}

func TestFileStoreDeleteRemovesMemoryObject(t *testing.T) {
	store := NewFileStore()
	if err := store.PutWithContext(context.Background(), "doc-1", "a.md", []byte("hello")); err != nil {
		t.Fatalf("put memory file: %v", err)
	}
	if err := store.Delete(context.Background(), "doc-1"); err != nil {
		t.Fatalf("delete memory file: %v", err)
	}
	if _, ok := store.Get("doc-1"); ok {
		t.Fatal("expected deleted file to be missing")
	}
}

func TestFileStoreDeleteReturnsBackendError(t *testing.T) {
	expected := errors.New("delete unavailable")
	store := &FileStore{backend: failingFileBackend{err: expected}}

	err := store.Delete(context.Background(), "doc-1")
	if !errors.Is(err, expected) {
		t.Fatalf("expected backend error, got %v", err)
	}
}

func TestFileStoreDeleteKnowledgeSpaceRemovesCollectionObjects(t *testing.T) {
	store := NewFileStore()
	if err := store.PutWithCollection(context.Background(), "kb-a", "doc-1", "a.md", []byte("alpha")); err != nil {
		t.Fatalf("put kb-a doc-1: %v", err)
	}
	if err := store.PutWithCollection(context.Background(), "kb-b", "doc-2", "b.md", []byte("beta")); err != nil {
		t.Fatalf("put kb-b doc-2: %v", err)
	}

	if err := store.DeleteKnowledgeSpace(context.Background(), "kb-a"); err != nil {
		t.Fatalf("delete knowledge space: %v", err)
	}
	if _, ok, err := store.GetWithCollection(context.Background(), "kb-a", "doc-1"); err != nil {
		t.Fatalf("get kb-a doc-1: %v", err)
	} else if ok {
		t.Fatal("expected kb-a doc to be deleted")
	}
	if data, err := store.ReadWithCollection(context.Background(), "kb-b", "doc-2"); err != nil || string(data) != "beta" {
		t.Fatalf("expected kb-b doc to remain, data=%q err=%v", string(data), err)
	}
}
