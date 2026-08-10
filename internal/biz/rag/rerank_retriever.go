package rag

import (
	"context"
	"sort"
	"strconv"
	"strings"

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
	return restoreStrongKeywordAnchors(result, chunks, topK), nil
}

// RetrieveWithContext runs retrieval with the richer search context when supported.
func (r *RerankRetriever) RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	if r == nil || r.base == nil {
		return nil, nil
	}
	var (
		chunks []RetrievedChunk
		err    error
	)
	if intentAware, ok := r.base.(IntentAwareRetriever); ok {
		chunks, err = intentAware.RetrieveWithContext(ctx, sc)
	} else {
		question := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
		chunks, err = r.base.Retrieve(ctx, question, sc.TopK)
	}
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

	question := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
	reranked, err := r.rerank.Rerank(ctx, question, candidates, sc.TopK)
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
	return restoreStrongKeywordAnchors(result, chunks, sc.TopK), nil
}

func restoreStrongKeywordAnchors(reranked, candidates []RetrievedChunk, topK int) []RetrievedChunk {
	if len(reranked) == 0 || len(candidates) == 0 {
		return reranked
	}
	if topK <= 0 {
		topK = len(reranked)
	}
	present := make(map[string]bool, len(reranked))
	for _, chunk := range reranked {
		present[chunk.ID] = true
	}
	anchors := make([]RetrievedChunk, 0)
	for _, chunk := range candidates {
		if present[chunk.ID] || !isStrongKeywordAnchor(chunk) {
			continue
		}
		anchors = append(anchors, chunk)
	}
	if len(anchors) == 0 {
		return truncateRerankResult(reranked, topK)
	}
	sort.SliceStable(anchors, func(i, j int) bool {
		return keywordAnchorScore(anchors[i]) > keywordAnchorScore(anchors[j])
	})
	anchorLimit := topK / 5
	if anchorLimit < 1 {
		anchorLimit = 1
	}
	if anchorLimit > 3 {
		anchorLimit = 3
	}
	if len(anchors) > anchorLimit {
		anchors = anchors[:anchorLimit]
	}
	result := make([]RetrievedChunk, 0, topK)
	result = append(result, anchors...)
	used := make(map[string]bool, len(result))
	for _, chunk := range result {
		used[chunk.ID] = true
	}
	for _, chunk := range reranked {
		if used[chunk.ID] {
			continue
		}
		result = append(result, chunk)
		if len(result) >= topK {
			break
		}
	}
	return truncateRerankResult(result, topK)
}

func isStrongKeywordAnchor(chunk RetrievedChunk) bool {
	if len(chunk.Metadata) == 0 {
		return false
	}
	if strings.TrimSpace(chunk.Metadata["retrieval_channel"]) != "keyword" {
		return false
	}
	return keywordAnchorScore(chunk) >= 4
}

func keywordAnchorScore(chunk RetrievedChunk) float64 {
	if len(chunk.Metadata) == 0 {
		return 0
	}
	score, err := strconv.ParseFloat(strings.TrimSpace(chunk.Metadata["keyword_score"]), 64)
	if err != nil {
		return 0
	}
	return score
}

func truncateRerankResult(chunks []RetrievedChunk, topK int) []RetrievedChunk {
	if topK > 0 && len(chunks) > topK {
		return chunks[:topK]
	}
	return chunks
}
