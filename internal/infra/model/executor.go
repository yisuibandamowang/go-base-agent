package model

import "fmt"

// RoutingExecutor holds shared state for executing model calls with fallback.
// Aligns with Java ModelRoutingExecutor.
type RoutingExecutor struct {
	health *HealthStore
}

// NewRoutingExecutor creates a new RoutingExecutor.
func NewRoutingExecutor(health *HealthStore) *RoutingExecutor {
	return &RoutingExecutor{health: health}
}

// ExecuteWithFallback tries each target in order, skipping unavailable or unhealthy ones.
// Returns the first successful result, or an error if all targets fail.
// This is a package-level generic function; the executor parameter provides the health store.
func ExecuteWithFallback[C any, T any](
	exec *RoutingExecutor,
	capability Capability,
	targets []Target,
	resolveClient func(Target) (C, bool),
	callClient func(client C, target Target) (T, error),
) (T, error) {
	label := capability.DisplayName()

	if len(targets) == 0 {
		var zero T
		return zero, fmt.Errorf("no %s model candidates available", label)
	}

	var last error
	for _, target := range targets {
		client, ok := resolveClient(target)
		if !ok {
			continue
		}

		if !exec.health.AllowCall(target.ID) {
			continue
		}

		result, err := callClient(client, target)
		if err == nil {
			exec.health.MarkSuccess(target.ID)
			return result, nil
		}

		last = err
		exec.health.MarkFailure(target.ID)
	}

	var zero T
	return zero, fmt.Errorf("all %s model candidates failed: %w", label, last)
}
