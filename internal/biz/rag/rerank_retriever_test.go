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

func TestRerankRetriever_RetrieveWithContextPassesSearchContext(t *testing.T) {
	base := &recordingIntentAwareRetriever{
		chunks: []RetrievedChunk{{ID: "a", Text: "A", Score: 0.1}},
	}
	reranker := &fakeRerankService{result: []rerank.Chunk{{ID: "a", Text: "A", Score: 0.99}}}
	retriever := NewRerankRetriever(base, reranker)

	chunks, err := retriever.RetrieveWithContext(context.Background(), SearchContext{
		OriginalQuestion:  "会员问题",
		RewrittenQuestion: "会员积分怎么查",
		Intents: []SubQuestionIntent{{
			SubQuestion: "会员积分怎么查",
			NodeScores:  []NodeScore{{Node: IntentNode{ID: "leaf-kb", CollectionName: "member_kb", Kind: IntentKindKB}, Score: 0.9}},
		}},
		TopK: 3,
	})
	if err != nil {
		t.Fatalf("retrieve with context: %v", err)
	}
	if len(base.contexts) != 1 || len(base.contexts[0].Intents) != 1 {
		t.Fatalf("expected search context to be passed to base retriever, got %+v", base.contexts)
	}
	if reranker.query != "会员积分怎么查" || reranker.topN != 3 {
		t.Fatalf("unexpected rerank call: query=%q topN=%d", reranker.query, reranker.topN)
	}
	if len(chunks) != 1 || chunks[0].Score != 0.99 {
		t.Fatalf("unexpected reranked chunks: %+v", chunks)
	}
}
