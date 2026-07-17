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

func (f failingFileBackend) Put(ctx context.Context, docID, name string, data []byte) error {
	return f.err
}

func (f failingFileBackend) Get(ctx context.Context, docID string) (*storedFile, bool, error) {
	return nil, false, f.err
}

func (f failingFileBackend) Read(ctx context.Context, docID string) ([]byte, error) {
	return nil, f.err
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
