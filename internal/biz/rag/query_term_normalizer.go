package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/infra/chat"
)

// QueryTermMappingLister lists term mappings for query normalization.
type QueryTermMappingLister interface {
	ListByDomain(ctx context.Context, domain string, page, size int) ([]intentModel.QueryTermMapping, int64, error)
}

// QueryTermNormalizer normalizes user questions before query rewriting.
type QueryTermNormalizer interface {
	Normalize(ctx context.Context, text string) (string, error)
}

// DBQueryTermNormalizer loads enabled mappings from the database and applies them safely.
type DBQueryTermNormalizer struct {
	lister QueryTermMappingLister
	domain string
}

const queryTermMappingPageSize = 200

// NewDBQueryTermNormalizer creates a DB-backed query term normalizer.
func NewDBQueryTermNormalizer(lister QueryTermMappingLister) *DBQueryTermNormalizer {
	return &DBQueryTermNormalizer{lister: lister}
}

// Normalize applies enabled source-to-target mappings in priority order.
func (n *DBQueryTermNormalizer) Normalize(ctx context.Context, text string) (string, error) {
	if n == nil || strings.TrimSpace(text) == "" {
		return text, nil
	}
	if n.lister == nil {
		return text, nil
	}
	mappings, err := n.loadMappings(ctx)
	if err != nil {
		return text, err
	}
	result := text
	for _, mapping := range mappings {
		if mapping.Enabled != 1 || mapping.MatchType != 1 {
			continue
		}
		if strings.TrimSpace(mapping.SourceTerm) == "" || strings.TrimSpace(mapping.TargetTerm) == "" {
			continue
		}
		result = applyQueryTermMapping(result, mapping.SourceTerm, mapping.TargetTerm)
	}
	if result != text {
		slog.Info("query term normalized", "from", text, "to", result)
	}
	return result, nil
}

func (n *DBQueryTermNormalizer) loadMappings(ctx context.Context) ([]intentModel.QueryTermMapping, error) {
	page := 1
	mappings := make([]intentModel.QueryTermMapping, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, total, err := n.lister.ListByDomain(ctx, n.domain, page, queryTermMappingPageSize)
		if err != nil {
			return nil, fmt.Errorf("load query term mappings: %w", err)
		}
		mappings = append(mappings, batch...)
		if len(batch) == 0 || int64(len(mappings)) >= total {
			break
		}
		page++
	}

	sort.SliceStable(mappings, func(i, j int) bool {
		if mappings[i].Priority != mappings[j].Priority {
			return mappings[i].Priority > mappings[j].Priority
		}
		leftLen := utf8.RuneCountInString(mappings[i].SourceTerm)
		rightLen := utf8.RuneCountInString(mappings[j].SourceTerm)
		if leftLen != rightLen {
			return leftLen > rightLen
		}
		return mappings[i].SourceTerm < mappings[j].SourceTerm
	})
	return mappings, nil
}

func applyQueryTermMapping(text, sourceTerm, targetTerm string) string {
	if text == "" || sourceTerm == "" || targetTerm == "" {
		return text
	}

	var b strings.Builder
	idx := 0
	for idx < len(text) {
		hit := strings.Index(text[idx:], sourceTerm)
		if hit < 0 {
			b.WriteString(text[idx:])
			break
		}
		hit += idx
		b.WriteString(text[idx:hit])

		alreadyTarget := hit+len(targetTerm) <= len(text) && strings.HasPrefix(text[hit:], targetTerm)
		if alreadyTarget {
			b.WriteString(text[hit : hit+len(targetTerm)])
			idx = hit + len(targetTerm)
			continue
		}

		b.WriteString(targetTerm)
		idx = hit + len(sourceTerm)
	}
	return b.String()
}

// NormalizingRewriter wraps a rewriter with a query term normalizer.
type NormalizingRewriter struct {
	normalizer QueryTermNormalizer
	next       QueryRewriter
}

// NewNormalizingRewriter creates a rewriter chain that normalizes first.
func NewNormalizingRewriter(normalizer QueryTermNormalizer, next QueryRewriter) *NormalizingRewriter {
	return &NormalizingRewriter{normalizer: normalizer, next: next}
}

// Rewrite normalizes the question before delegating to the next rewriter.
func (r *NormalizingRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	normalized := question
	if r != nil && r.normalizer != nil {
		nextQuestion, err := r.normalizer.Normalize(ctx, question)
		if err != nil {
			slog.Warn("query term normalization failed", "err", err)
		} else if strings.TrimSpace(nextQuestion) != "" {
			normalized = nextQuestion
		}
	}
	if r == nil || r.next == nil {
		return &RewriteResult{RewrittenQuestion: normalized}, nil
	}
	return r.next.Rewrite(ctx, normalized, history)
}
