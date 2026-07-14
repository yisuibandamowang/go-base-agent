package rag

import (
	"context"
	"testing"

	"go-base-agent/internal/infra/rerank"
)

type fakeRerankService struct {
	query      string
	candidates []rerank.Chunk
	topN       int
	result     []rerank.Chunk
}

func (s *fakeRerankService) Rerank(ctx context.Context, query string, candidates []rerank.Chunk, topN int) ([]rerank.Chunk, error) {
	s.query = query
	s.candidates = candidates
	s.topN = topN
	return s.result, nil
}

func TestRerankRetriever_ReranksAndPreservesMetadata(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.1, Metadata: map[string]string{"doc_name": "a.md"}},
		{ID: "b", Text: "B", Score: 0.2, Metadata: map[string]string{"doc_name": "b.md"}},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "b", Text: "B", Score: 0.99},
		{ID: "a", Text: "A", Score: 0.88},
	}}
	retriever := NewRerankRetriever(base, reranker)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if reranker.query != "query" || reranker.topN != 2 || len(reranker.candidates) != 2 {
		t.Fatalf("unexpected rerank call: query=%q topN=%d candidates=%d", reranker.query, reranker.topN, len(reranker.candidates))
	}
	if len(chunks) != 2 || chunks[0].ID != "b" || chunks[0].Score != 0.99 || chunks[0].Metadata["doc_name"] != "b.md" {
		t.Fatalf("unexpected reranked chunks: %+v", chunks)
	}
}
