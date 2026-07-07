package model

import (
	"fmt"
	"strings"

	"go-base-agent/internal/framework/config"
)

// ResolveURL resolves the full API URL for a model target and capability.
// Priority: candidate.URL > provider.URL + provider.Endpoints[capability].
// Aligns with Java ModelUrlResolver.
func ResolveURL(provider config.AIProviderConfig, candidate config.AICandidateConfig, capability Capability) (string, error) {
	if candidate.URL != "" {
		return candidate.URL, nil
	}

	if provider.URL == "" {
		return "", fmt.Errorf("provider base url is missing")
	}

	key := capability.String()
	path := provider.Endpoints[key]
	if path == "" {
		return "", fmt.Errorf("provider endpoint is missing: %s", key)
	}

	return joinURL(provider.URL, path), nil
}

// joinURL joins a base URL and path, handling slashes correctly.
func joinURL(baseURL, path string) string {
	baseSlash := strings.HasSuffix(baseURL, "/")
	pathSlash := strings.HasPrefix(path, "/")

	if baseSlash && pathSlash {
		return baseURL + path[1:]
	}
	if !baseSlash && !pathSlash {
		return baseURL + "/" + path
	}
	return baseURL + path
}
