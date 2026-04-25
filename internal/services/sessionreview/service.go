// Package sessionreview owns the session-native "Review" turn flow: a single
// follow-up turn that asks the configured coding agent to review its own
// changes via the agent's curated review surface (Claude Code's /review
// skill, etc.).
//
// This is intentionally scoped narrowly. It does NOT model reviews as PR
// repair actions — reviews can run before a PR exists, eligibility is a
// function of session state (not PR health), and the design explicitly
// separates review semantics from PullRequestRepairAction* to keep the two
// flows from drifting into each other. See doc 63 for the rationale.
package sessionreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// ReviewModeProvider returns the review modes the configured adapter for the
// given agent type supports natively. An empty/nil result means the agent
// has no native review surface — the Review button should hide for sessions
// running that agent (no fallback prompt-based review by design).
type ReviewModeProvider func(models.AgentType) []models.SessionReviewMode

// Service owns the lifecycle of a session-native review turn.
type Service struct {
	sessions        *db.SessionStore
	sessionMessages *db.SessionMessageStore
	jobs            *db.JobStore
	reviewModes     ReviewModeProvider
	logger          zerolog.Logger
}

// Deps bundles the dependencies for constructing a Service. All fields are
// required; nil dependencies will surface as nil-pointer errors at the first
// API call rather than at construction so wiring problems don't crash boot.
type Deps struct {
	Sessions        *db.SessionStore
	SessionMessages *db.SessionMessageStore
	Jobs            *db.JobStore
	ReviewModes     ReviewModeProvider
	Logger          zerolog.Logger
}

// NewService wires a session review service. The ReviewModes provider is
// expected to be sourced from the agent package's AdapterReviewModes helper
// composed with the orchestrator's adapter map.
func NewService(deps Deps) *Service {
	return &Service{
		sessions:        deps.Sessions,
		sessionMessages: deps.SessionMessages,
		jobs:            deps.Jobs,
		reviewModes:     deps.ReviewModes,
		logger:          deps.Logger,
	}
}

// Errors callers can match on. Distinct values let HTTP handlers map them
// to specific status codes (404 vs 409 vs 400) without string comparison.
var (
	ErrSessionNotFound        = errors.New("session not found")
	ErrSessionNotResumable    = errors.New("session is not in a resumable state")
	ErrSnapshotExpired        = errors.New("session sandbox snapshot has expired")
	ErrNoChangesToReview      = errors.New("session has no changes to review")
	ErrAgentReviewUnsupported = errors.New("agent does not support native review")
	ErrReviewModeUnsupported  = errors.New("requested review mode is not supported by this agent")
)

// Capabilities reports whether the session can run a review turn now and
// which modes the agent's adapter supports natively. The Review button uses
// this to decide whether to render and what its dropdown should contain.
func (s *Service) Capabilities(ctx context.Context, orgID, sessionID uuid.UUID) (*models.SessionReviewCapabilities, error) {
	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("load session: %w", err)
	}

	// Always return a non-nil slice. A nil slice marshals to JSON `null`,
	// and the React component reads `.length` on it directly — a `null`
	// would crash every non-review-capable session detail page.
	modes := s.reviewModes(session.AgentType)
	if modes == nil {
		modes = []models.SessionReviewMode{}
	}
	caps := &models.SessionReviewCapabilities{Modes: modes}

	if len(modes) == 0 {
		caps.Reason = "this agent does not have a native review surface"
		return caps, nil
	}
	if reason, ok := sessionReviewReadiness(session); !ok {
		caps.Reason = reason
		return caps, nil
	}

	caps.CanReview = true
	return caps, nil
}

