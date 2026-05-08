package linear

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// MidSessionInput is the bounded set of inputs detection scans on a follow-up
// message inside an existing session. Mid-session scope is strictly the user
// input from the current turn: the free-form message body plus any detached
// structured references submitted with that message. We still do not scan
// historic context; that would re-trigger linking on every send and surprise
// users by linking refs they never typed in this turn.
//
// AddedByUserID is forwarded so the resulting session_issue_links row attributes
// the link to the human who sent the follow-up — matching the create path's
// audit shape.
type MidSessionInput struct {
	OrgID         uuid.UUID
	SessionID     uuid.UUID
	MessageBody   string
	ReferenceText string
	UserID        *uuid.UUID
}

// ResolveAndLinkMidSession is the SendMessage entry point. It detects Linear
// refs in the follow-up message body and enqueues the
// link_linear_issue_mid_session worker for async resolution + linking.
//
// Returns silently with no error when:
//   - Linear is not enabled for the org (Path C — design 62).
//   - The message has no detectable refs.
//
// All linking is async because SendMessage already does its own DB work in a
// transaction and we don't want to add a Linear API round-trip to user-facing
// latency. The worker fires the SSE links-changed event so the composer's
// LinkedIssueChips updates without a manual reload.
func (s *Service) ResolveAndLinkMidSession(ctx context.Context, in MidSessionInput) error {
	if s == nil || !s.Enabled(ctx, in.OrgID) {
		return nil
	}
	if in.MessageBody == "" && in.ReferenceText == "" {
		return nil
	}
	allow, err := s.TeamKeyAllowlist(ctx, in.OrgID)
	if err != nil {
		return fmt.Errorf("load team key allowlist: %w", err)
	}
	inputs := make([]string, 0, 2)
	if in.MessageBody != "" {
		inputs = append(inputs, in.MessageBody)
	}
	if in.ReferenceText != "" {
		inputs = append(inputs, in.ReferenceText)
	}
	hits := ScanInputs(inputs, allow)
	if len(hits) == 0 {
		return nil
	}
	for i := range hits {
		hits[i].Source = DetectionSourceMidSession
	}
	return s.enqueueMidSessionLinkWorker(ctx, in, hits)
}

func (s *Service) enqueueMidSessionLinkWorker(ctx context.Context, in MidSessionInput, hits []Detected) error {
	enqueuer := s.loadJobEnqueuer()
	if enqueuer == nil {
		return errors.New("linear job enqueuer is not configured")
	}
	identifiers := make([]string, 0, len(hits))
	for _, h := range hits {
		identifiers = append(identifiers, h.Identifier)
	}
	// Order-invariant dedupe: a user who edits and re-sends the same set of
	// refs (or whose retry surfaces them in a different order) should not
	// produce a duplicate enqueue. Order is preserved in the payload so
	// linkRelatedRefs still walks them in detection order.
	hashInput := make([]string, 0, len(identifiers)+1)
	hashInput = append(hashInput, identifiers...)
	sort.Strings(hashInput)
	hashInput = append(hashInput, in.SessionID.String())
	hash := SourceInputsHash(hashInput)
	dedupeKey := "link_linear_issue_mid_session:" + in.SessionID.String() + ":" + hash
	payload := map[string]any{
		"org_id":      in.OrgID.String(),
		"session_id":  in.SessionID.String(),
		"identifiers": identifiers,
		"refs":        linkRefsFromDetected(hits),
	}
	if in.UserID != nil {
		payload["user_id"] = in.UserID.String()
	}
	return enqueuer(ctx, in.OrgID, "link_linear_issue_mid_session", payload, &dedupeKey)
}

// LinkMidSessionRefs is the worker-side entry point. It resolves each ref and
// links it as related — never primary, since the primary slot is reserved for
// session-create per design 62 ("primary is the issue this session is about,
// chosen at create time").
//
// Idempotent on the (session_id, issue_id) unique constraint inside
// SessionIssueLinkStore.CreateAllowingNullRepo: a re-run for the same ref is a
// no-op insert that returns the existing link id.
//
// linear_prepare_state is intentionally untouched. The create path owns that
// gate (it controls whether turn 1 may start) and a follow-up message must not
// reopen it after the agent run is already in flight.
//
// Allowlist staleness is acceptable here: the team-key gate runs in
// ScanInputs at enqueue time, not in ResolvePrimary. So a bare-id ref whose
// team prefix gets removed between enqueue and worker drain still resolves
// (against the cached issue row) and gets linked. That's the desired
// behavior — the user already saw their ref accepted; revoking the team key
// shouldn't retroactively reject in-flight follow-ups.
func (s *Service) LinkMidSessionRefs(ctx context.Context, orgID, sessionID uuid.UUID, refs []LinkRef, userID *uuid.UUID) error {
	if s == nil || len(refs) == 0 {
		return nil
	}
	startPosition := 1
	if existing, err := s.links.ListBySession(ctx, orgID, sessionID); err == nil {
		// Continue numbering after existing related links so sort order
		// reflects the order issues were linked across sessions and follow-ups.
		// Position collisions are harmless (the sort is stable on created_at as
		// a tiebreaker) but giving each new link a fresh slot keeps the UI
		// chronological.
		for _, link := range existing {
			if link.Position >= startPosition {
				startPosition = link.Position + 1
			}
		}
	}
	linked := false
	for i, ref := range refs {
		resolved, err := s.ResolvePrimary(ctx, orgID, ref.detected())
		if errors.Is(err, ErrCrossWorkspace) {
			s.logger.Debug().
				Str("identifier", ref.Identifier).
				Str("session_id", sessionID.String()).
				Msg("mid-session linear ref dropped: cross-workspace")
			continue
		}
		if err != nil {
			s.logger.Warn().Err(err).
				Str("identifier", ref.Identifier).
				Str("session_id", sessionID.String()).
				Msg("failed to resolve mid-session linear issue")
			continue
		}
		if _, err := s.LinkResolved(ctx, orgID, sessionID, resolved, models.SessionIssueLinkRoleRelated, startPosition+i, userID); err != nil {
			s.logger.Warn().Err(err).
				Str("identifier", ref.Identifier).
				Str("session_id", sessionID.String()).
				Msg("failed to link mid-session linear issue")
			continue
		}
		linked = true
	}
	if linked {
		// LinkResolved already emits an "inserted" notification per ref, but a
		// terminal "refreshed" lets clients that buffer mid-stream events know
		// the worker batch is complete and they can stop showing a spinner.
		s.notifyLinksChanged(ctx, orgID, sessionID, "refreshed")
	}
	return nil
}
