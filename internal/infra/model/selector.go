package model

import (
	"fmt"
	"slices"
	"strings"

	"go-base-agent/internal/framework/config"
)

// Selector selects and sorts model candidates based on configuration and health state.
// Aligns with Java ModelSelector.
type Selector struct {
	cfg    config.AIConfig
	health *HealthStore
}

// NewSelector creates a new Selector.
func NewSelector(cfg config.AIConfig, health *HealthStore) *Selector {
	return &Selector{cfg: cfg, health: health}
}

// SelectChatCandidates returns sorted chat model targets.
func (s *Selector) SelectChatCandidates(deepThinking bool) []Target {
	group := s.cfg.Chat
	firstChoice := group.DefaultModel
	if deepThinking && group.DeepThinkingModel != "" {
		firstChoice = group.DeepThinkingModel
	}

	candidates := make([]config.AICandidateConfig, 0, len(group.Candidates))
	for _, c := range group.Candidates {
		if !c.IsEnabled() {
			continue
		}
		if deepThinking && !c.SupportsThinking {
			continue
		}
		candidates = append(candidates, c)
	}

	sortCandidates(candidates, func(c config.AICandidateConfig) (string, int) {
		return c.ID, c.Priority
	}, firstChoice)

	return s.buildChatTargets(candidates)
}

// SelectEmbeddingCandidates returns sorted embedding model targets.
func (s *Selector) SelectEmbeddingCandidates() []Target {
	group := s.cfg.Embedding
	firstChoice := group.DefaultModel

	candidates := make([]config.AIEmbeddingCandidateConfig, 0, len(group.Candidates))
	for _, c := range group.Candidates {
		if !c.IsEnabled() {
			continue
		}
		candidates = append(candidates, c)
	}

	sortCandidates(candidates, func(c config.AIEmbeddingCandidateConfig) (string, int) {
		return c.ID, c.Priority
	}, firstChoice)

	return s.buildEmbeddingTargets(candidates)
}

// SelectRerankCandidates returns sorted rerank model targets.
func (s *Selector) SelectRerankCandidates() []Target {
	group := s.cfg.Rerank
	firstChoice := group.DefaultModel

	candidates := make([]config.AIRerankCandidateConfig, 0, len(group.Candidates))
	for _, c := range group.Candidates {
		if !c.IsEnabled() {
			continue
		}
		candidates = append(candidates, c)
	}

	sortCandidates(candidates, func(c config.AIRerankCandidateConfig) (string, int) {
		return c.ID, c.Priority
	}, firstChoice)

	return s.buildRerankTargets(candidates)
}

// SelectVlmCandidates returns sorted VLM model targets.
func (s *Selector) SelectVlmCandidates() []Target {
	group := s.cfg.VLM
	firstChoice := group.DefaultModel

	candidates := make([]config.AIVLMCandidateConfig, 0, len(group.Candidates))
	for _, c := range group.Candidates {
		if !c.IsEnabled() {
			continue
		}
		candidates = append(candidates, c)
	}

	sortCandidates(candidates, func(c config.AIVLMCandidateConfig) (string, int) {
		return c.ID, c.Priority
	}, firstChoice)

	return s.buildVlmTargets(candidates)
}

// sortCandidates sorts candidates by first choice first, then priority, then id.
func sortCandidates[T any](candidates []T, keyFn func(T) (string, int), firstChoice string) {
	slices.SortStableFunc(candidates, func(a, b T) int {
		aID, aPrio := keyFn(a)
		bID, bPrio := keyFn(b)

		aFirst := aID != firstChoice
		bFirst := bID != firstChoice
		if aFirst != bFirst {
			if aFirst {
				return 1
			}
			return -1
		}
		if aPrio != bPrio {
			return aPrio - bPrio
		}
		return strings.Compare(aID, bID)
	})
}

func (s *Selector) buildChatTargets(candidates []config.AICandidateConfig) []Target {
	targets := make([]Target, 0, len(candidates))
	for _, c := range candidates {
		if !s.passFilter(c.ID, c.Provider, c.Model) {
			continue
		}
		targets = append(targets, Target{
			ID:        modelID(c.ID, c.Provider, c.Model),
			Candidate: c,
			Provider:  s.cfg.Providers[c.Provider],
		})
	}
	return targets
}

func (s *Selector) buildEmbeddingTargets(candidates []config.AIEmbeddingCandidateConfig) []Target {
	targets := make([]Target, 0, len(candidates))
	for _, c := range candidates {
		if !s.passFilter(c.ID, c.Provider, c.Model) {
			continue
		}
		targets = append(targets, Target{
			ID:        modelID(c.ID, c.Provider, c.Model),
			Candidate: toChatCandidate(c),
			Provider:  s.cfg.Providers[c.Provider],
		})
	}
	return targets
}

func (s *Selector) buildRerankTargets(candidates []config.AIRerankCandidateConfig) []Target {
	targets := make([]Target, 0, len(candidates))
	for _, c := range candidates {
		if !s.passFilter(c.ID, c.Provider, c.Model) {
			continue
		}
		targets = append(targets, Target{
			ID:        modelID(c.ID, c.Provider, c.Model),
			Candidate: toChatCandidateFromRerank(c),
			Provider:  s.cfg.Providers[c.Provider],
		})
	}
	return targets
}

func (s *Selector) buildVlmTargets(candidates []config.AIVLMCandidateConfig) []Target {
	targets := make([]Target, 0, len(candidates))
	for _, c := range candidates {
		if !s.passFilter(c.ID, c.Provider, c.Model) {
			continue
		}
		targets = append(targets, Target{
			ID:        modelID(c.ID, c.Provider, c.Model),
			Candidate: toChatCandidateFromVlm(c),
			Provider:  s.cfg.Providers[c.Provider],
		})
	}
	return targets
}

func (s *Selector) passFilter(id, provider, model string) bool {
	mid := modelID(id, provider, model)
	if s.health.IsUnavailable(mid) {
		return false
	}
	if !s.checkProvider(provider) {
		return false
	}
	return true
}

func modelID(id, provider, model string) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("%s::%s", provider, model)
}

func (s *Selector) checkProvider(providerID string) bool {
	if providerID == "noop" {
		return true
	}
	_, ok := s.cfg.Providers[providerID]
	return ok
}

func toChatCandidate(c config.AIEmbeddingCandidateConfig) config.AICandidateConfig {
	return config.AICandidateConfig{
		ID:        c.ID,
		Provider:  c.Provider,
		Model:     c.Model,
		URL:       c.URL,
		Dimension: c.Dimension,
		Priority:  c.Priority,
	}
}

func toChatCandidateFromRerank(c config.AIRerankCandidateConfig) config.AICandidateConfig {
	return config.AICandidateConfig{
		ID:       c.ID,
		Provider: c.Provider,
		Model:    c.Model,
		URL:      c.URL,
		Priority: c.Priority,
	}
}

func toChatCandidateFromVlm(c config.AIVLMCandidateConfig) config.AICandidateConfig {
	return config.AICandidateConfig{
		ID:       c.ID,
		Provider: c.Provider,
		Model:    c.Model,
		URL:      c.URL,
		Priority: c.Priority,
	}
}