// StartReview validates the session, builds a session-scoped review revision
// context, claims the session, persists a short prompt, and enqueues a
// continue_session job. The orchestrator picks up the job and the configured
// adapter routes the turn to its native review surface.
//
// This deliberately does NOT touch PR repair tables, PR repair enums, or
// pull-request health snapshots. Reviews are session-native; if a PR exists,
// it's irrelevant to whether and how a review runs.
func (s *Service) StartReview(ctx context.Context, orgID, sessionID, userID uuid.UUID, mode models.SessionReviewMode) (*models.SessionReviewResponse, error) {
	if mode == "" {
		mode = models.SessionReviewModeDefault
	}
	// An unknown mode from the client is the same user-visible failure as
	// "this agent doesn't support this mode" — normalize so the handler
	// returns 400 instead of leaking a raw error as 500.
	if err := mode.Validate(); err != nil {
		return nil, ErrReviewModeUnsupported
	}

	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("load session: %w", err)
	}

	supportedModes := s.reviewModes(session.AgentType)
	if len(supportedModes) == 0 {
		return nil, ErrAgentReviewUnsupported
	}
	if !containsMode(supportedModes, mode) {
		return nil, ErrReviewModeUnsupported
	}
	if reason, ok := sessionReviewReadiness(session); !ok {
		// Snapshot expiry is a permanent state; surface it distinctly so
		// the API can return 410 Gone instead of a generic 409.
		if session.SandboxState == string(models.SandboxStateDestroyed) {
			return nil, ErrSnapshotExpired
		}
		if reason == reasonNoDiff {
			return nil, ErrNoChangesToReview
		}
		return nil, ErrSessionNotResumable
	}

	revisionContextJSON, err := buildReviewRevisionContext(session, mode)
	if err != nil {
		return nil, fmt.Errorf("encode review revision context: %w", err)
	}

	tx, err := s.sessions.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin session review tx: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			s.logger.Error().Err(rbErr).Str("session_id", sessionID.String()).Msg("failed to rollback session review tx")
		}
	}()

	txSessions := db.NewSessionStore(tx)
	txMessages := db.NewSessionMessageStore(tx)

	// Try claiming an idle session first, falling back to a paused/terminal
	// resume. Mirrors SendMessage semantics so the review affordance matches
	// what users already expect from "send a follow-up message".
	claimed, claimErr := txSessions.ClaimIdle(ctx, orgID, sessionID)
	if claimErr != nil {
		var resumeErr error
		claimed, resumeErr = txSessions.ClaimForResume(ctx, orgID, sessionID)
		if resumeErr != nil {
			// Both attempts failed. pgx.ErrNoRows means the session was in
			// a non-claimable state (the expected case — return 409). Any
			// other error is a real DB problem worth surfacing in logs so
			// on-call doesn't see a 409 with no signal.
			if !errors.Is(claimErr, pgx.ErrNoRows) || !errors.Is(resumeErr, pgx.ErrNoRows) {
				s.logger.Warn().
					Err(resumeErr).
					AnErr("claim_idle_err", claimErr).
					Str("session_id", sessionID.String()).
					Msg("session review claim failed")
			}
			return nil, ErrSessionNotResumable
		}
	}

	if err := txSessions.UpdateRevisionContext(ctx, orgID, claimed.ID, revisionContextJSON); err != nil {
		return nil, fmt.Errorf("persist review revision context: %w", err)
	}

	msg := &models.SessionMessage{
		SessionID:  claimed.ID,
		OrgID:      orgID,
		UserID:     &userID,
		TurnNumber: claimed.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    reviewPromptForMode(mode),
	}
	if err := txMessages.Create(ctx, msg); err != nil {
		return nil, fmt.Errorf("create review message: %w", err)
	}

	payload := map[string]string{
		"session_id": claimed.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := s.jobs.EnqueueInTx(ctx, tx, orgID, "agent", "continue_session", payload, 5, nil); err != nil {
		return nil, fmt.Errorf("enqueue continue_session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit session review: %w", err)
	}
	committed = true

	return &models.SessionReviewResponse{
		SessionID: claimed.ID,
		Mode:      mode,
	}, nil
}

const reasonNoDiff = "session has no changes yet"

// sessionReviewReadiness centralizes the "can we kick off a review turn?"
// decision so Capabilities and StartReview agree to the byte. Returns a
// human-readable reason when not ready.
//
// The snapshot-key check is load-bearing: without a snapshot, the
// orchestrator's continue_session takes the fresh-clone path which forces
// `Continuation = false`, and the Claude Code adapter's `/review` branch
// only fires when `Continuation == true`. Letting a no-snapshot session
// reach the adapter would silently degrade the review into a regular
// resumed turn against an empty workspace. Mirrors PR repair's
// canResumeRepairSession check (pr_health_service.go).
func sessionReviewReadiness(session models.Session) (string, bool) {
	if session.SandboxState == string(models.SandboxStateDestroyed) {
		return "session sandbox has expired and can no longer be resumed", false
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return "session has no snapshot to review against yet", false
	}
	switch session.Status {
	case string(models.SessionStatusRunning), string(models.SessionStatusPending):
		return "session is currently running", false
	case string(models.SessionStatusIdle),
		string(models.SessionStatusCompleted),
		string(models.SessionStatusPRCreated),
		string(models.SessionStatusFailed),
		string(models.SessionStatusCancelled),
		string(models.SessionStatusAwaitingInput),
		string(models.SessionStatusNeedsHumanGuidance):
		// resumable
	default:
		return fmt.Sprintf("session status %q is not resumable", session.Status), false
	}
	if session.Diff == nil || *session.Diff == "" {
		return reasonNoDiff, false
	}
	return "", true
}

// buildReviewRevisionContext assembles the JSON payload persisted on
// sessions.revision_context. The orchestrator parses this on the next turn
// and threads it into AgentPrompt.RevisionContext for the adapter.
func buildReviewRevisionContext(session models.Session, mode models.SessionReviewMode) ([]byte, error) {
	previousDiff := ""
	if session.Diff != nil {
		previousDiff = *session.Diff
	}
	payload := agent.RevisionContext{
		ReviewContext: &agent.SessionReviewContext{
			Mode:           mode,
			PreviousDiff:   previousDiff,
			RequestSummary: requestSummaryForMode(mode),
		},
	}
	return json.Marshal(payload)
}

// reviewPromptForMode is the short prompt persisted as a user message for
// the review turn. Adapters with native review surfaces swap this for the
// vendor slash-command at Execute time; this text is what gets stored in
// the conversation log and is also what non-review-capable adapters (which
// shouldn't be reachable here) would receive verbatim.
func reviewPromptForMode(mode models.SessionReviewMode) string {
	switch mode {
	case models.SessionReviewModeSecurity:
		return "Please run a security review on your changes."
	default:
		return "Please review your changes."
	}
}

func requestSummaryForMode(mode models.SessionReviewMode) string {
	switch mode {
	case models.SessionReviewModeSecurity:
		return "User requested an agent security review."
	default:
		return "User requested an agent review."
	}
}

func containsMode(modes []models.SessionReviewMode, mode models.SessionReviewMode) bool {
	for _, m := range modes {
		if m == mode {
			return true
		}
	}
	return false
}
