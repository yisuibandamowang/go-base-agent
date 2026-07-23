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
	if len(intentKbs) != 1 || intentKbs[0].CollectionName != "collection_b" {
		t.Fatalf("expected both mode to prefer intent collection, got %+v", intentKbs)
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
