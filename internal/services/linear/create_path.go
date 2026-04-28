package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// CreateInput is the bounded set of inputs detection scans on the
// session-create path. Defined here (not in handlers/) so the service is
// the single source of truth for what counts as a Linear ref.
type CreateInput struct {
	OrgID         uuid.UUID
	SessionID     uuid.UUID
	MessageBody   string
	SessionTitle  string
	BranchName    string
	ReferenceText string
	UserID        *uuid.UUID
}

// CreateResult is what the session-create handler needs to know to finish
// the request: did we resolve the primary inline (so the run_agent worker
// may proceed immediately), and what identifier should we hint into the
// branch name and PR title pipeline.
type CreateResult struct {
	PrepareInline     bool
	PrimaryIdentifier string
	PrimaryTitle      string
}

// inlineBudget is the strict latency budget the session-create handler is
// willing to spend on Linear before falling back to the async worker. Past
// this we set linear_prepare_state=pending and let the worker drive
// resolution; turn 1 will block on prepare_state="ready".
//
// Tuned so a single unscaled Linear FetchIssue (median ~250ms) fits with
// margin, but we still give up before users feel the page hang.
const inlineBudget = 2500 * time.Millisecond

// ResolveAndLinkAtCreate is the session-create entry point. It owns:
//
//  1. Detection across the bounded inputs.
//  2. Inline primary resolution under inlineBudget; on miss, schedule the
//     prepare worker and gate run_agent on prepare_state.
//  3. Linking the primary as session_issue_links.role='primary'.
//  4. Enqueueing async link_linear_issue jobs for any additional refs.
//
// Returns silently with PrepareInline=true and an empty identifier if no
// Linear refs were detected — the caller treats that as a no-op.
func (s *Service) ResolveAndLinkAtCreate(ctx context.Context, in CreateInput) (CreateResult, error) {
	if !s.Enabled(ctx, in.OrgID) {
		return CreateResult{PrepareInline: true}, nil
	}

	allow, err := s.TeamKeyAllowlist(ctx, in.OrgID)
	if err != nil {
		return CreateResult{PrepareInline: true}, fmt.Errorf("load team key allowlist: %w", err)
	}

	hits := ScanInputs([]string{in.MessageBody, in.SessionTitle, in.ReferenceText, in.BranchName}, allow)
	if len(hits) == 0 {
		return CreateResult{PrepareInline: true}, nil
	}

	// Try inline resolution under a strict budget. Falls back to the async
	// worker if the call is slow or fails.
	primaryHit := hits[0]
	resolved, err := s.resolveWithBudget(ctx, in.OrgID, primaryHit)
	if err != nil || resolved == nil {
		// Couldn't resolve inline; mark the session as preparing and let the
		// worker drive the rest. Turn 1 stays gated.
		_ = s.sessions.SetLinearPrepareState(ctx, in.OrgID, in.SessionID, models.LinearPrepareStatePending)
		s.enqueueLinkWorker(ctx, in, hits)
		return CreateResult{PrepareInline: false}, nil
	}

	linkID, err := s.LinkResolved(ctx, in.OrgID, in.SessionID, resolved, models.SessionIssueLinkRolePrimary, 0, in.UserID)
	if err != nil {
		// Link write rejected (e.g. explicit repo mismatch). Skip primary
		// linking; do not block the session, but warn so audit can pick up.
		s.logger.Warn().Err(err).Str("identifier", resolved.Identifier).Msg("primary linear link rejected; skipping")
		return CreateResult{PrepareInline: true}, nil
	}

	// Snapshot the linked issue context for turn 0 so the worker doesn't
	// have to re-fetch when it boots.
	if err := s.snapshotPrimaryContext(ctx, in.OrgID, linkID, resolved); err != nil {
		s.logger.Warn().Err(err).Msg("failed to snapshot linear context; turn 1 may need to refetch")
	}

	// Mark prepare state so run_agent can start immediately.
	_ = s.sessions.SetLinearPrepareState(ctx, in.OrgID, in.SessionID, models.LinearPrepareStateReady)

	// Schedule async follow-up for additional refs (related links, mid-
	// session re-detection later).
	if len(hits) > 1 {
		s.enqueueLinkWorker(ctx, in, hits[1:])
	}

	return CreateResult{
		PrepareInline:     true,
		PrimaryIdentifier: resolved.Identifier,
		PrimaryTitle:      resolved.Issue.Title,
	}, nil
}

