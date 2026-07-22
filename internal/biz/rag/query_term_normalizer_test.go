package rag

import (
	"context"
	"testing"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	appctx "go-base-agent/internal/framework/context"
)

type testTermMappingLister struct {
	mappings []intentModel.QueryTermMapping
	total    int64
	domain   string
	calls    int
}

func (l *testTermMappingLister) ListByDomain(ctx context.Context, domain string, page, size int) ([]intentModel.QueryTermMapping, int64, error) {
	l.calls++
	l.domain = domain
	if domain == "" {
		return l.mappings, l.total, nil
	}
	filtered := make([]intentModel.QueryTermMapping, 0, len(l.mappings))
	for _, mapping := range l.mappings {
		if mapping.Domain == domain {
			filtered = append(filtered, mapping)
		}
	}
	return filtered, int64(len(filtered)), nil
}

type fakeQueryTermMappingCache struct {
	loadMappings []intentModel.QueryTermMapping
	loadHit      bool
	loadDomain   string
	loadCalls    int
	saveDomain   string
	saveMappings []intentModel.QueryTermMapping
	saveCalls    int
	clearDomain  string
	clearCalls   int
}

func (c *fakeQueryTermMappingCache) LoadMappings(ctx context.Context, domain string) ([]intentModel.QueryTermMapping, bool, error) {
	c.loadCalls++
	c.loadDomain = domain
	return append([]intentModel.QueryTermMapping(nil), c.loadMappings...), c.loadHit, nil
}

func (c *fakeQueryTermMappingCache) SaveMappings(ctx context.Context, domain string, mappings []intentModel.QueryTermMapping) error {
	c.saveCalls++
	c.saveDomain = domain
	c.saveMappings = append([]intentModel.QueryTermMapping(nil), mappings...)
	return nil
}

func (c *fakeQueryTermMappingCache) ClearMappings(ctx context.Context, domain string) error {
	c.clearCalls++
	c.clearDomain = domain
	return nil
}

func TestDBQueryTermNormalizer_NormalizeAppliesEnabledMappings(t *testing.T) {
	normalizer := NewDBQueryTermNormalizer(&testTermMappingLister{
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

func TestDBQueryTermNormalizer_UsesCacheBeforeLister(t *testing.T) {
	cache := &fakeQueryTermMappingCache{
		loadMappings: []intentModel.QueryTermMapping{
			{SourceTerm: "VIP", TargetTerm: "会员", MatchType: 1, Priority: 100, Enabled: 1},
		},
		loadHit: true,
	}
	lister := &testTermMappingLister{
		mappings: []intentModel.QueryTermMapping{
			{SourceTerm: "VIP", TargetTerm: "贵宾", MatchType: 1, Priority: 100, Enabled: 1},
		},
		total: 1,
	}
	normalizer := NewDBQueryTermNormalizer(lister)
	normalizer.SetCacheManager(cache)

	got, err := normalizer.Normalize(context.Background(), "VIP权益")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "会员权益" {
		t.Fatalf("expected cached mapping to apply, got %q", got)
	}
	if cache.loadCalls != 1 || cache.saveCalls != 0 {
		t.Fatalf("unexpected cache calls: %+v", cache)
	}
	if lister.calls != 0 {
		t.Fatalf("expected lister to be skipped on cache hit, got %d calls", lister.calls)
	}
}

func TestDBQueryTermNormalizer_SavesCacheAfterDBLoad(t *testing.T) {
	cache := &fakeQueryTermMappingCache{}
	lister := &testTermMappingLister{
		mappings: []intentModel.QueryTermMapping{
			{Domain: "membership", SourceTerm: "VIP", TargetTerm: "会员", MatchType: 1, Priority: 100, Enabled: 1},
		},
		total: 1,
	}
	normalizer := NewDBQueryTermNormalizer(lister)
	normalizer.SetCacheManager(cache)
	ctx := appctx.WithTenant(context.Background(), &appctx.TenantContext{TenantID: "tenant-1", Domain: "membership"})

	got, err := normalizer.Normalize(ctx, "VIP权益")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "会员权益" {
		t.Fatalf("expected db mapping to apply, got %q", got)
	}
	if cache.loadCalls != 1 || cache.saveCalls != 1 {
		t.Fatalf("unexpected cache calls: %+v", cache)
	}
	if cache.saveDomain != "membership" {
		t.Fatalf("expected membership cache domain, got %q", cache.saveDomain)
	}
	if lister.calls != 1 {
		t.Fatalf("expected lister to be called once, got %d", lister.calls)
	}
}

func TestDBQueryTermNormalizer_UsesTenantDomainFromContext(t *testing.T) {
	lister := &testTermMappingLister{
		mappings: []intentModel.QueryTermMapping{
			{Domain: "payment", SourceTerm: "保", TargetTerm: "保司", MatchType: 1, Priority: 100, Enabled: 1},
			{Domain: "membership", SourceTerm: "保", TargetTerm: "保险", MatchType: 1, Priority: 100, Enabled: 1},
		},
		total: 2,
	}
	normalizer := NewDBQueryTermNormalizer(lister)
	ctx := appctx.WithTenant(context.Background(), &appctx.TenantContext{TenantID: "tenant-1", Domain: "membership"})

	got, err := normalizer.Normalize(ctx, "保单查询")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "保险单查询" {
		t.Fatalf("expected membership mapping to apply, got %q", got)
	}
	if lister.domain != "membership" {
		t.Fatalf("expected domain membership, got %q", lister.domain)
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
