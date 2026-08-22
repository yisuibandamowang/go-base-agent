package rag

import (
	"context"
	"reflect"
	"strings"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
)

type recordingTopKRetriever struct {
	topKs []int
}

func (r *recordingTopKRetriever) Retrieve(_ context.Context, _ string, topK int) ([]RetrievedChunk, error) {
	r.topKs = append(r.topKs, topK)
	return []RetrievedChunk{{ID: "chunk-1", Text: "vector result"}}, nil
}

type recordingGlobalRetriever struct {
	topKs      []int
	globalKs   []int
	supports   bool
	globalUsed bool
}

func (r *recordingGlobalRetriever) Retrieve(_ context.Context, _ string, topK int) ([]RetrievedChunk, error) {
	r.topKs = append(r.topKs, topK)
	return []RetrievedChunk{{ID: "local", Text: "local vector result"}}, nil
}

func (r *recordingGlobalRetriever) SupportsGlobalRetrieval() bool {
	return r.supports
}

func (r *recordingGlobalRetriever) RetrieveGlobal(_ context.Context, _ string, topK int) ([]RetrievedChunk, error) {
	r.globalKs = append(r.globalKs, topK)
	r.globalUsed = true
	return []RetrievedChunk{{ID: "global", Text: "global vector result"}}, nil
}

type recordingSearchBackend struct {
	kbs                []knowledgeModel.KnowledgeBase
	keywordCollections []string
	keywordQueries     []string
	keywordTopKs       []int
	recentCollections  []string
	recentTopKs        []int
	intentQueries      []string
	intentLimits       []int
	keywordChunks      []RetrievedChunk
	keywordChunksByKB  map[string][]RetrievedChunk
	recentChunks       []RetrievedChunk
	intentCollections  []string
}

func (b *recordingSearchBackend) ListKnowledgeBases(context.Context) ([]knowledgeModel.KnowledgeBase, error) {
	return b.kbs, nil
}

func (b *recordingSearchBackend) SearchKeywordChunks(_ context.Context, kb knowledgeModel.KnowledgeBase, query string, topK int) ([]RetrievedChunk, error) {
	b.keywordCollections = append(b.keywordCollections, kb.CollectionName)
	b.keywordQueries = append(b.keywordQueries, query)
	b.keywordTopKs = append(b.keywordTopKs, topK)
	if b.keywordChunksByKB != nil {
		return b.keywordChunksByKB[kb.CollectionName], nil
	}
	return b.keywordChunks, nil
}

func (b *recordingSearchBackend) SearchRecentChunks(_ context.Context, collectionName string, topK int) ([]RetrievedChunk, error) {
	b.recentCollections = append(b.recentCollections, collectionName)
	b.recentTopKs = append(b.recentTopKs, topK)
	return b.recentChunks, nil
}

func (b *recordingSearchBackend) MatchIntentCollections(_ context.Context, query string, limit int) ([]string, error) {
	b.intentQueries = append(b.intentQueries, query)
	b.intentLimits = append(b.intentLimits, limit)
	return b.intentCollections, nil
}

func TestKeywordSearchTermsExtractsMixedChineseAndEnglishSignal(t *testing.T) {
	terms := keywordSearchTerms("扶摇线上Tag去重问题是什么导致的?")
	termSet := make(map[string]bool, len(terms))
	for _, term := range terms {
		termSet[term] = true
	}
	for _, want := range []string{"扶摇", "线上", "tag", "去重", "问题"} {
		if !termSet[want] {
			t.Fatalf("expected term %q in %v", want, terms)
		}
	}
	if termSet["什么"] || termSet["是什"] {
		t.Fatalf("expected generic question terms to be filtered, got %v", terms)
	}
}

