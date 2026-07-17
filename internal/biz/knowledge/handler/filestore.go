package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"

	"go-base-agent/internal/framework/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// FileStore provides file storage for document uploads.
type FileStore struct {
	backend fileStoreBackend
}

type fileStoreBackend interface {
	Put(ctx context.Context, docID, name string, data []byte) error
	Get(ctx context.Context, docID string) (*storedFile, bool, error)
	Read(ctx context.Context, docID string) ([]byte, error)
}

type memoryFileBackend struct {
	mu    sync.RWMutex
	files map[string]*storedFile
}

type storedFile struct {
	Name        string
	ContentType string
	Data        []byte
}

// NewFileStore creates a FileStore.
func NewFileStore() *FileStore {
	return &FileStore{backend: &memoryFileBackend{files: make(map[string]*storedFile)}}
}

// NewConfiguredFileStore creates a FileStore from RustFS/S3 configuration.
// It falls back to memory storage when object storage is not fully configured.
func NewConfiguredFileStore(cfg config.RustFSConfig) (*FileStore, error) {
	if strings.TrimSpace(cfg.URL) == "" ||
		strings.TrimSpace(cfg.AccessKeyID) == "" ||
		strings.TrimSpace(cfg.SecretAccessKey) == "" ||
		strings.TrimSpace(cfg.KBBucket) == "" {
		return NewFileStore(), nil
	}
	return NewS3FileStore(context.Background(), cfg)
}

// NewS3FileStore creates a FileStore backed by S3-compatible object storage.
func NewS3FileStore(ctx context.Context, cfg config.RustFSConfig) (*FileStore, error) {
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load s3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(strings.TrimRight(cfg.URL, "/"))
		o.UsePathStyle = true
	})
	return &FileStore{backend: &s3FileBackend{
		client: client,
		bucket: cfg.KBBucket,
	}}, nil
}

// Put stores a file.
func (s *FileStore) Put(docID string, name string, data []byte) {
	if err := s.PutWithContext(context.Background(), docID, name, data); err != nil {
		slog.Warn("file store put failed", "docId", docID, "name", name, "err", err)
	}
}

// PutWithContext stores a file and returns storage errors to the caller.
func (s *FileStore) PutWithContext(ctx context.Context, docID string, name string, data []byte) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("file store backend is nil")
	}
	return s.backend.Put(ctx, docID, name, data)
}

// Get retrieves a file.
func (s *FileStore) Get(docID string) (*storedFile, bool) {
	if s == nil || s.backend == nil {
		return nil, false
	}
	f, ok, err := s.backend.Get(context.Background(), docID)
	if err != nil {
		slog.Warn("file store get failed", "docId", docID, "err", err)
		return nil, false
	}
	return f, ok
}

// Read implements service.FileReader for chunk processing.
func (s *FileStore) Read(docID string) ([]byte, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("file store backend is nil")
	}
	return s.backend.Read(context.Background(), docID)
}

func (s *memoryFileBackend) Put(ctx context.Context, docID string, name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]byte, len(data))
	copy(copied, data)
	s.files[docID] = &storedFile{Name: name, Data: copied, ContentType: "application/octet-stream"}
	return nil
}

func (s *memoryFileBackend) Get(ctx context.Context, docID string) (*storedFile, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[docID]
	if !ok {
		return nil, false, nil
	}
	data := make([]byte, len(f.Data))
	copy(data, f.Data)
	return &storedFile{Name: f.Name, ContentType: f.ContentType, Data: data}, true, nil
}

func (s *memoryFileBackend) Read(ctx context.Context, docID string) ([]byte, error) {
	f, ok, err := s.Get(ctx, docID)
	if err != nil || !ok {
		return nil, err
	}
	return f.Data, nil
}

type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type s3FileBackend struct {
	client s3API
	bucket string
}

func (s *s3FileBackend) Put(ctx context.Context, docID, name string, data []byte) error {
	key := s.objectKey(docID)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(detectMIME(name)),
		Metadata: map[string]string{
			"original-name": name,
		},
	})
	if err != nil {
		return fmt.Errorf("put object %s/%s: %w", s.bucket, key, err)
	}
	return nil
}

func (s *s3FileBackend) Get(ctx context.Context, docID string) (*storedFile, bool, error) {
	key := s.objectKey(docID)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, false, fmt.Errorf("get object %s/%s: %w", s.bucket, key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read object %s/%s: %w", s.bucket, key, err)
	}
	name := ""
	if out.Metadata != nil {
		name = out.Metadata["original-name"]
	}
	if name == "" {
		name = docID
	}
	contentType := "application/octet-stream"
	if out.ContentType != nil && *out.ContentType != "" {
		contentType = *out.ContentType
	}
	return &storedFile{Name: name, ContentType: contentType, Data: data}, true, nil
}

func (s *s3FileBackend) Read(ctx context.Context, docID string) ([]byte, error) {
	f, ok, err := s.Get(ctx, docID)
	if err != nil || !ok {
		return nil, err
	}
	return f.Data, nil
}

func (s *s3FileBackend) objectKey(docID string) string {
	return path.Join("documents", strings.TrimSpace(docID))
}
