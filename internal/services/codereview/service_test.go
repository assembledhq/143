package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestService_HandleReviewRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    ReviewRequestedInput
		setup    func(*policyStub, *metadataStub)
		expected func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub)
	}{
		{
			name: "ignores unrelated reviewer",
			input: newReviewRequestedInput(func(in *ReviewRequestedInput) {
				in.RequestedLogin = "octocat"
			}),
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "unrelated reviewer request should not be processed")
				require.Equal(t, "reviewer_not_configured", result.IgnoredReason, "ignored result should explain reviewer mismatch")
				require.Equal(t, 0, sessions.createCalls, "unrelated reviewer request should not create a session")
			},
		},
		{
			name: "creates session and enqueues durable review job",
			input: newReviewRequestedInput(func(in *ReviewRequestedInput) {
				in.FromFork = true
			}),
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "matching reviewer request should be processed")
				require.False(t, result.Reused, "new PR head should create a fresh review")
				require.Equal(t, 1, policies.saveCalls, "missing DB policy should be materialized for FK-backed audit")
				require.Equal(t, 1, metadata.staleCalls, "older running heads should be marked stale")
				require.Equal(t, 1, sessions.createCalls, "service should create a normal 143 session")
				require.Equal(t, models.SessionOriginCodeReview, sessions.created.Origin, "session should use code_review origin")
				require.Equal(t, models.SessionInteractionModeSingleRun, sessions.created.InteractionMode, "review sessions should be single-run")
				require.Equal(t, 1, metadata.createCalls, "service should create code review metadata")
				require.True(t, metadata.created.FromFork, "service should persist fork source evidence on review metadata")
				require.Equal(t, 1, jobs.enqueueCalls, "service should enqueue the code review worker job")
				require.Equal(t, models.JobTypeRunCodeReview, jobs.jobType, "service should use the code review job type")
				require.NotEmpty(t, jobs.dedupeKey, "service should dedupe by stable output key")
				require.True(t, jobs.payload.FromFork, "service should carry fork source evidence into worker payload")
				require.Equal(t, "143-code-reviewer", jobs.payload.RequestedReviewerLogin, "service should carry requested reviewer login for stale-request cleanup")

				var revisionContext map[string]any
				require.NoError(t, json.Unmarshal(sessions.created.RevisionContext, &revisionContext), "session revision context should be valid JSON")
				require.Equal(t, true, revisionContext["from_fork"], "session revision context should include fork source evidence")
			},
		},
		{
			name:  "reuses running review for same head and policy",
			input: newReviewRequestedInput(nil),
			setup: func(p *policyStub, m *metadataStub) {
				m.running = models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: uuid.New()}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "matching duplicate request should be processed")
				require.True(t, result.Reused, "duplicate request for same head should reuse running review")
				require.Equal(t, metadata.running.SessionID, result.SessionID, "result should point to running session")
				require.Equal(t, 0, sessions.createCalls, "duplicate request should not create another session")
				require.Equal(t, 0, jobs.enqueueCalls, "duplicate request should not enqueue another job")
			},
		},
		{
			name:  "honors disabled policy",
			input: newReviewRequestedInput(nil),
			setup: func(p *policyStub, m *metadataStub) {
				cfg := models.DefaultCodeReviewPolicyConfig()
				cfg.Enabled = false
				p.resolved.Config = cfg
				p.resolved.Policy = &models.CodeReviewPolicyRecord{ID: uuid.New(), Version: 1, Enabled: false}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "disabled policy should not start a review")
				require.Equal(t, "policy_disabled", result.IgnoredReason, "ignored result should explain disabled policy")
				require.Equal(t, 0, sessions.createCalls, "disabled policy should not create a session")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policies := newPolicyStub()
			metadata := &metadataStub{runningErr: pgx.ErrNoRows}
			if tt.setup != nil {
				tt.setup(policies, metadata)
			}
			sessions := &sessionStub{}
			jobs := &jobStub{jobID: uuid.New()}
			svc := NewService(policies, metadata, sessions, jobs, zerolog.Nop(), Config{AppReviewerLogins: []string{"143-code-reviewer"}})

			result, err := svc.HandleReviewRequested(context.Background(), tt.input)

			require.NoError(t, err, "HandleReviewRequested should not error for valid test input")
			tt.expected(t, result, policies, metadata, sessions, jobs)
		})
	}
}

