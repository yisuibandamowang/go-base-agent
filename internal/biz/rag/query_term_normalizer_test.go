package rag

import (
	"context"
	"testing"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
)

type testTermMappingLister struct {
	mappings []intentModel.QueryTermMapping
	total    int64
}

func (l testTermMappingLister) ListByDomain(ctx context.Context, domain string, page, size int) ([]intentModel.QueryTermMapping, int64, error) {
	return l.mappings, l.total, nil
}

func TestDBQueryTermNormalizer_NormalizeAppliesEnabledMappings(t *testing.T) {
	normalizer := NewDBQueryTermNormalizer(testTermMappingLister{
		mappings: []intentModel.QueryTermMapping{
			{SourceTerm: "保", TargetTerm: "保司", MatchType: 1, Priority: 100, Enabled: 1},
			{SourceTerm: "VIP", TargetTerm: "会员", MatchType: 1, Priority: 10, Enabled: 0},
		},
		total: 2,
	})

	got, err := normalizer.Normalize(context.Background(), "VIP保司支持什么")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "VIP保司支持什么" {
		t.Fatalf("expected safe normalization to keep target text unchanged, got %q", got)
	}
}

func TestNormalizingRewriter_UsesNormalizedQuestion(t *testing.T) {
	normalizer := &staticQueryNormalizer{value: "会员权益如何查询"}
	next := &recordingRewriter{result: "会员权益如何查询"}
	rewriter := NewNormalizingRewriter(normalizer, next)

	result, err := rewriter.Rewrite(context.Background(), "VIP权益如何查询", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.lastQuestion != "会员权益如何查询" {
		t.Fatalf("expected normalized question to be passed through, got %q", next.lastQuestion)
	}
	if result.RewrittenQuestion != "会员权益如何查询" {
		t.Fatalf("unexpected rewritten question: %q", result.RewrittenQuestion)
	}
}

type staticQueryNormalizer struct {
	value string
}

func (n *staticQueryNormalizer) Normalize(ctx context.Context, text string) (string, error) {
	return n.value, nil
}
