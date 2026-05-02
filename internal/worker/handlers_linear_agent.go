package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	LinearCommentID      string `json:"linear_comment_id"`
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
	ClientForOrg   func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	Logger         zerolog.Logger
}

// newLinearAgentEventHandler returns the worker function for
// `linear_agent_event` jobs. Returns nil when the agent stores aren't
// wired (phase 1 plumbing not yet rolled out) — registration site
// short-circuits.
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
	_ = activities // referenced below in the created path

	return func(ctx context.Context, jobType string, raw json.RawMessage) error {
		_ = jobType
		var payload linearAgentEventPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return fmt.Errorf("unmarshal linear_agent_event payload: %w", err)
		}
		switch payload.Action {
		case "created":
			// fall through — handled below
		case "prompted":
			return handleLinearAgentPrompted(ctx, deps, agentSessions, payload, logger)
		default:
			logger.Info().Str("action", payload.Action).Msg("linear_agent_event: unknown action; skipping")
			return nil
		}

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

		// 3. Fetch the live issue. The dispatcher stored the issueId
		// from the webhook envelope; we fetch the full payload here
		// because (a) labels and project metadata aren't always in the
		// webhook, (b) the issue body may have changed between dispatch
		// and worker execution, and (c) we need the team key for
		// per-team enabled gating below.
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

		// 4. Per-team enable gate. Loaded after the FetchIssue so we
		// have the canonical team key.
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

		// 5. Resolve the repo. The resolver returns a stable Source
		// string so the operator debug surface can answer "how was this
		// session's repo chosen?".
		repoResult, err := deps.RepoResolver.Resolve(ctx, linear.AgentRepoResolveInput{
			OrgID:           orgID,
			LinearTeamID:    fetched.TeamID,
			LinearProjectID: payload.LinearIssueProjectID,
			Labels:          nil, // Phase 4 enriches FetchedIssue with labels; for now we skip the override.
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
		// issues row to attach via session_issue_links. Reuse the same
		// upsert helper that powers the existing detection flow so the
		// data shape stays consistent.
		issue, err := upsertLinearIssueForAgent(ctx, deps.Stores, orgID, fetched, &repoResult.RepositoryID)
		if err != nil {
			return fmt.Errorf("upsert linear issue: %w", err)
		}

		// 7. Build the session. Origin=IssueTrigger so PM / automations
		// hooks know it's an external trigger, not a user-initiated run.
		// The PMApproach carries the issue body so the agent has the
		// context without requiring a follow-up Linear context fetch.
		session := buildAgentSession(orgID, repoResult, issue, fetched, payload.LinearAgentSessionID)
		if err := deps.Stores.Sessions.Create(ctx, session); err != nil {
			return fmt.Errorf("create session: %w", err)
		}

		// 8. Persist the AgentSessionID on the provider-state row so
		// HandleMilestone's fan-out can find it. This is what flips
		// HandleAgentMilestone from "skip" to "emit" for subsequent
		// milestones on this session.
		// The link insert was done by SessionStore.Create when
		// PrimaryIssueID was set; resolve its id back via the link
		// store to write the provider state.
		if err := writeAgentProviderState(ctx, deps.Stores, deps.ProviderState, orgID, session.ID, issue.ID, payload.LinearAgentSessionID, fetched); err != nil {
			logger.Warn().Err(err).
				Str("session_id", session.ID.String()).
				Msg("linear_agent_event: failed to record agent_session_id in provider_state; agent activities for this session will silently drop")
		}

		// 9. Update the bridge row so subsequent webhooks (prompted /
		// recovery sweeps) can find this 143 session.
		if err := agentSessions.AttachSession(ctx, orgID, row.ID, session.ID); err != nil {
			// AttachSession is the only piece that, if it fails,
			// produces a hard inconsistency between Linear and 143.
			// Surface as a job error so the worker retries. The
			// session row itself is already created — a retry will
			// fail on the SessionStore.Create UNIQUE (because session
			// has no UNIQUE we can hit), so the next attempt should
			// instead lookup-and-attach. The dispatcher's retry path
			// re-enters at step 1 above where the session_id-already-
			// set guard takes the right short-circuit.
			return fmt.Errorf("attach session to linear_agent_sessions: %w", err)
		}

		// 10. Enqueue run_agent. The orchestrator picks it up and
		// streams milestones back through HandleMilestone +
		// HandleAgentMilestone.
		if err := enqueueRunAgentForLinearAgent(ctx, deps.Stores, orgID, session.ID); err != nil {
			return fmt.Errorf("enqueue run_agent: %w", err)
		}

		logger.Info().
			Str("agent_session_id", payload.LinearAgentSessionID).
			Str("session_id", session.ID.String()).
			Str("repository_id", repoResult.RepositoryID.String()).
			Str("repo_resolution", repoResult.Source).
			Msg("linear_agent_event: session created")
		return nil
	}
}

