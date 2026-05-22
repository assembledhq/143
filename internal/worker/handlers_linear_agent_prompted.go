package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// promptedAwaitingCreateBackoff is the fixed wait used when a `prompted`
// event lands before its sibling `created` handler has finished attaching
// the 143 session. Linear delivered the prompted webhook to us and we
// already 200'd the dispatcher, so Linear won't redeliver — the only way
// the prompted survives is for the worker to retry. Picked to be long
// enough that the typical created path (FetchIssue + session create + a
// few inserts) completes, short enough that human-perceived latency on
// the follow-up comment stays bounded.
var promptedAwaitingCreateBackoff = 5 * time.Second

// promptedAwaitingCreateDeadline bounds how long the worker will retry a
// prompted event waiting on `created` before giving up. The
// RetryableError contract does not consume the worker's Attempts counter
// (worker.go preserveAttempts), so without an explicit deadline a prompted
// job that never sees its sibling `created` would loop until the global
// maxRetryableDuration (8 min) dead-letters it — silent to the user on
// the Linear side. Two minutes is long enough to absorb out-of-order
// webhook delivery and slow created paths (Linear API + session
// bootstrapping) while still surfacing a real "stuck" condition to the
// operator before the dead-letter sweep.
var promptedAwaitingCreateDeadline = 2 * time.Minute

// promptedAppendRaceBackoff is the short retry used when a prompted event
// initially observes a running session, but the turn finishes before the
// handler can append under the row lock. Retrying re-enters the normal
// idle/resume path and atomically enqueues continue_session.
var promptedAppendRaceBackoff = 500 * time.Millisecond

