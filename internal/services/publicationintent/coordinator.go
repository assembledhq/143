package publicationintent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
)

type ResultStatus string

const (
	ResultReviewStarted             ResultStatus = "review_started"
	ResultReviewInProgress          ResultStatus = "review_in_progress"
	ResultPRQueued                  ResultStatus = "pr_queued"
	ResultAlreadyPublished          ResultStatus = "already_published"
	ResultManualPublicationRequired ResultStatus = "manual_publication_required"
	ResultBlocked                   ResultStatus = "blocked"
)

type RequestPullRequest struct {
	Draft      *bool
	AuthorMode string
	// ChangesetID selects a specific changeset for UI/API stack publication.
	// Agent and automation requests omit it and therefore target the primary
	// changeset. The store lookup remains tenant- and session-scoped.
	ChangesetID *uuid.UUID
	// Source is the channel the request arrived through — the sandbox
	// internal API or the authenticated UI. It is deliberately independent of
	// TriggerKind, which records why the request was made: an agent may relay
	// an explicit user instruction, and a user may not impersonate the agent
	// tool. Only the caller knows the channel, so callers must set it.
	Source            models.SessionPublicationSource
	TriggerKind       models.SessionPublicationTriggerKind
	MergeWhenReady    bool
	RequestedByUserID *uuid.UUID
	RequestedRole     string
	IssueSnapshotID   *uuid.UUID
}

type PublicationIntentResult struct {
	Status         ResultStatus
	SessionID      uuid.UUID
	PublicationID  *uuid.UUID
	ReviewLoopID   *uuid.UUID
	PullRequestURL *string
	Reason         *string
	ReviewBypassed bool
}

type PublicationIntentCoordinator interface {
	RequestPullRequest(
		ctx context.Context,
		orgID uuid.UUID,
		sessionID uuid.UUID,
		req RequestPullRequest,
	) (*PublicationIntentResult, error)
	PublicationExecutionEnabled(source models.SessionPublicationSource) bool
	ReviewExecutionEnabled(source models.SessionPublicationSource) bool
}

type ErrorCode string

const (
	ErrorSessionNotEligible ErrorCode = "SESSION_NOT_PUBLICATION_ELIGIBLE"
	ErrorWorkspaceNotReady  ErrorCode = "WORKSPACE_NOT_READY"
	ErrorPublicationFailed  ErrorCode = "PUBLICATION_INTENT_FAILED"
)

type Error struct {
	Code ErrorCode
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

type SessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

type ChangesetStore interface {
	GetPrimary(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionChangeset, error)
	GetByID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionChangeset, error)
}

type PullRequestStore interface {
	GetByChangesetID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.PullRequest, error)
	GetPrimaryBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.PullRequest, error)
}

type OrganizationStore interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

type UserStore interface {
	GetByIDGlobalWithSettings(ctx context.Context, userID uuid.UUID) (models.UserWithSettings, error)
}

type PublicationStore interface {
	EnsureRequested(ctx context.Context, orgID uuid.UUID, publication *models.SessionPublication) error
	ApplyReviewBypass(ctx context.Context, orgID uuid.UUID, publication *models.SessionPublication) error
	ApplyManualTakeover(ctx context.Context, orgID uuid.UUID, publication *models.SessionPublication) error
	GetByChangeset(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionPublication, error)
}

type RepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type JobStore interface {
	QueueChangesetPRCreation(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, queue string, payload any, priority int) (uuid.UUID, bool, error)
}

type Coordinator struct {
	sessions           SessionStore
	changesets         ChangesetStore
	pullRequests       PullRequestStore
	organizations      OrganizationStore
	users              UserStore
	publications       PublicationStore
	jobs               JobStore
	repositories       RepositoryStore
	publicationEnabled bool
	reviewEnabled      bool
	logger             zerolog.Logger
}

func (c *Coordinator) SetRepositoryStore(store RepositoryStore) {
	c.repositories = store
}