// upsertLinearIssueForAgent persists or refreshes the 143 issues row for
// a Linear issue. Reuses the existing IssueStore semantics: source=linear,
// external_id=Linear's UUID, repository_id=resolved repo so detection-time
// joins still work. The fingerprint shape ("linear:<external_id>") matches
// services/linear/service.go upsertLinearIssue so a session that triggers
// through both the agent path and a manual paste sees a single issues row.
func upsertLinearIssueForAgent(ctx context.Context, stores *Stores, orgID uuid.UUID, fetched *linear.FetchedIssue, repoID *uuid.UUID) (*models.Issue, error) {
	now := timeNow()
	title := fetched.Title
	if fetched.Identifier != "" {
		title = fetched.Identifier + ": " + fetched.Title
	}
	desc := fetched.Description
	issue := &models.Issue{
		OrgID:           orgID,
		Source:          models.IssueSourceLinear,
		ExternalID:      fetched.ID,
		Title:           title,
		Description:     &desc,
		Status:          "open",
		FirstSeenAt:     now,
		LastSeenAt:      now,
		OccurrenceCount: 1,
		Severity:        "medium",
		Fingerprint:     "linear:" + fetched.ID,
		RepositoryID:    repoID,
	}
	if err := stores.Issues.Upsert(ctx, issue); err != nil {
		return nil, fmt.Errorf("issues upsert: %w", err)
	}
	return issue, nil
}

// buildAgentSession assembles the session row written by the worker. The
// PMApproach carries the issue body so run_agent has all the context it
// needs without re-fetching Linear data.
func buildAgentSession(orgID uuid.UUID, repo linear.AgentRepoResolveResult, issue *models.Issue, fetched *linear.FetchedIssue, agentSessionID string) *models.Session {
	primaryIssueID := issue.ID
	repoID := repo.RepositoryID
	identifier := fetched.Identifier
	title := fetched.Title
	if title == "" {
		title = identifier
	}
	approach := buildIssueApproachPrompt(fetched)
	return &models.Session{
		OrgID:                orgID,
		AgentType:            models.AgentTypeCodex,
		Status:               string(models.SessionStatusPending),
		Origin:               models.SessionOriginIssueTrigger,
		Title:                &title,
		PMApproach:           &approach,
		RepositoryID:         &repoID,
		PrimaryIssueID:       &primaryIssueID,
		LinearIdentifierHint: &identifier,
		LinearPrepareState:   models.LinearPrepareStateReady,
		InteractionMode:      models.SessionInteractionModeSingleRun,
		ValidationPolicy:     models.SessionValidationPolicyOnTurnComplete,
	}
}

// buildIssueApproachPrompt is the deterministic prompt body 143 hands the
// agent on session start. Captures issue identifier, title, description,
// and recent comments so the agent can boot without a live Linear fetch.
func buildIssueApproachPrompt(fetched *linear.FetchedIssue) string {
	if fetched == nil {
		return ""
	}
	out := fmt.Sprintf("Linear issue %s — %s\n\n%s",
		fetched.Identifier, fetched.Title, fetched.Description)
	if len(fetched.Comments) > 0 {
		out += "\n\nRecent discussion:\n"
		for _, c := range fetched.Comments {
			out += fmt.Sprintf("- %s: %s\n", c.Author, c.Body)
		}
	}
	if len(fetched.Attachments) > 0 {
		out += "\nLinked references:\n"
		for _, a := range fetched.Attachments {
			out += fmt.Sprintf("- %s — %s\n", a.Title, a.URL)
		}
	}
	return out
}

// writeAgentProviderState records the AgentSessionID against the link's
// provider_state row so HandleMilestone fan-outs can discover it. The
// link itself was inserted by SessionStore.Create when PrimaryIssueID was
// set on the session; we look it up and write the merged provider_state.
func writeAgentProviderState(ctx context.Context, stores *Stores, providerState *db.LinearProviderStateStore, orgID, sessionID, issueID uuid.UUID, agentSessionID string, fetched *linear.FetchedIssue) error {
	if stores.SessionIssueLinks == nil {
		return errors.New("session_issue_links store unavailable")
	}
	if providerState == nil {
		return errors.New("provider_state store unavailable")
	}
	links, err := stores.SessionIssueLinks.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("list session issue links: %w", err)
	}
	var primary *models.SessionIssueLink
	for i := range links {
		if links[i].IssueID == issueID && links[i].Role == models.SessionIssueLinkRolePrimary {
			primary = &links[i]
			break
		}
	}
	if primary == nil {
		return errors.New("primary link not found")
	}
	return providerState.Merge(ctx, orgID, primary.ID, db.LinearProviderState{
		Identifier:     fetched.Identifier,
		TeamID:         fetched.TeamID,
		WorkspaceSlug:  fetched.WorkspaceSlug,
		AgentSessionID: agentSessionID,
	})
}

