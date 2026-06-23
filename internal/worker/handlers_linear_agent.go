package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// linearAgentEventPayload mirrors the dispatcher's job payload. Kept in
// lockstep with the corresponding map literal in
// handlers/linear_agent_webhook.go.
type linearAgentEventPayload struct {
	Action               string `json:"action"`
	OrgID                string `json:"org_id"`
	IntegrationID        string `json:"integration_id"`
	AgentSessionRowID    string `json:"agent_session_row_id"`
	LinearAgentSessionID string `json:"linear_agent_session_id"`
	LinearIssueID        string `json:"linear_issue_id"`
	LinearIssueTeamID    string `json:"linear_issue_team_id"`
	LinearIssueProjectID string `json:"linear_issue_project_id"`
	LinearCreatorUserID  string `json:"linear_creator_user_id"`
	LinearCreatorEmail   string `json:"linear_creator_email"`
	LinearCreatorName    string `json:"linear_creator_name"`
	LinearCommentID      string `json:"linear_comment_id"`
	LinearPromptBody     string `json:"linear_prompt_body"`
}

// LinearAgentEventHandlerDeps captures the wiring the handler needs. The
// dependencies stay narrow so tests can swap in fakes per piece without
// standing up the full worker bundle.
//
// Exported because cmd/server/main.go and the worker bundle wiring construct
// this struct directly; the handler itself is registered through Services.
type LinearAgentEventHandlerDeps struct {
	Stores         *Stores
	Linear         *linear.Service
	RepoResolver   *linear.AgentRepoResolver
	ProviderState  *db.LinearProviderStateStore
	SettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
	// OrgSettingsLoader resolves full org settings for session construction
	// choices that live outside the LinearAgent subdocument, such as the
	// default coding-agent type.
	OrgSettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error)
	ClientForOrg      func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	// Metrics records per-session-created counters. Optional; nil
	// silently skips recording.
	Metrics LinearAgentWorkerMetricsRecorder
	Logger  zerolog.Logger
}

// LinearAgentWorkerMetricsRecorder is the narrow surface the worker
// handler needs for metrics. Distinct from the writer's recorder so each
// boundary can evolve independently.
type LinearAgentWorkerMetricsRecorder interface {
	RecordSessionCreated(ctx context.Context, repoSource string)
}

// newLinearAgentEventHandler returns the worker function for
// `linear_agent_event` jobs. Returns nil when the agent stores aren't
// wired (phase 1 plumbing not yet rolled out) — the registration site
// short-circuits.
//
// The closure is intentionally tiny: parse the envelope, dispatch by
// action. Each action's body lives in its own file
// (handlers_linear_agent_created.go, handlers_linear_agent_prompted.go)
// so adding a third action — e.g. `cancelled` — is a single new file
// rather than a 200-line conditional inside this closure.
func newLinearAgentEventHandler(deps LinearAgentEventHandlerDeps) JobHandler {
	if deps.Stores == nil || deps.Stores.Sessions == nil || deps.Stores.Issues == nil {
		return nil
	}
	if deps.Linear == nil || deps.Linear.AgentSessionStore() == nil {
		return nil
	}
	if deps.RepoResolver == nil || deps.ClientForOrg == nil {
		return nil
	}
	logger := deps.Logger.With().Str("handler", "linear_agent_event").Logger()
	agentSessions := deps.Linear.AgentSessionStore()
	activities := deps.Linear.AgentActivityStore()

	return func(ctx context.Context, _ string, raw json.RawMessage) error {
		var payload linearAgentEventPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("unmarshal linear_agent_event payload: %w", err)
		}
		switch payload.Action {
		case "created":
			return handleLinearAgentCreated(ctx, deps, agentSessions, activities, payload, logger)
		case "prompted":
			return handleLinearAgentPrompted(ctx, deps, agentSessions, payload, logger)
		default:
			logger.Info().Str("action", payload.Action).Msg("linear_agent_event: unknown action; skipping")
			return nil
		}
	}
}