func (c *Coordinator) SetReviewEnabled(enabled bool) {
	c.reviewEnabled = enabled
}

// SetPublicationEnabled controls execution without changing stored customer
// policy. Handlers keep using the coordinator while this is off so the wire
// contract stays typed and idempotent during an emergency rollback.
func (c *Coordinator) SetPublicationEnabled(enabled bool) {
	c.publicationEnabled = enabled
}

// PublicationExecutionEnabled is consulted both at intent admission and by
// workers immediately before publication side effects. The agent publication
// switch deliberately affects only the agent-tool path. Explicit user
// publication remains the manual fallback, while configured automation and
// backend-owned project publication retain their independent policies.
func (c *Coordinator) PublicationExecutionEnabled(source models.SessionPublicationSource) bool {
	return c.publicationEnabled || source != models.SessionPublicationSourceAgentTool
}

// ReviewExecutionEnabled is a runtime gate, not stored customer policy. The
// review switch applies to every pre-publication loop; disabling agent
// publication additionally parks agent-tool review without weakening an
// explicit user's manual path or an automation's snapshotted policy.
func (c *Coordinator) ReviewExecutionEnabled(source models.SessionPublicationSource) bool {
	if !c.reviewEnabled {
		return false
	}
	return c.PublicationExecutionEnabled(source)
}

func NewCoordinator(
	sessions SessionStore,
	changesets ChangesetStore,
	pullRequests PullRequestStore,
	organizations OrganizationStore,
	users UserStore,
	publications PublicationStore,
	jobs JobStore,
	logger zerolog.Logger,
) *Coordinator {
	return &Coordinator{
		sessions: sessions, changesets: changesets, pullRequests: pullRequests,
		organizations: organizations, users: users, publications: publications,
		jobs: jobs, publicationEnabled: true, reviewEnabled: true, logger: logger,
	}
}

func publicationDisabledResult(sessionID uuid.UUID) *PublicationIntentResult {
	reason := "PR publication is temporarily disabled; verified changes remain available for manual follow-up"
	return &PublicationIntentResult{
		Status:    ResultManualPublicationRequired,
		SessionID: sessionID,
		Reason:    &reason,
	}
}

func reviewDisabledResult(sessionID uuid.UUID, publicationID *uuid.UUID) *PublicationIntentResult {
	reason := "pre-PR review is temporarily disabled; publication remains pending for manual follow-up"
	return &PublicationIntentResult{
		Status: ResultManualPublicationRequired, SessionID: sessionID,
		PublicationID: publicationID, Reason: &reason,
	}
}

