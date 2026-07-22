package codereview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type PolicyStore interface {
	ResolvePolicy(ctx context.Context, orgID uuid.UUID) (models.CodeReviewResolvedPolicy, error)
	SavePolicy(ctx context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error)
}

type MetadataStore interface {
	CreateSessionMetadata(ctx context.Context, metadata *models.CodeReviewSessionMetadata) error
	GetByOutputKey(ctx context.Context, orgID uuid.UUID, outputKey string) (models.CodeReviewSessionMetadata, error)
	GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	GetLatestByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	GetLatestSubmittedByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	HasApprovedByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (bool, error)
	GetLatestByPullRequestHead(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	FailReview(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error)
	FailReviewWithStatus(ctx context.Context, orgID uuid.UUID, params db.FailCodeReviewParams) (models.CodeReviewSessionMetadata, error)
	MarkSupersededBy(ctx context.Context, orgID, sessionID, replacementSessionID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) (int64, error)
}

type PullRequestStore interface {
	GetByID(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.PullRequest, error)
	GetHealthCurrent(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.PullRequestHealthCurrent, error)
}

type PullRequestSyncer interface {
	SyncPullRequestState(ctx context.Context, orgID, pullRequestID uuid.UUID) error
}

type SessionStore interface {
	Create(ctx context.Context, session *models.Session) error
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
	UpdateFailure(ctx context.Context, orgID, sessionID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
}

type JobStore interface {
	EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
	HasActiveByDedupeKey(ctx context.Context, orgID uuid.UUID, queue, dedupeKey string) (bool, error)
}

const (
	codeReviewJobMaxAttempts        = 8
	codeReviewJobEnqueueGracePeriod = time.Minute
)

type Service struct {
	policies          PolicyStore
	metadata          MetadataStore
	sessions          SessionStore
	jobs              JobStore
	triggers          GitHubTriggerStore
	pullRequests      PullRequestStore
	pullRequestSyncer PullRequestSyncer
	logger            zerolog.Logger
	cfg               Config
}

type Config struct {
	AppReviewerLogins []string
	AliasLogins       []string
	TeamSlugs         []string
}

type ReviewRequestedInput struct {
	OrgID             uuid.UUID
	RepositoryID      uuid.UUID
	PullRequestID     uuid.UUID
	GitHubRepo        string
	GitHubPRNumber    int
	GitHubPRURL       string
	PullRequestTitle  string
	PullRequestAuthor string
	BaseSHA           string
	HeadSHA           string
	FromFork          bool
	RequestedLogin    string
	RequestedTeam     string
}

// ReviewChangedInput describes a new externally-observed state for a pull
// request that has already been assigned to 143 Code Reviewer. ChangeKey must
// be stable for the material code-head state so equivalent webhook
// deliveries reuse the same assessment.
type ReviewChangedInput struct {
	OrgID             uuid.UUID `json:"org_id"`
	RepositoryID      uuid.UUID `json:"repository_id"`
	PullRequestID     uuid.UUID `json:"pull_request_id"`
	PriorSessionID    uuid.UUID `json:"prior_session_id"`
	GitHubRepo        string    `json:"github_repo"`
	GitHubPRNumber    int       `json:"github_pr_number"`
	GitHubPRURL       string    `json:"github_pr_url"`
	PullRequestTitle  string    `json:"pull_request_title"`
	PullRequestAuthor string    `json:"pull_request_author,omitempty"`
	BaseSHA           string    `json:"base_sha"`
	HeadSHA           string    `json:"head_sha"`
	FromFork          bool      `json:"from_fork"`
	ChangeKey         string    `json:"change_key"`
	ChangeReason      string    `json:"change_reason"`
}

type ReviewRequestedResult struct {
	Processed         bool
	Reused            bool
	Deferred          bool
	DispatchConfirmed bool
	SessionID         uuid.UUID
	MetadataID        uuid.UUID
	JobID             uuid.UUID
	TriggerSource     models.CodeReviewTriggerSource
	IgnoredReason     string
}

type RetryReviewInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
}

type RetryReviewResult struct {
	PreviousSessionID uuid.UUID `json:"previous_session_id"`
	SessionID         uuid.UUID `json:"session_id"`
	MetadataID        uuid.UUID `json:"metadata_id"`
	JobID             uuid.UUID `json:"job_id"`
}

type RetryReviewConflictCode string

const (
	RetryReviewConflictCompleted    RetryReviewConflictCode = "completed"
	RetryReviewConflictSuperseded   RetryReviewConflictCode = "superseded"
	RetryReviewConflictNotRetryable RetryReviewConflictCode = "not_retryable"
	RetryReviewConflictNewerAttempt RetryReviewConflictCode = "newer_attempt"
	RetryReviewConflictHeadChanged  RetryReviewConflictCode = "head_changed"
	RetryReviewConflictPRClosed     RetryReviewConflictCode = "pull_request_closed"
	RetryReviewConflictPolicyOff    RetryReviewConflictCode = "policy_disabled"
)

type RetryReviewConflictError struct {
	Code    RetryReviewConflictCode
	Message string
}

func (e *RetryReviewConflictError) Error() string { return e.Message }

type RunCodeReviewJobPayload struct {
	OrgID                   uuid.UUID `json:"org_id"`
	SessionID               uuid.UUID `json:"session_id"`
	MetadataID              uuid.UUID `json:"metadata_id"`
	RepositoryID            uuid.UUID `json:"repository_id"`
	PullRequestID           uuid.UUID `json:"pull_request_id"`
	PolicyID                uuid.UUID `json:"policy_id"`
	PolicyVersion           int       `json:"policy_version"`
	HeadSHA                 string    `json:"head_sha"`
	FromFork                bool      `json:"from_fork"`
	PullRequestAuthor       string    `json:"pull_request_author,omitempty"`
	OutputKey               string    `json:"review_output_key"`
	RequestedReviewerLogin  string    `json:"requested_reviewer_login,omitempty"`
	RequestedTeamSlug       string    `json:"requested_team_slug,omitempty"`
	PreviousOutputKey       string    `json:"previous_review_output_key,omitempty"`
	ExistingGitHubReviewID  *int64    `json:"existing_github_review_id,omitempty"`
	ExistingGitHubReviewURL *string   `json:"existing_github_review_url,omitempty"`
}

type reviewStartOptions struct {
	triggerSource           models.CodeReviewTriggerSource
	forceReassessment       bool
	changeKey               string
	changeReason            string
	previousOutputKey       string
	existingGitHubReviewID  *int64
	existingGitHubReviewURL *string
}

func NewService(policies PolicyStore, metadata MetadataStore, sessions SessionStore, jobs JobStore, logger zerolog.Logger, cfg Config) *Service {
	return &Service{
		policies: policies,
		metadata: metadata,
		sessions: sessions,
		jobs:     jobs,
		logger:   logger,
		cfg:      normalizeConfig(cfg),
	}
}

func (s *Service) SetGitHubTriggerStore(triggers GitHubTriggerStore) {
	s.triggers = triggers
}

func (s *Service) SetRetryDependencies(pullRequests PullRequestStore, syncer PullRequestSyncer) {
	s.pullRequests = pullRequests
	s.pullRequestSyncer = syncer
}

// RetryReview creates a replacement attempt for a terminal retryable failure.
// It never mutates the terminal status of the selected attempt. Concurrent
// requests converge through startReview's deterministic retry output key, then
// compare-and-set the old row's supersession link to that winner.
func (s *Service) RetryReview(ctx context.Context, input RetryReviewInput) (RetryReviewResult, error) {
	if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil {
		return RetryReviewResult{}, fmt.Errorf("org_id and session_id are required")
	}
	if s.pullRequests == nil || s.pullRequestSyncer == nil {
		return RetryReviewResult{}, fmt.Errorf("code review retry dependencies are unavailable")
	}

	failed, err := s.metadata.GetBySessionID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return RetryReviewResult{}, fmt.Errorf("load code review retry source: %w", err)
	}
	if err := validateRetrySource(failed); err != nil {
		return RetryReviewResult{}, err
	}
	approved, err := s.metadata.HasApprovedByPullRequest(ctx, input.OrgID, failed.PullRequestID)
	if err != nil {
		return RetryReviewResult{}, fmt.Errorf("check prior code review approval before retry: %w", err)
	}
	if approved {
		return RetryReviewResult{}, &RetryReviewConflictError{
			Code: RetryReviewConflictCompleted, Message: "This pull request already has a completed approval.",
		}
	}
	if err := s.ensureLatestRetrySource(ctx, failed); err != nil {
		return RetryReviewResult{}, err
	}

	if err := s.pullRequestSyncer.SyncPullRequestState(ctx, input.OrgID, failed.PullRequestID); err != nil && !errors.Is(err, ghservice.ErrPullRequestMergeabilityPending) {
		return RetryReviewResult{}, fmt.Errorf("refresh pull request before code review retry: %w", err)
	}
	pr, err := s.pullRequests.GetByID(ctx, input.OrgID, failed.PullRequestID)
	if err != nil {
		return RetryReviewResult{}, fmt.Errorf("load refreshed pull request before code review retry: %w", err)
	}
	if pr.Status != models.PullRequestStatusOpen {
		return RetryReviewResult{}, &RetryReviewConflictError{
			Code: RetryReviewConflictPRClosed, Message: "This pull request is no longer open.",
		}
	}
	health, err := s.pullRequests.GetHealthCurrent(ctx, input.OrgID, failed.PullRequestID)
	if err != nil {
		return RetryReviewResult{}, fmt.Errorf("load refreshed pull request head before code review retry: %w", err)
	}
	currentHead := strings.TrimSpace(health.HeadSHA)
	if currentHead == "" || currentHead != strings.TrimSpace(failed.HeadSHA) {
		return RetryReviewResult{}, &RetryReviewConflictError{
			Code: RetryReviewConflictHeadChanged, Message: "The pull request head changed; wait for or request a review of the current commit.",
		}
	}
	if err := s.ensureLatestRetrySource(ctx, failed); err != nil {
		return RetryReviewResult{}, err
	}

	priorSession, err := s.sessions.GetByID(ctx, input.OrgID, failed.SessionID)
	if err != nil {
		return RetryReviewResult{}, fmt.Errorf("load failed code review session: %w", err)
	}
	requested := ReviewRequestedInput{
		OrgID:             input.OrgID,
		RepositoryID:      failed.RepositoryID,
		PullRequestID:     failed.PullRequestID,
		GitHubRepo:        pr.GitHubRepo,
		GitHubPRNumber:    pr.GitHubPRNumber,
		GitHubPRURL:       pr.GitHubPRURL,
		PullRequestTitle:  pr.Title,
		PullRequestAuthor: codeReviewRevisionContextString(priorSession.RevisionContext, "pull_request_author"),
		BaseSHA:           strings.TrimSpace(health.BaseSHA),
		HeadSHA:           currentHead,
		FromFork:          failed.FromFork,
		RequestedLogin:    codeReviewRevisionContextString(priorSession.RevisionContext, "requested_reviewer_login"),
		RequestedTeam:     codeReviewRevisionContextString(priorSession.RevisionContext, "requested_team_slug"),
	}
	submitted := failed
	if submitted.GitHubReviewID == nil {
		submitted, err = s.metadata.GetLatestSubmittedByPullRequest(ctx, input.OrgID, failed.PullRequestID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return RetryReviewResult{}, fmt.Errorf("load submitted code review before retry: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			submitted = models.CodeReviewSessionMetadata{}
		}
	}
	started, err := s.startReview(ctx, requested, reviewStartOptions{
		triggerSource:           failed.TriggerSource,
		previousOutputKey:       submitted.ReviewOutputKey,
		existingGitHubReviewID:  submitted.GitHubReviewID,
		existingGitHubReviewURL: submitted.GitHubReviewURL,
	})
	if err != nil {
		if started.SessionID != uuid.Nil {
			if linkErr := s.linkRetryReplacement(ctx, failed, started.SessionID); linkErr != nil {
				return retryReviewResult(failed.SessionID, started), errors.Join(err, linkErr)
			}
			return retryReviewResult(failed.SessionID, started), err
		}
		return RetryReviewResult{}, err
	}
	if started.IgnoredReason == "policy_disabled" {
		return RetryReviewResult{}, &RetryReviewConflictError{
			Code: RetryReviewConflictPolicyOff, Message: "Code review is disabled by the current policy.",
		}
	}
	if !started.Processed || started.SessionID == uuid.Nil {
		return RetryReviewResult{}, fmt.Errorf("code review retry did not create a replacement attempt")
	}
	if !started.DispatchConfirmed {
		return RetryReviewResult{}, &RetryReviewConflictError{
			Code: RetryReviewConflictNewerAttempt, Message: "A replacement attempt is still being queued.",
		}
	}
	if err := s.linkRetryReplacement(ctx, failed, started.SessionID); err != nil {
		return RetryReviewResult{}, err
	}
	return retryReviewResult(failed.SessionID, started), nil
}

func (s *Service) linkRetryReplacement(ctx context.Context, failed models.CodeReviewSessionMetadata, replacementSessionID uuid.UUID) error {
	if _, err := s.metadata.MarkSupersededBy(ctx, failed.OrgID, failed.SessionID, replacementSessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, getErr := s.metadata.GetBySessionID(ctx, failed.OrgID, failed.SessionID)
			if getErr == nil && current.SupersededBySessionID != nil && *current.SupersededBySessionID == replacementSessionID {
				return nil
			}
			return &RetryReviewConflictError{
				Code: RetryReviewConflictSuperseded, Message: "This failed review already has a replacement attempt.",
			}
		}
		return fmt.Errorf("link failed code review to replacement: %w", err)
	}
	return nil
}

