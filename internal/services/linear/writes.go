package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// MilestoneEvent names a session milestone that should be reflected back to
// Linear. These map to the AttachmentSubtitle table in design 62 §"The
// attachment is the durable handle".
type MilestoneEvent string

const (
	MilestoneLinked    MilestoneEvent = "linked"
	MilestonePROpened  MilestoneEvent = "pr_opened"
	MilestonePRMerged  MilestoneEvent = "pr_merged"
	MilestoneEndedNoPR MilestoneEvent = "ended_no_pr"
	MilestoneFailed    MilestoneEvent = "failed"
)

// MilestoneInput is the mutation request used by HandleMilestone. Callers
// pass the session and link in the same shape regardless of the milestone;
// the service decides whether to hit attachmentCreate vs attachmentUpdate
// and whether to commentCreate vs commentUpdate based on persisted state.
//
// The session deep-link sent to Linear is built by the service via
// SessionURL(session.ID); callers must not pass URLs in. This is what keeps
// the attachment URL and the comment body in sync, and ensures we never
// post a relative path that Linear renders as plain text.
type MilestoneInput struct {
	Event      MilestoneEvent
	Session    *models.Session
	Link       models.SessionIssueLink
	IssueID    string // Linear (external) issue id
	PRNumber   int    // 0 when not applicable
	IssueIdent string // e.g. "ACS-1234"
}

// botCommentPrefix is mandatory: the comment is authored by the integration
// installer's account on most Linear plans (no true bot identity yet — see
// design 62 §"Authoring identity"), so readers need an unambiguous "this is
// a bot voice" marker. Required by design.
const botCommentPrefix = "🤖 143 automated update —"