func (c *Coordinator) RequestPullRequest(
	ctx context.Context,
	orgID uuid.UUID,
	sessionID uuid.UUID,
	req RequestPullRequest,
) (result *PublicationIntentResult, err error) {
	if req.Source == "" {
		req.Source = models.SessionPublicationSourceAgentTool
	}
	if req.TriggerKind == "" {
		req.TriggerKind = models.SessionPublicationTriggerAgentReady
	}
	// Registered before validation so every rejection is counted, including a
	// malformed request.
	defer func() { recordIntentOutcome(ctx, req.Source, result, err) }()

	if validateErr := req.Source.Validate(); validateErr != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: validateErr}
	}
	if validateErr := req.TriggerKind.Validate(); validateErr != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: validateErr}
	}
	if req.TriggerKind == models.SessionPublicationTriggerAgentReady &&
		req.Source != models.SessionPublicationSourceAgentTool {
		return nil, &Error{
			Code: ErrorPublicationFailed,
			Err:  errors.New("agent-ready publication must originate from the agent tool"),
		}
	}
	if req.Source == models.SessionPublicationSourceAutomation &&
		req.TriggerKind != models.SessionPublicationTriggerPolicy {
		return nil, &Error{Code: ErrorPublicationFailed, Err: errors.New("automation publication must use a policy trigger")}
	}

	session, err := c.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	automationRequest := req.Source == models.SessionPublicationSourceAutomation &&
		req.TriggerKind == models.SessionPublicationTriggerPolicy && session.AutomationRunID != nil
	backendPolicyRequest := req.Source == models.SessionPublicationSourceBackend &&
		req.TriggerKind == models.SessionPublicationTriggerPolicy && session.ProjectTaskID != nil
	if session.RepositoryID == nil || session.Origin == models.SessionOriginCodeReview ||
		session.Origin == models.SessionOriginRevision || (session.AutomationRunID != nil) != automationRequest {
		return nil, &Error{Code: ErrorSessionNotEligible, Err: errors.New("session cannot create a new pull request")}
	}
	var changeset models.SessionChangeset
	if req.ChangesetID == nil {
		changeset, err = c.changesets.GetPrimary(ctx, orgID, sessionID)
	} else {
		changeset, err = c.changesets.GetByID(ctx, orgID, sessionID, *req.ChangesetID)
	}
	if err != nil {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: fmt.Errorf("resolve publication changeset: %w", err)}
	}
	existingPublication, publicationErr := c.publications.GetByChangeset(ctx, orgID, sessionID, changeset.ID)
	if publicationErr != nil && !errors.Is(publicationErr, pgx.ErrNoRows) {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("check existing publication intent: %w", publicationErr)}
	}
	hasExistingPublication := publicationErr == nil
	// The audited draft bypass is the resolution offered for a publication whose
	// review already stopped for human attention. It is deliberately not a way
	// to open a first-time request without review: "give me a draft PR" and
	// "skip the review gate" must not be the same request.
	executionParked := hasExistingPublication && !existingPublication.State.Terminal() &&
		(!c.PublicationExecutionEnabled(existingPublication.Source) ||
			(existingPublication.ReviewGateState == models.SessionPublicationReviewGatePending &&
				!c.ReviewExecutionEnabled(existingPublication.Source)))
	bypassRequested := hasExistingPublication &&
		(reviewBlocksPublication(existingPublication) || executionParked) &&
		authorizedDraftBypass(req)
	manualTakeoverRequested := executionParked && authorizedManualTakeover(req)
	// A retryable terminal outcome (no-op or failed) is not a durable answer:
	// re-requesting must reach EnsureRequested, whose generation-guarded reopen
	// is the only path back. Anything else is a live intent the caller rejoins.
	retryingTerminalPublication := hasExistingPublication && retryablePublicationOutcome(existingPublication.State)
	if hasExistingPublication && !bypassRequested && !manualTakeoverRequested && !retryingTerminalPublication {
		if !existingPublication.State.Terminal() && !c.PublicationExecutionEnabled(existingPublication.Source) {
			return publicationDisabledResult(sessionID), nil
		}
		if !existingPublication.State.Terminal() &&
			existingPublication.ReviewGateState == models.SessionPublicationReviewGatePending &&
			!c.ReviewExecutionEnabled(existingPublication.Source) {
			return reviewDisabledResult(sessionID, &existingPublication.ID), nil
		}
		return ExistingPublicationResult(existingPublication), nil
	}
	resumingRecordedDraft := false
	if !hasExistingPublication || retryingTerminalPublication {
		var prErr error
		if changeset.IsPrimary {
			_, prErr = c.pullRequests.GetPrimaryBySessionID(ctx, orgID, sessionID)
		} else {
			_, prErr = c.pullRequests.GetByChangesetID(ctx, orgID, sessionID, changeset.ID)
		}
		if prErr == nil {
			// A terminal draft-first intent already owns a PR by design. It is
			// not published yet: reopen the intent and let the worker reuse and
			// finalize that draft. All other existing PRs remain final results.
			resumingRecordedDraft = retryingTerminalPublication &&
				existingPublication.HandoffMode == models.PRHandoffModeDraftFirst &&
				existingPublication.GitHubPRNumber != nil
			if !resumingRecordedDraft {
				return &PublicationIntentResult{Status: ResultAlreadyPublished, SessionID: sessionID}, nil
			}
		} else if !errors.Is(prErr, pgx.ErrNoRows) {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("check existing pull request: %w", prErr)}
		}
	}
	// Idempotent replays resolve above before checking whether the mutable
	// changeset still looks publishable. A completed intent or an adopted PR is
	// already the authoritative outcome; statuses such as pr_open must not turn
	// a repeated request into a workspace error.
	if changeset.MaterializationError != nil || changeset.RestackConfirmationRequired {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: errors.New("primary changeset is not in a publishable state")}
	}
	if !changeset.IsPrimary && changeset.WorktreePath == nil {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: errors.New("non-primary changeset must be materialized before publication")}
	}
	headBranch, desiredHeadSHA, err := resolvePublicationTarget(session, changeset)
	if err != nil {
		return nil, err
	}

	policy := EffectivePolicy{
		CreatePRWhenAgentReady: true,
		CreatePRSource:         models.PublicationPolicySourceAutomation,
		ReviewSource:           models.PublicationPolicySourceAutomation,
	}
	if !automationRequest && !backendPolicyRequest {
		var initiatorRole string
		policy, initiatorRole, err = c.resolvePolicy(ctx, orgID, session.TriggeredByUserID)
		if err != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: err}
		}
		// Agent-tool callers authenticate the sandbox, not a mutable browser
		// role. Always derive their authority from the session initiator so a
		// forged or omitted requested_role cannot bypass the builder review gate.
		if req.Source == models.SessionPublicationSourceAgentTool {
			req.RequestedRole = initiatorRole
		}
		if req.TriggerKind == models.SessionPublicationTriggerAgentReady && !policy.CreatePRWhenAgentReady {
			metrics.RecordAutomaticPRManualOverride(ctx, "off")
			reason := "automatic PR handoff is disabled by effective policy"
			return &PublicationIntentResult{
				Status: ResultManualPublicationRequired, SessionID: sessionID, Reason: &reason,
			}, nil
		}
		if req.TriggerKind == models.SessionPublicationTriggerExplicitAction && !policy.CreatePRWhenAgentReady {
			metrics.RecordAutomaticPRManualOverride(ctx, "on")
		}
	}
	if !c.PublicationExecutionEnabled(req.Source) {
		return publicationDisabledResult(sessionID), nil
	}
	handoffMode := models.PRHandoffModePrePublish
	if resumingRecordedDraft {
		// The existing GitHub draft is now the authoritative handoff shape,
		// even if repository settings changed after it was created.
		handoffMode = models.PRHandoffModeDraftFirst
	} else if c.repositories != nil {
		repo, repoErr := c.repositories.GetByID(ctx, orgID, *session.RepositoryID)
		if repoErr != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load repository handoff policy: %w", repoErr)}
		}
		// Unreadable repository settings must not block publication. pre_publish
		// is the conservative fallback: review runs before anything is visible
		// on GitHub. This matches how the sessions API resolves the same policy.
		if repoSettings, parseErr := models.ParseRepositorySettings(repo.Settings); parseErr != nil {
			c.logger.Warn().Err(parseErr).
				Str("session_id", sessionID.String()).
				Str("repository_id", session.RepositoryID.String()).
				Msg("falling back to pre-publish repository handoff policy")
		} else {
			handoffMode = repoSettings.PRHandoffMode
		}
	}

	automaticSource := policy.CreatePRSource
	if req.TriggerKind == models.SessionPublicationTriggerExplicitAction {
		automaticSource = models.PublicationPolicySourceExplicitAction
	}
	// Execution switches must never rewrite effective review policy. If review
	// is required while execution is stopped, persist a pending gate and park
	// the intent; treating the switch as review=off would publish unreviewed.
	reviewRequired := !automationRequest && !backendPolicyRequest && policy.ReviewBeforePR
	if manualTakeoverRequested && existingPublication.ReviewGateState == models.SessionPublicationReviewGatePending &&
		existingPublication.ReviewMaxPasses != nil {
		// A manual takeover changes execution authority, never the review
		// decision already recorded on the durable intent.
		reviewRequired = true
		policy.ReviewMaxPasses = *existingPublication.ReviewMaxPasses
		policy.ReviewSource = existingPublication.ReviewPolicySource
	}
	reviewBypassed := bypassRequested
	if !changeset.IsPrimary && req.RequestedRole == string(models.RoleBuilder) {
		return nil, &Error{Code: ErrorSessionNotEligible, Err: errors.New("builder review evidence does not cover a separate changeset worktree")}
	}
	reviewExecutionBlocked := reviewRequired && !reviewBypassed && !c.ReviewExecutionEnabled(req.Source)
	reviewPolicySource := policy.ReviewSource
	if reviewBypassed {
		metrics.RecordAutomaticPRManualOverride(ctx, "review_bypass")
		reviewPolicySource = models.PublicationPolicySourceExplicitBypass
	}
	publicationQueue := models.SessionPublicationJobQueueAgent
	queue := "agent"
	priority := 5
	// Backend policy requests are project-owned work. Preserve the historical
	// default-worker routing and priority while still running them through the
	// same durable coordinator as every other entry point.
	if automationRequest || backendPolicyRequest {
		publicationQueue = models.SessionPublicationJobQueueDefault
		queue = "default"
		priority = 0
	}
	payload := map[string]any{}
	if manualTakeoverRequested && !bypassRequested {
		// A takeover transfers only runtime authority. Preserve the original
		// handoff mode and every caller option (draft, authorship, issue snapshot,
		// merge intent) so a later settings change or an option-less Resume click
		// cannot reinterpret the durable request.
		if err := json.Unmarshal(existingPublication.RequestPayload, &payload); err != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("decode parked publication request: %w", err)}
		}
		handoffMode = existingPublication.HandoffMode
		payload["session_id"] = sessionID.String()
		payload["changeset_id"] = changeset.ID.String()
		payload["org_id"] = orgID.String()
		payload["publication_source"] = string(existingPublication.Source)
		payload["publication_execution_source"] = string(req.Source)
		payload["publication_queue"] = string(publicationQueue)
		if req.RequestedRole != "" {
			payload["requested_role"] = req.RequestedRole
		}
	} else {
		payload = map[string]any{
			"session_id":                 sessionID.String(),
			"changeset_id":               changeset.ID.String(),
			"org_id":                     orgID.String(),
			"publication_source":         string(req.Source),
			"publication_queue":          string(publicationQueue),
			"publication_trigger_kind":   string(req.TriggerKind),
			"publication_handoff_mode":   string(handoffMode),
			"automatic_pr_policy_source": string(automaticSource),
			"review_policy_source":       string(reviewPolicySource),
			"initiated_by_user_id":       session.TriggeredByUserID,
		}
		if req.IssueSnapshotID != nil {
			payload["issue_snapshot_id"] = req.IssueSnapshotID.String()
		}
		if handoffMode == models.PRHandoffModeDraftFirst {
			payload["draft"] = true
		} else if req.Draft != nil {
			payload["draft"] = *req.Draft
		}
		if req.AuthorMode != "" && req.AuthorMode != "auto" {
			payload["author_mode"] = req.AuthorMode
		}
		if req.MergeWhenReady {
			payload["merge_when_ready"] = true
			payload["requested_by_user_id"] = req.RequestedByUserID
		}
		if req.RequestedRole != "" {
			payload["requested_role"] = req.RequestedRole
		}
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("encode publication request: %w", err)}
	}
	publication := models.SessionPublication{
		OrgID: orgID, SessionID: sessionID, ChangesetID: changeset.ID, RepositoryID: *session.RepositoryID,
		Source: req.Source, TriggerKind: req.TriggerKind,
		HandoffMode: handoffMode, InitiatedByUserID: session.TriggeredByUserID,
		AutomaticPolicySource: automaticSource, ReviewPolicySource: reviewPolicySource,
		ReviewGateState: models.SessionPublicationReviewGateNotRequired,
		JobQueue:        publicationQueue, RequestPayload: encodedPayload,
		RequestGenerationAt: time.Now().UTC(), BaseBranch: changeset.BaseBranch,
		HeadBranch: headBranch, DesiredHeadSHA: desiredHeadSHA,
	}
	if reviewRequired && !reviewBypassed {
		maxPasses := policy.ReviewMaxPasses
		publication.ReviewMaxPasses = &maxPasses
		publication.ReviewGateState = models.SessionPublicationReviewGatePending
	}
	bypassIntent := publication
	takeoverIntent := publication
	if ensureErr := c.publications.EnsureRequested(ctx, orgID, &publication); ensureErr != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("persist publication intent: %w", ensureErr)}
	}
	// EnsureRequested reopens a retryable terminal outcome only when this
	// request's generation is newer than the stored one, and those two clocks
	// have different sources. If the reopen did not take, the row is still
	// terminal: report that instead of queueing a job the worker will discard
	// as a terminal replay, which would leave the caller watching a UI that
	// never moves.
	if publication.State.Terminal() {
		return ExistingPublicationResult(publication), nil
	}
	if reviewBypassed {
		bypassIntent.ID = publication.ID
		if bypassErr := c.publications.ApplyReviewBypass(ctx, orgID, &bypassIntent); bypassErr != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("persist publication review bypass: %w", bypassErr)}
		}
		publication = bypassIntent
	} else if manualTakeoverRequested {
		takeoverIntent.ID = publication.ID
		if takeoverErr := c.publications.ApplyManualTakeover(ctx, orgID, &takeoverIntent); takeoverErr != nil {
			if errors.Is(takeoverErr, pgx.ErrNoRows) {
				current, currentErr := c.publications.GetByChangeset(ctx, orgID, sessionID, changeset.ID)
				if currentErr == nil && current.State.Terminal() {
					return ExistingPublicationResult(current), nil
				}
			}
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("persist publication manual takeover: %w", takeoverErr)}
		}
		publication = takeoverIntent
	}
	if reviewExecutionBlocked {
		return reviewDisabledResult(sessionID, &publication.ID), nil
	}
	_, queued, queueErr := c.jobs.QueueChangesetPRCreation(ctx, orgID, sessionID, changeset.ID, queue, payload, priority)
	if queueErr != nil {
		reason := "publication intent is durable, but immediate queueing failed; reconciliation will retry"
		c.logger.Error().Err(queueErr).
			Str("org_id", orgID.String()).
			Str("session_id", sessionID.String()).
			Str("changeset_id", changeset.ID.String()).
			Str("publication_id", publication.ID.String()).
			Msg("failed to queue durable agent publication intent")
		return &PublicationIntentResult{
			Status: ResultBlocked, SessionID: sessionID,
			PublicationID: &publication.ID, Reason: &reason,
		}, nil
	}
	if !queued {
		// QueueChangesetPRCreation is generation- and changeset-idempotent. A
		// concurrent caller already owns the same durable request, so return the
		// state we just ensured instead of reviving the retired PR_IN_FLIGHT 409.
		return ExistingPublicationResult(publication), nil
	}
	c.logger.Info().
		Str("org_id", orgID.String()).
		Str("session_id", sessionID.String()).
		Str("changeset_id", changeset.ID.String()).
		Str("publication_id", publication.ID.String()).
		Str("trigger_kind", string(req.TriggerKind)).
		Msg("agent publication intent queued")
	status := ResultPRQueued
	if reviewRequired && !reviewBypassed {
		status = ResultReviewInProgress
	}
	return &PublicationIntentResult{
		Status: status, SessionID: sessionID, PublicationID: &publication.ID,
		ReviewBypassed: reviewBypassed,
	}, nil
}

