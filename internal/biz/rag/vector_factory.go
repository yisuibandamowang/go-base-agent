package rag

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// NewVectorStore creates the configured vector store.
func NewVectorStore(ctx context.Context, vectorType, milvusURI string, db *gorm.DB, dimension int, metricType string) (VectorStore, error) {
	switch strings.ToLower(strings.TrimSpace(vectorType)) {
	case "", "pg", "postgres", "pgvector":
		return NewPgVectorStore(db), nil
	case "milvus":
		if strings.TrimSpace(milvusURI) == "" {
			return nil, fmt.Errorf("milvus uri is empty")
		}
		client, err := newMilvusClient(ctx, strings.TrimSpace(milvusURI))
		if err != nil {
			return nil, fmt.Errorf("connect milvus: %w", err)
		}
		return NewMilvusVectorStore(client, dimension, metricType), nil
	default:
		return nil, fmt.Errorf("unsupported vector store type: %s", vectorType)
	}
}
