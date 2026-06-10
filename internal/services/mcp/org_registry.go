package mcp

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
)

// OrgCredentials carries the resolved per-org integration configs used to
// build a server-side tool registry for the local agent gateway
// (POST /api/v1/cli/tools/invoke). This is the laptop-mode counterpart of
// BuildRegistryFromEnv: same constructors, but credentials come from the
// org's integrations table and never leave the server.
//
// LinearAccessToken is a plain string (not *models.LinearConfig) because the
// caller is expected to run the refresh-aware resolver first — handing this
// builder a possibly-stale config would push token-rotation concerns into
// the wrong layer.
type OrgCredentials struct {
	Sentry            *models.SentryConfig
	LinearAccessToken string
	Notion            *models.NotionConfig
	CircleCI          *models.CircleCIConfig
	Mezmo             *models.MezmoConfig
}

// BuildRegistryFromOrg constructs an integration registry from org-resolved
// credentials. Only connected integrations register tools, so the gateway's
// tool list mirrors exactly what the org has connected — an org without
// Notion doesn't surface Notion tools to local agents.
func BuildRegistryFromOrg(c OrgCredentials) *integration.Registry {
	reg := integration.NewRegistry()

	if c.Sentry != nil && c.Sentry.AccessToken != "" {
		reg.RegisterErrorTracker(integration.NewSentryErrorTracker(integration.SentryTrackerConfig{
			AuthToken: c.Sentry.AccessToken,
			OrgSlug:   c.Sentry.OrgSlug,
		}))
	}

	if c.LinearAccessToken != "" {
		reg.RegisterTaskManager(integration.NewLinearTaskManager(integration.LinearManagerConfig{
			AuthToken: c.LinearAccessToken,
		}))
	}

	if c.Notion != nil && c.Notion.AccessToken != "" {
		reg.RegisterDocumentStore(integration.NewNotionDocumentStore(integration.NotionDocumentStoreConfig{
			AuthToken: c.Notion.AccessToken,
		}))
	}

	if c.CircleCI != nil && c.CircleCI.AuthToken != "" && c.CircleCI.ProjectSlug != "" {
		reg.RegisterCITestInsights(integration.NewCircleCITestInsights(integration.CircleCIConfig{
			AuthToken:   c.CircleCI.AuthToken,
			ProjectSlug: c.CircleCI.ProjectSlug,
		}))
	}

	if c.Mezmo != nil && c.Mezmo.APIKey != "" {
		reg.RegisterLogProvider(integration.NewMezmoProvider(integration.MezmoConfig{
			APIKey:  c.Mezmo.APIKey,
			BaseURL: c.Mezmo.BaseURL,
			Dataset: c.Mezmo.Dataset,
		}))
	}

	return reg
}