// reviewBlocksPublication reports whether a durable publication is stopped on a
// review decision that only a human can resolve. A 'failed' gate is terminal by
// construction on both write paths, so it is not bypassable — the caller
// retries that one, which reopens the intent and reviews it afresh.
func reviewBlocksPublication(publication models.SessionPublication) bool {
	return !publication.State.Terminal() &&
		publication.ReviewGateState == models.SessionPublicationReviewGateNeedsHuman
}

// authorizedDraftBypass reports whether the request itself is the audited
// "Create draft PR" action: an explicit, authenticated, adequately privileged
// user action asking for a draft.
func authorizedDraftBypass(req RequestPullRequest) bool {
	return req.Source == models.SessionPublicationSourceUser &&
		req.TriggerKind == models.SessionPublicationTriggerExplicitAction &&
		req.Draft != nil && *req.Draft && req.RequestedByUserID != nil &&
		(req.RequestedRole == string(models.RoleAdmin) || req.RequestedRole == string(models.RoleMember))
}

func authorizedManualTakeover(req RequestPullRequest) bool {
	return req.Source == models.SessionPublicationSourceUser &&
		req.TriggerKind == models.SessionPublicationTriggerExplicitAction &&
		req.RequestedByUserID != nil &&
		(req.RequestedRole == string(models.RoleAdmin) ||
			req.RequestedRole == string(models.RoleMember) ||
			req.RequestedRole == string(models.RoleBuilder))
}

