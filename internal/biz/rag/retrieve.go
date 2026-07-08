package rag

import "context"

// RetrievedChunk represents a retrieved document chunk.
type RetrievedChunk struct {
	ID    string
	Text  string
	Score float64
}

// Retriever retrieves relevant chunks for a question.
// Aligns with Java RetrieverService / RetrievalEngine.
type Retriever interface {
	Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error)
}

// NoopRetriever returns empty results.
type NoopRetriever struct{}

func (n *NoopRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	return nil, nil
}
