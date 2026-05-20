package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
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
	agentSessionRowID, err := uuid.Parse(payload.AgentSessionRowID)
	if err != nil {
		return fmt.Errorf("invalid agent_session_row_id: %w", err)
	}
	registerLinearAgentCreatedDeadLetter(ctx, deps, agentSessions, activities, orgID, agentSessionRowID, payload.LinearAgentSessionID, logger)

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
		// A prior delivery already created the 143 session and bound it
		// to this bridge row. Re-run the idempotent tail steps
		// (provider-state merge, run_agent enqueue) in case the prior
		// delivery died between AttachSession and those writes — without
		// this reconciliation a transient failure of writeAgentProviderState
		// would silently drop every milestone for this session.
		return reconcileLinearAgentCreated(ctx, deps, row, *row.SessionID, payload, logger)
	}

	// 2. Resolve the integration's Linear client. Required for both
	// the issue fetch (to get team/project context) and the
	// follow-up activity emits.
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve linear client: %w", err)
	}
	bootstrapIdentifier := row.LinearIssueIdentifier
	if bootstrapIdentifier == "" {
		bootstrapIdentifier = payload.LinearIssueID
	}
	if err := emitLinearAgentBootstrap(ctx, client, activities, row, bootstrapIdentifier, logger); err != nil {
		logger.Warn().Err(err).
			Str("agent_session_id", row.LinearAgentSessionID).
			Msg("linear_agent_event: failed to re-emit bootstrap activity")
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
				models.LinearAgentSessionStateComplete, logger)
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
		emitErr := emitOnce(ctx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity, logger)
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
	agentType, err := resolveLinearAgentSessionAgentType(ctx, deps, orgID)
	if err != nil {
		return err
	}
	session := buildAgentSession(orgID, repoResult, issue, fetched, agentType)
	if err := createAndAttachLinearAgentSession(ctx, deps.Stores, orgID, row.ID, session); err != nil {
		return err
	}

	// 8. Persist the AgentSessionID on the provider-state row so
	// HandleMilestone's fan-out can find it. Idempotent (Merge).
	if err := writeAgentProviderState(ctx, deps.Stores, deps.ProviderState, orgID, session.ID, issue.ID, payload.LinearAgentSessionID, fetched); err != nil {
		return fmt.Errorf("record agent_session_id in provider state: %w", err)
	}

	// 9. Enqueue run_agent. The orchestrator picks it up and streams
	// milestones back through HandleMilestone + HandleAgentMilestone.
	// Idempotent via the run_agent:<session_id> dedupe key.
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

func registerLinearAgentCreatedDeadLetter(
	ctx context.Context,
	deps LinearAgentEventHandlerDeps,
	agentSessions *db.LinearAgentSessionStore,
	activities *db.LinearAgentActivityLogStore,
	orgID, agentSessionRowID uuid.UUID,
	linearAgentSessionID string,
	logger zerolog.Logger,
) {
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		if agentSessions == nil || activities == nil || deps.ClientForOrg == nil {
			logger.Warn().
				Str("agent_session_id", linearAgentSessionID).
				Msg("linear_agent_event: cannot close dead-lettered AgentSession; dependencies unavailable")
			return
		}
		row, err := agentSessions.GetByID(hookCtx, orgID, agentSessionRowID)
		if err != nil {
			logger.Warn().Err(err).
				Str("agent_session_id", linearAgentSessionID).
				Msg("linear_agent_event: failed to load bridge row for dead-letter close")
			return
		}
		if row.State.IsTerminal() {
			return
		}
		client, err := deps.ClientForOrg(hookCtx, orgID)
		if err != nil {
			logger.Warn().Err(err).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("linear_agent_event: failed to resolve Linear client for dead-letter close")
			if stateErr := agentSessions.SetState(hookCtx, orgID, row.ID, models.LinearAgentSessionStateError); stateErr != nil {
				logger.Warn().Err(stateErr).
					Str("agent_session_id", row.LinearAgentSessionID).
					Msg("linear_agent_event: failed to record dead-letter error state")
			}
			return
		}

		activity := linear.AgentMilestoneActivity{
			Type:            models.LinearAgentActivityError,
			Body:            "I hit an internal error before I could start the coding session. Please retry or check the 143 session logs.",
			IdemKey:         "bootstrap:failed",
			PinSessionState: "error",
		}
		if err := emitOnce(hookCtx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity, logger); err != nil {
			logger.Warn().Err(err).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("linear_agent_event: failed to emit dead-letter error activity")
		}
		if err := agentSessions.SetState(hookCtx, orgID, row.ID, models.LinearAgentSessionStateError); err != nil {
			logger.Warn().Err(err).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("linear_agent_event: failed to record dead-letter error state")
		}
		if deadLetterErr != nil {
			logger.Error().Err(deadLetterErr).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("linear_agent_event: created job dead-lettered before session creation")
		}
	})
}

