package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// CreateInput is the bounded set of inputs detection scans on the
// session-create path. Defined here (not in handlers/) so the service is
// the single source of truth for what counts as a Linear ref.
//
// LinearPrivate suppresses every downstream Linear write for the session
// (attachment, comment, state move). On the inline create path it's also
// checked here to skip the linked-milestone enqueue when the flag is true,
// avoiding one no-op job per private session. The handler-side
// short-circuit in HandleMilestone is the authoritative gate; the
// inline-path skip is a free optimization because the flag is in the
// CreateInput already. The worker (PrepareLinearPrimaryRefs) does not
// re-check the flag — see comments there.
type CreateInput struct {
	OrgID                   uuid.UUID
	SessionID               uuid.UUID
	MessageBody             string
	SessionTitle            string
	BranchName              string
	ReferenceText           string
	RepositoryID            *uuid.UUID
	UserID                  *uuid.UUID
	LinearPrivate           bool
	LinearStateSyncDisabled bool
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

// LinkRef is the worker payload shape for a detected Linear reference. It
// preserves the workspace slug from URL refs so async fallback can enforce
// the same cross-workspace drop behavior as the inline path.
type LinkRef struct {
	Identifier string `json:"identifier"`
	Workspace  string `json:"workspace,omitempty"`
}

func linkRefsFromDetected(hits []Detected) []LinkRef {
	refs := make([]LinkRef, 0, len(hits))
	for _, h := range hits {
		refs = append(refs, LinkRef{Identifier: h.Identifier, Workspace: h.Workspace})
	}
	return refs
}

func linkRefsFromIdentifiers(identifiers []string) []LinkRef {
	refs := make([]LinkRef, 0, len(identifiers))
	for _, ident := range identifiers {
		refs = append(refs, LinkRef{Identifier: ident})
	}
	return refs
}

func (r LinkRef) detected() Detected {
	source := DetectionSourceIdentifier
	if r.Workspace != "" {
		source = DetectionSourceURL
	}
	return Detected{Identifier: r.Identifier, Workspace: r.Workspace, Source: source}
}

// inlineBudget is the strict latency budget the session-create handler is
// willing to spend on Linear before falling back to the async worker. Past
// this we set linear_prepare_state=pending and let the worker drive
// resolution; turn 1 will block on prepare_state="ready".
//
// Tuned so a single unscaled Linear FetchIssue (median ~250ms) fits with
// margin, but we still give up before users feel the page hang.
const inlineBudget = 2500 * time.Millisecond

// snapshotWriteTimeout caps the detached-context window we give the post-
// link snapshot write. Detached from the request context because closing the
// browser tab between LinkResolved and the snapshot write would otherwise
// drop the cache and force turn 1 onto a live Linear fetch. We still cap so
// a wedged DB connection can't hold the goroutine forever.
const snapshotWriteTimeout = 5 * time.Second

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
	if errors.Is(err, errLinearRefDropped) {
		return CreateResult{PrepareInline: true}, nil
	}
	if err != nil || resolved == nil {
		// Couldn't resolve inline; mark the session as preparing and let the
		// worker drive the rest. Turn 1 stays gated.
		if err := s.sessions.SetLinearPrepareState(ctx, in.OrgID, in.SessionID, models.LinearPrepareStatePending); err != nil {
			return CreateResult{}, fmt.Errorf("mark linear prepare pending: %w", err)
		}
		if err := s.enqueuePrepareWorker(ctx, in, hits); err != nil {
			if stateErr := s.sessions.SetLinearPrepareState(ctx, in.OrgID, in.SessionID, models.LinearPrepareStateFailed); stateErr != nil {
				s.logger.Warn().Err(stateErr).Msg("failed to mark linear prepare failed after enqueue failure")
			}
			return CreateResult{}, fmt.Errorf("enqueue linear prepare worker: %w", err)
		}
		return CreateResult{PrepareInline: false}, nil
	}

	linkOpts := LinkOptions{AllowRepositoryMismatch: in.RepositoryID != nil}
	linkID, err := s.LinkResolvedWithOptions(ctx, in.OrgID, in.SessionID, resolved, models.SessionIssueLinkRolePrimary, 0, in.UserID, linkOpts)
	if err != nil {
		// Link write rejected (e.g. explicit repo mismatch). Skip primary
		// linking; do not block the session, but warn so audit can pick up.
		s.logger.Warn().Err(err).Str("identifier", resolved.Identifier).Msg("primary linear link rejected; skipping")
		return CreateResult{PrepareInline: true}, nil
	}

	// Snapshot the linked issue context for turn 0 so the worker doesn't
	// have to re-fetch when it boots. Detached from the request context so a
	// closed-tab cancellation between LinkResolved and the snapshot write
	// doesn't drop the cache and force turn 1 onto a live Linear fetch.
	snapshotCtx, snapshotCancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotWriteTimeout)
	if err := s.snapshotPrimaryContext(snapshotCtx, in.OrgID, linkID, resolved); err != nil {
		s.logger.Warn().Err(err).Msg("failed to snapshot linear context; turn 1 may need to refetch")
	}
	snapshotCancel()

	// Mark prepare state so the run-agent gate sees an explicit "ready"
	// instead of falling through on the default "none". A failure here is
	// non-fatal — the gate treats "none" as pass-through, so turn 1 still
	// starts with the snapshot already written above — but we log so an
	// operator chasing why a session never showed "ready" can find the
	// failure rather than guess.
	if err := s.sessions.SetLinearPrepareState(ctx, in.OrgID, in.SessionID, models.LinearPrepareStateReady); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", in.SessionID.String()).
			Str("identifier", resolved.Identifier).
			Msg("inline linear primary linked but prepare-state ready flip failed; turn 1 still starts via default-state pass-through")
	}
	// Skip the linked-milestone enqueue for private sessions: HandleMilestone
	// would short-circuit anyway, but emitting the job still produces an
	// audit row and an SSE notification we'd rather not fire for an opt-out.
	if !in.LinearPrivate {
		s.enqueueLinkedMilestone(ctx, in.OrgID, in.SessionID)
	}

	// Schedule async follow-up for additional refs (related links, mid-
	// session re-detection later).
	if len(hits) > 1 {
		// Enqueue the full hit set, not just hits[1:]. The worker treats
		// identifiers[0] as primary and the rest as related, so replaying
		// the already-linked primary first is what preserves roles.
		if err := s.enqueueLinkWorker(ctx, in, hits); err != nil {
			s.logger.Warn().Err(err).Msg("failed to enqueue related linear link worker")
		}
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
		return nil, errLinearRefDropped
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
		if errors.Is(err, ErrUnauthorized) {
			// 401 from the inline path means the org's Linear token is dead.
			// Flip the integration row to error + stamp the reason so the
			// integrations settings page surfaces a Reconnect CTA instead of
			// the user only learning about it via the per-session "prepare
			// failed" chip after the worker dead-letters. Detached context so
			// the inline budget cancellation doesn't kill the status write.
			markCtx, markCancel := context.WithTimeout(context.WithoutCancel(parent), markIntegrationStatusTimeout)
			s.MarkIntegrationUnauthorized(markCtx, orgID)
			markCancel()
		}
		s.logger.Debug().
			Dur("elapsed", elapsed).
			Err(err).
			Str("identifier", hit.Identifier).
			Str("outcome", "error").
			Msg("linear inline resolution failed")
		return nil, err
	}
}

