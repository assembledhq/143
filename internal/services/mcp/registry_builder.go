package mcp

import (
	"fmt"
	"io"
	"os"

	"github.com/assembledhq/143/internal/services/integration"
)

// BuildRegistryFromEnv creates an integration registry from environment variables.
// Each integration is optional — only configured integrations are registered.
// Diagnostic messages are written to logger (typically stderr).
func BuildRegistryFromEnv(logger io.Writer) *integration.Registry {
	reg := integration.NewRegistry()

	if token := os.Getenv("SENTRY_AUTH_TOKEN"); token != "" {
		orgSlug := os.Getenv("SENTRY_ORG_SLUG")
		baseURL := os.Getenv("SENTRY_BASE_URL")

		tracker := integration.NewSentryErrorTracker(integration.SentryTrackerConfig{
			AuthToken: token,
			OrgSlug:   orgSlug,
			BaseURL:   baseURL,
		})
		reg.RegisterErrorTracker(tracker)
		fmt.Fprintf(logger, "143-tools: registered sentry (org=%s)\n", orgSlug)
	}

	if token := os.Getenv("LINEAR_ACCESS_TOKEN"); token != "" {
		apiURL := os.Getenv("LINEAR_API_URL")

		manager := integration.NewLinearTaskManager(integration.LinearManagerConfig{
			AuthToken: token,
			APIURL:    apiURL,
		})
		reg.RegisterTaskManager(manager)
		fmt.Fprintln(logger, "143-tools: registered linear")
	}

	return reg
}
