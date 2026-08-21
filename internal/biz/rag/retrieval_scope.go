package rag

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
)

// RetrievalScope is the knowledge-base range resolved for one retrieval request.
type RetrievalScope struct {
	Directed              bool
	TopScore              float64
	Intents               []NodeScore
	TargetCollections     []string
	SupplementCollections []string
}

// RetrievalScopeResolver resolves one shared scope for all retrieval channels.
type RetrievalScopeResolver struct {
	backend             KnowledgeSearchBackend
	confidenceThreshold float64
	minIntentScore      float64
}

// NewRetrievalScopeResolver creates a resolver backed by the knowledge-base provider.
func NewRetrievalScopeResolver(backend KnowledgeSearchBackend, confidenceThreshold, minIntentScore float64) *RetrievalScopeResolver {
	if confidenceThreshold <= 0 {
		confidenceThreshold = 0.6
	}
	if minIntentScore < 0 {
		minIntentScore = 0.4
	}
	return &RetrievalScopeResolver{
		backend:             backend,
		confidenceThreshold: confidenceThreshold,
		minIntentScore:      minIntentScore,
	}
}

// Resolve resolves the effective scope once for a retrieval request.
func (r *RetrievalScopeResolver) Resolve(ctx context.Context, subIntents []SubQuestionIntent) (RetrievalScope, error) {
	if r == nil || r.backend == nil {
		return RetrievalScope{}, nil
	}
	kbs, err := r.backend.ListKnowledgeBases(ctx)
	if err != nil {
		return RetrievalScope{}, fmt.Errorf("list knowledge bases for retrieval scope: %w", err)
	}
	active := activeKnowledgeCollections(kbs)
	intents := effectiveScopeIntents(subIntents, r.minIntentScore)
	topScore := 0.0
	for _, nodeScore := range intents {
		if nodeScore.Score > topScore {
			topScore = nodeScore.Score
		}
	}
	if len(intents) == 0 || topScore < r.confidenceThreshold {
		return RetrievalScope{TopScore: topScore, TargetCollections: active}, nil
	}

	bound := make(map[string]struct{})
	for _, nodeScore := range intents {
		for _, collection := range nodeScore.Node.EffectiveCollectionNames() {
			collection = strings.TrimSpace(collection)
			if collection != "" {
				bound[collection] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(bound))
	for _, collection := range active {
		if _, ok := bound[collection]; ok {
			targets = append(targets, collection)
		}
	}
	if len(targets) == 0 {
		slog.Warn("retrieval scope bindings are stale, falling back to global scope", "collections", keysOfStringSet(bound))
		return RetrievalScope{TopScore: topScore, TargetCollections: active}, nil
	}

	supplement := make([]string, 0, len(active)-len(targets))
	targetSet := make(map[string]struct{}, len(targets))
	for _, collection := range targets {
		targetSet[collection] = struct{}{}
	}
	for _, collection := range active {
		if _, ok := targetSet[collection]; !ok {
			supplement = append(supplement, collection)
		}
	}
	return RetrievalScope{
		Directed:              true,
		TopScore:              topScore,
		Intents:               intents,
		TargetCollections:     targets,
		SupplementCollections: supplement,
	}, nil
}

func effectiveScopeIntents(subIntents []SubQuestionIntent, minScore float64) []NodeScore {
	byID := make(map[string]NodeScore)
	order := make([]string, 0)
	for _, subIntent := range subIntents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB || nodeScore.Score < minScore || len(nodeScore.Node.EffectiveCollectionNames()) == 0 {
				continue
			}
			id := strings.TrimSpace(nodeScore.Node.ID)
			if id == "" {
				id = strings.Join(nodeScore.Node.EffectiveCollectionNames(), ",")
			}
			previous, exists := byID[id]
			if !exists {
				order = append(order, id)
				byID[id] = nodeScore
			} else if nodeScore.Score > previous.Score {
				byID[id] = nodeScore
			}
		}
	}
	intents := make([]NodeScore, 0, len(order))
	for _, id := range order {
		intents = append(intents, byID[id])
	}
	return intents
}

func keysOfStringSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// ScopeQuota splits one channel budget between the directed and supplement scopes.
type ScopeQuota struct {
	Primary    int
	Supplement int
}

// SplitScopeQuota keeps at least one slot for the directed scope when supplementing.
func SplitScopeQuota(scope *RetrievalScope, budget int, ratio float64) ScopeQuota {
	if scope == nil || !scope.Directed || len(scope.SupplementCollections) == 0 || ratio <= 0 || budget <= 1 {
		return ScopeQuota{Primary: budget}
	}
	supplement := int(math.Round(float64(budget) * ratio))
	if supplement < 1 {
		supplement = 1
	}
	if supplement > budget-1 {
		supplement = budget - 1
	}
	return ScopeQuota{Primary: budget - supplement, Supplement: supplement}
}

func capScopeChunks(chunks []RetrievedChunk, limit int) []RetrievedChunk {
	if limit <= 0 {
		return nil
	}
	if len(chunks) <= limit {
		return chunks
	}
	return chunks[:limit]
}