// markIntegrationStatusTimeout caps the side-channel status flip on
// ErrUnauthorized. The write is a single short UPDATE; we still bound it so
// a wedged DB doesn't pile up goroutines on a Linear-outage retry storm.
const markIntegrationStatusTimeout = 3 * time.Second

var errLinearRefDropped = errors.New("linear ref dropped")

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
func (s *Service) enqueuePrepareWorker(ctx context.Context, in CreateInput, hits []Detected) error {
	return s.enqueueLinearWorker(ctx, in, "prepare_linear_primary", hits)
}

func (s *Service) enqueueLinkWorker(ctx context.Context, in CreateInput, hits []Detected) error {
	return s.enqueueLinearWorker(ctx, in, "link_linear_issue", hits)
}

func (s *Service) enqueueLinearWorker(ctx context.Context, in CreateInput, jobType string, hits []Detected) error {
	enqueuer := s.loadJobEnqueuer()
	if enqueuer == nil {
		return fmt.Errorf("linear job enqueuer is not configured")
	}
	identifiers := make([]string, 0, len(hits))
	for _, h := range hits {
		identifiers = append(identifiers, h.Identifier)
	}
	// Sort identifiers for the dedupe hash so re-detections that surface
	// the same set of refs in a different order (e.g. user edits message
	// text and reorders mentions) collapse to the same dedupe key. The
	// payload `identifiers` slice keeps detection order so primary
	// resolution still picks position[0] — only the hash is order-invariant.
	hashInput := make([]string, 0, len(identifiers)+1)
	hashInput = append(hashInput, identifiers...)
	sort.Strings(hashInput)
	hashInput = append(hashInput, in.SessionID.String())
	hash := SourceInputsHash(hashInput)
	dedupeKey := jobType + ":" + in.SessionID.String() + ":" + hash
	payload := map[string]any{
		"org_id":      in.OrgID.String(),
		"session_id":  in.SessionID.String(),
		"identifiers": identifiers,
		"refs":        linkRefsFromDetected(hits),
	}
	if in.RepositoryID != nil {
		payload["allow_repository_mismatch"] = true
	}
	if in.UserID != nil {
		payload["user_id"] = in.UserID.String()
	}
	if err := enqueuer(ctx, in.OrgID, jobType, payload, &dedupeKey); err != nil {
		return err
	}
	return nil
}

