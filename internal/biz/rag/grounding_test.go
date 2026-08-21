package rag

import (
	"testing"
)

func TestAssembleGroundingChunksKeepsBestChunkPerDocument(t *testing.T) {
	chunks := AssembleGroundingChunks([]RetrievedChunk{
		{ID: "low-a", Text: "低分片段", Score: 0.2, Metadata: map[string]string{"doc_id": "doc-a", "doc_name": "文档A"}},
		{ID: "best-a", Text: "最高分片段", Score: 0.9, Metadata: map[string]string{"doc_id": "doc-a", "doc_name": "文档A"}},
		{ID: "best-b", Text: "文档B片段", Score: 0.8, Metadata: map[string]string{"doc_id": "doc-b", "doc_name": "文档B"}},
		{ID: "no-doc", Text: "没有文档归属", Score: 1, Metadata: map[string]string{}},
	})

	if len(chunks) != 2 {
		t.Fatalf("expected two grounding chunks, got %+v", chunks)
	}
	if chunks[0].DocName != "文档A" || chunks[0].Text != "最高分片段" {
		t.Fatalf("expected highest scoring document chunk first, got %+v", chunks[0])
	}
	if chunks[1].DocName != "文档B" {
		t.Fatalf("unexpected second grounding chunk: %+v", chunks[1])
	}
}

func TestAssembleGroundingChunksLimitsToEightDocuments(t *testing.T) {
	input := make([]RetrievedChunk, 0, 10)
	for i := 0; i < 10; i++ {
		input = append(input, RetrievedChunk{
			ID:       "chunk-" + string(rune('a'+i)),
			Text:     "片段",
			Score:    float64(i),
			Metadata: map[string]string{"doc_id": "doc-" + string(rune('a'+i))},
		})
	}

	got := AssembleGroundingChunks(input)
	if len(got) != 8 {
		t.Fatalf("expected at most eight grounding chunks, got %d", len(got))
	}
	if got[0].DocName != "doc-j" {
		t.Fatalf("expected grounding chunks ordered by score, got %+v", got)
	}
}
