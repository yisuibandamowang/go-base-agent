package embedding

import "context"

// Service provides text vectorization for RAG retrieval and indexing.
// Aligns with Java EmbeddingService.
type Service interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedWithModel(ctx context.Context, text, modelID string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	EmbedBatchWithModel(ctx context.Context, texts []string, modelID string) ([][]float32, error)
	Dimension() int
}