// retryablePublicationOutcome reports whether a terminal publication may be
// reopened by a newer request. A completed publication has a pull request and
// is final; a no-op or failed one is a dead end the caller can legitimately
// retry once the underlying cause is gone.
func retryablePublicationOutcome(state models.SessionPublicationState) bool {
	return state == models.SessionPublicationStateCompletedNoop ||
		state == models.SessionPublicationStateTerminalFailed
}

// ExistingPublicationResult maps a durable publication onto the caller's view
// of it. ResultBlocked is deliberately not used here: it means "the intent is
// durable but nothing was enqueued", which callers surface as a retryable
// server error. Everything below is a settled state the caller must act on,
// so it carries a reason and maps onto a conflict instead.
func ExistingPublicationResult(publication models.SessionPublication) *PublicationIntentResult {
	result := &PublicationIntentResult{
		SessionID: publication.SessionID, PublicationID: &publication.ID,
		ReviewLoopID: publication.ReviewLoopID, PullRequestURL: publication.GitHubPRURL,
	}
	blocked := func(reason string) *PublicationIntentResult {
		result.Status = ResultManualPublicationRequired
		result.Reason = &reason
		return result
	}
	switch publication.State {
	case models.SessionPublicationStateCompleted:
		result.Status = ResultAlreadyPublished
		return result
	case models.SessionPublicationStateCompletedNoop:
		return blocked("the previous publication completed with nothing to publish")
	case models.SessionPublicationStateTerminalFailed:
		return blocked("publication failed terminally and requires attention")
	}
	switch publication.ReviewGateState {
	case models.SessionPublicationReviewGatePending:
		result.Status = ResultReviewInProgress
	case models.SessionPublicationReviewGateNeedsHuman, models.SessionPublicationReviewGateFailed:
		return blocked("publication review requires attention before the pull request can continue")
	default:
		result.Status = ResultPRQueued
	}
	return result
}

