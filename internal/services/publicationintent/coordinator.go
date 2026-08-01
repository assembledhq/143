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
}

type ErrorCode string

const (
	ErrorSessionNotEligible ErrorCode = "SESSION_NOT_PUBLICATION_ELIGIBLE"
	ErrorWorkspaceNotReady  ErrorCode = "WORKSPACE_NOT_READY"
	ErrorPublicationFailed  ErrorCode = "PUBLICATION_INTENT_FAILED"
	ErrorPRInFlight         ErrorCode = "PR_IN_FLIGHT"
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
}

type PullRequestStore interface {
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
	GetByChangeset(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionPublication, error)
}

type RepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type JobStore interface {
	QueueChangesetPRCreation(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, queue string, payload any, priority int) (uuid.UUID, bool, error)
}

type Coordinator struct {
	sessions      SessionStore
	changesets    ChangesetStore
	pullRequests  PullRequestStore
	organizations OrganizationStore
	users         UserStore
	publications  PublicationStore
	jobs          JobStore
	repositories  RepositoryStore
	reviewEnabled bool
	logger        zerolog.Logger
}

func (c *Coordinator) SetRepositoryStore(store RepositoryStore) {
	c.repositories = store
}

func (c *Coordinator) SetReviewEnabled(enabled bool) {
	c.reviewEnabled = enabled
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
		jobs: jobs, reviewEnabled: true, logger: logger,
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

	session, err := c.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	if session.RepositoryID == nil || session.Origin == models.SessionOriginCodeReview ||
		session.Origin == models.SessionOriginRevision || session.AutomationRunID != nil {
		return nil, &Error{Code: ErrorSessionNotEligible, Err: errors.New("session cannot create a new pull request")}
	}
	changeset, err := c.changesets.GetPrimary(ctx, orgID, sessionID)
	if err != nil {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: fmt.Errorf("resolve primary changeset: %w", err)}
	}
	if changeset.MaterializationError != nil || changeset.RestackConfirmationRequired {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: errors.New("primary changeset is not in a publishable state")}
	}
	headBranch, desiredHeadSHA, err := resolvePublicationTarget(session, changeset)
	if err != nil {
		return nil, err
	}
	bypassRequested := req.Source == models.SessionPublicationSourceUser &&
		req.TriggerKind == models.SessionPublicationTriggerExplicitAction &&
		req.Draft != nil && *req.Draft && req.RequestedByUserID != nil &&
		(req.RequestedRole == string(models.RoleAdmin) || req.RequestedRole == string(models.RoleMember))
	existingPublication, publicationErr := c.publications.GetByChangeset(ctx, orgID, sessionID, changeset.ID)
	if publicationErr == nil && !bypassRequested {
		return existingPublicationResult(existingPublication), nil
	}
	if publicationErr != nil && !errors.Is(publicationErr, pgx.ErrNoRows) {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("check existing publication intent: %w", publicationErr)}
	}
	if errors.Is(publicationErr, pgx.ErrNoRows) {
		if _, prErr := c.pullRequests.GetPrimaryBySessionID(ctx, orgID, sessionID); prErr == nil {
			return &PublicationIntentResult{Status: ResultAlreadyPublished, SessionID: sessionID}, nil
		} else if !errors.Is(prErr, pgx.ErrNoRows) {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("check existing pull request: %w", prErr)}
		}
	}

	policy, err := c.resolvePolicy(ctx, orgID, session.TriggeredByUserID)
	if err != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: err}
	}
	if req.TriggerKind == models.SessionPublicationTriggerAgentReady && !policy.CreatePRWhenAgentReady {
		reason := "automatic PR handoff is disabled by effective policy"
		return &PublicationIntentResult{
			Status: ResultManualPublicationRequired, SessionID: sessionID, Reason: &reason,
		}, nil
	}
	handoffMode := models.PRHandoffModePrePublish
	if c.repositories != nil {
		repo, repoErr := c.repositories.GetByID(ctx, orgID, *session.RepositoryID)
		if repoErr != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load repository handoff policy: %w", repoErr)}
		}
		repoSettings, parseErr := models.ParseRepositorySettings(repo.Settings)
		if parseErr != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("parse repository handoff policy: %w", parseErr)}
		}
		handoffMode = repoSettings.PRHandoffMode
	}

	automaticSource := policy.CreatePRSource
	if req.TriggerKind == models.SessionPublicationTriggerExplicitAction {
		automaticSource = models.PublicationPolicySourceExplicitAction
	}
	reviewRequired := c.reviewEnabled && policy.ReviewBeforePR
	reviewBypassed := bypassRequested
	reviewPolicySource := policy.ReviewSource
	if reviewBypassed {
		reviewPolicySource = models.PublicationPolicySourceExplicitBypass
	}
	payload := map[string]any{
		"session_id":                 sessionID.String(),
		"changeset_id":               changeset.ID.String(),
		"org_id":                     orgID.String(),
		"publication_source":         string(req.Source),
		"publication_queue":          string(models.SessionPublicationJobQueueAgent),
		"publication_trigger_kind":   string(req.TriggerKind),
		"publication_handoff_mode":   string(handoffMode),
		"automatic_pr_policy_source": string(automaticSource),
		"review_policy_source":       string(reviewPolicySource),
		"initiated_by_user_id":       session.TriggeredByUserID,
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
		JobQueue:        models.SessionPublicationJobQueueAgent, RequestPayload: encodedPayload,
		RequestGenerationAt: time.Now().UTC(), BaseBranch: changeset.BaseBranch,
		HeadBranch: headBranch, DesiredHeadSHA: desiredHeadSHA,
	}
	if reviewRequired && !reviewBypassed {
		maxPasses := policy.ReviewMaxPasses
		publication.ReviewMaxPasses = &maxPasses
		publication.ReviewGateState = models.SessionPublicationReviewGatePending
	}
	bypassIntent := publication
	if ensureErr := c.publications.EnsureRequested(ctx, orgID, &publication); ensureErr != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("persist publication intent: %w", ensureErr)}
	}
	if reviewBypassed {
		bypassIntent.ID = publication.ID
		if bypassErr := c.publications.ApplyReviewBypass(ctx, orgID, &bypassIntent); bypassErr != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("persist publication review bypass: %w", bypassErr)}
		}
		publication = bypassIntent
	}
	_, queued, queueErr := c.jobs.QueueChangesetPRCreation(ctx, orgID, sessionID, changeset.ID, "agent", payload, 5)
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
		return nil, &Error{Code: ErrorPRInFlight, Err: errors.New("publication was concurrently queued")}
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

