package db

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// LinearAgentSettingsView adapts an *OrganizationStore into the
// LoadAgentSettings(ctx, orgID) shape consumed by both the router-side
// dispatcher (handlers.linearAgentOrgSettings) and the worker-side repo
// resolver (linear.AgentSettingsLoader). Centralized here so the same
// adapter doesn't ship in two copies across cmd/server/main.go and
// internal/api/router.go.
//
// Nil-receiver-safe via the Orgs nil-check: boot stages that haven't yet
// constructed an OrgStore can wire a zero-value view and get a sensible
// "no settings" default rather than a panic.
type LinearAgentSettingsView struct {
	Orgs  *OrganizationStore
	Repos *RepositoryStore
}

// LoadAgentSettings returns the linear_agent sub-section of the org's
// settings, applying defaults via ParseOrgSettings. Returns the zero value
// when the Orgs field is nil so callers don't have to special-case unwired
// stages.
func (v LinearAgentSettingsView) LoadAgentSettings(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error) {
	if v.Orgs == nil {
		return models.LinearAgentSettings{}, nil
	}
	org, err := v.Orgs.GetByID(ctx, orgID)
	if err != nil {
		return models.LinearAgentSettings{}, err
	}
	parsed, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return models.LinearAgentSettings{}, err
	}
	return parsed.LinearAgent, nil
}

// LoadDefaultWorkRepositoryID returns the shared org-level repository default
// used by integrations when their provider-specific default is unset.
func (v LinearAgentSettingsView) LoadDefaultWorkRepositoryID(ctx context.Context, orgID uuid.UUID) (*uuid.UUID, error) {
	if v.Orgs != nil {
		org, err := v.Orgs.GetByID(ctx, orgID)
		if err != nil {
			return nil, err
		}
		parsed, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			return nil, err
		}
		if parsed.DefaultWorkRepositoryID != nil {
			return parsed.DefaultWorkRepositoryID, nil
		}
	}
	if v.Repos == nil {
		return nil, nil
	}
	repos, err := v.Repos.ListByOrg(ctx, orgID, RepositoryFilters{})
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, nil
	}
	repoID := repos[0].ID
	return &repoID, nil
}