// HandleMilestone idempotently writes the durable attachment and the single
// rolling comment for the given milestone. Skipped silently when:
//
//   - the org has Linear disabled or the integration is missing
//   - the session opted into "Private" mode (LinearPrivate=true)
//   - the linked issue is `related` (only `primary` drives lifecycle)
//   - org/team automation flags say "don't post session links to Linear"
//
// Authorization: writes are made with the org-level integration token, not
// the requesting user's token. The Service-type doc comment explains the
// access model — in short, any user who can create a linking session can
// trigger these writes, and admins gate by toggling automation flags org-
// wide or per-team rather than per-user.
//
// Workspace scoping: this method does not pre-fetch the issue to verify
// that the integration's token still authorizes against the issue's
// workspace. The Linear API enforces this implicitly — a token for
// workspace B has no access to an issue in workspace A, so attachmentCreate
// or commentCreate against a cross-workspace issue ID returns a
// permission error, which surfaces here as a write failure (no silent
// cross-workspace write happens). HandleStateTransition does an explicit
// state-fetch and slug-compare for the same drift, so audited skip events
// flow through that path; the milestone path relies on the API's own
// authorization to fail closed.
//
// Idempotency: AttachmentID and CommentID in provider_state act as the
// dedupe anchors. Re-running this with the same milestone is a no-op modulo
// Linear-side metadata refresh.
//
// Race-safety: the rolling comment is created at most once per link even
// when two distinct milestone events (e.g. `linked` from the create path
// and `pr_opened` from the GitHub webhook) race. The provider-state row is
// locked with SELECT ... FOR UPDATE for the duration of the
// CreateComment/CreateOrUpdateAttachment calls so the loser observes the
// winner's CommentID and takes the update branch instead of posting a
// duplicate. The job-level dedupe key on (session_id, event) only collapses
// replays of the same event — it does not prevent two different events from
// both seeing CommentID == "", which is what the row lock here addresses.
func (s *Service) HandleMilestone(ctx context.Context, in MilestoneInput) error {
	if in.Session == nil {
		return fmt.Errorf("nil session")
	}
	if in.Link.Role != models.SessionIssueLinkRolePrimary {
		// Related issues never get attachments or comments in v1.
		return nil
	}
	if in.Session.LinearPrivate {
		_ = s.providerState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
			LastSkippedReason: string(db.LinearStateSkipPrivateSession),
		})
		return nil
	}

	teamKey := teamKeyFromIdentifier(in.IssueIdent)
	if !s.shouldPostSessionLinks(ctx, in.Session.OrgID, teamKey) {
		return nil
	}

	_, token, err := s.integrationFor(ctx, in.Session.OrgID)
	if err != nil {
		return err
	}
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}

	outcome := outcomeForMilestone(in.Event)
	subtitle := subtitleForMilestone(in.Event, in.PRNumber)
	title := attachmentTitle(in.Session)
	sessionURL := s.SessionURL(in.Session.ID)
	body := commentBodyForMilestone(in.Event, in.IssueIdent, sessionURL, in.PRNumber)

	// Durable handles captured from successful API calls. If the locked tx
	// later fails to commit, we replay these via a non-tx Merge so a retry
	// observes the live CommentID/AttachmentID and takes the update branch
	// rather than calling commentCreate/attachmentCreate a second time. The
	// Linear API has no client-supplied idempotency key on commentCreate so
	// this best-effort rescue is the closest we can get to outbox semantics
	// without a side table.
	//
	// Attachments are idempotent on (issueID, url) at the Linear API level
	// — passing the same sessionURL returns the existing attachment — so
	// the duplicate-post risk is comment-only. For comments we mitigate
	// the lost-response zone (Linear write succeeded server-side but our
	// response was lost) via FindRecentBotCommentByURL: when state has no
	// recorded CommentID we scan recent comments for our session-URL
	// signature before issuing commentCreate. This adds one extra GraphQL
	// call per link's first milestone — every subsequent milestone takes
	// the UpdateComment branch and skips the scan — so the lifetime cost
	// is one extra round-trip per session-link, not per milestone.
	//
	// Residual risk: if the scan itself fails (rate limit, transient API
	// error) we log and fall through to commentCreate, which may double-
	// post on a lost-response retry. Operators chasing this can delete
	// the older comment in Linear; the AttachmentID we record is the
	// latest write so future updates flow to the right object.
	var rescue db.LinearProviderState
	hasRescue := false

	txErr := s.withProviderStateLocked(ctx, in.Session.OrgID, in.Link.ID,
		func(ctx context.Context, txState providerStateStore, _ stateEventStore, state db.LinearProviderState) error {
			attachmentResult, err := client.CreateOrUpdateAttachment(ctx, AttachmentWriteInput{
				IssueID:  in.IssueID,
				PriorID:  state.AttachmentID,
				Title:    title,
				Subtitle: subtitle,
				URL:      sessionURL,
				Metadata: db.LinearAttachmentMetadata{
					Service:   "143",
					SessionID: in.Session.ID.String(),
					Primary:   true,
					Outcome:   string(outcome),
				},
			})
			if err != nil {
				return fmt.Errorf("write linear attachment: %w", err)
			}
			if attachmentResult.ID != "" && attachmentResult.ID != state.AttachmentID {
				rescue.AttachmentID = attachmentResult.ID
				rescue.AttachmentURL = attachmentResult.URL
				hasRescue = true
			}

			if state.CommentID == "" {
				// Recovery scan for the lost-response zone: a prior attempt
				// may have created a comment server-side that we never
				// observed. Linear has no idempotency key on commentCreate,
				// so we look for a comment whose body contains our
				// deterministic session URL before issuing a fresh create.
				// Best-effort: a scan failure is logged and we fall through
				// to commentCreate (worst case is a duplicate, same as
				// before this recovery existed).
				orphan, scanErr := client.FindRecentBotCommentByURL(ctx, in.IssueID, sessionURL)
				if scanErr != nil {
					s.logger.Warn().Err(scanErr).
						Str("link_id", in.Link.ID.String()).
						Msg("linear comment-recovery scan failed; proceeding with commentCreate (may double-post on lost-response retry)")
				}
				if orphan != "" {
					s.logger.Info().
						Str("link_id", in.Link.ID.String()).
						Str("recovered_comment_id", orphan).
						Msg("linear comment-recovery: found orphaned comment from prior attempt; updating in place")
					if err := client.UpdateComment(ctx, orphan, body); err != nil {
						return fmt.Errorf("update recovered linear comment: %w", err)
					}
					state.CommentID = orphan
					rescue.CommentID = orphan
					hasRescue = true
				} else {
					// First write — create. Subsequent milestones update in
					// place to avoid notification fatigue.
					commentID, err := client.CreateComment(ctx, in.IssueID, body)
					if err != nil {
						return fmt.Errorf("create linear comment: %w", err)
					}
					state.CommentID = commentID
					rescue.CommentID = commentID
					hasRescue = true
				}
			} else {
				if err := client.UpdateComment(ctx, state.CommentID, body); err != nil {
					return fmt.Errorf("update linear comment: %w", err)
				}
			}

			state.AttachmentID = attachmentResult.ID
			state.AttachmentURL = attachmentResult.URL
			state.LastWriteOutcome = string(outcome)
			state.LastSkippedReason = ""
			if err := txState.Upsert(ctx, in.Session.OrgID, in.Link.ID, state); err != nil {
				return fmt.Errorf("persist provider state after write: %w", err)
			}
			return nil
		})

	if txErr != nil && hasRescue {
		// Locked tx failed after we already created Linear-side artifacts.
		// Persist the durable handles via the non-tx store on a separate
		// connection so a retry sees them and takes the update branch.
		// Best-effort: if this also fails the retry will double-post.
		if mergeErr := s.providerState.Merge(ctx, in.Session.OrgID, in.Link.ID, rescue); mergeErr != nil {
			s.logger.Error().Err(mergeErr).
				Str("link_id", in.Link.ID.String()).
				Msg("failed to rescue linear durable handles after locked tx failure; retry may double-post")
		}
	}

	return txErr
}