var errPromptedMessageAlreadyProcessed = errors.New("linear_agent_event: prompted comment already processed")

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
		if errors.Is(err, db.ErrLinearAgentSessionNotFound) {
			if promptedDeadlineExceeded(ctx) {
				logger.Error().Str("agent_session_id", payload.LinearAgentSessionID).
					Dur("deadline", promptedAwaitingCreateDeadline).
					Msg("linear_agent_event: prompted gave up waiting for created event; treating as terminal")
				// No bridge row exists (created never arrived). We can't
				// dedupe via activity_log without a row id, so we just
				// drop the job and rely on operator visibility through the
				// logged error. Returning nil marks the job complete so
				// it doesn't continue to retry — Linear already 200'd at
				// dispatcher time so there's no Linear-side resend.
				return nil
			}
			logger.Warn().Str("agent_session_id", payload.LinearAgentSessionID).
				Msg("prompted received before created row exists; retrying shortly")
			return &RetryableError{
				Err:        errors.New("linear_agent_event: prompted arrived before created handler recorded session row"),
				RetryAfter: &promptedAwaitingCreateBackoff,
			}
		}
		return fmt.Errorf("lookup agent session: %w", err)
	}
	if row.SessionID == nil {
		if promptedDeadlineExceeded(ctx) {
			logger.Error().Str("agent_session_id", payload.LinearAgentSessionID).
				Str("row_id", row.ID.String()).
				Dur("deadline", promptedAwaitingCreateDeadline).
				Msg("linear_agent_event: prompted gave up waiting for created handler to attach session_id; emitting Linear error activity")
			if emitErr := emitPromptedAwaitingCreatedTimeout(ctx, deps, agentSessions, row, orgID, logger); emitErr != nil {
				logger.Warn().Err(emitErr).Str("row_id", row.ID.String()).
					Msg("prompted: failed to emit timeout error activity; dropping job anyway")
			}
			return nil
		}
		// `created` hasn't completed yet. The dispatcher already 200'd
		// Linear so Linear won't redeliver — only the worker's retry
		// loop can keep this prompted alive. Return a retryable error
		// with a fixed short wait so we don't busy-loop or fall into
		// exponential backoff that would defer the follow-up turn for
		// minutes.
		logger.Warn().Str("agent_session_id", payload.LinearAgentSessionID).
			Msg("prompted received but 143 session not yet created; retrying shortly")
		return &RetryableError{
			Err:        errors.New("linear_agent_event: prompted arrived before created handler attached session_id"),
			RetryAfter: &promptedAwaitingCreateBackoff,
		}
	}
	sessionID := *row.SessionID

	commentBody, err := resolvePromptedCommentBody(ctx, deps, payload, orgID, logger)
	if err != nil {
		// Transient Linear error (rate limit, 5xx, transient unauthorized).
		// Surface the retryable error so the worker reschedules instead of
		// silently advancing the session with a placeholder while Linear is
		// flapping.
		return err
	}
	allowRevision := true
	if deps.SettingsLoader != nil {
		settings, err := deps.SettingsLoader(ctx, orgID)
		if err != nil {
			return fmt.Errorf("load agent settings: %w", err)
		}
		// Do not re-apply EnabledFor(team) on prompted events. The created
		// event already passed the per-team gate; follow-up @143 mentions are
		// treated as part of that ongoing session so disabling a team does not
		// strand in-flight work. The revision knob below still controls whether
		// late prompts may reopen terminal sessions.
		allowRevision = settings.EffectiveAllowRevisionPerPrompt()
	}

	// Claim the session for a follow-up turn. ClaimIdle is the happy
	// path; if the session has already wrapped up (PR merged + completed)
	// we fall back to ClaimForResume which lifts terminal sessions back
	// to running. The user's intent is "respond to my new message",
	// regardless of whether the prior run technically finished.
	revertStatus := models.SessionStatusIdle
	session, err := deps.Stores.Sessions.ClaimIdle(ctx, orgID, sessionID)
	if err != nil {
		appendState, stateErr := deps.Stores.Sessions.GetMessageAppendState(ctx, orgID, sessionID)
		if stateErr == nil && appendState.Status == models.SessionStatusRunning {
			err := appendPromptedMessageToRunningSession(ctx, deps, orgID, row.ID, appendState, payload, commentBody, logger)
			if errors.Is(err, errPromptedMessageAlreadyProcessed) {
				return nil
			}
			return err
		}
		if stateErr != nil {
			return fmt.Errorf("inspect session append state for prompted turn: %w", stateErr)
		}
		if !allowRevision {
			if err := respondRevisionPromptDisabled(ctx, deps, agentSessions, row, orgID, logger); err != nil {
				return err
			}
			logger.Info().
				Str("agent_session_id", payload.LinearAgentSessionID).
				Str("session_id", sessionID.String()).
				Msg("linear_agent_event: prompted ignored because revisions are disabled")
			return nil
		}
		session, err = deps.Stores.Sessions.ClaimForResume(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("claim session for prompted turn: %w", err)
		}
		revertStatus = appendState.Status
	}

	if deps.Stores.SessionMessages == nil {
		return errors.New("session_messages store unavailable")
	}
	msg := &models.SessionMessage{
		SessionID:  session.ID,
		OrgID:      orgID,
		TurnNumber: session.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    commentBody,
	}
	if err := appendPromptedMessageAndEnqueueContinue(ctx, deps.Stores, orgID, row.ID, payload.LinearCommentID, session, msg); err != nil {
		// Best-effort revert: the session is now claimed (running) but
		// we couldn't atomically persist the message and queue the
		// continuation. Restore the pre-claim status so terminal or paused
		// sessions do not get corrupted to idle.
		if updateErr := deps.Stores.Sessions.UpdateStatus(ctx, orgID, sessionID, revertStatus); updateErr != nil {
			logger.Warn().Err(updateErr).Str("session_id", sessionID.String()).
				Msg("prompted: failed to revert session status after append/enqueue failure")
		}
		if errors.Is(err, errPromptedMessageAlreadyProcessed) {
			return nil
		}
		return err
	}

	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", sessionID.String()).
		Int("turn_number", msg.TurnNumber).
		Msg("linear_agent_event: prompted -> turn appended + continue_session enqueued")
	return nil
}

// promptedDeadlineExceeded returns true when the worker job has been
// retrying for longer than promptedAwaitingCreateDeadline. The job's
// CreatedAt comes from jobctx; in tests / direct callers without a job
// context the function returns false so the legacy retry-forever behavior
// stays intact for paths that explicitly opt out.
func promptedDeadlineExceeded(ctx context.Context) bool {
	createdAt, ok := jobctx.JobCreatedAtFromContext(ctx)
	if !ok {
		return false
	}
	return time.Since(createdAt) > promptedAwaitingCreateDeadline
}

