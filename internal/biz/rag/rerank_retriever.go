package rag

import (
	"context"

	"go-base-agent/internal/infra/rerank"
)

type RerankRetriever struct {
	base    Retriever
	rerank  rerank.Service
	enabled bool
}

func NewRerankRetriever(base Retriever, rerankSvc rerank.Service) *RerankRetriever {
	return &RerankRetriever{base: base, rerank: rerankSvc, enabled: base != nil && rerankSvc != nil}
}

func (r *RerankRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	chunks, err := r.base.Retrieve(ctx, question, topK)
	if err != nil || !r.enabled || len(chunks) == 0 {
		return chunks, err
	}

	byID := make(map[string]RetrievedChunk, len(chunks))
	candidates := make([]rerank.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
		candidates = append(candidates, rerank.Chunk{
			ID:    chunk.ID,
			Text:  chunk.Text,
			Score: chunk.Score,
		})
	}

	reranked, err := r.rerank.Rerank(ctx, question, candidates, topK)
	if err != nil {
		return nil, err
	}
	result := make([]RetrievedChunk, 0, len(reranked))
	for _, item := range reranked {
		chunk, ok := byID[item.ID]
		if !ok {
			continue
		}
		chunk.Score = item.Score
		result = append(result, chunk)
	}
	return result, nil
}