func validateRetrySource(metadata models.CodeReviewSessionMetadata) error {
	if metadata.Status == models.CodeReviewSessionStatusCompleted {
		return &RetryReviewConflictError{Code: RetryReviewConflictCompleted, Message: "Completed code reviews cannot be retried."}
	}
	if metadata.SupersededBySessionID != nil {
		return &RetryReviewConflictError{Code: RetryReviewConflictSuperseded, Message: "This failed review already has a replacement attempt."}
	}
	if metadata.Status != models.CodeReviewSessionStatusFailed || !metadata.RetryableFailure {
		return &RetryReviewConflictError{Code: RetryReviewConflictNotRetryable, Message: "Only terminal retryable code review failures can be retried."}
	}
	return nil
}

func (s *Service) ensureLatestRetrySource(ctx context.Context, source models.CodeReviewSessionMetadata) error {
	latest, err := s.metadata.GetLatestByPullRequest(ctx, source.OrgID, source.PullRequestID)
	if err != nil {
		return fmt.Errorf("load latest code review attempt: %w", err)
	}
	if latest.SessionID == source.SessionID {
		return nil
	}
	if latest.Status == models.CodeReviewSessionStatusQueued || latest.Status == models.CodeReviewSessionStatusRunning {
		outputKey := strings.TrimSpace(latest.ReviewOutputKey)
		active, activeErr := s.jobs.HasActiveByDedupeKey(ctx, source.OrgID, "agent", "code_review:"+outputKey)
		if activeErr != nil {
			return fmt.Errorf("check newer code review dispatch: %w", activeErr)
		}
		if !active && (latest.CreatedAt.IsZero() || time.Since(latest.CreatedAt) >= codeReviewJobEnqueueGracePeriod) {
			const reason = "code review replacement job was not queued"
			failed, failErr := s.metadata.FailReviewWithStatus(ctx, source.OrgID, db.FailCodeReviewParams{
				SessionID: latest.SessionID,
				Reason:    reason,
				Code:      models.CodeReviewStatusCodeWorkerFailed,
				Message:   "The replacement review was not queued. Retry this attempt to continue.",
				Retryable: true,
			})
			if failErr != nil && !errors.Is(failErr, pgx.ErrNoRows) {
				return fmt.Errorf("recover unqueued code review replacement: %w", failErr)
			}
			if failErr == nil {
				s.reconcileStrandedSession(ctx, source.OrgID, failed.SessionID, reason)
				if linkErr := s.linkRetryReplacement(ctx, source, failed.SessionID); linkErr != nil {
					return linkErr
				}
			}
		}
	}
	return &RetryReviewConflictError{Code: RetryReviewConflictNewerAttempt, Message: "A newer code review attempt already exists."}
}