// emitPromptedAwaitingCreatedTimeout closes a stuck AgentSession with an
// error activity so the Linear user sees that we never managed to attach
// the 143 session and won't be retrying. Best-effort: any failure is
// logged by the caller; we never want a timeout-cleanup hiccup to keep
// the job alive past its deadline.
func emitPromptedAwaitingCreatedTimeout(ctx context.Context, deps LinearAgentEventHandlerDeps, agentSessions *db.LinearAgentSessionStore, row *db.LinearAgentSession, orgID uuid.UUID, logger zerolog.Logger) error {
	if deps.Linear == nil || deps.Linear.AgentActivityStore() == nil || deps.ClientForOrg == nil {
		return errors.New("linear activity surface not configured")
	}
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve linear client for prompted-timeout: %w", err)
	}
	activity := linear.AgentMilestoneActivity{
		Type:            models.LinearAgentActivityError,
		Body:            "We didn't receive a session-start event from Linear in time to handle this follow-up. Re-assign the issue to @143 to try again.",
		IdemKey:         "prompted:awaiting_created_timeout",
		PinSessionState: string(models.LinearAgentSessionStateError),
	}
	if err := emitOnce(ctx, client, deps.Linear.AgentActivityStore(), orgID, row.ID, row.LinearAgentSessionID, activity, logger); err != nil {
		return fmt.Errorf("emit prompted timeout error activity: %w", err)
	}
	return agentSessions.SetState(ctx, orgID, row.ID, models.LinearAgentSessionStateError)
}

func appendPromptedMessageAndEnqueueContinue(ctx context.Context, stores *Stores, orgID, agentSessionRowID uuid.UUID, linearCommentID string, session models.Session, msg *models.SessionMessage) error {
	if stores == nil || stores.Sessions == nil {
		return errors.New("sessions store unavailable")
	}
	if stores.SessionMessages == nil {
		return errors.New("session_messages store unavailable")
	}
	if stores.Jobs == nil {
		return errors.New("jobs store unavailable")
	}
	tx, err := stores.Sessions.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin prompted append transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	inserted, err := db.NewLinearAgentPromptedMessageStore(tx).Reserve(ctx, orgID, agentSessionRowID, session.ID, linearCommentID)
	if err != nil {
		return err
	}
	if !inserted {
		return errPromptedMessageAlreadyProcessed
	}
	if err := db.NewSessionMessageStore(tx).Create(ctx, msg); err != nil {
		return fmt.Errorf("create session message: %w", err)
	}
	jobID, err := enqueueContinueForLinearAgentInTx(ctx, stores.Jobs, tx, orgID, session)
	if err != nil {
		return fmt.Errorf("enqueue continue_session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit prompted append transaction: %w", err)
	}
	committed = true
	stores.Jobs.Notify(ctx, jobID)
	return nil
}

// respondRevisionPromptDisabled posts a "this session has ended and
// revisions are disabled" response to Linear when a follow-up @143 prompt
// lands on a terminal session that the org has opted out of reopening.
//
// Idempotency note: the idem key is "prompted:revision_disabled" keyed to
// the bridge row id, so the response only emits ONCE per AgentSession,
// even if the user pings @143 multiple times on the same closed session.
// This is intentional — telling the user the same thing on every ping
// would be noise; the first response is the user-actionable signal and
// subsequent pings silently no-op via the Reserve UNIQUE collision. The
// design table specifies "response per event" but this concrete tradeoff
// (UX > strict per-event spec) is the right call for a static close
// message. If we ever change the message to carry per-prompt context,
// include the comment id in the idem key.
func respondRevisionPromptDisabled(ctx context.Context, deps LinearAgentEventHandlerDeps, agentSessions *db.LinearAgentSessionStore, row *db.LinearAgentSession, orgID uuid.UUID, logger zerolog.Logger) error {
	if deps.Linear == nil || deps.Linear.AgentActivityStore() == nil || deps.ClientForOrg == nil {
		return nil
	}
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve linear client for disabled revision prompt: %w", err)
	}
	activity := linear.AgentMilestoneActivity{
		Type:            models.LinearAgentActivityResponse,
		Body:            "This @143 session has already ended, and this workspace has disabled automatic revisions from late prompts.",
		IdemKey:         "prompted:revision_disabled",
		PinSessionState: "complete",
	}
	if err := emitOnce(ctx, client, deps.Linear.AgentActivityStore(), orgID, row.ID, row.LinearAgentSessionID, activity, logger); err != nil {
		return fmt.Errorf("emit disabled revision prompt response: %w", err)
	}
	return agentSessions.SetState(ctx, orgID, row.ID, models.LinearAgentSessionStateComplete)
}