// shouldPostSessionLinks consults the parsed org settings. Defaults
// (true/true) live on the models.LinearAutomationSettings.Effective*()
// accessors so nil pointers stay distinguishable from explicit false. If
// the loader fails or the org row is missing entirely we fall back to
// design 62's documented default-on with a logged warning so dogfooding
// spots it.
func (s *Service) shouldPostSessionLinks(ctx context.Context, orgID uuid.UUID, teamKey string) bool {
	settings := s.linearAutomationSettingsOrFallback(ctx, orgID)
	if teamKey == "" {
		return settings.EffectivePostSessionLinks()
	}
	return settings.PostSessionLinksFor(teamKey)
}

func (s *Service) shouldMoveStates(ctx context.Context, orgID uuid.UUID, teamKey string) bool {
	settings := s.linearAutomationSettingsOrFallback(ctx, orgID)
	if teamKey == "" {
		return settings.EffectiveMoveWorkflowStates()
	}
	return settings.MoveWorkflowStatesFor(teamKey)
}

func (s *Service) reviewStatePreferences(ctx context.Context, orgID uuid.UUID) []string {
	settings := s.linearAutomationSettingsOrFallback(ctx, orgID)
	if len(settings.ReviewStateNamePreferences) > 0 {
		return settings.ReviewStateNamePreferences
	}
	return models.DefaultLinearReviewStateNames
}

// linearAutomationSettingsOrFallback returns the parsed automation block.
// On a loader error we log and apply the design's default-on shape so a
// transient DB hiccup doesn't accidentally suppress writes for everyone.
func (s *Service) linearAutomationSettingsOrFallback(ctx context.Context, orgID uuid.UUID) models.LinearAutomationSettings {
	if s.orgSettingsLoader == nil {
		return defaultLinearAutomationSettings()
	}
	settings, err := s.orgSettingsLoader(ctx, orgID)
	if err != nil {
		s.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to load org linear automation settings; using safe defaults")
		return defaultLinearAutomationSettings()
	}
	return settings.LinearAutomation
}

// defaultLinearAutomationOn is the shared *true used by the package-level
// default settings below. Held in a var so we have a stable address to take
// without re-allocating on every loader-failure path.
var defaultLinearAutomationOn = true