func retryReviewResult(previousSessionID uuid.UUID, started ReviewRequestedResult) RetryReviewResult {
	return RetryReviewResult{
		PreviousSessionID: previousSessionID,
		SessionID:         started.SessionID,
		MetadataID:        started.MetadataID,
		JobID:             started.JobID,
	}
}

func (s *Service) HandleReviewRequested(ctx context.Context, input ReviewRequestedInput) (ReviewRequestedResult, error) {
	if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil || input.PullRequestID == uuid.Nil {
		return ReviewRequestedResult{}, fmt.Errorf("org_id, repository_id, and pull_request_id are required")
	}
	if strings.TrimSpace(input.HeadSHA) == "" {
		return ReviewRequestedResult{}, fmt.Errorf("head_sha is required")
	}
	source, ok, err := s.matchRequestedReviewer(ctx, input)
	if err != nil {
		return ReviewRequestedResult{}, err
	}
	if !ok {
		return ReviewRequestedResult{IgnoredReason: "reviewer_not_configured"}, nil
	}
	approved, err := s.metadata.HasApprovedByPullRequest(ctx, input.OrgID, input.PullRequestID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("check prior code review approval: %w", err)
	}
	if approved {
		return ReviewRequestedResult{IgnoredReason: "already_approved", TriggerSource: source}, nil
	}
	return s.startReview(ctx, input, reviewStartOptions{triggerSource: source})
}

