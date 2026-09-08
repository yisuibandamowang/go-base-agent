package rag

import (
	"context"
	"math"
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
	retriever := NewRerankRetriever(base, reranker, 0)

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
	retriever := NewRerankRetriever(base, reranker, 0)

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
	retriever := NewRerankRetriever(base, reranker, 0)

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

// rrScore 是与 RRF 量级分叉的精排分：实现若退回读 Score 必须变红
func rrScore(v float64) *float64 { return &v }

func TestRerankRetriever_CopiesRerankScoreBackToChunks(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.1},
		{ID: "b", Text: "B", Score: 0.2},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "b", Text: "B", Score: 0.85, RerankScore: rrScore(0.85)},
		{ID: "a", Text: "A", Score: 0.10, RerankScore: rrScore(0.10)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %+v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.RerankScore == nil || *chunk.RerankScore != chunk.Score {
			t.Fatalf("expected rerank score copied back, got %+v", chunk)
		}
	}
}

func TestRerankRetriever_EvidenceGatePassesThroughWhenRerankScoreAbsent(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.03},
		{ID: "b", Text: "B", Score: 0.03},
	}}
	// noop 降级只截断不打分：照拦会在上游最不稳时关掉 KB 侧
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.03},
		{ID: "b", Text: "B", Score: 0.03},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected noop rerank to pass the gate, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateKeepsBatchWhenTopScoreClearsFloor(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.9},
		{ID: "b", Text: "B", Score: 0.8},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.85, RerankScore: rrScore(0.85)},
		{ID: "b", Text: "B", Score: 0.10, RerankScore: rrScore(0.10)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("整批凭最高分过闸，弱证据跟着一起留, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateDropsWholeBatchWhenTopScoreBelowFloor(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.92},
		{ID: "b", Text: "B", Score: 0.88},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.05, RerankScore: rrScore(0.05)},
		{ID: "b", Text: "B", Score: 0.01, RerankScore: rrScore(0.01)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("哪怕 score 上是高位余弦，最高精排分低于下限也应整批丢弃, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateUsesBatchMaxNotFirstChunk(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.03},
		{ID: "b", Text: "B", Score: 0.03},
		{ID: "c", Text: "C", Score: 0.03},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.01, RerankScore: rrScore(0.01)},
		{ID: "b", Text: "B", Score: 0.03},
		{ID: "c", Text: "C", Score: 0.90, RerankScore: rrScore(0.90)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 3)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("rerank 客户端未承诺返回序，取整批最高分而非首条, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateKeepsEvidenceExactlyAtFloor(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{{ID: "a", Text: "A", Score: 0.03}}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.2, RerankScore: rrScore(0.2)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("恰好等于下限视为过线，边界不误杀, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateTreatsNonFiniteRerankScoreAsAbsent(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.92},
		{ID: "b", Text: "B", Score: 0.88},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: math.NaN(), RerankScore: rrScore(math.NaN())},
		{ID: "b", Text: "B", Score: 0.88},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.Retrieve(context.Background(), "query", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("NaN 参与比较会把毒值抬成最高分，非有限精排分按缺分处理走放行, got %+v", chunks)
	}
}

func TestRerankRetriever_EvidenceGateDisabledWhenFloorNotPositive(t *testing.T) {
	base := staticRetriever{chunks: []RetrievedChunk{
		{ID: "a", Text: "A", Score: 0.92},
	}}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.05, RerankScore: rrScore(0.05)},
	}}
	for _, floor := range []float64{0, -1} {
		retriever := NewRerankRetriever(base, reranker, floor)
		chunks, err := retriever.Retrieve(context.Background(), "query", 1)
		if err != nil {
			t.Fatalf("retrieve(floor=%v): %v", floor, err)
		}
		if len(chunks) != 1 {
			t.Fatalf("下限 %v <=0 即关闭闸门，是配置侧的回退路径, got %+v", floor, chunks)
		}
	}
}

func TestRerankRetriever_EvidenceGateAppliesToContextRetrieval(t *testing.T) {
	base := &recordingIntentAwareRetriever{
		chunks: []RetrievedChunk{{ID: "a", Text: "A", Score: 0.9}},
	}
	reranker := &fakeRerankService{result: []rerank.Chunk{
		{ID: "a", Text: "A", Score: 0.02, RerankScore: rrScore(0.02)},
	}}
	retriever := NewRerankRetriever(base, reranker, 0.2)

	chunks, err := retriever.RetrieveWithContext(context.Background(), SearchContext{OriginalQuestion: "q", TopK: 1})
	if err != nil {
		t.Fatalf("retrieve with context: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected gate to drop the batch in context retrieval, got %+v", chunks)
	}
}