// defaultLinearAutomationSettingsValue is the design-62 default-on shape we
// fall back to when the org row or settings parser is unavailable. Returned
// by-value from defaultLinearAutomationSettings so callers can't mutate the
// shared pointers.
var defaultLinearAutomationSettingsValue = models.LinearAutomationSettings{
	PostSessionLinks:           &defaultLinearAutomationOn,
	MoveWorkflowStates:         &defaultLinearAutomationOn,
	ReviewStateNamePreferences: models.DefaultLinearReviewStateNames,
}

func defaultLinearAutomationSettings() models.LinearAutomationSettings {
	return defaultLinearAutomationSettingsValue
}

// maxAttachmentTitleLen caps the attachment title length sent to Linear.
// Linear renders attachment titles in compact list views — anything past
// ~120 chars is truncated by their UI anyway, and capping locally avoids
// shipping pathological lengths over the wire when a user pastes a
// novella into the title field.
const maxAttachmentTitleLen = 200

func attachmentTitle(session *models.Session) string {
	t := "143 session"
	if session != nil && session.Title != nil && *session.Title != "" {
		t = "143: " + sanitizeAttachmentTitle(*session.Title)
	}
	return t
}

// sanitizeAttachmentTitle strips ASCII/Unicode control characters from a
// user-set session title and caps its length on a rune boundary before it
// flows into Linear. We can't fully sanitize markdown here (Linear renders
// comment bodies as markdown but attachment titles as plain text — the body
// path uses only system-controlled fields so it's safe), but stripping
// control characters protects against unicode-direction-override tricks and
// stray newlines that confuse Linear's UI without rejecting legitimate
// punctuation in titles.
func sanitizeAttachmentTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == utf8.RuneError {
			continue
		}
		// Allow common whitespace chars to pass through but collapse them
		// to a regular space so titles stay single-line.
		if r == '\t' || r == '\n' || r == '\r' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) <= maxAttachmentTitleLen {
		return out
	}
	// Truncate on a rune boundary so we don't split a multi-byte character.
	count := 0
	for i := range out {
		if count == maxAttachmentTitleLen {
			return strings.TrimSpace(out[:i]) + "…"
		}
		count++
	}
	return out
}

func subtitleForMilestone(event MilestoneEvent, prNumber int) string {
	switch event {
	case MilestonePROpened:
		if prNumber > 0 {
			return fmt.Sprintf("PR #%d open", prNumber)
		}
		return "PR open"
	case MilestonePRMerged:
		if prNumber > 0 {
			return fmt.Sprintf("PR #%d merged", prNumber)
		}
		return "PR merged"
	case MilestoneEndedNoPR:
		return "Ended without PR"
	case MilestoneFailed:
		return "Failed"
	default:
		return "Running"
	}
}

func outcomeForMilestone(event MilestoneEvent) db.LinearAttachmentOutcome {
	switch event {
	case MilestoneLinked:
		return db.LinearAttachmentOutcomeRunning
	case MilestonePROpened:
		return db.LinearAttachmentOutcomePROpen
	case MilestonePRMerged:
		return db.LinearAttachmentOutcomeMerged
	case MilestoneEndedNoPR:
		return db.LinearAttachmentOutcomeEndedNoPR
	case MilestoneFailed:
		return db.LinearAttachmentOutcomeFailed
	default:
		return db.LinearAttachmentOutcomeRunning
	}
}

// isPRDrivenTransition reports whether the milestone is a PR-lifecycle
// event that Linear's native GitHub integration also transitions on
// (open → in_review, merged → completed). Coexistence-suppression
// applies to both so we don't double-move an issue Linear already moved.
func isPRDrivenTransition(event MilestoneEvent) bool {
	return event == MilestonePROpened || event == MilestonePRMerged
}