// QueueReviewChanged durably records a pass-relevant webhook change. The
// starter job waits for any older assessment to finish, so a change cannot be
// acknowledged and then lost merely because reviewer agents are still active.
func (s *Service) QueueReviewChanged(ctx context.Context, input ReviewChangedInput) (ReviewRequestedResult, error) {
	if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil || input.PullRequestID == uuid.Nil {
		return ReviewRequestedResult{}, fmt.Errorf("org_id, repository_id, and pull_request_id are required")
	}
	if strings.TrimSpace(input.HeadSHA) == "" {
		return ReviewRequestedResult{}, fmt.Errorf("head_sha is required")
	}
	if strings.TrimSpace(input.ChangeKey) == "" {
		return ReviewRequestedResult{}, fmt.Errorf("change_key is required")
	}
	approved, err := s.metadata.HasApprovedByPullRequest(ctx, input.OrgID, input.PullRequestID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("check prior code review approval before queueing reassessment: %w", err)
	}
	if approved {
		return ReviewRequestedResult{IgnoredReason: "already_approved"}, nil
	}
	latest, err := s.metadata.GetLatestByPullRequest(ctx, input.OrgID, input.PullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{IgnoredReason: "review_not_previously_requested"}, nil
	}
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("load current code review before queueing reassessment: %w", err)
	}
	input.PriorSessionID = latest.SessionID
	dedupeKey := fmt.Sprintf("code_review_reassessment:%s:%s", input.PullRequestID, strings.TrimSpace(input.ChangeKey))
	jobID, err := s.jobs.EnqueueWithOpts(ctx, input.OrgID, db.EnqueueOpts{
		Queue:       "agent",
		JobType:     models.JobTypeStartCodeReviewReassessment,
		Payload:     input,
		Priority:    5,
		DedupeKey:   &dedupeKey,
		MaxAttempts: codeReviewJobMaxAttempts,
	})
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("enqueue code review reassessment starter: %w", err)
	}
	return ReviewRequestedResult{Processed: true, JobID: jobID}, nil
}

