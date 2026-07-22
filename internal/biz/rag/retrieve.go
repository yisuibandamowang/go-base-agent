package rag

import "context"

// RetrievedChunk represents a retrieved document chunk.
type RetrievedChunk struct {
	ID       string
	Text     string
	Score    float64
	Metadata map[string]string
}

// Retriever retrieves relevant chunks for a question.
// Aligns with Java RetrieverService / RetrievalEngine.
type Retriever interface {
	Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error)
}

// IntentAwareRetriever can receive the richer search context with intents.
type IntentAwareRetriever interface {
	RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error)
}

// NoopRetriever returns empty results.
type NoopRetriever struct{}

func (n *NoopRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	return nil, nil
}