func existingPublicationResult(publication models.SessionPublication) *PublicationIntentResult {
	result := &PublicationIntentResult{
		SessionID: publication.SessionID, PublicationID: &publication.ID,
		ReviewLoopID: publication.ReviewLoopID, PullRequestURL: publication.GitHubPRURL,
	}
	if publication.State == models.SessionPublicationStateCompleted ||
		publication.State == models.SessionPublicationStateCompletedNoop {
		result.Status = ResultAlreadyPublished
		return result
	}
	if publication.State == models.SessionPublicationStateTerminalFailed {
		result.Status = ResultBlocked
		reason := "publication failed terminally and requires attention"
		result.Reason = &reason
		return result
	}
	switch publication.ReviewGateState {
	case models.SessionPublicationReviewGatePending:
		result.Status = ResultReviewInProgress
	case models.SessionPublicationReviewGateNeedsHuman, models.SessionPublicationReviewGateFailed:
		result.Status = ResultBlocked
		reason := "publication review requires attention before the pull request can continue"
		result.Reason = &reason
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

func (c *Coordinator) resolvePolicy(ctx context.Context, orgID uuid.UUID, initiatorID *uuid.UUID) (EffectivePolicy, error) {
	org, err := c.organizations.GetByID(ctx, orgID)
	if err != nil {
		return EffectivePolicy{}, fmt.Errorf("load organization policy: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return EffectivePolicy{}, fmt.Errorf("parse organization policy: %w", err)
	}
	var personal *models.AutomaticPRFollowThroughSettings
	if initiatorID != nil {
		user, userErr := c.users.GetByIDGlobalWithSettings(ctx, *initiatorID)
		if userErr != nil {
			return EffectivePolicy{}, fmt.Errorf("load session initiator policy: %w", userErr)
		}
		if user.OrgID != orgID {
			return EffectivePolicy{}, errors.New("session initiator is outside organization scope")
		}
		personal = user.Settings.AutomaticPRFollowThrough
	}
	return ResolvePolicy(settings.SessionAutomation.AutomaticFollowThrough, personal), nil
}