// HandleReviewChanged recomputes the recommendation for a PR that previously
// requested 143 Code Reviewer. PRs without review history are intentionally
// ignored so ordinary repository webhook traffic does not become always-on
// code review.
func (s *Service) HandleReviewChanged(ctx context.Context, input ReviewChangedInput) (ReviewRequestedResult, error) {
	if input.OrgID == uuid.Nil || input.RepositoryID == uuid.Nil || input.PullRequestID == uuid.Nil {
		return ReviewRequestedResult{}, fmt.Errorf("org_id, repository_id, and pull_request_id are required")
	}
	if strings.TrimSpace(input.HeadSHA) == "" {
		return ReviewRequestedResult{}, fmt.Errorf("head_sha is required")
	}
	approved, err := s.metadata.HasApprovedByPullRequest(ctx, input.OrgID, input.PullRequestID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("check prior code review approval before reassessment: %w", err)
	}
	if approved {
		return ReviewRequestedResult{IgnoredReason: "already_approved"}, nil
	}
	latest, err := s.metadata.GetLatestByPullRequest(ctx, input.OrgID, input.PullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{IgnoredReason: "review_not_previously_requested"}, nil
	}
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("load latest code review for reassessment: %w", err)
	}
	if input.PriorSessionID != uuid.Nil && latest.SessionID != input.PriorSessionID {
		switch latest.Status {
		case models.CodeReviewSessionStatusQueued, models.CodeReviewSessionStatusRunning, models.CodeReviewSessionStatusCompleted:
			return ReviewRequestedResult{
				Processed: true, Reused: true, SessionID: latest.SessionID, MetadataID: latest.ID,
				TriggerSource: latest.TriggerSource, IgnoredReason: "change_already_reassessed",
			}, nil
		}
	}
	if strings.TrimSpace(input.PullRequestAuthor) == "" {
		priorSession, sessionErr := s.sessions.GetByID(ctx, input.OrgID, latest.SessionID)
		if sessionErr != nil {
			return ReviewRequestedResult{}, fmt.Errorf("load prior code review session for reassessment: %w", sessionErr)
		}
		input.PullRequestAuthor = codeReviewRevisionContextString(priorSession.RevisionContext, "pull_request_author")
	}
	input.FromFork = input.FromFork || latest.FromFork
	submitted := latest
	if submitted.GitHubReviewID == nil {
		submitted, err = s.metadata.GetLatestSubmittedByPullRequest(ctx, input.OrgID, input.PullRequestID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ReviewRequestedResult{}, fmt.Errorf("load submitted code review for reassessment: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			submitted = models.CodeReviewSessionMetadata{}
		}
	}
	requested := ReviewRequestedInput{
		OrgID:             input.OrgID,
		RepositoryID:      input.RepositoryID,
		PullRequestID:     input.PullRequestID,
		GitHubRepo:        input.GitHubRepo,
		GitHubPRNumber:    input.GitHubPRNumber,
		GitHubPRURL:       input.GitHubPRURL,
		PullRequestTitle:  input.PullRequestTitle,
		PullRequestAuthor: input.PullRequestAuthor,
		BaseSHA:           input.BaseSHA,
		HeadSHA:           input.HeadSHA,
		FromFork:          input.FromFork,
	}
	return s.startReview(ctx, requested, reviewStartOptions{
		triggerSource:           latest.TriggerSource,
		forceReassessment:       true,
		changeKey:               input.ChangeKey,
		changeReason:            input.ChangeReason,
		previousOutputKey:       submitted.ReviewOutputKey,
		existingGitHubReviewID:  submitted.GitHubReviewID,
		existingGitHubReviewURL: submitted.GitHubReviewURL,
	})
}

