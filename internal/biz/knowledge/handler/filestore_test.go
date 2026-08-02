package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"go-base-agent/internal/framework/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

type captureS3API struct {
	putInput *s3.PutObjectInput
	getInput *s3.GetObjectInput
}

func (c *captureS3API) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	c.putInput = params
	return &s3.PutObjectOutput{}, nil
}

func (c *captureS3API) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.getInput = params
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader([]byte("hello"))),
		Metadata: map[string]string{
			originalNameMetadataKey: encodeOriginalName("测试 文件.md"),
		},
		ContentType: aws.String("text/markdown"),
	}, nil
}

func (c *captureS3API) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}

func (c *captureS3API) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return &s3.ListObjectsV2Output{}, nil
}

func TestS3FileBackend_EncodesOriginalNameMetadata(t *testing.T) {
	api := &captureS3API{}
	backend := &s3FileBackend{client: api, bucket: "kb"}

	err := backend.Put(context.Background(), "goknowladge", "doc-1", "测试 文件.md", []byte("hello"))
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if api.putInput == nil {
		t.Fatal("expected put input to be captured")
	}
	meta := api.putInput.Metadata[originalNameMetadataKey]
	if meta == "" {
		t.Fatal("expected original-name metadata to be set")
	}
	if got := decodeOriginalName(meta); got != "测试 文件.md" {
		t.Fatalf("unexpected decoded metadata: %q", got)
	}
	if meta == "测试 文件.md" {
		t.Fatal("expected metadata to be encoded before upload")
	}
}

func TestS3FileBackend_DecodesOriginalNameMetadata(t *testing.T) {
	api := &captureS3API{}
	backend := &s3FileBackend{client: api, bucket: "kb"}

	file, ok, err := backend.Get(context.Background(), "goknowladge", "doc-1")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if !ok {
		t.Fatal("expected object to exist")
	}
	if file.Name != "测试 文件.md" {
		t.Fatalf("unexpected file name: %q", file.Name)
	}
	if string(file.Data) != "hello" {
		t.Fatalf("unexpected file data: %q", string(file.Data))
	}
}
