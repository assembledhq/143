package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// handleLinearAgentCreated processes a `created` AgentSessionEvent. Linear
// delivers this when the agent user is assigned (or @-mentioned via an
// agent session). The handler walks the documented orchestration:
//
//  1. Idempotency guard against re-deliveries.
//  2. Fetch the live Linear issue (project, labels, comments).
//  3. Per-team enable gate.
//  4. Resolve the target 143 repository.
//  5. Upsert the 143 issues row.
//  6. Create the 143 session with Origin=IssueTrigger.
//  7. Record the AgentSessionID on the link's provider state so
//     HandleAgentMilestone fan-outs can find it.
//  8. Attach the session id to the bridge row.
//  9. Enqueue run_agent.
//
// Failure modes documented per step. Errors that should retry come back
// via fmt.Errorf wrapping; benign user states (team disabled, repo
// unmapped) close the AgentSession with a `response` activity and
// return nil.
func handleLinearAgentCreated(
	ctx context.Context,
	deps LinearAgentEventHandlerDeps,
	agentSessions *db.LinearAgentSessionStore,
	activities *db.LinearAgentActivityLogStore,
	payload linearAgentEventPayload,
	logger zerolog.Logger,
) error {
	orgID, err := uuid.Parse(payload.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org_id: %w", err)
	}
	if _, err := uuid.Parse(payload.AgentSessionRowID); err != nil {
		return fmt.Errorf("invalid agent_session_row_id: %w", err)
	}

	// 1. Idempotency guard. The dispatcher's UNIQUE on
	// linear_agent_sessions guarantees we have at most one row per
	// AgentSessionID, but a job re-delivery (worker pod kill mid-run)
	// can fire this handler twice. Re-load the row and short-circuit
	// if session_id is already populated — the first run already
	// created the 143 session and we shouldn't make a duplicate.
	row, err := agentSessions.Lookup(ctx, orgID, payload.LinearAgentSessionID)
	if err != nil {
		return fmt.Errorf("lookup linear_agent_sessions: %w", err)
	}
	if row.SessionID != nil {
		logger.Info().
			Str("agent_session_id", payload.LinearAgentSessionID).
			Str("session_id", row.SessionID.String()).
			Msg("linear_agent_event: session already created on a prior delivery; skipping")
		return nil
	}

	// 2. Resolve the integration's Linear client. Required for both
	// the issue fetch (to get team/project context) and the
	// follow-up activity emits.
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve linear client: %w", err)
	}

	// 3. Fetch the live issue. We trust the live read over the
	// dispatcher's webhook envelope because (a) labels and project
	// metadata aren't always in the webhook, (b) the issue body may
	// have changed between dispatch and worker execution, and (c) we
	// need the team key for per-team enabled gating below.
	issueIdent := row.LinearIssueIdentifier
	if issueIdent == "" {
		issueIdent = payload.LinearIssueID
	}
	fetched, err := client.FetchIssue(ctx, issueIdent)
	if err != nil {
		return fmt.Errorf("fetch linear issue %q: %w", issueIdent, err)
	}
	if fetched == nil {
		return errors.New("fetch linear issue returned nil")
	}

	// 4. Per-team enable gate. Loaded after FetchIssue so we have the
	// canonical team key.
	if deps.SettingsLoader != nil {
		settings, err := deps.SettingsLoader(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load agent settings: %w", err)
		}
		if !settings.EnabledFor(fetched.TeamKey) {
			logger.Info().
				Str("team_key", fetched.TeamKey).
				Msg("linear_agent_event: team disabled by org settings; closing AgentSession")
			return finalizeUnsupported(ctx, client, agentSessions, activities, row, orgID,
				"This Linear team is not currently enabled for the @143 agent.",
				models.LinearAgentSessionStateComplete)
		}
	}

	// 5. Resolve the repo. The resolver returns a stable Source string
	// so the operator debug surface can answer "how was this session's
	// repo chosen?". project_id and labels both come from the live
	// FetchIssue payload — the dispatcher payload captures team/project
	// too but issue metadata can change between dispatch and worker.
	projectID := fetched.ProjectID
	if projectID == "" {
		projectID = payload.LinearIssueProjectID
	}
	repoResult, err := deps.RepoResolver.Resolve(ctx, linear.AgentRepoResolveInput{
		OrgID:           orgID,
		LinearTeamID:    fetched.TeamID,
		LinearProjectID: projectID,
		Labels:          fetched.Labels,
	})
	if errors.Is(err, linear.ErrAgentRepoUnmapped) {
		// Expected user state — not an error. Render an actionable
		// message and close the AgentSession so the user knows what
		// to do.
		activity := linear.UnmappedRepoActivity(fetched.TeamName)
		emitErr := emitOnce(ctx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity)
		if emitErr != nil {
			logger.Warn().Err(emitErr).Msg("linear_agent_event: failed to emit unmapped-repo response; replays will short-circuit")
		}
		if stateErr := agentSessions.SetState(ctx, orgID, row.ID, models.LinearAgentSessionStateComplete); stateErr != nil {
			logger.Warn().Err(stateErr).Msg("linear_agent_event: failed to record terminal state on unmapped-repo close")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	// 6. Upsert the issues row. The session-create path needs a 143
	// issues row to attach via session_issue_links. Reuses the same
	// upsert helper that powers the existing detection flow so the
	// data shape stays consistent.
	issue, err := upsertLinearIssueForAgent(ctx, deps.Stores, orgID, fetched, &repoResult.RepositoryID)
	if err != nil {
		return fmt.Errorf("upsert linear issue: %w", err)
	}

	// 7. Build the session. Origin=IssueTrigger so PM / automations
	// hooks know it's an external trigger, not a user-initiated run.
	// PMApproach carries the issue body so run_agent has all the
	// context it needs without re-fetching Linear data.
	session := buildAgentSession(orgID, repoResult, issue, fetched, payload.LinearAgentSessionID)
	if err := deps.Stores.Sessions.Create(ctx, session); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// 8. Persist the AgentSessionID on the provider-state row so
	// HandleMilestone's fan-out can find it. This is what flips
	// HandleAgentMilestone from "skip" to "emit" for subsequent
	// milestones on this session.
	if err := writeAgentProviderState(ctx, deps.Stores, deps.ProviderState, orgID, session.ID, issue.ID, payload.LinearAgentSessionID, fetched); err != nil {
		logger.Warn().Err(err).
			Str("session_id", session.ID.String()).
			Msg("linear_agent_event: failed to record agent_session_id in provider_state; agent activities for this session will silently drop")
	}

	// 9. Update the bridge row so subsequent webhooks (prompted /
	// recovery sweeps) can find this 143 session.
	if err := agentSessions.AttachSession(ctx, orgID, row.ID, session.ID); err != nil {
		// AttachSession is the only piece that, if it fails, produces
		// a hard inconsistency between Linear and 143. Surface as a
		// job error so the worker retries. The session row is already
		// created; the retry path re-enters this handler where the
		// session_id-already-set guard takes the right short-circuit.
		return fmt.Errorf("attach session to linear_agent_sessions: %w", err)
	}

	// 10. Enqueue run_agent. The orchestrator picks it up and streams
	// milestones back through HandleMilestone + HandleAgentMilestone.
	if err := enqueueRunAgentForLinearAgent(ctx, deps.Stores, orgID, session.ID); err != nil {
		return fmt.Errorf("enqueue run_agent: %w", err)
	}

	if deps.Metrics != nil {
		deps.Metrics.RecordSessionCreated(ctx, repoResult.Source)
	}
	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", session.ID.String()).
		Str("repository_id", repoResult.RepositoryID.String()).
		Str("repo_resolution", repoResult.Source).
		Msg("linear_agent_event: session created")
	return nil
}