// JobEnqueuer is the function signature exported by the worker layer for
// scheduling jobs. We hold this as a field so the service stays decoupled
// from the JobStore.
type JobEnqueuer func(ctx context.Context, orgID uuid.UUID, jobType string, payload any, dedupeKey *string) error

// SetJobEnqueuer wires the job enqueuer. Backed by atomic.Pointer so a
// request that lands during boot — before Build's Set call returns —
// observes `nil` cleanly instead of racing on the field assignment.
func (s *Service) SetJobEnqueuer(enqueuer JobEnqueuer) {
	if enqueuer == nil {
		s.jobEnqueuer.Store(nil)
		return
	}
	s.jobEnqueuer.Store(&jobEnqueuerHolder{fn: enqueuer})
}

// loadJobEnqueuer returns the current enqueuer (or nil if none has been
// wired). Hot path on session create + every milestone fire — keeping the
// nil-check here so callers don't have to repeat it.
func (s *Service) loadJobEnqueuer() JobEnqueuer {
	holder := s.jobEnqueuer.Load()
	if holder == nil {
		return nil
	}
	return holder.fn
}

// PrepareLinearPrimary is the worker-side entry point used by the
// prepare_linear_primary job. It re-runs detection, picks the first hit as
// primary, links it, and flips linear_prepare_state to "ready" or "failed".
func (s *Service) PrepareLinearPrimary(ctx context.Context, orgID, sessionID uuid.UUID, identifiers []string, userID *uuid.UUID) error {
	return s.PrepareLinearPrimaryRefs(ctx, orgID, sessionID, linkRefsFromIdentifiers(identifiers), userID)
}

func (s *Service) PrepareLinearPrimaryRefs(ctx context.Context, orgID, sessionID uuid.UUID, refs []LinkRef, userID *uuid.UUID) error {
	return s.PrepareLinearPrimaryRefsWithOptions(ctx, orgID, sessionID, refs, userID, LinkOptions{})
}

func (s *Service) PrepareLinearPrimaryRefsWithOptions(ctx context.Context, orgID, sessionID uuid.UUID, refs []LinkRef, userID *uuid.UUID, linkOpts LinkOptions) error {
	if len(refs) == 0 {
		if err := s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateNone); err != nil {
			return fmt.Errorf("clear linear prepare state: %w", err)
		}
		return nil
	}

	resolved, err := s.ResolvePrimary(ctx, orgID, refs[0].detected())
	if errors.Is(err, ErrCrossWorkspace) {
		// Cross-workspace drops use the unguarded write because "none" is
		// the terminal "no Linear preparation needed" outcome: a sibling
		// worker that already linked successfully and reached "ready" would
		// be clobbered, but cross-workspace can only fire on a fresh session
		// where no successful sibling has run yet.
		if stateErr := s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateNone); stateErr != nil {
			return fmt.Errorf("clear linear prepare state after cross-workspace drop: %w", stateErr)
		}
		return nil
	}
	if err != nil {
		// Keep the session pending while the worker retries. The worker's
		// dead-letter hook is the single terminal writer for "failed" so a
		// transient Linear outage cannot make run_agent fail before retry
		// recovery has a chance to link the primary.
		return err
	}

	linkID, err := s.LinkResolvedWithOptions(ctx, orgID, sessionID, resolved, models.SessionIssueLinkRolePrimary, 0, userID, linkOpts)
	if err != nil {
		// Same retry contract as the resolve path above: leave the row in
		// "pending" until the prepare job is truly exhausted.
		return err
	}
	// Snapshot + identifier-hint failures are non-fatal: turn 1 can still
	// proceed without the cached blob (the agent context-builder will fall
	// back to live fetches) and branch naming will use the non-Linear slug.
	// We log explicitly here because a silent `_ =` would leave operators
	// guessing why a session marked "ready" booted without a snapshot.
	if err := s.snapshotPrimaryContext(ctx, orgID, linkID, resolved); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("identifier", resolved.Identifier).
			Msg("worker prepare_linear_primary: failed to snapshot primary context; turn 1 may need to refetch")
	}
	if err := s.sessions.SetLinearIdentifierHint(ctx, orgID, sessionID, resolved.Identifier); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("identifier", resolved.Identifier).
			Msg("worker prepare_linear_primary: failed to persist linear identifier hint; branch naming will fall back to non-linear slug")
	}

	s.linkRelatedRefs(ctx, orgID, sessionID, refs[1:], userID, linkOpts)

	if err := s.sessions.SetLinearPrepareState(ctx, orgID, sessionID, models.LinearPrepareStateReady); err != nil {
		return err
	}
	// The inline path peeks at in.LinearPrivate to skip the milestone
	// enqueue cheaply, but the worker payload doesn't carry the flag and
	// reading it here would cost an extra GetByID per prepare-job firing.
	// HandleMilestone is the authoritative gate (it short-circuits private
	// sessions before any Linear write), so the worst case of always
	// enqueueing is one dequeued no-op job — cheaper than a hot-path DB
	// round-trip on every firing.
	s.enqueueLinkedMilestone(ctx, orgID, sessionID)
	// Tell the session detail UI the session moved to "ready" so the
	// run_agent unblock + linked-issue chips appear without a manual
	// reload. Failures notifying are non-fatal.
	s.notifyLinksChanged(ctx, orgID, sessionID, "refreshed")
	return nil
}