func TestIntentDirectedTargetsFromContextUsesKbIntents(t *testing.T) {
	sc := SearchContext{
		TopK: 10,
		Intents: []SubQuestionIntent{{
			SubQuestion: "会员积分和等级",
			NodeScores: []NodeScore{
				{Node: IntentNode{ID: "a", CollectionName: "collection_a", TopK: 3, Kind: IntentKindKB}, Score: 0.95},
				{Node: IntentNode{ID: "b", CollectionName: "collection_b", TopK: 5, Kind: IntentKindKB}, Score: 0.90},
				{Node: IntentNode{ID: "c", CollectionName: "", TopK: 8, Kind: IntentKindKB}, Score: 0.85},
			},
		}},
	}

	targets := intentDirectedTargetsFromContext(sc, 0.5)
	if len(targets) != 2 {
		t.Fatalf("expected 2 intent-directed targets, got %+v", targets)
	}
	if targets[0].collectionName != "collection_a" || targets[0].topK != 3 {
		t.Fatalf("unexpected first target: %+v", targets[0])
	}
	if targets[1].collectionName != "collection_b" || targets[1].topK != 5 {
		t.Fatalf("unexpected second target: %+v", targets[1])
	}
}

func TestBackendKeywordSearchChannelUsesSearchBackend(t *testing.T) {
	backend := &recordingSearchBackend{
		kbs: []knowledgeModel.KnowledgeBase{
			{Name: "会员知识库", CollectionName: "collection_a"},
			{Name: "支付知识库", CollectionName: "collection_b"},
		},
		keywordChunks: []RetrievedChunk{{ID: "kw-1", Text: "会员等级规则", Score: 0.5}},
	}
	channel := NewBackendKeywordSearchChannel(backend, 5)
	channel.SetKeywordOptions("both", 2)

	result, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion:  "会员怎么算",
		RewrittenQuestion: "会员等级规则",
		TopK:              5,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "kb", CollectionName: "collection_b", Kind: IntentKindKB},
				Score: 0.8,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := backend.keywordCollections, []string{"collection_b", "collection_a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collections mismatch: got %v, want %v", got, want)
	}
	if got, want := backend.keywordQueries, []string{"会员等级规则", "会员等级规则"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries mismatch: got %v, want %v", got, want)
	}
	if got, want := backend.keywordTopKs, []int{10, 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topK mismatch: got %v, want %v", got, want)
	}
	if len(result.Chunks) != 2 || result.Chunks[0].ID != "kw-1" || result.Chunks[1].ID != "kw-1" {
		t.Fatalf("unexpected chunks: %+v", result.Chunks)
	}
}

func TestBackendKeywordSearchChannelSortsResultsAcrossKnowledgeBasesByScore(t *testing.T) {
	backend := &recordingSearchBackend{
		kbs: []knowledgeModel.KnowledgeBase{
			{Name: "错误知识库", CollectionName: "wrong_collection"},
			{Name: "会员知识库", CollectionName: "member"},
		},
		keywordChunksByKB: map[string][]RetrievedChunk{
			"wrong_collection": {
				{ID: "wrong-1", Text: "线上问题泛匹配", Score: 1},
			},
			"member": {
				{ID: "right-1", Text: "扶摇 tag 去重线上修复", Score: 17},
			},
		},
	}
	channel := NewBackendKeywordSearchChannel(backend, 5)
	channel.SetKeywordOptions("both", 1)

	result, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "扶摇线上tag去重问题是什么导致的?",
		TopK:             10,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "wrong", CollectionName: "wrong_collection", Kind: IntentKindKB},
				Score: 0.8,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("expected two keyword chunks, got %+v", result.Chunks)
	}
	if result.Chunks[0].ID != "right-1" {
		t.Fatalf("expected highest scoring keyword result first, got %+v", result.Chunks)
	}
}

func TestBackendKeywordSearchChannelUsesSharedScopeSupplementBudget(t *testing.T) {
	backend := &recordingSearchBackend{kbs: []knowledgeModel.KnowledgeBase{
		{CollectionName: "target"},
		{CollectionName: "other"},
	}}
	channel := NewBackendKeywordSearchChannel(backend, 5)
	channel.SetKeywordOptions("both", 1)
	channel.SetSupplementRatio(0.25)

	_, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "会员规则",
		TopK:             4,
		RetrievalScope: &RetrievalScope{
			Directed:              true,
			TargetCollections:     []string{"target"},
			SupplementCollections: []string{"other"},
		},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := backend.keywordCollections, []string{"target", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope collections mismatch: got %v want %v", got, want)
	}
	if got, want := backend.keywordTopKs, []int{3, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope quota mismatch: got %v want %v", got, want)
	}
}