// resolveWithBudget calls ResolvePrimary under a strict timeout; on miss it
// returns (nil, nil) so the caller can switch to the async path. Errors
// other than timeout are surfaced.
//
// Emits a debug-level log line with the elapsed inline duration on every
// path (success, timeout, cross-workspace, hard error) so operators can
// observe the session-create p95 contribution from Linear without standing
// up dedicated metrics. The session-create handler runs synchronously, so
// this duration is what shows up in user-perceived latency.
func (s *Service) resolveWithBudget(parent context.Context, orgID uuid.UUID, hit Detected) (*ResolvedIssue, error) {
	ctx, cancel := context.WithTimeout(parent, inlineBudget)
	defer cancel()
	start := time.Now()
	resolved, err := s.ResolvePrimary(ctx, orgID, hit)
	elapsed := time.Since(start)
	switch {
	case err == nil:
		s.logger.Debug().
			Dur("elapsed", elapsed).
			Str("identifier", hit.Identifier).
			Str("outcome", "ok").
			Msg("linear inline resolution finished")
		return resolved, nil
	case errors.Is(err, ErrCrossWorkspace):
		// Drop silently per design; primary won't be set.
		s.logger.Debug().
			Dur("elapsed", elapsed).
			Str("identifier", hit.Identifier).
			Str("outcome", "cross_workspace").
			Msg("linear inline resolution dropped")
		return nil, nil
	case ctx.Err() != nil:
		// Budget exceeded — fall back to async path, no error to surface.
		s.logger.Debug().
			Dur("elapsed", elapsed).
			Dur("budget", inlineBudget).
			Str("identifier", hit.Identifier).
			Str("outcome", "budget_exceeded").
			Msg("linear inline resolution fell back to async")
		return nil, nil
	default:
		s.logger.Debug().
			Dur("elapsed", elapsed).
			Err(err).
			Str("identifier", hit.Identifier).
			Str("outcome", "error").
			Msg("linear inline resolution failed")
		return nil, err
	}
}

// snapshotPrimaryContext writes the turn-0 issue snapshot so the agent's
// boot path has everything without a live read. Used by both the inline
// and the async resolution paths.
func (s *Service) snapshotPrimaryContext(ctx context.Context, orgID, linkID uuid.UUID, resolved *ResolvedIssue) error {
	if s.providerState == nil {
		return nil
	}
	pkg := s.SnapshotForTurn(ctx, resolved.Issue, 8)
	raw, err := json.Marshal(pkg)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	// Persist into provider_state as `last_known_state_*` plus the
	// snapshot blob the agent context-builder reads. We don't introduce a
	// per-turn table here because session_turn_issue_snapshots already
	// covers turn 1 onwards; this is the pre-turn-0 cache.
	current, err := s.providerState.Get(ctx, orgID, linkID)
	if err != nil {
		return err
	}
	current.LastKnownStateName = resolved.Issue.StateName
	current.LastKnownStateType = resolved.Issue.StateType
	current.TeamID = resolved.Issue.TeamID
	current.PrimarySnapshot = raw
	return s.providerState.Upsert(ctx, orgID, linkID, current)
}

// enqueueLinkWorker schedules link_linear_issue for follow-up work. The
// worker dedupes on (session_id, source_inputs_hash) so re-detection is a
// no-op.
//
// user_id is forwarded so the async link path can attribute the resulting
// session_issue_links row to the human who triggered the session create.
// Without it, an inline-failure → async-catch-up fallback silently drops
// added_by_user_id, which downstream audit and "linked by you" filters
// rely on.
func (s *Service) enqueueLinkWorker(ctx context.Context, in CreateInput, hits []Detected) {
	if s.jobEnqueuer == nil {
		return
	}
	identifiers := make([]string, 0, len(hits))
	for _, h := range hits {
		identifiers = append(identifiers, h.Identifier)
	}
	// Build the hash input on a fresh slice — the bare append below would
	// alias `identifiers` whenever its underlying array still has spare
	// capacity, mutating it out from under the payload we attach to the
	// job. Allocating once with the exact length keeps the two values
	// independent.
	hashInput := make([]string, len(identifiers), len(identifiers)+1)
	copy(hashInput, identifiers)
	hashInput = append(hashInput, in.SessionID.String())
	hash := SourceInputsHash(hashInput)
	dedupeKey := "link_linear:" + in.SessionID.String() + ":" + hash
	payload := map[string]any{
		"org_id":      in.OrgID.String(),
		"session_id":  in.SessionID.String(),
		"identifiers": identifiers,
	}
	if in.UserID != nil {
		payload["user_id"] = in.UserID.String()
	}
	if err := s.jobEnqueuer(ctx, in.OrgID, "link_linear_issue", payload, &dedupeKey); err != nil {
		s.logger.Warn().Err(err).Msg("failed to enqueue link_linear_issue job")
	}
}

// JobEnqueuer is the function signature exported by the worker layer for
// scheduling jobs. We hold this as a field so the service stays decoupled
// from the JobStore.
type JobEnqueuer func(ctx context.Context, orgID uuid.UUID, jobType string, payload any, dedupeKey *string) error

// SetJobEnqueuer wires the job enqueuer. Called once at startup; not
// thread-safe afterwards.
func (s *Service) SetJobEnqueuer(enqueuer JobEnqueuer) {
	s.jobEnqueuer = enqueuer
}