func codeReviewRevisionContextString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func (s *Service) startReview(ctx context.Context, input ReviewRequestedInput, opts reviewStartOptions) (ReviewRequestedResult, error) {
	source := opts.triggerSource

	resolved, err := s.policies.ResolvePolicy(ctx, input.OrgID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("resolve code review policy: %w", err)
	}
	policy := resolved.Policy
	if policy == nil {
		record, err := s.policies.SavePolicy(ctx, input.OrgID, resolved.Config, nil)
		if err != nil {
			return ReviewRequestedResult{}, fmt.Errorf("materialize default code review policy: %w", err)
		}
		policy = &record
	}
	if !resolved.Config.Enabled {
		return ReviewRequestedResult{IgnoredReason: "policy_disabled", TriggerSource: source}, nil
	}
	outputKey := StableOutputKey(input.PullRequestID, input.HeadSHA, policy.ID, policy.Version)
	if opts.forceReassessment && strings.TrimSpace(opts.changeKey) != "" {
		outputKey = reassessmentOutputKey(outputKey, opts.changeKey)
		existing, existingErr := s.metadata.GetByOutputKey(ctx, input.OrgID, outputKey)
		if existingErr == nil {
			return reusedReviewRequestedResult(existing, source), nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return ReviewRequestedResult{}, fmt.Errorf("lookup code review reassessment by output key: %w", existingErr)
		}
	}
	latest, err := s.metadata.GetLatestByPullRequestHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, policy.ID)
	if err == nil {
	resolveLatest:
		for {
			switch latest.Status {
			case models.CodeReviewSessionStatusCompleted:
				if !opts.forceReassessment {
					return reusedReviewRequestedResult(latest, source), nil
				}
				break resolveLatest
			case models.CodeReviewSessionStatusQueued, models.CodeReviewSessionStatusRunning:
				activeOutputKey := strings.TrimSpace(latest.ReviewOutputKey)
				if activeOutputKey == "" {
					activeOutputKey = outputKey
				}
				active, activeErr := s.jobs.HasActiveByDedupeKey(ctx, input.OrgID, "agent", "code_review:"+activeOutputKey)
				if activeErr != nil {
					return ReviewRequestedResult{}, fmt.Errorf("lookup active code review job: %w", activeErr)
				}
				if active {
					result := reusedReviewRequestedResult(latest, source)
					result.Deferred = opts.forceReassessment
					result.DispatchConfirmed = true
					return result, nil
				}
				if !latest.CreatedAt.IsZero() && time.Since(latest.CreatedAt) < codeReviewJobEnqueueGracePeriod {
					result := reusedReviewRequestedResult(latest, source)
					result.Deferred = opts.forceReassessment
					return result, nil
				}

				const reason = "code review job is no longer active; replaced by a new reviewer request"
				failed, failErr := s.metadata.FailReview(ctx, input.OrgID, latest.SessionID, reason)
				if errors.Is(failErr, pgx.ErrNoRows) {
					// Another request changed the latest attempt after our lookup.
					// Re-run the complete state check so a newly queued winner gets
					// the active-job and enqueue-grace protections above.
					latest, failErr = s.metadata.GetLatestByPullRequestHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, policy.ID)
					if failErr != nil {
						return ReviewRequestedResult{}, fmt.Errorf("reload code review after concurrent takeover: %w", failErr)
					}
					continue
				}
				if failErr != nil {
					return ReviewRequestedResult{}, fmt.Errorf("fail stranded code review: %w", failErr)
				}
				s.reconcileStrandedSession(ctx, input.OrgID, failed.SessionID, reason)
				latest = failed
				break resolveLatest
			case models.CodeReviewSessionStatusFailed, models.CodeReviewSessionStatusStale, models.CodeReviewSessionStatusCancelled:
				// A reviewer rerequest is an explicit request for another attempt.
				// Derive the next key from the latest terminal row so concurrent
				// rerequests collapse onto the same replacement attempt.
				break resolveLatest
			default:
				return ReviewRequestedResult{}, fmt.Errorf("unsupported existing code review status %q", latest.Status)
			}
		}
		if !opts.forceReassessment || strings.TrimSpace(opts.changeKey) == "" {
			outputKey = retryOutputKey(outputKey, latest.ID)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{}, fmt.Errorf("lookup latest code review: %w", err)
	}

	title := fmt.Sprintf("Code review for %s#%d", input.GitHubRepo, input.GitHubPRNumber)
	modelOverride := resolved.Config.AgentRoster.OrchestratorModel
	reasoningEffort := resolved.Config.AgentRoster.ReasoningEffort
	revisionContext, err := json.Marshal(map[string]any{
		"kind":                     "code_review",
		"github_repo":              input.GitHubRepo,
		"github_pr_number":         input.GitHubPRNumber,
		"github_pr_url":            input.GitHubPRURL,
		"pull_request_title":       input.PullRequestTitle,
		"pull_request_author":      input.PullRequestAuthor,
		"base_sha":                 input.BaseSHA,
		"head_sha":                 input.HeadSHA,
		"from_fork":                input.FromFork,
		"requested_reviewer_login": strings.TrimSpace(input.RequestedLogin),
		"requested_team_slug":      strings.TrimSpace(input.RequestedTeam),
		"policy_id":                policy.ID,
		"policy_version":           policy.Version,
		"trigger_source":           source,
		"change_reason":            strings.TrimSpace(opts.changeReason),
	})
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("marshal code review revision context: %w", err)
	}
	session := &models.Session{
		OrgID:            input.OrgID,
		Origin:           models.SessionOriginCodeReview,
		InteractionMode:  models.SessionInteractionModeSingleRun,
		ValidationPolicy: models.SessionValidationPolicySkip,
		AgentType:        resolved.Config.AgentRoster.Orchestrator,
		ModelOverride:    modelOverride,
		ReasoningEffort:  &reasoningEffort,
		Status:           models.SessionStatusIdle,
		AutonomyLevel:    models.SessionAutonomySupervised,
		TokenMode:        models.DefaultSessionTokenMode,
		RepositoryID:     &input.RepositoryID,
		BaseCommitSHA:    &input.HeadSHA,
		RevisionContext:  revisionContext,
		Title:            &title,
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("create code review session: %w", err)
	}

	metadata := &models.CodeReviewSessionMetadata{
		OrgID:           input.OrgID,
		SessionID:       session.ID,
		RepositoryID:    input.RepositoryID,
		PullRequestID:   input.PullRequestID,
		PolicyID:        policy.ID,
		BaseSHA:         input.BaseSHA,
		HeadSHA:         input.HeadSHA,
		FromFork:        input.FromFork,
		TriggerSource:   source,
		Status:          models.CodeReviewSessionStatusQueued,
		Phase:           codeReviewPhasePtr(models.CodeReviewPhaseSyncingGitHub),
		ReviewOutputKey: outputKey,
	}
	if err := s.metadata.CreateSessionMetadata(ctx, metadata); err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("create code review metadata: %w", err)
	}
	if metadata.SessionID != session.ID {
		if _, staleErr := s.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, &metadata.SessionID); staleErr != nil {
			return ReviewRequestedResult{}, staleErr
		}
		return ReviewRequestedResult{Processed: true, Reused: true, SessionID: metadata.SessionID, MetadataID: metadata.ID, TriggerSource: source}, nil
	}
	if _, err := s.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, &session.ID); err != nil {
		return ReviewRequestedResult{}, err
	}

	payload := RunCodeReviewJobPayload{
		OrgID:                   input.OrgID,
		SessionID:               session.ID,
		MetadataID:              metadata.ID,
		RepositoryID:            input.RepositoryID,
		PullRequestID:           input.PullRequestID,
		PolicyID:                policy.ID,
		PolicyVersion:           policy.Version,
		HeadSHA:                 input.HeadSHA,
		FromFork:                input.FromFork,
		PullRequestAuthor:       strings.TrimSpace(input.PullRequestAuthor),
		OutputKey:               outputKey,
		RequestedReviewerLogin:  input.RequestedLogin,
		RequestedTeamSlug:       input.RequestedTeam,
		PreviousOutputKey:       opts.previousOutputKey,
		ExistingGitHubReviewID:  opts.existingGitHubReviewID,
		ExistingGitHubReviewURL: opts.existingGitHubReviewURL,
	}
	dedupeKey := "code_review:" + outputKey
	jobID, err := s.jobs.EnqueueWithOpts(ctx, input.OrgID, db.EnqueueOpts{
		Queue:       "agent",
		JobType:     models.JobTypeRunCodeReview,
		Payload:     payload,
		Priority:    5,
		DedupeKey:   &dedupeKey,
		MaxAttempts: codeReviewJobMaxAttempts,
	})
	if err != nil {
		reason := fmt.Sprintf("enqueue code review job: %v", err)
		if _, failErr := s.metadata.FailReviewWithStatus(ctx, input.OrgID, db.FailCodeReviewParams{
			SessionID: session.ID,
			Reason:    reason,
			Code:      models.CodeReviewStatusCodeWorkerFailed,
			Message:   "The review could not be queued. Retry the review to start a fresh attempt.",
			Retryable: true,
		}); failErr != nil {
			return ReviewRequestedResult{}, errors.Join(
				fmt.Errorf("enqueue code review job: %w", err),
				fmt.Errorf("mark unqueued code review retryable: %w", failErr),
			)
		}
		s.reconcileStrandedSession(ctx, input.OrgID, session.ID, reason)
		return ReviewRequestedResult{
			Processed: true, SessionID: session.ID, MetadataID: metadata.ID, TriggerSource: source,
		}, fmt.Errorf("enqueue code review job: %w", err)
	}
	return ReviewRequestedResult{
		Processed:         true,
		DispatchConfirmed: true,
		SessionID:         session.ID,
		MetadataID:        metadata.ID,
		JobID:             jobID,
		TriggerSource:     source,
	}, nil
}

