package rag

import (
	"context"
	"reflect"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
)

type scopeTestBackend struct {
	kbs []knowledgeModel.KnowledgeBase
}

func (b *scopeTestBackend) ListKnowledgeBases(context.Context) ([]knowledgeModel.KnowledgeBase, error) {
	return b.kbs, nil
}

func (b *scopeTestBackend) SearchKeywordChunks(context.Context, knowledgeModel.KnowledgeBase, string, int) ([]RetrievedChunk, error) {
	return nil, nil
}

func (b *scopeTestBackend) SearchRecentChunks(context.Context, string, int) ([]RetrievedChunk, error) {
	return nil, nil
}

func (b *scopeTestBackend) MatchIntentCollections(context.Context, string, int) ([]string, error) {
	return nil, nil
}

func TestRetrievalScopeResolverFallsBackWhenAllIntentCollectionsAreStale(t *testing.T) {
	resolver := NewRetrievalScopeResolver(&scopeTestBackend{
		kbs: []knowledgeModel.KnowledgeBase{
			{CollectionName: "active_a"},
			{CollectionName: "active_b"},
		},
	}, 0.6, 0.4)

	scope, err := resolver.Resolve(context.Background(), []SubQuestionIntent{{NodeScores: []NodeScore{{
		Node:  IntentNode{ID: "deleted-intent", Kind: IntentKindKB, CollectionName: "deleted"},
		Score: 0.95,
	}}}})
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if scope.Directed {
		t.Fatalf("stale intent binding must fall back to global scope: %+v", scope)
	}
	if !reflect.DeepEqual(scope.TargetCollections, []string{"active_a", "active_b"}) {
		t.Fatalf("unexpected global target collections: %+v", scope.TargetCollections)
	}
	if len(scope.SupplementCollections) != 0 {
		t.Fatalf("global scope must not have supplement collections: %+v", scope)
	}
}

func TestMultiChannelRetrievalEnginePassesOneScopeToAllChannels(t *testing.T) {
	backend := &scopeTestBackend{kbs: []knowledgeModel.KnowledgeBase{
		{CollectionName: "active_a"},
		{CollectionName: "active_b"},
	}}
	resolver := NewRetrievalScopeResolver(backend, 0.6, 0.4)
	channels := []*scopeRecordingChannel{
		{name: "vector", typ: ChannelVectorGlobal},
		{name: "keyword", typ: ChannelKeyword},
		{name: "graph", typ: ChannelGraph},
	}
	engine := NewMultiChannelRetrievalEngine([]SearchChannel{channels[0], channels[1], channels[2]}, nil)
	engine.SetRetrievalScopeResolver(resolver)

	_, err := engine.Retrieve(context.Background(), SearchContext{
		OriginalQuestion: "会员规则",
		Intents: []SubQuestionIntent{{NodeScores: []NodeScore{{
			Node:  IntentNode{ID: "member", Kind: IntentKindKB, CollectionName: "active_a"},
			Score: 0.9,
		}}}},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	for _, channel := range channels {
		if channel.scope == nil {
			t.Fatalf("channel %s did not receive a retrieval scope", channel.name)
		}
		if !channel.scope.Directed || !reflect.DeepEqual(channel.scope.TargetCollections, []string{"active_a"}) {
			t.Fatalf("channel %s received inconsistent scope: %+v", channel.name, channel.scope)
		}
	}
}

func TestSplitScopeQuotaReservesSupplementBudget(t *testing.T) {
	quota := SplitScopeQuota(&RetrievalScope{
		Directed:              true,
		SupplementCollections: []string{"other"},
	}, 4, 0.25)
	if quota.Primary != 3 || quota.Supplement != 1 {
		t.Fatalf("unexpected scope quota: %+v", quota)
	}

	global := SplitScopeQuota(&RetrievalScope{TargetCollections: []string{"all"}}, 4, 0.25)
	if global.Primary != 4 || global.Supplement != 0 {
		t.Fatalf("global scope must not reserve supplement budget: %+v", global)
	}
}

type scopeRecordingChannel struct {
	name  string
	typ   SearchChannelType
	scope *RetrievalScope
}

func (c *scopeRecordingChannel) Name() string                 { return c.name }
func (c *scopeRecordingChannel) Priority() int                { return 1 }
func (c *scopeRecordingChannel) Type() SearchChannelType      { return c.typ }
func (c *scopeRecordingChannel) IsEnabled(SearchContext) bool { return true }
func (c *scopeRecordingChannel) Search(_ context.Context, sc SearchContext) (SearchChannelResult, error) {
	if sc.RetrievalScope != nil {
		copy := *sc.RetrievalScope
		c.scope = &copy
	}
	return SearchChannelResult{ChannelType: c.typ, ChannelName: c.name}, nil
}