func TestRetrieverSearchChannelVectorGlobalUsesConfidenceAndTopKMultiplier(t *testing.T) {
	retriever := &recordingTopKRetriever{}
	channel := NewRetrieverSearchChannel("VectorGlobalSearch", ChannelVectorGlobal, 10, retriever)
	channel.SetVectorGlobalOptions(true, 0.6, 3, 0.8)

	highConfidenceIntent := SearchContext{
		OriginalQuestion: "会员等级规则",
		TopK:             5,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "intent-1", CollectionName: "collection_a", Kind: IntentKindKB},
				Score: 0.9,
			}},
		}},
	}
	if channel.IsEnabled(highConfidenceIntent) {
		t.Fatalf("expected vector global channel to be disabled for high confidence intent")
	}

	if !channel.IsEnabled(SearchContext{OriginalQuestion: "会员等级规则", TopK: 5}) {
		t.Fatalf("expected vector global channel to be enabled when no intents are resolved")
	}
	_, err := channel.Search(context.Background(), SearchContext{OriginalQuestion: "会员等级规则", TopK: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := retriever.topKs, []int{15}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topK mismatch: got %v, want %v", got, want)
	}
}

func TestRetrieverSearchChannelVectorGlobalSupplementsSingleMediumConfidenceIntent(t *testing.T) {
	channel := NewRetrieverSearchChannel("VectorGlobalSearch", ChannelVectorGlobal, 10, &recordingTopKRetriever{})
	channel.SetVectorGlobalOptions(true, 0.6, 3, 0.8)

	mediumSingleIntent := SearchContext{
		OriginalQuestion: "会员等级规则",
		TopK:             5,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "intent-1", CollectionName: "collection_a", Kind: IntentKindKB},
				Score: 0.7,
			}},
		}},
	}
	if !channel.IsEnabled(mediumSingleIntent) {
		t.Fatalf("expected vector global channel to supplement a single medium confidence intent")
	}
}

func TestRetrieverSearchChannelVectorGlobalIgnoresNonKbIntents(t *testing.T) {
	channel := NewRetrieverSearchChannel("VectorGlobalSearch", ChannelVectorGlobal, 10, &recordingTopKRetriever{})
	channel.SetVectorGlobalOptions(true, 0.6, 3, 0.8)

	systemIntent := SearchContext{
		OriginalQuestion: "收银台诊断工具支持哪些接口",
		TopK:             5,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "sys-about", Kind: IntentKindSystem},
				Score: 0.95,
			}},
		}},
	}
	if !channel.IsEnabled(systemIntent) {
		t.Fatalf("expected vector global channel to remain enabled for non-kb intents")
	}

	kbWithoutCollection := SearchContext{
		OriginalQuestion: "收银台诊断工具支持哪些接口",
		TopK:             5,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "kb-empty", Kind: IntentKindKB},
				Score: 0.95,
			}},
		}},
	}
	if !channel.IsEnabled(kbWithoutCollection) {
		t.Fatalf("expected vector global channel to remain enabled for kb intents without collection")
	}
}

