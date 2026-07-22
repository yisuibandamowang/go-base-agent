package rag

import (
	"strings"
	"testing"
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