func codeReviewPhasePtr(phase models.CodeReviewPhase) *models.CodeReviewPhase {
	return &phase
}

func (s *Service) reconcileStrandedSession(ctx context.Context, orgID, sessionID uuid.UUID, reason string) {
	if err := s.sessions.UpdateStatus(ctx, orgID, sessionID, models.SessionStatusFailed); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to mark stranded code review session failed")
		return
	}
	if err := s.sessions.UpdateFailure(ctx, orgID, sessionID, reason, "code_review_job_failed",
		[]string{"Request the code reviewer again to start a fresh attempt."}, true); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to record stranded code review session failure")
	}
}

func StableOutputKey(pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID, policyVersion int) string {
	return fmt.Sprintf("pr:%s:head:%s:policy:%s:v%d", pullRequestID, headSHA, policyID, policyVersion)
}

func retryOutputKey(base string, previousAttemptID uuid.UUID) string {
	return fmt.Sprintf("%s:retry:%s", base, previousAttemptID)
}

func reassessmentOutputKey(base, changeKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(changeKey)))
	return fmt.Sprintf("%s:change:%x", base, sum[:])
}

func reusedReviewRequestedResult(metadata models.CodeReviewSessionMetadata, source models.CodeReviewTriggerSource) ReviewRequestedResult {
	return ReviewRequestedResult{
		Processed:     true,
		Reused:        true,
		SessionID:     metadata.SessionID,
		MetadataID:    metadata.ID,
		TriggerSource: source,
	}
}

