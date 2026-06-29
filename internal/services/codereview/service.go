package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type PolicyStore interface {
	ResolvePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) (models.CodeReviewResolvedPolicy, error)
	SavePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID, config models.CodeReviewPolicyConfig, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error)
}

type MetadataStore interface {
	CreateSessionMetadata(ctx context.Context, metadata *models.CodeReviewSessionMetadata) error
	GetByOutputKey(ctx context.Context, orgID uuid.UUID, outputKey string) (models.CodeReviewSessionMetadata, error)
	GetRunningByPullRequestHead(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) (int64, error)
}

type SessionStore interface {
	Create(ctx context.Context, session *models.Session) error
}

type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type Service struct {
	policies PolicyStore
	metadata MetadataStore
	sessions SessionStore
	jobs     JobStore
	triggers GitHubTriggerStore
	logger   zerolog.Logger
	cfg      Config
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

type ReviewRequestedResult struct {
	Processed     bool
	Reused        bool
	SessionID     uuid.UUID
	MetadataID    uuid.UUID
	JobID         uuid.UUID
	TriggerSource models.CodeReviewTriggerSource
	IgnoredReason string
}

type RunCodeReviewJobPayload struct {
	OrgID                  uuid.UUID `json:"org_id"`
	SessionID              uuid.UUID `json:"session_id"`
	MetadataID             uuid.UUID `json:"metadata_id"`
	RepositoryID           uuid.UUID `json:"repository_id"`
	PullRequestID          uuid.UUID `json:"pull_request_id"`
	PolicyID               uuid.UUID `json:"policy_id"`
	PolicyVersion          int       `json:"policy_version"`
	HeadSHA                string    `json:"head_sha"`
	FromFork               bool      `json:"from_fork"`
	PullRequestAuthor      string    `json:"pull_request_author,omitempty"`
	OutputKey              string    `json:"review_output_key"`
	RequestedReviewerLogin string    `json:"requested_reviewer_login,omitempty"`
	RequestedTeamSlug      string    `json:"requested_team_slug,omitempty"`
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

	repositoryID := input.RepositoryID
	resolved, err := s.policies.ResolvePolicy(ctx, input.OrgID, &repositoryID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("resolve code review policy: %w", err)
	}
	policy := resolved.Policy
	if policy == nil {
		record, err := s.policies.SavePolicy(ctx, input.OrgID, &repositoryID, resolved.Config, nil)
		if err != nil {
			return ReviewRequestedResult{}, fmt.Errorf("materialize default code review policy: %w", err)
		}
		policy = &record
	}
	if !resolved.Config.Enabled {
		return ReviewRequestedResult{IgnoredReason: "policy_disabled", TriggerSource: source}, nil
	}
	if policy.RepositoryID != nil && policy.Config().Inheritance.InheritOrgDefaults && !reflect.DeepEqual(policy.Config(), resolved.Config) {
		record, err := s.policies.SavePolicy(ctx, input.OrgID, &repositoryID, resolved.Config, nil)
		if err != nil {
			return ReviewRequestedResult{}, fmt.Errorf("materialize inherited code review policy: %w", err)
		}
		policy = &record
	}
	if existing, err := s.metadata.GetRunningByPullRequestHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, policy.ID); err == nil {
		if _, staleErr := s.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, &existing.SessionID); staleErr != nil {
			return ReviewRequestedResult{}, staleErr
		}
		return ReviewRequestedResult{Processed: true, Reused: true, SessionID: existing.SessionID, MetadataID: existing.ID, TriggerSource: source}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{}, fmt.Errorf("lookup running code review: %w", err)
	}
	outputKey := StableOutputKey(input.PullRequestID, input.HeadSHA, policy.ID, policy.Version)
	if existing, err := s.metadata.GetByOutputKey(ctx, input.OrgID, outputKey); err == nil {
		if _, staleErr := s.metadata.MarkStaleForPullRequestExceptHead(ctx, input.OrgID, input.PullRequestID, input.HeadSHA, &existing.SessionID); staleErr != nil {
			return ReviewRequestedResult{}, staleErr
		}
		return ReviewRequestedResult{Processed: true, Reused: true, SessionID: existing.SessionID, MetadataID: existing.ID, TriggerSource: source}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{}, fmt.Errorf("lookup code review by output key: %w", err)
	}

	title := fmt.Sprintf("Code review for %s#%d", input.GitHubRepo, input.GitHubPRNumber)
	modelOverride := resolved.Config.AgentRoster.OrchestratorModel
	revisionContext, err := json.Marshal(map[string]any{
		"kind":                "code_review",
		"github_repo":         input.GitHubRepo,
		"github_pr_number":    input.GitHubPRNumber,
		"github_pr_url":       input.GitHubPRURL,
		"pull_request_title":  input.PullRequestTitle,
		"pull_request_author": input.PullRequestAuthor,
		"base_sha":            input.BaseSHA,
		"head_sha":            input.HeadSHA,
		"from_fork":           input.FromFork,
		"policy_id":           policy.ID,
		"policy_version":      policy.Version,
		"trigger_source":      source,
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
		Status:           models.SessionStatusIdle,
		AutonomyLevel:    models.SessionAutonomySupervised,
		TokenMode:        models.DefaultSessionTokenMode,
		RepositoryID:     &repositoryID,
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
		OrgID:                  input.OrgID,
		SessionID:              session.ID,
		MetadataID:             metadata.ID,
		RepositoryID:           input.RepositoryID,
		PullRequestID:          input.PullRequestID,
		PolicyID:               policy.ID,
		PolicyVersion:          policy.Version,
		HeadSHA:                input.HeadSHA,
		FromFork:               input.FromFork,
		PullRequestAuthor:      strings.TrimSpace(input.PullRequestAuthor),
		OutputKey:              outputKey,
		RequestedReviewerLogin: input.RequestedLogin,
		RequestedTeamSlug:      input.RequestedTeam,
	}
	dedupeKey := "code_review:" + outputKey
	jobID, err := s.jobs.Enqueue(ctx, input.OrgID, "agent", models.JobTypeRunCodeReview, payload, 5, &dedupeKey)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("enqueue code review job: %w", err)
	}
	return ReviewRequestedResult{
		Processed:     true,
		SessionID:     session.ID,
		MetadataID:    metadata.ID,
		JobID:         jobID,
		TriggerSource: source,
	}, nil
}

func StableOutputKey(pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID, policyVersion int) string {
	return fmt.Sprintf("pr:%s:head:%s:policy:%s:v%d", pullRequestID, headSHA, policyID, policyVersion)
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