// PrepareLinearPrimary is the worker-side entry point used by the
// prepare_linear_primary job. It re-runs detection, picks the first hit as
// primary, links it, and flips linear_prepare_state to "ready" or "failed".
func (s *Service) PrepareLinearPrimary(ctx context.Context, orgID, sessionID uuid.UUID, identifiers []string, userID *uuid.UUID) error {
	if len(identifiers) == 0 {
		_ = s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateNone)
		return nil
	}

	resolved, err := s.ResolvePrimary(ctx, orgID, Detected{Identifier: identifiers[0], Source: DetectionSourceIdentifier})
	if err != nil {
		_ = s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateFailed)
		return err
	}

	linkID, err := s.LinkResolved(ctx, orgID, sessionID, resolved, models.SessionIssueLinkRolePrimary, 0, userID)
	if err != nil {
		_ = s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateFailed)
		return err
	}
	_ = s.snapshotPrimaryContext(ctx, orgID, linkID, resolved)
	_ = s.sessions.SetLinearIdentifierHint(ctx, orgID, sessionID, resolved.Identifier)

	for i, ident := range identifiers[1:] {
		related, err := s.ResolvePrimary(ctx, orgID, Detected{Identifier: ident, Source: DetectionSourceIdentifier})
		if err != nil {
			s.logger.Warn().Err(err).Str("identifier", ident).Msg("failed to resolve related linear issue")
			continue
		}
		if _, err := s.LinkResolved(ctx, orgID, sessionID, related, models.SessionIssueLinkRoleRelated, i+1, userID); err != nil {
			s.logger.Warn().Err(err).Str("identifier", ident).Msg("failed to link related linear issue")
		}
	}

	if err := s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateReady); err != nil {
		return err
	}
	// Tell the session detail UI the session moved to "ready" so the
	// run_agent unblock + linked-issue chips appear without a manual
	// reload. Failures notifying are non-fatal.
	s.notifyLinksChanged(ctx, orgID, sessionID, "refreshed")
	return nil
}

// PostLink is called by HandleMilestone callers to fire the linked-event
// attachment + comment + state move in one go. Convenience wrapper.
//
// The session URL sent to Linear is built inside the service from its
// configured AppBaseURL — callers no longer pass a URL because two callers
// disagreeing on the format produced split-brain comment bodies.
func (s *Service) PostLink(ctx context.Context, session *models.Session, link models.SessionIssueLink, identifier, issueID string) {
	if session == nil {
		return
	}
	in := MilestoneInput{
		Event:      MilestoneLinked,
		Session:    session,
		Link:       link,
		IssueID:    issueID,
		IssueIdent: identifier,
	}
	if err := s.HandleMilestone(ctx, in); err != nil {
		s.logger.Warn().Err(err).Msg("linear linked-milestone write failed")
	}
	if err := s.HandleStateTransition(ctx, in); err != nil {
		s.logger.Warn().Err(err).Msg("linear linked-state transition failed")
	}
}

// HasLinearProviderState exposes "is the link backed by Linear provider state"
// to callers (the operator debug surface, the LinkedIssueCard fetcher) without
// exporting the persisted state struct.
func (s *Service) HasLinearProviderState(ctx context.Context, orgID, linkID uuid.UUID) bool {
	state, err := s.providerState.Get(ctx, orgID, linkID)
	if err != nil {
		return false
	}
	return state.AttachmentID != "" || state.CommentID != "" || state.TeamID != ""
}

// ProviderStateSnapshot returns a coarse view of the Linear provider state
// for the operator debug surface ("attachment present", "comment present",
// "last skip reason"). Strings are pre-resolved to display copy so the
// frontend doesn't have to know our enum vocabulary.
func (s *Service) ProviderStateSnapshot(ctx context.Context, orgID, linkID uuid.UUID) ProviderStateSnapshot {
	state, err := s.providerState.Get(ctx, orgID, linkID)
	if err != nil {
		return ProviderStateSnapshot{}
	}
	return ProviderStateSnapshot{
		AttachmentPresent: state.AttachmentID != "",
		CommentPresent:    state.CommentID != "",
		LastWriteOutcome:  state.LastWriteOutcome,
		LastSkippedReason: state.LastSkippedReason,
		IssueRepoStale:    state.IssueRepoStale != nil && *state.IssueRepoStale,
		LinkAuditReason:   state.LinkAuditReason,
	}
}

// ProviderStateSnapshot is the minimal view the operator debug surface
// needs.
//
// SECURITY: every field exported here MUST be a controlled-vocabulary enum
// value (LinearStateSkipReason, LinearAttachmentOutcome, etc.) or a known-
// safe string slug like a bool or audit reason code. NEVER add prompt
// text, comment bodies, or anything user-editable; the operator debug
// pane is rendered to non-admins on the session detail page and must not
// leak content that would otherwise be gated by per-user permissions.
type ProviderStateSnapshot struct {
	AttachmentPresent bool   `json:"attachment_present"`
	CommentPresent    bool   `json:"comment_present"`
	LastWriteOutcome  string `json:"last_write_outcome,omitempty"`
	LastSkippedReason string `json:"last_skipped_reason,omitempty"`
	IssueRepoStale    bool   `json:"issue_repo_stale,omitempty"`
	LinkAuditReason   string `json:"link_audit_reason,omitempty"`
}
