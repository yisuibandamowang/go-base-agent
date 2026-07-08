package embedding

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// Client is a provider-specific embedding implementation.
// Aligns with Java EmbeddingClient.
type Client interface {
	Provider() string
	Embed(ctx context.Context, text string, target model.Target) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string, target model.Target) ([][]float32, error)
}
