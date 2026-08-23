package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"text/template"

	agentRepo "go-base-agent/internal/biz/agent/repo"
)

// PromptResolver resolves runtime prompts from agent profiles.
type PromptResolver struct {
	repo  *agentRepo.AgentRepo
	mode  string
	cache PromptCacheManager

	mu      sync.RWMutex
	prompts map[string]string
}

// NewPromptResolver creates a prompt resolver.
func NewPromptResolver(repo *agentRepo.AgentRepo, mode string, caches ...PromptCacheManager) *PromptResolver {
	var promptCache PromptCacheManager
	if len(caches) > 0 {
		promptCache = caches[0]
	}
	return &PromptResolver{
		repo:    repo,
		mode:    normalizeMode(mode),
		cache:   promptCache,
		prompts: make(map[string]string),
	}
}

// Refresh reloads prompts from the builtin and active agent profiles.
func (r *PromptResolver) Refresh(ctx context.Context) error {
	if r == nil || r.repo == nil {
		return nil
	}
	builtin, err := r.repo.FindBuiltinProfile(ctx)
	if err != nil {
		return err
	}
	active, err := r.repo.FindActiveProfile(ctx)
	if err != nil {
		return err
	}

	next := make(map[string]string)
	if builtin != nil {
		if prompts, err := r.loadPrompts(ctx, builtin.ID); err == nil {
			mergeNonBlank(next, prompts)
		} else {
			return err
		}
	}
	if active != nil && (builtin == nil || active.ID != builtin.ID) {
		if prompts, err := r.loadPrompts(ctx, active.ID); err == nil {
			mergeNonBlank(next, prompts)
		} else {
			return err
		}
	}

	r.mu.Lock()
	r.prompts = next
	r.mu.Unlock()
	if r.cache != nil {
		if err := r.cache.Save(context.Background(), next); err != nil {
			slog.Warn("save agent prompts cache failed", "err", err)
		}
	}
	return nil
}

// Resolve returns the resolved prompt text for a slot.
func (r *PromptResolver) Resolve(slotKey string) string {
	if r == nil {
		return ""
	}
	if r.cache != nil {
		if prompts, hit, err := r.cache.Load(context.Background()); err != nil {
			slog.Warn("load agent prompts cache failed", "err", err)
		} else if hit {
			r.mu.Lock()
			r.prompts = prompts
			r.mu.Unlock()
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.prompts[strings.ToUpper(strings.TrimSpace(slotKey))]
}

// Render renders the resolved prompt template for a slot.
func (r *PromptResolver) Render(slotKey string, data any) (string, error) {
	raw := strings.TrimSpace(r.Resolve(slotKey))
	if raw == "" {
		return "", nil
	}
	tmpl, err := template.New(strings.ToUpper(strings.TrimSpace(slotKey))).Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse agent prompt %s: %w", slotKey, err)
	}
	var builder strings.Builder
	if err := tmpl.Execute(&builder, data); err != nil {
		return "", fmt.Errorf("render agent prompt %s: %w", slotKey, err)
	}
	return strings.TrimSpace(builder.String()), nil
}

func (r *PromptResolver) loadPrompts(ctx context.Context, agentID string) (map[string]string, error) {
	prompts, err := r.repo.ListPromptsByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	own := make(map[string]string, len(prompts))
	for _, prompt := range prompts {
		own[strings.ToUpper(strings.TrimSpace(prompt.SlotKey))] = strings.TrimSpace(prompt.Content)
	}
	return own, nil
}

func mergeNonBlank(target map[string]string, source map[string]string) {
	if len(source) == 0 {
		return
	}
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := strings.TrimSpace(source[key]); value != "" {
			target[key] = value
		}
	}
}
