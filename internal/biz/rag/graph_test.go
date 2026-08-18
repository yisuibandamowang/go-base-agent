package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
)

func TestGraphFileSourceParse(t *testing.T) {
	encoded := EncodeGraphFileSource("member_kb", "1954071234567890100")
	if encoded != "member_kb_1954071234567890100" {
		t.Fatalf("unexpected encoded source: %q", encoded)
	}

	source := ParseGraphFileSource("/tmp/member_kb_1954071234567890100.txt")
	if source == nil {
		t.Fatal("expected source to parse")
	}
	if source.CollectionName != "member_kb" || source.DocID != "1954071234567890100" {
		t.Fatalf("unexpected parsed source: %+v", source)
	}
}

func TestLightRagClientRetrieveByScopeSplitsByCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": "ctx",
			"references": []map[string]any{
				{
					"reference_id": "r0",
					"file_path":    "kb_hr_1954071234567890200",
					"content":      []string{"别库证据"},
				},
				{
					"reference_id": "r1",
					"file_path":    "kb_1954071234567890100.txt",
					"content":      []string{"本库证据"},
				},
			},
		})
	}))
	defer server.Close()

	client := NewLightRagClient(server.URL, "", nil, 0)
	evidence := client.RetrieveByScope(context.Background(), "报销流程", "mix", 10, []string{"kb"})

	if got, want := idsOf(evidence.Matched), []string{"r1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matched ids mismatch: got %v want %v", got, want)
	}
	if got, want := idsOf(evidence.Unmatched), []string{"r0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unmatched ids mismatch: got %v want %v", got, want)
	}
	if evidence.Matched[0].Metadata["doc_id"] != "1954071234567890100" {
		t.Fatalf("unexpected doc id: %+v", evidence.Matched[0])
	}
}

func TestLightRagClientInsertTextAndDeleteByDoc(t *testing.T) {
	var gotInsertBody map[string]any
	var gotDeleteBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/documents/text":
			_ = json.NewDecoder(r.Body).Decode(&gotInsertBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/documents":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statuses": map[string]any{
					"ready": []map[string]any{
						{"id": "remote-1", "file_path": "kb_1954071234567890100.txt"},
						{"id": "remote-2", "file_path": "kb_other.txt"},
					},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/documents/delete_document":
			_ = json.NewDecoder(r.Body).Decode(&gotDeleteBody)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLightRagClient(server.URL, "", nil, 0)
	if err := client.InsertText(context.Background(), "第一段\n\n第二段", "kb_1954071234567890100"); err != nil {
		t.Fatalf("insert text: %v", err)
	}
	if gotInsertBody["text"] != "第一段\n\n第二段" {
		t.Fatalf("unexpected insert body: %+v", gotInsertBody)
	}
	if gotInsertBody["file_source"] != "kb_1954071234567890100" {
		t.Fatalf("unexpected file source: %+v", gotInsertBody)
	}

	if err := client.DeleteByDoc(context.Background(), "1954071234567890100"); err != nil {
		t.Fatalf("delete by doc: %v", err)
	}
	docIDs, ok := gotDeleteBody["doc_ids"].([]any)
	if !ok || len(docIDs) != 1 || !strings.Contains(strings.TrimSpace(docIDs[0].(string)), "remote-1") {
		t.Fatalf("unexpected delete body: %+v", gotDeleteBody)
	}
}

func TestLightRagClientDeleteByCollectionMatchesExactCollection(t *testing.T) {
	var gotDeleteBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/documents":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statuses": map[string]any{
					"ready": []map[string]any{
						{"id": "remote-kb", "file_path": "kb_1954071234567890100.txt"},
						{"id": "remote-kb-hr", "file_path": "kb_hr_1954071234567890200.txt"},
					},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/documents/delete_document":
			_ = json.NewDecoder(r.Body).Decode(&gotDeleteBody)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLightRagClient(server.URL, "", nil, 0)
	if err := client.DeleteByCollection(context.Background(), "kb"); err != nil {
		t.Fatalf("delete by collection: %v", err)
	}
	docIDs, ok := gotDeleteBody["doc_ids"].([]any)
	if !ok || len(docIDs) != 1 || docIDs[0] != "remote-kb" {
		t.Fatalf("unexpected delete body: %+v", gotDeleteBody)
	}
}

func TestGraphSearchChannelBoostsTopKAndUsesCollections(t *testing.T) {
	backend := &recordingGraphBackend{
		kbs: []knowledgeModel.KnowledgeBase{
			{Name: "会员知识库", CollectionName: "kb"},
			{Name: "支付知识库", CollectionName: "kb_pay"},
		},
	}
	searcher := &recordingGraphSearcher{
		evidence: GraphEvidence{
			Matched: []RetrievedChunk{
				{ID: "r1", Text: "本库证据", Score: 1},
			},
			Unmatched: []RetrievedChunk{
				{ID: "r0", Text: "别库证据", Score: 0.5},
			},
		},
	}
	channel := NewGraphSearchChannel(backend, searcher, "hybrid", 1)

	result, err := channel.Search(context.Background(), SearchContext{
		OriginalQuestion: "报销流程",
		TopK:             10,
		Intents: []SubQuestionIntent{{
			NodeScores: []NodeScore{{
				Node:  IntentNode{ID: "kb", CollectionName: "kb", Kind: IntentKindKB},
				Score: 0.9,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := searcher.topKs, []int{30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("topK mismatch: got %v want %v", got, want)
	}
	if got, want := searcher.collections, [][]string{{"kb"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collections mismatch: got %v want %v", got, want)
	}
	if got, want := idsOf(result.Chunks), []string{"r1", "r0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected chunks: %+v", result.Chunks)
	}
}

func (b *recordingGraphBackend) ListKnowledgeBases(context.Context) ([]knowledgeModel.KnowledgeBase, error) {
	return b.kbs, nil
}

func (b *recordingGraphBackend) SearchKeywordChunks(context.Context, knowledgeModel.KnowledgeBase, string, int) ([]RetrievedChunk, error) {
	return nil, nil
}

func (b *recordingGraphBackend) SearchRecentChunks(context.Context, string, int) ([]RetrievedChunk, error) {
	return nil, nil
}

func (b *recordingGraphBackend) MatchIntentCollections(context.Context, string, int) ([]string, error) {
	return nil, nil
}

type recordingGraphBackend struct {
	kbs []knowledgeModel.KnowledgeBase
}

type recordingGraphSearcher struct {
	collections [][]string
	topKs       []int
	evidence    GraphEvidence
}

func (r *recordingGraphSearcher) RetrieveByScope(_ context.Context, _ string, _ string, topK int, collections []string) GraphEvidence {
	r.topKs = append(r.topKs, topK)
	r.collections = append(r.collections, append([]string(nil), collections...))
	return r.evidence
}

func idsOf(chunks []RetrievedChunk) []string {
	ids := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		ids = append(ids, chunk.ID)
	}
	return ids
}