func commentBodyForMilestone(event MilestoneEvent, issueIdent, sessionURL string, prNumber int) string {
	var b strings.Builder
	b.WriteString(botCommentPrefix)
	b.WriteString("\n\n")
	switch event {
	case MilestoneLinked:
		fmt.Fprintf(&b, "Started a session for **%s**. Follow along: %s", issueIdent, sessionURL)
	case MilestonePROpened:
		if prNumber > 0 {
			fmt.Fprintf(&b, "Pull request #%d opened for **%s**. Session: %s", prNumber, issueIdent, sessionURL)
		} else {
			fmt.Fprintf(&b, "Pull request opened for **%s**. Session: %s", issueIdent, sessionURL)
		}
	case MilestonePRMerged:
		if prNumber > 0 {
			fmt.Fprintf(&b, "Pull request #%d merged for **%s**. Session: %s", prNumber, issueIdent, sessionURL)
		} else {
			fmt.Fprintf(&b, "Pull request merged for **%s**. Session: %s", issueIdent, sessionURL)
		}
	case MilestoneEndedNoPR:
		fmt.Fprintf(&b, "Session for **%s** ended without opening a pull request. Session: %s", issueIdent, sessionURL)
	case MilestoneFailed:
		fmt.Fprintf(&b, "Session for **%s** failed. Session: %s", issueIdent, sessionURL)
	}
	return b.String()
}

// teamKeyFromIdentifier extracts "ACS" from "ACS-1234".
func teamKeyFromIdentifier(identifier string) string {
	idx := strings.IndexByte(identifier, '-')
	if idx <= 0 {
		return ""
	}
	return identifier[:idx]
}

