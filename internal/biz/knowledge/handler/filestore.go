package handler

import (
	"sync"
)

// FileStore provides simple in-memory file storage for document uploads.
type FileStore struct {
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
	return &FileStore{files: make(map[string]*storedFile)}
}

// Put stores a file.
func (s *FileStore) Put(docID string, name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[docID] = &storedFile{Name: name, Data: data, ContentType: "application/octet-stream"}
}

// Get retrieves a file.
func (s *FileStore) Get(docID string) (*storedFile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[docID]
	return f, ok
}
