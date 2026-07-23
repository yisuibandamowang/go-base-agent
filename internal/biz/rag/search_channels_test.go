package rag

import (
	"context"
	"reflect"
	"strings"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
)

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
