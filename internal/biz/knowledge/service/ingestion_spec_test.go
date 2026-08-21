package service

import (
	"strings"
	"testing"

	"go-base-agent/internal/biz/knowledge/model"
)

func TestNormalizeIngestionSpecUsesJavaWireShapeAndDefaults(t *testing.T) {
	got, err := NormalizeIngestionSpec(`{"parseProfile":"fidelity","maxChars":512,"overlapChars":64,"rowsPerChunk":12,"toleranceFactor":4}`)
	if err != nil {
		t.Fatalf("NormalizeIngestionSpec: %v", err)
	}
	if !strings.Contains(got, `"parseProfile":"fidelity"`) ||
		!strings.Contains(got, `"maxChars":512`) ||
		!strings.Contains(got, `"overlapChars":64`) ||
		!strings.Contains(got, `"rowsPerChunk":12`) ||
		!strings.Contains(got, `"toleranceFactor":4`) {
		t.Fatalf("unexpected normalized ingestion spec: %s", got)
	}
}

func TestIngestionSpecParticipatesInChunkConfigHashAndOverlap(t *testing.T) {
	left := &model.KnowledgeDocument{IngestionSpec: `{"maxChars":256,"overlapChars":16}`}
	right := &model.KnowledgeDocument{IngestionSpec: `{"maxChars":512,"overlapChars":32}`}
	if chunkConfigHash(left) == chunkConfigHash(right) {
		t.Fatal("expected ingestion spec changes to alter chunk config hash")
	}
	if got := documentOverlapSize(left); got != 16 {
		t.Fatalf("expected ingestion overlap 16, got %d", got)
	}
}

func TestNormalizeIngestionSpecRejectsInvalidBudget(t *testing.T) {
	_, err := NormalizeIngestionSpec(`{"parseProfile":"fast","maxChars":128,"overlapChars":128}`)
	if err == nil || !strings.Contains(err.Error(), "overlapChars") {
		t.Fatalf("expected overlap validation error, got %v", err)
	}
}

func TestNormalizeIngestionSpecDerivesOverlapFromConfiguredMaxChars(t *testing.T) {
	got, err := NormalizeIngestionSpec(`{"maxChars":256}`)
	if err != nil {
		t.Fatalf("NormalizeIngestionSpec: %v", err)
	}
	if !strings.Contains(got, `"overlapChars":32`) {
		t.Fatalf("expected overlap derived from maxChars, got %s", got)
	}
}

func TestNormalizeIngestionSpecFallsBackForNonPositiveOptionalBudgetValues(t *testing.T) {
	got, err := NormalizeIngestionSpec(`{"maxChars":0,"overlapChars":-1,"rowsPerChunk":0,"toleranceFactor":0}`)
	if err != nil {
		t.Fatalf("NormalizeIngestionSpec: %v", err)
	}
	for _, part := range []string{`"maxChars":1024`, `"overlapChars":128`, `"rowsPerChunk":50`, `"toleranceFactor":3`} {
		if !strings.Contains(got, part) {
			t.Fatalf("expected %s in normalized spec, got %s", part, got)
		}
	}
}

func TestChunkingOptionsForIngestionSpecConsumesBudget(t *testing.T) {
	opts, profile, err := chunkingOptionsForIngestionSpec(`{"parseProfile":"fidelity","maxChars":256,"overlapChars":32,"rowsPerChunk":7,"toleranceFactor":2}`)
	if err != nil {
		t.Fatalf("chunkingOptionsForIngestionSpec: %v", err)
	}
	if profile != "fidelity" || opts.ChunkSize != 256 || opts.OverlapSize != 32 || opts.RowsPerChunk != 7 || opts.ToleranceSize != 512 {
		t.Fatalf("unexpected options: profile=%q opts=%+v", profile, opts)
	}
}