// appendPromptedMessageToRunningSession inserts a session_messages row for a
// follow-up @143 prompt that arrived while the prior turn is still running.
// It deliberately does NOT enqueue continue_session — the in-flight turn's
// finisher does that.
//
// Drain contract (must stay in lockstep with the finisher):
//
//   - The orchestrator runs drainQueuedMessagesAfterProcessedID at the end of
//     every turn (services/agent/orchestrator.go:3451) and also defensively
//     in RunAgent's deferred path (orchestrator.go:1917). The drain lists
//     all session_messages, picks user-role rows with id > processedMessageID,
//     and enqueues continue_session with the drain-specific dedupe key.
//   - We hold SELECT ... FOR UPDATE on the sessions row before the INSERT, so
//     either (a) we win and the message lands before the finisher's drain
//     reads it, or (b) the finisher already advanced the session past running
//     and we observe a non-running status — in which case we return
//     RetryableError and the worker re-enters the normal idle/resume branch
//     above, which DOES enqueue continue_session itself.
//
// Net effect: the message never gets stranded — either the finisher's drain
// picks it up, or the retry path's claim-idle/claim-for-resume runs the
// normal append + enqueue.
func appendPromptedMessageToRunningSession(
	ctx context.Context,
	deps LinearAgentEventHandlerDeps,
	orgID uuid.UUID,
	agentSessionRowID uuid.UUID,
	appendState db.SessionMessageAppendState,
	payload linearAgentEventPayload,
	commentBody string,
	logger zerolog.Logger,
) error {
	if deps.Stores == nil || deps.Stores.Sessions == nil {
		return errors.New("sessions store unavailable")
	}
	if deps.Stores.SessionMessages == nil {
		return errors.New("session_messages store unavailable")
	}
	tx, err := deps.Stores.Sessions.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin running-session prompted append transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	lockedState, err := deps.Stores.Sessions.LockMessageAppendState(ctx, tx, orgID, appendState.ID)
	if err != nil {
		return fmt.Errorf("lock running session append state: %w", err)
	}
	if lockedState.Status != models.SessionStatusRunning {
		return &RetryableError{
			Err:        fmt.Errorf("linear_agent_event: prompted append raced with session status %q", lockedState.Status),
			RetryAfter: &promptedAppendRaceBackoff,
		}
	}
	inserted, err := db.NewLinearAgentPromptedMessageStore(tx).Reserve(ctx, orgID, agentSessionRowID, lockedState.ID, payload.LinearCommentID)
	if err != nil {
		return err
	}
	if !inserted {
		return errPromptedMessageAlreadyProcessed
	}

	msg := &models.SessionMessage{
		SessionID:  lockedState.ID,
		OrgID:      orgID,
		TurnNumber: lockedState.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    commentBody,
	}
	if err := db.NewSessionMessageStore(tx).Create(ctx, msg); err != nil {
		return fmt.Errorf("create running session message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit running-session prompted append transaction: %w", err)
	}
	committed = true
	logger.Info().
		Str("agent_session_id", payload.LinearAgentSessionID).
		Str("session_id", lockedState.ID.String()).
		Int("turn_number", msg.TurnNumber).
		Msg("linear_agent_event: prompted -> message appended to running session")
	return nil
}

// resolvePromptedCommentBody fetches the user's follow-up Linear comment
// so we can surface it verbatim into the 143 session's next turn.
//
// Errors are split into two buckets:
//
//   - Transient (rate limit, transient unauthorized, 5xx, network). The
//     function returns a retryable error so the worker reschedules. We
//     don't want to advance the session with a placeholder while Linear is
//     flapping — the user typed a real message and a placeholder corrupts
//     the conversation.
//   - Permanent (comment deleted, no client wired, empty id). Falls back
//     to a deterministic placeholder so the session can advance. The
//     placeholder is detectable in operator logs via the original error.
func resolvePromptedCommentBody(ctx context.Context, deps LinearAgentEventHandlerDeps, payload linearAgentEventPayload, orgID uuid.UUID, logger zerolog.Logger) (string, error) {
	const placeholder = "(Linear follow-up — see the linked issue for the full message.)"
	if payload.LinearPromptBody != "" {
		return payload.LinearPromptBody, nil
	}
	if payload.LinearCommentID == "" {
		return placeholder, nil
	}
	client, err := deps.ClientForOrg(ctx, orgID)
	if err != nil {
		// Treat client-resolution errors as permanent for this attempt —
		// they typically indicate a missing token or decryption failure
		// and retrying right away won't help.
		logger.Warn().Err(err).Msg("prompted: failed to resolve linear client; using placeholder comment text")
		return placeholder, nil
	}
	body, err := fetchLinearComment(ctx, client, payload.LinearCommentID)
	if err != nil {
		if linearFetchErrorIsTransient(err) {
			logger.Warn().Err(err).Str("comment_id", payload.LinearCommentID).
				Msg("prompted: transient linear failure fetching comment; deferring turn")
			retryAfter := linearTransientRetryAfter(err)
			return "", &RetryableError{
				Err:        fmt.Errorf("fetch linear comment: %w", err),
				RetryAfter: retryAfter,
			}
		}
		logger.Warn().Err(err).Str("comment_id", payload.LinearCommentID).
			Msg("prompted: permanent linear failure fetching comment; using placeholder")
		return placeholder, nil
	}
	if body == "" {
		return placeholder, nil
	}
	return body, nil
}