func TestRetrieverSearchChannelVectorGlobalUsesCandidateBudgetForGlobalRetriever(t *testing.T) {
	retriever := &recordingGlobalRetriever{supports: true}
	channel := NewRetrieverSearchChannel("VectorGlobalSearch", ChannelVectorGlobal, 10, retriever)
	channel.SetVectorGlobalOptions(true, 0.6, 3, 0.8)
	channel.SetVectorGlobalCandidateBudget(100)

	result, err := channel.Search(context.Background(), SearchContext{OriginalQuestion: "会员等级规则", TopK: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !retriever.globalUsed {
		t.Fatalf("expected global retrieval to be used")
	}
	if got, want := retriever.globalKs, []int{100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate budget mismatch: got %v, want %v", got, want)
	}
	if len(retriever.topKs) != 0 {
		t.Fatalf("expected per-collection retrieve not to be used, got %v", retriever.topKs)
	}
	if len(result.Chunks) != 1 || result.Chunks[0].ID != "global" {
		t.Fatalf("unexpected chunks: %+v", result.Chunks)
	}
}

func TestBackendIntentDirectedSearchChannelDoesNotUseRecentChunksFallback(t *testing.T) {
	backend := &recordingSearchBackend{
		recentChunks: []RetrievedChunk{{ID: "recent-1", Text: "最新会员规则", Score: 0.7}},
	}
	channel := NewBackendIntentDirectedSearchChannel(backend, nil, nil, 1)

	result, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "会员等级规则",
		TopK:             10,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "intent-1", CollectionName: "collection_a", TopK: 3, Kind: IntentKindKB},
				Score: 0.96,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(backend.recentCollections) != 0 {
		t.Fatalf("expected intent channel not to use recent chunks as semantic fallback, got %v", backend.recentCollections)
	}
	if len(backend.recentTopKs) != 0 {
		t.Fatalf("expected no recent topK calls, got %v", backend.recentTopKs)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("expected no chunks when intent vector search is unavailable, got %+v", result.Chunks)
	}
}

func TestPgKeywordSearchChannelResolvesModeAndTopKMultiplier(t *testing.T) {
	kbs := []knowledgeModel.KnowledgeBase{
		{Name: "会员知识库", CollectionName: "collection_a"},
		{Name: "支付知识库", CollectionName: "collection_b"},
	}
	channel := NewPgKeywordSearchChannel(nil, fakeKnowledgeBaseLister{kbs: kbs}, 5)
	channel.SetKeywordOptions("both", 2)

	intentKbs, err := channel.resolveKnowledgeBases(context.Background(), SearchContext{
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{
				{Node: IntentNode{ID: "kb", CollectionName: "collection_b", Kind: IntentKindKB}, Score: 0.8},
				{Node: IntentNode{ID: "mcp", CollectionName: "collection_a", Kind: IntentKindMCP}, Score: 0.9},
			},
		}},
	})
	if err != nil {
		t.Fatalf("resolve knowledge bases: %v", err)
	}
	if len(intentKbs) != 2 ||
		intentKbs[0].CollectionName != "collection_b" ||
		intentKbs[1].CollectionName != "collection_a" {
		t.Fatalf("expected both mode to search intent collection first then global collections, got %+v", intentKbs)
	}
	if got, want := channel.resolveTopK(5), 10; got != want {
		t.Fatalf("topK mismatch: got %d, want %d", got, want)
	}

	globalKbs, err := channel.resolveKnowledgeBases(context.Background(), SearchContext{})
	if err != nil {
		t.Fatalf("resolve global knowledge bases: %v", err)
	}
	if len(globalKbs) != 2 {
		t.Fatalf("expected both mode to fallback to global collections, got %+v", globalKbs)
	}

	channel.SetKeywordOptions("intent", 2)
	intentOnlyKbs, err := channel.resolveKnowledgeBases(context.Background(), SearchContext{})
	if err != nil {
		t.Fatalf("resolve intent-only knowledge bases: %v", err)
	}
	if len(intentOnlyKbs) != 0 {
		t.Fatalf("expected intent mode without intents to return no collections, got %+v", intentOnlyKbs)
	}
}

func TestPgIntentDirectedSearchChannelUsesVectorSearchForIntentCollections(t *testing.T) {
	emb := &recordingEmbeddingService{}
	searcher := &recordingVectorSearcher{
		results: []VectorChunk{{
			ChunkID: "chunk-1",
			Content: "会员等级按成长值计算",
			Score:   0.88,
			Metadata: map[string]string{
				"doc_id": "doc-1",
			},
		}},
	}
	kb := knowledgeModel.KnowledgeBase{
		Name:           "会员知识库",
		EmbeddingModel: "emb-member",
		CollectionName: "collection_a",
	}
	kb.ID = "kb-1"
	channel := NewPgIntentDirectedVectorSearchChannel(nil, searcher, emb, fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{kb}}, 1)

	result, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion:  "会员等级怎么算",
		RewrittenQuestion: "会员等级规则",
		TopK:              10,
		Intents: []SubQuestionIntent{{
			SubQuestion: "会员等级规则",
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "intent-1", CollectionName: "collection_a", TopK: 3, Kind: IntentKindKB},
				Score: 0.96,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if got, want := emb.modelIDs, []string{"emb-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("embedding models mismatch: got %v, want %v", got, want)
	}
	if got, want := searcher.collections, []string{"collection_a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collections mismatch: got %v, want %v", got, want)
	}
	if got, want := searcher.topKs, []int{3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topK mismatch: got %v, want %v", got, want)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("expected one chunk, got %+v", result.Chunks)
	}
	meta := result.Chunks[0].Metadata
	if meta["kb_name"] != "会员知识库" || meta["collection_name"] != "collection_a" {
		t.Fatalf("expected knowledge base metadata, got %+v", meta)
	}
}