// HandleStateTransition fires the Linear state move for a milestone, applying
// every guard from design 62 §"Guards (all must hold)". Records the decision
// (transition or skip) in session_issue_link_state_events for fire-once and
// audit. Replays are no-ops.
//
// Authorization: state transitions are made with the org-level integration
// token. Per the Service-type doc comment, any user who can create a
// linking session can trigger this code path, and admins must gate at the
// org/team level via LinearAutomationSettings.MoveWorkflowStates rather
// than per-user. The user-driven flag LinearStateSyncDisabled lets a
// session opt out, and AllowPerSessionOverrides=false on the org settings
// prevents users from setting that flag — both are enforced upstream of
// this function.
//
// Atomicity: the row-level lock on provider_state plus the unique constraint
// on (session_id, issue_id, event_kind) gate the Linear API call so a
// crash between UpdateIssueState and the local writes can't cause a second
// retry to repeat the move. The event row is inserted inside the same
// transaction as the API call: on commit both are durable; on rollback (or
// process death) neither is, and the unique constraint prevents an
// already-fired event from re-entering this code path.
func (s *Service) HandleStateTransition(ctx context.Context, in MilestoneInput) error {
	if in.Session == nil {
		return fmt.Errorf("nil session")
	}
	skipReason := db.LinearStateSkipReason("")

	switch {
	case in.Link.Role != models.SessionIssueLinkRolePrimary:
		skipReason = db.LinearStateSkipNotPrimary
	case in.Session.LinearPrivate:
		skipReason = db.LinearStateSkipPrivateSession
	case in.Session.LinearStateSyncDisabled:
		skipReason = db.LinearStateSkipDisabledByUser
	}

	teamKey := teamKeyFromIdentifier(in.IssueIdent)
	if skipReason == "" && !s.shouldMoveStates(ctx, in.Session.OrgID, teamKey) {
		skipReason = db.LinearStateSkipPerTeamDisabled
	}

	eventKind := stateEventKindFor(in.Event)
	if eventKind == "" {
		// Milestones we don't transition on (failed, ended_no_pr) — record
		// nothing; HandleMilestone already updated the attachment subtitle.
		return nil
	}

	if skipReason != "" {
		return s.recordSkip(ctx, in, eventKind, skipReason)
	}

	_, token, err := s.integrationFor(ctx, in.Session.OrgID)
	if err != nil {
		return err
	}
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}

	return s.withProviderStateLocked(ctx, in.Session.OrgID, in.Link.ID,
		func(ctx context.Context, txState providerStateStore, txEvents stateEventStore, state db.LinearProviderState) error {
			// Coexistence with Linear's GitHub integration: if their
			// attachments are already on the issue, suppress our PR-driven
			// transitions to avoid double moves and double cycle/sprint
			// membership. Applies to BOTH `pr_opened` (their integration
			// moves the issue to "In Review") and `pr_merged` (they move
			// it to "Done"); either event firing on top of theirs is a
			// double-transition.
			//
			// A lookup failure here is surfaced (not silently treated as
			// "no integration") so the worker retries: if Linear's
			// integration is in fact present and we transition anyway, we
			// double-move the issue. The fire-once unique constraint isn't
			// claimed yet, so a retry can re-enter cleanly.
			//
			// Cache invalidation: a sticky "true" without a TTL would keep
			// suppressing transitions forever after an operator removes
			// Linear's GitHub integration. CoexistsCheckIsStale uses an
			// asymmetric TTL — short for cached=true (the dangerous side
			// that suppresses) and long for cached=false (the safe side).
			if isPRDrivenTransition(in.Event) {
				// `hasObservation` distinguishes "no cache yet" from "cached
				// false." Without it, a `cached=false` row would always
				// re-fetch (the previous `!cached || stale` call site burned
				// the asymmetric-TTL design — the long TTL for false-cached
				// observations existed but was unreachable).
				hasObservation := state.CoexistsWithGitHubIntegration != nil
				cached := hasObservation && *state.CoexistsWithGitHubIntegration
				stale := db.CoexistsCheckIsStale(state.CoexistsWithGitHubIntegration, state.CoexistsCheckedAt, time.Now())
				coexists := cached
				if !hasObservation || stale {
					detected, detectErr := client.HasGitHubIntegrationAttachment(ctx, in.IssueID)
					if detectErr != nil {
						return fmt.Errorf("detect linear github integration attachment: %w", detectErr)
					}
					coexists = detected
					patch := db.LinearProviderState{
						CoexistsCheckedAt: db.TimePtr(time.Now()),
					}
					// Only flip the bool when the observation actually
					// changes — Merge treats any non-nil pointer as overwrite,
					// and we'd churn the JSONB row needlessly otherwise.
					if detected != cached {
						patch.CoexistsWithGitHubIntegration = db.BoolPtr(detected)
					}
					_ = txState.Merge(ctx, in.Session.OrgID, in.Link.ID, patch)
				}
				if coexists {
					return recordSkipInTx(ctx, txState, txEvents, in, eventKind, db.LinearStateSkipLinearGitHubIntegration)
				}
			}

			currentIssue, err := client.FetchIssue(ctx, in.IssueID)
			if err != nil {
				return fmt.Errorf("fetch current linear issue state: %w", err)
			}
			if currentIssue == nil {
				return fmt.Errorf("fetch current linear issue state: issue not found")
			}
			// Workspace consistency guard. The Linear API itself rejects
			// cross-workspace token usage (a token for workspace B cannot
			// access an issue in workspace A — that's caught by FetchIssue
			// returning an error or a nil issue). This check covers the
			// remaining narrow window: the token successfully authenticated
			// against the issue's workspace, but the workspace_slug we
			// persisted at link time disagrees with what FetchIssue returns
			// now. That can happen if an org reconnected its integration to
			// a different Linear workspace whose IDs happen to overlap, or
			// if Linear's slug for the workspace was renamed and the issue
			// payload now reflects the new slug. Skip rather than write —
			// surfacing the mismatch via a skip event lets operators see
			// the drift without us silently overwriting state under the
			// wrong workspace's branding.
			if state.WorkspaceSlug != "" && currentIssue.WorkspaceSlug != "" &&
				!strings.EqualFold(state.WorkspaceSlug, currentIssue.WorkspaceSlug) {
				s.logger.Warn().
					Str("link_id", in.Link.ID.String()).
					Str("persisted_workspace", state.WorkspaceSlug).
					Str("observed_workspace", currentIssue.WorkspaceSlug).
					Msg("linear: workspace slug drift detected at state-transition time; skipping")
				return recordSkipInTx(ctx, txState, txEvents, in, eventKind, db.LinearStateSkipAlreadyPastTarget)
			}
			state = mergeCurrentIssueObservation(state, currentIssue)
			if err := txState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
				LastKnownStateName: state.LastKnownStateName,
				LastKnownStateType: state.LastKnownStateType,
				TeamID:             state.TeamID,
			}); err != nil {
				return fmt.Errorf("persist current linear issue state: %w", err)
			}

			// Recent human edits: skip if a human moved the issue within
			// the last 10 minutes. This protects manual workflows.
			//
			// A lookup failure surfaces as a retry rather than "no edits"
			// — without this, a transient API hiccup could let us clobber a
			// manual move the user just made. The fire-once claim hasn't
			// been inserted yet, so the next retry re-enters cleanly.
			since := time.Now().Add(-10 * time.Minute)
			humanEdited, err := client.IssueRecentHumanEdits(ctx, in.IssueID, since)
			if err != nil {
				return fmt.Errorf("check linear recent human edits: %w", err)
			}
			if humanEdited {
				return recordSkipInTx(ctx, txState, txEvents, in, eventKind, db.LinearStateSkipUserRecentEdit)
			}

			// Resolve target state by type, applying the org's review-state
			// name preferences for the PR-open milestone.
			targetType := stateTypeFor(in.Event)
			prefer := []string{}
			if in.Event == MilestonePROpened {
				prefer = s.reviewStatePreferences(ctx, in.Session.OrgID)
			}
			target, err := client.WorkflowStateForType(ctx, state.TeamID, prefer, targetType)
			if err != nil {
				// Transient lookup failure (network, 401, missing teamID
				// during a race). Surface so the worker retries; do NOT
				// record a skip event — the unique constraint on
				// (session_id, issue_id, event_kind) would burn this
				// transition slot, and the retry would observe
				// ErrLinearStateEventExists and silently no-op.
				return fmt.Errorf("resolve target workflow state: %w", err)
			}
			if target == nil || target.ID == "" {
				// No target state (or one returned with an empty ID, which
				// shouldn't happen but would otherwise be sent to Linear and
				// produce a useless 422). Permanent condition — record a skip
				// so the audit trail explains it.
				return recordSkipInTx(ctx, txState, txEvents, in, eventKind, db.LinearStateSkipAlreadyPastTarget)
			}

			// State-id divergence guard. Catches the rollback-recovery race:
			// a previous attempt's UpdateIssueState succeeded but the local
			// tx commit failed, so the fire-once claim was rolled back too.
			// The retry would normally re-issue the move; instead, we
			// observe currentIssue.StateID == target.ID and skip
			// idempotently. This is a stricter check than the StateName/
			// StateType comparison below — IDs are unambiguous, names can
			// drift (Linear lets workspaces rename states).
			if currentIssue.StateID != "" && currentIssue.StateID == target.ID {
				return recordSkipInTx(ctx, txState, txEvents, in, eventKind, db.LinearStateSkipAlreadyInTargetState)
			}

			// Forward-only: refuse to move backwards. Differentiate
			// "already in target state" (no-op, expected during normal
			// operation) from "already past target" (issue moved on
			// without us — refuse to rewind) so the operator audit log
			// gives a faithful explanation.
			if !shouldMoveToTarget(in.Event, state.LastKnownStateType, state.LastKnownStateName, target.Type, target.Name) {
				reason := db.LinearStateSkipAlreadyPastTarget
				if state.LastKnownStateType == target.Type && strings.EqualFold(state.LastKnownStateName, target.Name) {
					reason = db.LinearStateSkipAlreadyInTargetState
				}
				return recordSkipInTx(ctx, txState, txEvents, in, eventKind, reason)
			}

			// Claim fire-once *before* hitting Linear: the unique
			// constraint on (session_id, issue_id, event_kind) collapses a
			// concurrent or post-crash retry into ErrLinearStateEventExists,
			// so even if we crash mid-call the next retry observes the
			// claim and returns without re-firing UpdateIssueState. The
			// row commits with the API call inside the same tx — on
			// rollback, the claim disappears too, leaving the next attempt
			// free to retry.
			priorID := state.LastKnownStateName
			err = txEvents.Insert(ctx, in.Session.OrgID, db.LinearStateEventInput{
				SessionID:      in.Session.ID,
				IssueID:        in.Link.IssueID,
				EventKind:      eventKind,
				TransitionFrom: priorID,
				TransitionTo:   target.Name,
			})
			if errors.Is(err, db.ErrLinearStateEventExists) {
				// Already fired by an earlier attempt — replay is a no-op.
				return nil
			}
			if err != nil {
				return fmt.Errorf("record state transition: %w", err)
			}

			if err := client.UpdateIssueState(ctx, in.IssueID, target.ID); err != nil {
				return fmt.Errorf("update linear issue state: %w", err)
			}
			if err := txState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
				PriorStateID:       priorID,
				LastKnownStateName: target.Name,
				LastKnownStateType: target.Type,
			}); err != nil {
				return fmt.Errorf("persist provider state after transition: %w", err)
			}
			return nil
		})
}

