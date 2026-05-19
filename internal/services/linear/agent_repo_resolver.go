package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// AgentRepoResolver maps a Linear-side issue context (team, project, labels)
// to a 143 repository_id so the inbound agent flow knows which repo to clone.
//
// The resolver runs on every `created` AgentSessionEvent so its hot path
// must be cheap. The store implementation does the lookup as a single
// indexed query — see (LinearTeamRepoMappingStore).Resolve in
// internal/db/linear_team_repo_mappings.go.
type AgentRepoResolver struct {
	mappings *db.LinearTeamRepoMappingStore
	settings AgentSettingsLoader
	repos    AgentRepoLookup
}

// AgentSettingsLoader returns the parsed org settings so the resolver can
// fall back to org_settings.linear_agent.default_repo_id. Decoupled from
// OrgStore so tests can inject deterministic settings without standing up
// a full org row.
type AgentSettingsLoader interface {
	LoadAgentSettings(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
}

// AgentRepoLookup is the narrow surface the resolver needs from the
// repository store: "given a name within this org, give me the repo".
// Used to honor the `repo:<name>` label override.
type AgentRepoLookup interface {
	GetByFullName(ctx context.Context, orgID uuid.UUID, fullName string) (models.Repository, error)
}

// NewAgentRepoResolver wires the resolver. settings and repos may be nil
// for tests that only exercise the team-mapping path; missing settings
// disables the org-default fallback, missing repos disables the label
// override.
func NewAgentRepoResolver(mappings *db.LinearTeamRepoMappingStore, settings AgentSettingsLoader, repos AgentRepoLookup) *AgentRepoResolver {
	return &AgentRepoResolver{
		mappings: mappings,
		settings: settings,
		repos:    repos,
	}
}

// AgentRepoResolveInput is the resolver's question for one inbound
// AgentSession. Labels is the issue's full label set (lowercased values
// expected — the resolver normalizes again defensively).
type AgentRepoResolveInput struct {
	OrgID           uuid.UUID
	LinearTeamID    string
	LinearProjectID string
	Labels          []string
}

// AgentRepoResolveResult captures both the chosen repo and *why* it was
// chosen. Source is one of "label_override", "team_project_mapping",
// "team_default_mapping", "org_default" — the operator debug surface
// renders this so installs that match unexpectedly are easy to triage.
type AgentRepoResolveResult struct {
	RepositoryID uuid.UUID
	// DefaultBranch is the optional override stored alongside the mapping.
	// Empty when no override is configured; the agent's branch-naming logic
	// falls back to the repository's normal default in that case.
	DefaultBranch string
	// Source is a stable enum value; do not rephrase casually — operator
	// dashboards depend on it.
	Source string
}

// Resolve walks the documented priority order:
//  1. label override (`repo:<full-name>`)
//  2. exact (team, project) mapping
//  3. team default mapping (project NULL)
//  4. org default (org_settings.linear_agent.default_repo_id)
//
// Returns ErrAgentRepoUnmapped when none match. The dispatcher is expected
// to convert that error into a graceful AgentSession `response` activity
// with a "configure a mapping" hint, not a 5xx — it's an expected user
// state, not a system failure.
func (r *AgentRepoResolver) Resolve(ctx context.Context, in AgentRepoResolveInput) (AgentRepoResolveResult, error) {
	if r == nil || r.mappings == nil {
		return AgentRepoResolveResult{}, errors.New("agent repo resolver not configured")
	}
	if in.OrgID == uuid.Nil {
		return AgentRepoResolveResult{}, errors.New("org_id is required")
	}
	if in.LinearTeamID == "" {
		return AgentRepoResolveResult{}, errors.New("linear_team_id is required")
	}

	// 1. Label override. Gated behind LinearAgentSettings.AllowLabelRepoOverride
	// (default off): without the flag, any Linear contributor with
	// label-write access could redirect work to any repo the org owns,
	// bypassing the admin-controlled linear_team_repo_mappings. Orgs
	// whose Linear membership equals their 143 admin set can opt in.
	if r.repos != nil && r.settings != nil && len(in.Labels) > 0 {
		settings, err := r.settings.LoadAgentSettings(ctx, in.OrgID)
		if err != nil {
			return AgentRepoResolveResult{}, fmt.Errorf("load agent settings: %w", err)
		}
		if settings.EffectiveAllowLabelRepoOverride() {
			if repo, ok := r.resolveLabelOverride(ctx, in); ok {
				return AgentRepoResolveResult{
					RepositoryID: repo.ID,
					Source:       "label_override",
				}, nil
			}
		}
	}

	// 2 + 3. Single round-trip; the store ranks exact match above team-default.
	mapping, err := r.mappings.Resolve(ctx, in.OrgID, db.ResolveInput{
		OrgID:           in.OrgID,
		LinearTeamID:    in.LinearTeamID,
		LinearProjectID: in.LinearProjectID,
	})
	switch {
	case err == nil:
		source := "team_default_mapping"
		if mapping.LinearProjectID != nil && *mapping.LinearProjectID == in.LinearProjectID && in.LinearProjectID != "" {
			source = "team_project_mapping"
		}
		return AgentRepoResolveResult{
			RepositoryID:  mapping.RepositoryID,
			DefaultBranch: mapping.DefaultBranch,
			Source:        source,
		}, nil
	case errors.Is(err, db.ErrLinearTeamRepoMappingNotFound):
		// fall through to org default
	default:
		return AgentRepoResolveResult{}, fmt.Errorf("resolve mapping: %w", err)
	}

	// 4. Org default.
	if r.settings != nil {
		settings, err := r.settings.LoadAgentSettings(ctx, in.OrgID)
		if err != nil {
			return AgentRepoResolveResult{}, fmt.Errorf("load agent settings: %w", err)
		}
		if settings.DefaultRepoID != nil {
			return AgentRepoResolveResult{
				RepositoryID: *settings.DefaultRepoID,
				Source:       "org_default",
			}, nil
		}
	}

	return AgentRepoResolveResult{}, ErrAgentRepoUnmapped
}

// resolveLabelOverride scans the issue labels for `repo:<full-name>` and
// returns the matched repo when found. Case-insensitive on the prefix; the
// rest of the label is matched against repositories.full_name verbatim
// (Linear preserves casing on labels).
func (r *AgentRepoResolver) resolveLabelOverride(ctx context.Context, in AgentRepoResolveInput) (models.Repository, bool) {
	const prefix = "repo:"
	for _, label := range in.Labels {
		trimmed := strings.TrimSpace(label)
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			continue
		}
		fullName := strings.TrimSpace(trimmed[len(prefix):])
		if fullName == "" {
			continue
		}
		repo, err := r.repos.GetByFullName(ctx, in.OrgID, fullName)
		if err != nil {
			// A label that points to a repo the org doesn't have is a user
			// error, not a system error. Fall through to the next label
			// (and ultimately to the team mapping) rather than blowing up.
			continue
		}
		return repo, true
	}
	return models.Repository{}, false
}

// ErrAgentRepoUnmapped is returned when the resolver finds no matching repo
// across all four tiers. Sentinel so the dispatcher can render an
// actionable Linear response activity instead of a 5xx.
var ErrAgentRepoUnmapped = errors.New("no repository mapped for this Linear team/project")