// MarkLinearPrepareFailed is called from the prepare job's dead-letter hook
// after retry exhaustion. It is intentionally separate from
// PrepareLinearPrimaryRefs so transient worker attempts leave run_agent gated
// in "pending" instead of tripping the fatal "failed" gate immediately.
func (s *Service) MarkLinearPrepareFailed(ctx context.Context, orgID, sessionID uuid.UUID) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	return s.sessions.SetLinearPrepareStateIfNotReady(ctx, orgID, sessionID, models.LinearPrepareStateFailed)
}

func (s *Service) MarkLinearPrepareFailedWithError(ctx context.Context, orgID, sessionID uuid.UUID, message string) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	if err := s.sessions.SetLinearPrepareStateIfNotReady(ctx, orgID, sessionID, models.LinearPrepareStateFailed); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		return nil
	}
	return s.sessions.UpdateResult(ctx, orgID, sessionID, models.SessionStatusFailed, &models.SessionResult{Error: &message})
}

// LinkRelatedLinearIssues is the worker-side catch-up path after the primary
// has already been prepared inline. The payload includes the primary first so
// old workers and dedupe keys stay stable; this method intentionally skips it
// and never mutates linear_prepare_state.
func (s *Service) LinkRelatedLinearIssues(ctx context.Context, orgID, sessionID uuid.UUID, identifiers []string, userID *uuid.UUID) error {
	return s.LinkRelatedLinearRefs(ctx, orgID, sessionID, linkRefsFromIdentifiers(identifiers), userID)
}

func (s *Service) LinkRelatedLinearRefs(ctx context.Context, orgID, sessionID uuid.UUID, refs []LinkRef, userID *uuid.UUID) error {
	if len(refs) <= 1 {
		return nil
	}
	s.linkRelatedRefs(ctx, orgID, sessionID, refs[1:], userID, LinkOptions{})
	s.notifyLinksChanged(ctx, orgID, sessionID, "refreshed")
	return nil
}

func (s *Service) linkRelatedRefs(ctx context.Context, orgID, sessionID uuid.UUID, refs []LinkRef, userID *uuid.UUID, linkOpts LinkOptions) {
	for i, ref := range refs {
		related, err := s.ResolvePrimary(ctx, orgID, ref.detected())
		if err != nil {
			s.logger.Warn().Err(err).Str("identifier", ref.Identifier).Msg("failed to resolve related linear issue")
			continue
		}
		if _, err := s.LinkResolvedWithOptions(ctx, orgID, sessionID, related, models.SessionIssueLinkRoleRelated, i+1, userID, linkOpts); err != nil {
			s.logger.Warn().Err(err).Str("identifier", ref.Identifier).Msg("failed to link related linear issue")
		}
	}
}

func (s *Service) enqueueLinkedMilestone(ctx context.Context, orgID, sessionID uuid.UUID) {
	enqueuer := s.loadJobEnqueuer()
	if enqueuer == nil {
		return
	}
	dedupeKey := "linear_milestone:" + sessionID.String() + ":" + string(MilestoneLinked)
	payload := map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
		"event":      string(MilestoneLinked),
		"pr_number":  0,
	}
	if err := enqueuer(ctx, orgID, "linear_milestone", payload, &dedupeKey); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to enqueue linear linked milestone")
	}
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
