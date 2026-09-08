package rag

import "context"

// RetrievedChunk represents a retrieved document chunk.
type RetrievedChunk struct {
	ID   string
	Text string
	// Score 被余弦/BM25/RRF 轮番覆写认不出写入方，与 Java 一致地把精排分单独存放：
	// 只有真精排客户端会写 RerankScore，nil 表示该块没跑过精排。
	Score       float64
	RerankScore *float64
	Metadata    map[string]string
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

// RetrievalResult carries retrieved chunks and the scope information needed by prompt selection.
type RetrievalResult struct {
	Chunks            []RetrievedChunk
	DirectedIntentIDs map[string]struct{}
}

// ScopedIntentAwareRetriever returns retrieval results together with the resolved intent scope.
type ScopedIntentAwareRetriever interface {
	RetrieveWithContextResult(ctx context.Context, sc SearchContext) (RetrievalResult, error)
}

// GlobalRetriever can perform a single global retrieval with a total candidate budget.
type GlobalRetriever interface {
	SupportsGlobalRetrieval() bool
	RetrieveGlobal(ctx context.Context, question string, topK int) ([]RetrievedChunk, error)
}

// NoopRetriever returns empty results.
type NoopRetriever struct{}

func (n *NoopRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	return nil, nil
}