func createAndAttachLinearAgentSession(ctx context.Context, stores *Stores, orgID, agentSessionRowID uuid.UUID, session *models.Session) error {
	if stores == nil || stores.Sessions == nil {
		return errors.New("sessions store unavailable")
	}
	tx, err := stores.Sessions.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin linear agent session create transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	if err := stores.Sessions.CreateInTx(ctx, tx, session); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if err := db.NewLinearAgentSessionStore(tx).AttachSession(ctx, orgID, agentSessionRowID, session.ID); err != nil {
		return fmt.Errorf("attach session to linear_agent_sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit linear agent session create transaction: %w", err)
	}
	committed = true
	return nil
}

func emitLinearAgentBootstrap(ctx context.Context, client linear.Client, activities *db.LinearAgentActivityLogStore, row *db.LinearAgentSession, issueIdentifier string, logger zerolog.Logger) error {
	if row == nil {
		return errors.New("linear agent session row unavailable")
	}
	return emitOnce(ctx, client, activities, row.OrgID, row.ID, row.LinearAgentSessionID, linear.BootstrapActivity(issueIdentifier), logger)
}

// reconcileLinearAgentCreated re-runs the idempotent tail of the created
// handler — provider-state merge + run_agent enqueue — for a bridge row
// that already has its session_id attached. Called from the step-1
// short-circuit so a retry that lost the race between AttachSession and
// the tail writes does not leave the Linear AgentSession permanently
// blind to milestone activity.
//
// Both downstream calls are idempotent:
//   - writeAgentProviderState uses LinearProviderStateStore.Merge.
//   - enqueueRunAgentForLinearAgent dedupes on run_agent:<session_id>.
//
// The function re-fetches the Linear issue because the provider-state
// Merge needs the canonical identifier/team/workspace fields and those
// aren't kept on the bridge row.
func reconcileLinearAgentCreated(ctx context.Context, deps LinearAgentEventHandlerDeps, row *db.LinearAgentSession, sessionID uuid.UUID, payload linearAgentEventPayload, logger zerolog.Logger) error {
	orgID := row.OrgID
	session, err := deps.Stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("reconcile: lookup session: %w", err)
	}
	// session is a value type, so it cannot be nil. Confirm GetByID
	// actually populated it — pgx.CollectOneRow should return an error
	// when no row matches, but a corrupted scan path (e.g. a future
	// schema change that drops a NOT NULL guarantee) could surface as a
	// zero-value session here, and dereferencing PrimaryIssueID below
	// would silently follow the "no primary issue" branch on a real
	// session that never loaded.
	if session.ID == uuid.Nil {
		return fmt.Errorf("reconcile: session %s loaded as zero-value", sessionID)
	}
	if session.PrimaryIssueID == nil {
		// Session was created without a primary issue link. Nothing on
		// the provider-state side to reconcile; just guarantee run_agent
		// is enqueued and move on.
		logger.Warn().Str("session_id", sessionID.String()).
			Msg("linear_agent_event: reconcile found no primary issue id; only re-enqueuing run_agent")
		if err := enqueueRunAgentForLinearAgent(ctx, deps.Stores, orgID, sessionID); err != nil {
			return fmt.Errorf("reconcile run_agent enqueue: %w", err)
		}
		return nil
	}

	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("reconcile: resolve linear client: %w", err)
	}
	issueIdent := row.LinearIssueIdentifier
	if issueIdent == "" {
		issueIdent = payload.LinearIssueID
	}
	fetched, err := client.FetchIssue(ctx, issueIdent)
	if err != nil {
		return fmt.Errorf("reconcile: fetch linear issue %q: %w", issueIdent, err)
	}
	if fetched == nil {
		return errors.New("reconcile: fetch linear issue returned nil")
	}
	if err := writeAgentProviderState(ctx, deps.Stores, deps.ProviderState, orgID, sessionID, *session.PrimaryIssueID, payload.LinearAgentSessionID, fetched); err != nil {
		return fmt.Errorf("reconcile provider state: %w", err)
	}
	if err := enqueueRunAgentForLinearAgent(ctx, deps.Stores, orgID, sessionID); err != nil {
		return fmt.Errorf("reconcile run_agent enqueue: %w", err)
	}
	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", sessionID.String()).
		Msg("linear_agent_event: reconciled existing session")
	return nil
}
