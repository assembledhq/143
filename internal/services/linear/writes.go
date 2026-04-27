package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
type MilestoneInput struct {
	Event      MilestoneEvent
	Session    *models.Session
	Link       models.SessionIssueLink
	IssueID    string // Linear (external) issue id
	PRNumber   int    // 0 when not applicable
	SessionURL string // 143 deep link to the session
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
// Idempotency: AttachmentID and CommentID in provider_state act as the
// dedupe anchors. Re-running this with the same milestone is a no-op
// modulo Linear-side metadata refresh.
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

	state, err := s.providerState.Get(ctx, in.Session.OrgID, in.Link.ID)
	if err != nil {
		return fmt.Errorf("read provider state: %w", err)
	}

	teamKey := teamKeyFromIdentifier(in.IssueIdent)
	if !s.shouldPostSessionLinks(ctx, in.Session.OrgID, teamKey) {
		return nil
	}

	integration, token, err := s.integrationFor(ctx, in.Session.OrgID)
	if err != nil {
		return err
	}
	_ = integration
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}

	outcome := outcomeForMilestone(in.Event)
	subtitle := subtitleForMilestone(in.Event, in.PRNumber)
	title := attachmentTitle(in.Session)

	attachmentResult, err := client.CreateOrUpdateAttachment(ctx, AttachmentWriteInput{
		IssueID:  in.IssueID,
		PriorID:  state.AttachmentID,
		Title:    title,
		Subtitle: subtitle,
		URL:      in.SessionURL,
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

	body := commentBodyForMilestone(in.Event, in.IssueIdent, in.SessionURL, in.PRNumber)
	if state.CommentID == "" {
		// First write — create. Subsequent milestones update in place to
		// avoid notification fatigue.
		commentID, err := client.CreateComment(ctx, in.IssueID, body)
		if err != nil {
			return fmt.Errorf("create linear comment: %w", err)
		}
		state.CommentID = commentID
	} else {
		if err := client.UpdateComment(ctx, state.CommentID, body); err != nil {
			return fmt.Errorf("update linear comment: %w", err)
		}
	}

	state.AttachmentID = attachmentResult.ID
	state.AttachmentURL = attachmentResult.URL
	state.LastWriteOutcome = string(outcome)
	state.LastSkippedReason = ""
	if err := s.providerState.Upsert(ctx, in.Session.OrgID, in.Link.ID, state); err != nil {
		return fmt.Errorf("persist provider state after write: %w", err)
	}
	return nil
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

func defaultLinearAutomationSettings() models.LinearAutomationSettings {
	t := true
	return models.LinearAutomationSettings{
		PostSessionLinks:           &t,
		MoveWorkflowStates:         &t,
		ReviewStateNamePreferences: models.DefaultLinearReviewStateNames,
	}
}

func attachmentTitle(session *models.Session) string {
	t := "143 session"
	if session != nil && session.Title != nil && *session.Title != "" {
		t = "143: " + *session.Title
	}
	return t
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

	state, err := s.providerState.Get(ctx, in.Session.OrgID, in.Link.ID)
	if err != nil {
		return err
	}

	integration, token, err := s.integrationFor(ctx, in.Session.OrgID)
	if err != nil {
		return err
	}
	_ = integration
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}

	// Coexistence with Linear's GitHub integration: if their attachments are
	// already on the issue, suppress our merge-time writes to avoid double
	// transitions and double cycle/sprint membership.
	if in.Event == MilestonePRMerged {
		coexists := state.CoexistsWithGitHubIntegration != nil && *state.CoexistsWithGitHubIntegration
		if !coexists {
			detected, detectErr := client.HasGitHubIntegrationAttachment(ctx, in.IssueID)
			if detectErr == nil && detected {
				coexists = true
				_ = s.providerState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
					CoexistsWithGitHubIntegration: db.BoolPtr(true),
				})
			}
		}
		if coexists {
			return s.recordSkip(ctx, in, eventKind, db.LinearStateSkipLinearGitHubIntegration)
		}
	}

	// Recent human edits: skip if a human moved the issue within the last
	// 10 minutes. This protects manual workflows.
	since := time.Now().Add(-10 * time.Minute)
	humanEdited, err := client.IssueRecentHumanEdits(ctx, in.IssueID, since)
	if err == nil && humanEdited {
		return s.recordSkip(ctx, in, eventKind, db.LinearStateSkipUserRecentEdit)
	}

	// Resolve target state by type, applying the org's review-state name
	// preferences for the PR-open milestone.
	targetType := stateTypeFor(in.Event)
	prefer := []string{}
	if in.Event == MilestonePROpened {
		prefer = s.reviewStatePreferences(ctx, in.Session.OrgID)
	}
	target, err := client.WorkflowStateForType(ctx, state.TeamID, prefer, targetType)
	if err != nil || target == nil {
		// No target state available — record skip rather than error so the
		// audit trail explains it.
		return s.recordSkip(ctx, in, eventKind, db.LinearStateSkipAlreadyPastTarget)
	}

	// Forward-only: refuse to move backwards. Differentiate "already in
	// target state" (no-op, expected during normal operation) from
	// "already past target" (issue moved on without us — refuse to rewind)
	// so the operator audit log gives a faithful explanation.
	if !isForwardMove(state.LastKnownStateType, target.Type) {
		reason := db.LinearStateSkipAlreadyPastTarget
		if state.LastKnownStateType == target.Type {
			reason = db.LinearStateSkipAlreadyInTargetState
		}
		return s.recordSkip(ctx, in, eventKind, reason)
	}

	// Capture prior state, fire the transition, record the event.
	priorID := state.LastKnownStateName
	if err := client.UpdateIssueState(ctx, in.IssueID, target.ID); err != nil {
		return fmt.Errorf("update linear issue state: %w", err)
	}
	_ = s.providerState.Merge(ctx, in.Session.OrgID, in.Link.ID, db.LinearProviderState{
		PriorStateID:       priorID,
		LastKnownStateName: target.Name,
		LastKnownStateType: target.Type,
	})
	err = s.stateEvents.Insert(ctx, in.Session.OrgID, db.LinearStateEventInput{
		SessionID:      in.Session.ID,
		IssueID:        in.Link.IssueID,
		EventKind:      eventKind,
		TransitionFrom: priorID,
		TransitionTo:   target.Name,
	})
	if err != nil && !errors.Is(err, db.ErrLinearStateEventExists) {
		return fmt.Errorf("record state transition: %w", err)
	}
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
	case MilestoneEndedNoPR:
		return db.LinearStateEventEnded
	case MilestoneFailed:
		return db.LinearStateEventCanceled
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