// enqueueRunAgentForLinearAgent fires the agent orchestrator for the
// freshly-created session. Same dedupe shape as the manual path so retries
// collapse cleanly.
func enqueueRunAgentForLinearAgent(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID) error {
	dedupe := "run_agent:" + sessionID.String()
	_, err := stores.Jobs.Enqueue(ctx, orgID, "default", "run_agent", map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
	}, 5, &dedupe)
	return err
}

// finalizeUnsupported is the close-AgentSession-with-explanation path
// invoked when the team is disabled or the resolver gives up. Emits a
// `response` activity and pins state to the supplied terminal value.
func finalizeUnsupported(ctx context.Context, client linear.Client, sessions *db.LinearAgentSessionStore, activities *db.LinearAgentActivityLogStore, row *db.LinearAgentSession, orgID uuid.UUID, body string, state models.LinearAgentSessionState) error {
	activity := linear.AgentMilestoneActivity{
		Type:            models.LinearAgentActivityResponse,
		Body:            body,
		IdemKey:         "bootstrap:not_supported",
		PinSessionState: "complete",
	}
	if err := emitOnce(ctx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity); err != nil {
		return fmt.Errorf("emit close activity: %w", err)
	}
	return sessions.SetState(ctx, orgID, row.ID, state)
}

// emitOnce is a tiny wrapper that constructs an AgentActivityWriter on
// demand and calls Emit. Used by the worker for one-off close activities;
// HandleAgentMilestone uses its own writer for the milestone fan-out.
func emitOnce(ctx context.Context, client linear.Client, activities *db.LinearAgentActivityLogStore, orgID, rowID uuid.UUID, agentSessionID string, activity linear.AgentMilestoneActivity) error {
	if activities == nil {
		return errors.New("activity log store not configured")
	}
	writer := linear.NewAgentActivityWriter(client, activities, zerolog.Nop())
	_, err := writer.Emit(ctx, linear.EmitInput{
		OrgID:             orgID,
		AgentSessionRowID: rowID,
		AgentSessionID:    agentSessionID,
		Activity:          activity,
	})
	return err
}

// _ pgx errors import suppressor — kept because future expansion in this
// file may need pgx error types and removing the import would mask the
// build break until the next edit.
var _ = pgx.ErrNoRows

// timeNow is a thin alias so tests can stub it via the worker test
// helpers if they need deterministic FirstSeenAt/LastSeenAt values. Today
// it just delegates; the indirection keeps a future hook trivial.
func timeNow() time.Time {
	return time.Now().UTC()
}