func (s *Service) matchRequestedReviewer(ctx context.Context, input ReviewRequestedInput) (models.CodeReviewTriggerSource, bool, error) {
	login := strings.ToLower(strings.TrimSpace(input.RequestedLogin))
	team := strings.ToLower(strings.TrimSpace(input.RequestedTeam))
	if team != "" && s.triggers != nil {
		trigger, err := s.triggers.GetActiveGitHubTrigger(ctx, input.OrgID, input.RepositoryID)
		if err == nil && strings.EqualFold(strings.TrimSpace(trigger.TeamSlug), team) {
			return models.CodeReviewTriggerSourceTeamReviewer, true, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", false, fmt.Errorf("load code review GitHub trigger: %w", err)
		}
	}
	if login != "" && containsFold(s.cfg.AppReviewerLogins, login) {
		return models.CodeReviewTriggerSourceAppReviewer, true, nil
	}
	if login != "" && containsFold(s.cfg.AliasLogins, login) {
		return models.CodeReviewTriggerSourceAliasReviewer, true, nil
	}
	if team != "" && containsFold(s.cfg.TeamSlugs, team) {
		return models.CodeReviewTriggerSourceTeamReviewer, true, nil
	}
	return "", false, nil
}

func normalizeConfig(cfg Config) Config {
	if len(cfg.AppReviewerLogins) == 0 && len(cfg.AliasLogins) == 0 && len(cfg.TeamSlugs) == 0 {
		cfg.AppReviewerLogins = []string{"143-code-reviewer", "143 Code Reviewer"}
	}
	return cfg
}

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), needle) {
			return true
		}
	}
	return false
}

var _ PolicyStore = (*db.CodeReviewStore)(nil)
var _ MetadataStore = (*db.CodeReviewStore)(nil)
var _ GitHubTriggerStore = (*db.CodeReviewStore)(nil)
