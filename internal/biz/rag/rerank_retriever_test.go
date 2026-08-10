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

func TestRerankRetriever_PreservesStrongKeywordAnchorWhenRerankDropsIt(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{
			ID:    "keyword-扶摇",
			Text:  "这次事故的核心不是 Redis 序列化，而是 XLSX 解析阶段错误删除内部空白字符。",
			Score: 0.03,
			Metadata: map[string]string{
				"doc_name":          "扶摇 tag 去重线上修复.md",
				"retrieval_channel": "keyword",
				"keyword_score":     "18",
			},
		},
		{ID: "vector-1", Text: "收银台诊断工具能力", Score: 0.02, Metadata: map[string]string{"doc_name": "收银台诊断工具.md"}},
		{ID: "vector-2", Text: "收银台诊断工具手册", Score: 0.01, Metadata: map[string]string{"doc_name": "收银台诊断工具使用手册.md"}},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "vector-1", Text: "收银台诊断工具能力", Score: 0.99},
		{ID: "vector-2", Text: "收银台诊断工具手册", Score: 0.98},
	}}
	retriever := NewRerankRetriever(base, reranker)

	chunks, err := retriever.Retrieve(context.Background(), "扶摇线上tag去重问题是什么导致的?", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %+v", chunks)
	}
	if chunks[0].ID != "keyword-扶摇" {
		t.Fatalf("expected strong keyword anchor to be restored before rerank-only results, got %+v", chunks)
	}
	if chunks[1].ID != "vector-1" {
		t.Fatalf("expected rerank result to remain after keyword anchor, got %+v", chunks)
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