func newReviewRequestedInput(mutator func(*ReviewRequestedInput)) ReviewRequestedInput {
	in := ReviewRequestedInput{
		OrgID:             uuid.New(),
		RepositoryID:      uuid.New(),
		PullRequestID:     uuid.New(),
		GitHubRepo:        "acme/repo",
		GitHubPRNumber:    42,
		GitHubPRURL:       "https://github.com/acme/repo/pull/42",
		PullRequestTitle:  "Fix rounding",
		PullRequestAuthor: "anya",
		BaseSHA:           "base",
		HeadSHA:           "head",
		RequestedLogin:    "143-code-reviewer",
	}
	if mutator != nil {
		mutator(&in)
	}
	return in
}

type policyStub struct {
	resolved  models.CodeReviewResolvedPolicy
	saveCalls int
}

func newPolicyStub() *policyStub {
	cfg := models.DefaultCodeReviewPolicyConfig()
	return &policyStub{resolved: models.CodeReviewResolvedPolicy{Config: cfg, Source: "default"}}
}

func (s *policyStub) ResolvePolicy(_ context.Context, _ uuid.UUID, _ *uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	return s.resolved, nil
}

func (s *policyStub) SavePolicy(_ context.Context, orgID uuid.UUID, repositoryID *uuid.UUID, config models.CodeReviewPolicyConfig, _ *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	s.saveCalls++
	return models.CodeReviewPolicyRecord{ID: uuid.New(), OrgID: orgID, RepositoryID: repositoryID, Version: 1, Enabled: config.Enabled, ApprovalMode: config.ApprovalMode}, nil
}

type metadataStub struct {
	createCalls int
	staleCalls  int
	created     models.CodeReviewSessionMetadata
	running     models.CodeReviewSessionMetadata
	runningErr  error
}

func (s *metadataStub) CreateSessionMetadata(_ context.Context, metadata *models.CodeReviewSessionMetadata) error {
	s.createCalls++
	metadata.ID = uuid.New()
	s.created = *metadata
	return nil
}

func (s *metadataStub) GetRunningByPullRequestHead(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.running.ID != uuid.Nil {
		return s.running, nil
	}
	if s.runningErr != nil {
		return models.CodeReviewSessionMetadata{}, s.runningErr
	}
	return models.CodeReviewSessionMetadata{}, errors.New("unexpected running lookup")
}

func (s *metadataStub) MarkStaleForPullRequestExceptHead(_ context.Context, _, _ uuid.UUID, _ string) (int64, error) {
	s.staleCalls++
	return 1, nil
}

type sessionStub struct {
	createCalls int
	created     models.Session
}

func (s *sessionStub) Create(_ context.Context, session *models.Session) error {
	s.createCalls++
	session.ID = uuid.New()
	s.created = *session
	return nil
}

type jobStub struct {
	enqueueCalls int
	jobID        uuid.UUID
	jobType      string
	dedupeKey    string
	payload      RunCodeReviewJobPayload
}

func (s *jobStub) Enqueue(_ context.Context, _ uuid.UUID, _ string, jobType string, payload any, _ int, dedupeKey *string) (uuid.UUID, error) {
	s.enqueueCalls++
	s.jobType = jobType
	if typed, ok := payload.(RunCodeReviewJobPayload); ok {
		s.payload = typed
	}
	if dedupeKey != nil {
		s.dedupeKey = *dedupeKey
	}
	return s.jobID, nil
}