func mergeCurrentIssueObservation(state db.LinearProviderState, issue *FetchedIssue) db.LinearProviderState {
	if issue == nil {
		return state
	}
	if issue.StateName != "" {
		state.LastKnownStateName = issue.StateName
	}
	if issue.StateType != "" {
		state.LastKnownStateType = issue.StateType
	}
	if issue.TeamID != "" {
		state.TeamID = issue.TeamID
	}
	return state
}

// recordSkipInTx records a skip event using tx-bound stores so the skip
// shares a transaction with any sibling provider_state writes inside
// HandleStateTransition. Mirrors recordSkip but without re-acquiring the
// row lock.
func recordSkipInTx(
	ctx context.Context,
	txState providerStateStore,
	txEvents stateEventStore,
	in MilestoneInput,
	kind db.LinearStateEventKind,
	reason db.LinearStateSkipReason,
) error {
	err := txEvents.Insert(ctx, in.Session.OrgID, db.LinearStateEventInput{
		SessionID:     in.Session.ID,
		IssueID:       in.Link.IssueID,
		EventKind:     kind,
		SkippedReason: reason,
	})
	if err != nil && !errors.Is(err, db.ErrLinearStateEventExists) {
		return err
	}
	_ = txState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
		LastSkippedReason: string(reason),
	})
	return nil
}