func recordIntentOutcome(
	ctx context.Context,
	source models.SessionPublicationSource,
	result *PublicationIntentResult,
	err error,
) {
	outcome := "error"
	var intentErr *Error
	switch {
	case err != nil && errors.As(err, &intentErr):
		outcome = string(intentErr.Code)
	case err != nil:
	case result != nil:
		outcome = string(result.Status)
	}
	metrics.RecordAgentPRIntent(ctx, string(source), outcome)
}

// unpublishableChangesetStates are the states where the changeset's own
// lifecycle — not the freshness of any captured evidence — means a new pull
// request can never be opened from it.
var unpublishableChangesetStates = map[models.ChangesetStatus]struct{}{
	models.ChangesetStatusMaterializing:          {},
	models.ChangesetStatusNeedsRestack:           {},
	models.ChangesetStatusRestacking:             {},
	models.ChangesetStatusRestackConflict:        {},
	models.ChangesetStatusExternalUpdateDetected: {},
	models.ChangesetStatusMerged:                 {},
	models.ChangesetStatusAbandoned:              {},
	// A changeset whose own status says a pull request is already open must
	// not open a second one, independently of the existing-PR lookup above.
	models.ChangesetStatusPROpen: {},
}

// resolvePublicationTarget names the branch the publication will target.
//
// It deliberately does NOT inspect any captured diff. The agent calls
// create_pr from inside the sandbox while its turn is still running, and every
// server-side diff record is written after the turn ends —
// sessions.latest_diff_snapshot_id via SessionStore.UpdateResult, and
// session_changesets.materialized_diff only for non-primary changesets. Gating
// on that evidence rejects the first turn of every session (no snapshot yet)
// and validates stale evidence on later turns. The open_pr worker captures a
// fresh diff at execution time and already terminates an empty changeset as
// completed_noop, so it stays the authority on whether there is anything to
// publish.
//
// The working branch, by contrast, is durable before the agent starts: the
// orchestrator persists it right after `git checkout -b` (step 8b of RunAgent),
// so requiring one here only rejects a workspace that was never materialized.
func resolvePublicationTarget(
	session models.Session,
	changeset models.SessionChangeset,
) (headBranch string, desiredHeadSHA *string, err error) {
	if _, unpublishable := unpublishableChangesetStates[changeset.Status]; unpublishable {
		return "", nil, &Error{
			Code: ErrorWorkspaceNotReady,
			Err:  fmt.Errorf("primary changeset is %s", changeset.Status),
		}
	}
	headBranch = trimmedPointer(changeset.WorkingBranch)
	if headBranch == "" {
		headBranch = trimmedPointer(session.WorkingBranch)
	}
	if headBranch == "" {
		return "", nil, &Error{
			Code: ErrorWorkspaceNotReady,
			Err:  errors.New("primary changeset has no working branch to publish"),
		}
	}
	// Best-effort only: the worker re-derives the head SHA from the live
	// workspace before pushing, so a missing or stale value here is a
	// bookkeeping detail rather than a reason to refuse the request.
	if sha := trimmedPointer(changeset.HeadSHA); sha != "" {
		desiredHeadSHA = &sha
	}
	return headBranch, desiredHeadSHA, nil
}

func trimmedPointer(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (c *Coordinator) resolvePolicy(ctx context.Context, orgID uuid.UUID, initiatorID *uuid.UUID) (EffectivePolicy, string, error) {
	org, err := c.organizations.GetByID(ctx, orgID)
	if err != nil {
		return EffectivePolicy{}, "", fmt.Errorf("load organization policy: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return EffectivePolicy{}, "", fmt.Errorf("parse organization policy: %w", err)
	}
	var personal *models.AutomaticPRFollowThroughSettings
	initiatorRole := ""
	if initiatorID != nil {
		user, userErr := c.users.GetByIDGlobalWithSettings(ctx, *initiatorID)
		if userErr != nil {
			return EffectivePolicy{}, "", fmt.Errorf("load session initiator policy: %w", userErr)
		}
		if user.OrgID != orgID {
			return EffectivePolicy{}, "", errors.New("session initiator is outside organization scope")
		}
		personal = user.Settings.AutomaticPRFollowThrough
		initiatorRole = string(user.Role)
	}
	return ResolvePolicy(settings.SessionAutomation.AutomaticFollowThrough, personal), initiatorRole, nil
}