// handleLinearAgentPrompted processes a `prompted` AgentSessionEvent.
// Linear delivers this when a follow-up @mention or comment lands on an
// issue whose AgentSession is already alive. We turn-append the comment
// onto the existing 143 session (or spawn a revision if the session has
// reached terminal state).
//
// Steps:
//  1. Look up the linked 143 session via the agent_session_row.
//  2. Fetch the Linear comment body so we can include it as the user
//     message on the next turn. Falls back to a placeholder if the
//     fetch fails — better to advance the session with a partial
//     message than get stuck waiting for Linear.
//  3. ClaimIdle (or ClaimForResume for terminal sessions). Idle is the
//     happy path; resumable is the one-PR-already-merged-now-the-user-
//     wants-tweaks case.
//  4. Append a session_messages row with role=user, the Linear comment
//     body as content, and a TurnNumber bumped from session.CurrentTurn.
//  5. Enqueue continue_session with the same dedupe shape the manual
//     send-message path uses.
//
// Failure model: any step's error short-circuits and returns the err
// to the worker for retry. Linear-side noise (rate limits, transient
// FetchIssue failures) is mapped to RetryableError upstream.
func handleLinearAgentPrompted(ctx context.Context, deps LinearAgentEventHandlerDeps, agentSessions *db.LinearAgentSessionStore, payload linearAgentEventPayload, logger zerolog.Logger) error {
	orgID, err := uuid.Parse(payload.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org_id: %w", err)
	}
	row, err := agentSessions.Lookup(ctx, orgID, payload.LinearAgentSessionID)
	if err != nil {
		return fmt.Errorf("lookup agent session: %w", err)
	}
	if row.SessionID == nil {
		// `created` hasn't completed yet. Linear's webhook retry will
		// re-deliver the prompted; backing off here turns the worker
		// into a busy-loop that hammers the queue.
		logger.Warn().Str("agent_session_id", payload.LinearAgentSessionID).
			Msg("prompted received but 143 session not yet created; ignoring (created handler will catch up)")
		return nil
	}
	sessionID := *row.SessionID

	// Resolve the comment body. The dispatcher captured the comment id;
	// Linear's API gives us the canonical text. If the fetch fails we
	// fall back to a deterministic placeholder so the session still
	// advances (better than getting stuck waiting on Linear).
	commentBody := ""
	if payload.LinearCommentID != "" {
		client, err := deps.ClientForOrg(ctx, orgID)
		if err != nil {
			logger.Warn().Err(err).Msg("prompted: failed to resolve linear client; using placeholder comment text")
		} else {
			fetched, ferr := fetchLinearComment(ctx, client, payload.LinearAgentSessionID, payload.LinearCommentID)
			if ferr != nil {
				logger.Warn().Err(ferr).Str("comment_id", payload.LinearCommentID).
					Msg("prompted: failed to fetch linear comment; using placeholder")
			} else {
				commentBody = fetched
			}
		}
	}
	if commentBody == "" {
		commentBody = "(Linear follow-up — see the linked issue for the full message.)"
	}

	// Claim the session for a follow-up turn. ClaimIdle is the happy
	// path; if the session has already wrapped up (PR merged + completed)
	// we fall back to ClaimForResume which lifts terminal sessions back
	// to running. The user's intent is "respond to my new message",
	// regardless of whether the prior run technically finished.
	session, err := deps.Stores.Sessions.ClaimIdle(ctx, orgID, sessionID)
	if err != nil {
		session, err = deps.Stores.Sessions.ClaimForResume(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("claim session for prompted turn: %w", err)
		}
	}

	msg := &models.SessionMessage{
		SessionID:  session.ID,
		OrgID:      orgID,
		TurnNumber: session.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    commentBody,
	}
	if deps.Stores.SessionMessages == nil {
		return errors.New("session_messages store unavailable")
	}
	if err := deps.Stores.SessionMessages.Create(ctx, msg); err != nil {
		// Best-effort revert: the session is now claimed (running) but
		// we couldn't persist the message. Falling back to whatever
		// the prior status was keeps the system from showing a "running"
		// session that nobody is actually working on.
		_ = deps.Stores.Sessions.UpdateStatus(ctx, orgID, sessionID, "idle")
		return fmt.Errorf("create session message: %w", err)
	}

	if err := enqueueContinueForLinearAgent(ctx, deps.Stores, orgID, sessionID); err != nil {
		_ = deps.Stores.Sessions.UpdateStatus(ctx, orgID, sessionID, "idle")
		return fmt.Errorf("enqueue continue_session: %w", err)
	}

	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", sessionID.String()).
		Int("turn_number", msg.TurnNumber).
		Msg("linear_agent_event: prompted -> turn appended + continue_session enqueued")
	return nil
}

// fetchLinearComment pulls the body of a single Linear comment so we can
// surface the user's follow-up message verbatim into the 143 session's
// turn message. Returns the comment body on success, an empty string on
// any failure (caller falls back to a placeholder so the session still
// advances).
func fetchLinearComment(ctx context.Context, client linear.Client, _ string, commentID string) (string, error) {
	if commentID == "" {
		return "", errors.New("comment_id is empty")
	}
	comment, err := client.FetchComment(ctx, commentID)
	if err != nil {
		return "", err
	}
	if comment == nil {
		return "", linear.ErrCommentNotFound
	}
	return comment.Body, nil
}

// enqueueContinueForLinearAgent fires continue_session for a follow-up
// Linear-driven turn. Uses the same dedupe shape as the user-driven
// path so retries collapse cleanly. Different queue ("agent" vs the
// dispatcher's "linear") because continue_session is owned by the
// agent worker bundle.
func enqueueContinueForLinearAgent(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID) error {
	dedupe := "continue_session:" + sessionID.String()
	_, err := stores.Jobs.Enqueue(ctx, orgID, "agent", "continue_session", map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
	}, 5, &dedupe)
	return err
}