func TestPgIntentDirectedSearchChannelUsesSharedScopeSupplementBudget(t *testing.T) {
	emb := &recordingEmbeddingService{}
	searcher := &recordingVectorSearcher{results: []VectorChunk{{
		ChunkID: "chunk-1",
		Content: "规则",
		Score:   0.9,
	}}}
	channel := NewPgIntentDirectedVectorSearchChannel(nil, searcher, emb, fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{
		{CollectionName: "target", EmbeddingModel: "emb"},
		{CollectionName: "other", EmbeddingModel: "emb"},
	}}, 1)
	channel.SetIntentOptions(0.4, 1)
	channel.SetSupplementRatio(0.25)

	_, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "会员规则",
		TopK:             4,
		Intents: []SubQuestionIntent{{NodeScores: []NodeScore{{
			Node:  IntentNode{ID: "member", Kind: IntentKindKB, CollectionName: "target", TopK: 4},
			Score: 0.9,
		}}}},
		RetrievalScope: &RetrievalScope{
			Directed:              true,
			TargetCollections:     []string{"target"},
			SupplementCollections: []string{"other"},
		},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := searcher.collections, []string{"target", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope collections mismatch: got %v want %v", got, want)
	}
	if got, want := searcher.topKs, []int{4, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scope quota mismatch: got %v want %v", got, want)
	}
}

func TestPgIntentDirectedSearchChannelAppliesMinScoreAndTopKMultiplier(t *testing.T) {
	emb := &recordingEmbeddingService{}
	searcher := &recordingVectorSearcher{}
	kb := knowledgeModel.KnowledgeBase{
		Name:           "会员知识库",
		EmbeddingModel: "emb-member",
		CollectionName: "collection_high",
	}
	kb.ID = "kb-1"
	channel := NewPgIntentDirectedVectorSearchChannel(nil, searcher, emb, fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{kb}}, 1)
	channel.SetIntentOptions(0.4, 2)

	lowScoreContext := SearchContext{
		OriginalQuestion: "会员等级规则",
		TopK:             10,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "low", CollectionName: "collection_high", TopK: 3, Kind: IntentKindKB},
				Score: 0.2,
			}},
		}},
	}
	if channel.IsEnabled(lowScoreContext) {
		t.Fatalf("expected low score intent below threshold to disable channel")
	}

	_, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "会员等级规则",
		TopK:             10,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "high", CollectionName: "collection_high", TopK: 3, Kind: IntentKindKB},
				Score: 0.8,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := searcher.topKs, []int{6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topK mismatch: got %v, want %v", got, want)
	}
}

func TestParseWebSearchChunksKeepsSourceURL(t *testing.T) {
	body := []byte(`{"results":{"web":[{"url":"https://example.com/a","title":"会员Agent","description":"能力说明","snippets":["支持错误排查"]}]}}`)

	chunks := parseWebSearchChunks(body, 5)
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %+v", chunks)
	}
	if chunks[0].Metadata["source_url"] != "https://example.com/a" {
		t.Fatalf("expected source url metadata, got %+v", chunks[0].Metadata)
	}
	if !strings.Contains(chunks[0].Text, "支持错误排查") {
		t.Fatalf("expected snippet in text, got %q", chunks[0].Text)
	}
}

func TestParseWebSearchChunksAppliesLimitAfterDroppingEmptyResults(t *testing.T) {
	body := []byte(`{"results":{"web":[{}, {"url":"https://example.com/a","title":"有效网页","description":"网页说明"}],"news":[{"url":"https://example.com/news","title":"有效新闻","description":"新闻说明"}]}}`)

	chunks := parseWebSearchChunks(body, 2)
	if len(chunks) != 2 {
		t.Fatalf("expected two valid chunks after filtering, got %+v", chunks)
	}
	if chunks[0].ID != "https://example.com/a" || chunks[1].ID != "https://example.com/news" {
		t.Fatalf("unexpected result order after filtering: %+v", chunks)
	}
}

