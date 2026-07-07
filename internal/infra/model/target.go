package model

import "go-base-agent/internal/framework/config"

// Target holds a resolved model target with its configuration.
// Aligns with Java ModelTarget record.
type Target struct {
	ID        string
	Candidate config.AICandidateConfig
	Provider  config.AIProviderConfig
}