// linearFetchErrorIsTransient classifies a Linear client error so we can
// distinguish "Linear is flapping, retry" from "the comment is gone, fall
// back". 5xx is treated as transient via the generic "linear API returned"
// substring — the client wraps non-200 responses with that prefix.
//
// ErrUnauthorized is deliberately NOT transient. A 401 means the token is
// rejected and only an admin re-authorize can fix it; retrying produces the
// same failure forever and would stall every prompted job from this org
// until manual intervention. Treating it as terminal lets the handler fall
// through to the placeholder branch (session advances with a hint) and
// surfaces the issue to the operator via the existing
// linear.MarkIntegrationUnauthorized path on the next live Linear call.
// The worker's RetryableError contract does not consume the Attempts
// counter, so an infinite-retry classification here would never hit
// MaxAttempts — terminal-on-401 is the only bound.
func linearFetchErrorIsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, linear.ErrCommentNotFound) {
		return false
	}
	if errors.Is(err, linear.ErrUnauthorized) {
		return false
	}
	var rl *linear.RateLimitError
	if errors.As(err, &rl) {
		return true
	}
	// Network errors (DNS, connect, EOF) and 5xx responses both surface
	// here without a sentinel — assume transient. Permanent 4xx other than
	// 401 / 404 are rare on FetchComment; safest to retry once and let the
	// worker's retry budget bound the cost.
	return true
}

// linearTransientRetryAfter pulls a Retry-After hint off Linear's rate-
// limit error when present. Falls back to nil so the worker uses its
// default backoff curve.
func linearTransientRetryAfter(err error) *time.Duration {
	var rl *linear.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter == "" {
		return nil
	}
	if secs, parseErr := time.ParseDuration(rl.RetryAfter + "s"); parseErr == nil && secs > 0 {
		return &secs
	}
	return nil
}

// fetchLinearComment pulls the body of a single Linear comment so we can
// surface the user's follow-up message verbatim into the 143 session's
// turn message. Returns the comment body on success, an empty string on
// any failure (caller falls back to a placeholder so the session still
// advances).
func fetchLinearComment(ctx context.Context, client linear.Client, commentID string) (string, error) {
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
func enqueueContinueForLinearAgent(ctx context.Context, stores *Stores, orgID uuid.UUID, session models.Session) error {
	if stores == nil || stores.Jobs == nil {
		return errors.New("jobs store unavailable")
	}
	_, err := stores.Jobs.EnqueueWithOpts(ctx, orgID, continueLinearAgentEnqueueOpts(orgID, session))
	return err
}

func enqueueContinueForLinearAgentInTx(ctx context.Context, jobs *db.JobStore, tx pgx.Tx, orgID uuid.UUID, session models.Session) (uuid.UUID, error) {
	if jobs == nil {
		return uuid.Nil, errors.New("jobs store unavailable")
	}
	return jobs.EnqueueInTxWithOpts(ctx, tx, orgID, continueLinearAgentEnqueueOpts(orgID, session))
}

func continueLinearAgentEnqueueOpts(orgID uuid.UUID, session models.Session) db.EnqueueOpts {
	dedupe := db.ContinueSessionDedupeKey(session.ID)
	return db.EnqueueOpts{
		Queue:   "agent",
		JobType: "continue_session",
		Payload: map[string]any{
			"org_id":     orgID.String(),
			"session_id": session.ID.String(),
		},
		Priority:     5,
		DedupeKey:    &dedupe,
		TargetNodeID: models.SessionWorkerTarget(&session),
	}
}