// TestIntentDirectedTargetsExpandMultipleCollections 验证意图节点关联多个知识库时，
// 检索目标按 EffectiveCollectionNames 展开到全部绑定库。
func TestIntentDirectedTargetsExpandMultipleCollections(t *testing.T) {
	sc := SearchContext{
		OriginalQuestion:  "问题",
		RewrittenQuestion: "问题",
		TopK:              5,
		Intents: []SubQuestionIntent{
			{SubQuestion: "问题", NodeScores: []NodeScore{
				{
					Node: IntentNode{
						ID:              "multi-kb-intent",
						Kind:            IntentKindKB,
						CollectionName:  "collection_legacy",
						CollectionNames: []string{"collection_a", "collection_b"},
					},
					Score: 0.9,
				},
			}},
		},
	}

	targets := intentDirectedTargetsFromContext(sc, 0.5, 1)
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.collectionName)
	}
	// 新字段 collectionNames 非空时优先使用列表，旧单字段仅作空列表兜底。
	want := []string{"collection_a", "collection_b"}
	if len(names) != len(want) {
		t.Fatalf("expected targets %v, got %v", want, names)
	}
	gotSet := make(map[string]bool, len(names))
	for _, name := range names {
		gotSet[name] = true
	}
	for _, name := range want {
		if !gotSet[name] {
			t.Fatalf("expected target %q missing, got %v", name, names)
		}
	}
}

// TestKeywordIntentCollectionsUsesEffectiveNames 验证关键词通道同样按多库名展开。
func TestKeywordIntentCollectionsUsesEffectiveNames(t *testing.T) {
	sc := SearchContext{
		Intents: []SubQuestionIntent{
			{SubQuestion: "问题", NodeScores: []NodeScore{
				{
					Node: IntentNode{
						ID:              "intent-1",
						Kind:            IntentKindKB,
						CollectionNames: []string{"kb_one", "kb_two"},
					},
					Score: 0.9,
				},
			}},
		},
	}
	collections := keywordIntentCollections(sc)
	if len(collections) != 2 {
		t.Fatalf("expected 2 collections, got %v", collections)
	}
}

// TestIntentNodeEffectiveCollectionNamesDedupAndFallback 验证去重与旧字段兜底。
func TestIntentNodeEffectiveCollectionNamesDedupAndFallback(t *testing.T) {
	node := IntentNode{
		CollectionName:  "legacy",
		CollectionNames: []string{"primary", "  ", "primary", "secondary"},
	}
	got := node.EffectiveCollectionNames()
	want := []string{"primary", "secondary"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}

	legacyOnly := IntentNode{CollectionName: "only"}.EffectiveCollectionNames()
	if len(legacyOnly) != 1 || legacyOnly[0] != "only" {
		t.Fatalf("expected legacy fallback [only], got %v", legacyOnly)
	}

	empty := IntentNode{}.EffectiveCollectionNames()
	if len(empty) != 0 {
		t.Fatalf("expected empty, got %v", empty)
	}
}

// TestSortRetrievedChunksByScore 验证通道出口按相关性降序排序，
// 这是下游 RRF 按名次取分依赖的不变式。对齐 Java ChunkRanking「出口有序」契约。
func TestSortRetrievedChunksByScore(t *testing.T) {
	chunks := []RetrievedChunk{
		{ID: "c1", Score: 0.5}, // 库 A 的弱命中
		{ID: "c2", Score: 0.9}, // 库 B 的强命中，拼接序错排在前者之后
		{ID: "c3", Score: 0.7},
	}
	sortRetrievedChunksByScore(chunks)
	want := []string{"c2", "c3", "c1"}
	for i, id := range want {
		if chunks[i].ID != id {
			t.Fatalf("position %d: expected %s, got %s (full: %v)", i, id, chunks[i].ID, chunks)
		}
	}

	// 不足两条时不排序也不出错
	single := []RetrievedChunk{{ID: "only", Score: 0.1}}
	sortRetrievedChunksByScore(single)
	if single[0].ID != "only" {
		t.Fatal("single chunk should pass through")
	}
	sortRetrievedChunksByScore(nil)
}