func (s *Service) recordSkip(ctx context.Context, in MilestoneInput, kind db.LinearStateEventKind, reason db.LinearStateSkipReason) error {
	err := s.stateEvents.Insert(ctx, in.Session.OrgID, db.LinearStateEventInput{
		SessionID:     in.Session.ID,
		IssueID:       in.Link.IssueID,
		EventKind:     kind,
		SkippedReason: reason,
	})
	if err != nil && !errors.Is(err, db.ErrLinearStateEventExists) {
		return err
	}
	_ = s.providerState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
		LastSkippedReason: string(reason),
	})
	return nil
}

func stateEventKindFor(event MilestoneEvent) db.LinearStateEventKind {
	switch event {
	case MilestoneLinked:
		return db.LinearStateEventLinked
	case MilestonePROpened:
		return db.LinearStateEventPROpened
	case MilestonePRMerged:
		return db.LinearStateEventPRMerged
	}
	return ""
}

func stateTypeFor(event MilestoneEvent) string {
	switch event {
	case MilestoneLinked:
		return "started"
	case MilestonePROpened:
		return "started" // review states are typed `started` in Linear
	case MilestonePRMerged:
		return "completed"
	}
	return ""
}

// isForwardMove returns true when moving from `current` to `target` is a
// forward move under Linear's state-type ordering. We never move backwards
// or sideways from a `completed` or `canceled` state.
func isForwardMove(current, target string) bool {
	rank := map[string]int{
		"":          0,
		"triage":    1,
		"backlog":   2,
		"unstarted": 3,
		"started":   4,
		"completed": 5,
		"canceled":  5,
	}
	cr, cok := rank[current]
	tr, tok := rank[target]
	if !cok || !tok {
		// Unknown types — be conservative and refuse the move.
		return false
	}
	if current == "completed" || current == "canceled" {
		return false
	}
	return tr > cr
}

func shouldMoveToTarget(event MilestoneEvent, currentType, currentName, targetType, targetName string) bool {
	if event == MilestonePROpened &&
		currentType == "started" &&
		targetType == "started" &&
		!strings.EqualFold(currentName, targetName) {
		return true
	}
	return isForwardMove(currentType, targetType)
}
