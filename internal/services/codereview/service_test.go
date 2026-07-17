package codereview

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestService_HandleReviewRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     ReviewRequestedInput
		activeJob *bool
		setup     func(*policyStub, *metadataStub, *triggerStub)
		expected  func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub)
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
				require.Equal(t, models.SessionStatusIdle, sessions.created.Status, "review sessions should start idle so reviewer tabs can claim the first turn")
				require.Equal(t, 1, metadata.createCalls, "service should create code review metadata")
				require.True(t, metadata.created.FromFork, "service should persist fork source evidence on review metadata")
				require.Equal(t, 1, jobs.enqueueCalls, "service should enqueue the code review worker job")
				require.Equal(t, models.JobTypeRunCodeReview, jobs.jobType, "service should use the code review job type")
				require.Equal(t, codeReviewJobMaxAttempts, jobs.opts.MaxAttempts, "code review jobs should get the extended retry budget")
				require.NotEmpty(t, jobs.dedupeKey, "service should dedupe by stable output key")
				require.True(t, jobs.payload.FromFork, "service should carry fork source evidence into worker payload")
				require.Equal(t, "anya", jobs.payload.PullRequestAuthor, "service should carry GitHub author login into worker payload")
				require.Equal(t, "143-code-reviewer", jobs.payload.RequestedReviewerLogin, "service should carry requested reviewer login for stale-request cleanup")

				var revisionContext map[string]any
				require.NoError(t, json.Unmarshal(sessions.created.RevisionContext, &revisionContext), "session revision context should be valid JSON")
				require.Equal(t, true, revisionContext["from_fork"], "session revision context should include fork source evidence")
			},
		},
		{
			name: "creates session from configured GitHub team trigger",
			input: newReviewRequestedInput(func(in *ReviewRequestedInput) {
				in.RequestedLogin = ""
				in.RequestedTeam = "143-code-reviewer"
			}),
			setup: func(_ *policyStub, _ *metadataStub, triggers *triggerStub) {
				triggers.setting = models.CodeReviewGitHubTriggerSetting{
					OrgID:        triggers.orgID,
					RepositoryID: triggers.repositoryID,
					TeamSlug:     "143-code-reviewer",
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "configured team reviewer request should start a review")
				require.Equal(t, models.CodeReviewTriggerSourceTeamReviewer, result.TriggerSource, "team trigger should be recorded as team_reviewer")
				require.Equal(t, "143-code-reviewer", jobs.payload.RequestedTeamSlug, "worker payload should remember requested team for cleanup")
				require.Equal(t, 1, sessions.createCalls, "configured team reviewer request should create a session")
			},
		},
		{
			name: "ignores unrelated GitHub team trigger",
			input: newReviewRequestedInput(func(in *ReviewRequestedInput) {
				in.RequestedLogin = ""
				in.RequestedTeam = "other-team"
			}),
			setup: func(_ *policyStub, _ *metadataStub, triggers *triggerStub) {
				triggers.setting = models.CodeReviewGitHubTriggerSetting{
					OrgID:        triggers.orgID,
					RepositoryID: triggers.repositoryID,
					TeamSlug:     "143-code-reviewer",
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "unrelated team reviewer request should not be processed")
				require.Equal(t, "reviewer_not_configured", result.IgnoredReason, "ignored result should explain team mismatch")
				require.Equal(t, 0, sessions.createCalls, "unrelated team reviewer request should not create a session")
			},
		},
		{
			name:  "reuses running review for same head and policy",
			input: newReviewRequestedInput(nil),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					Status:          models.CodeReviewSessionStatusRunning,
					ReviewOutputKey: "running-output-key",
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "matching duplicate request should be processed")
				require.True(t, result.Reused, "duplicate request for same head should reuse running review")
				require.Equal(t, metadata.latest.SessionID, result.SessionID, "result should point to running session")
				require.Equal(t, 0, sessions.createCalls, "duplicate request should not create another session")
				require.Equal(t, 0, jobs.enqueueCalls, "duplicate request should not enqueue another job")
			},
		},
		{
			name:  "reuses terminal review for same output key",
			input: newReviewRequestedInput(nil),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusCompleted}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "matching duplicate terminal request should be processed")
				require.True(t, result.Reused, "duplicate terminal request should reuse the existing review")
				require.Equal(t, metadata.latest.SessionID, result.SessionID, "result should point to existing terminal session")
				require.Equal(t, 0, sessions.createCalls, "duplicate terminal request should not create a detached session")
				require.Equal(t, 0, metadata.createCalls, "duplicate terminal request should not create duplicate metadata")
				require.Equal(t, 0, jobs.enqueueCalls, "duplicate terminal request should not enqueue a broken job")
			},
		},
		{
			name:      "replaces running review whose job is no longer active",
			input:     newReviewRequestedInput(nil),
			activeJob: boolPtr(false),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					Status:          models.CodeReviewSessionStatusRunning,
					ReviewOutputKey: "stranded-output-key",
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "rerequest should be processed")
				require.False(t, result.Reused, "stranded review should not be silently reused")
				require.Equal(t, 1, metadata.failCalls, "stranded review should be marked failed before replacement")
				require.Equal(t, 1, sessions.updateStatusCalls, "stranded parent session should be terminalized")
				require.Equal(t, models.SessionStatusFailed, sessions.updatedStatus, "stranded parent session should be marked failed")
				require.Equal(t, 1, sessions.updateFailureCalls, "stranded parent session should record an actionable failure")
				require.Equal(t, 1, sessions.createCalls, "rerequest should create a fresh session")
				require.Equal(t, 1, jobs.enqueueCalls, "rerequest should enqueue a fresh review job")
				require.Contains(t, metadata.created.ReviewOutputKey, metadata.latest.ID.String(), "replacement output key should identify the prior failed attempt")
			},
		},
		{
			name:      "reuses newly queued review while its job enqueue is in flight",
			input:     newReviewRequestedInput(nil),
			activeJob: boolPtr(false),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					Status:          models.CodeReviewSessionStatusQueued,
					ReviewOutputKey: "new-output-key",
					CreatedAt:       time.Now(),
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "duplicate request during enqueue should be processed")
				require.True(t, result.Reused, "new metadata should be reused during the enqueue grace period")
				require.Equal(t, 0, metadata.failCalls, "enqueue grace period should not fail new metadata")
				require.Equal(t, 0, sessions.createCalls, "enqueue grace period should not create a duplicate session")
				require.Equal(t, 0, jobs.enqueueCalls, "the original request should remain responsible for enqueueing")
			},
		},
		{
			name:      "reuses concurrent replacement after losing stranded takeover",
			input:     newReviewRequestedInput(nil),
			activeJob: boolPtr(false),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					Status:          models.CodeReviewSessionStatusRunning,
					ReviewOutputKey: "stranded-output-key",
				}
				m.failErr = pgx.ErrNoRows
				m.latestAfterFail = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					Status:          models.CodeReviewSessionStatusQueued,
					ReviewOutputKey: "replacement-output-key",
					CreatedAt:       time.Now(),
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "concurrent replacement should satisfy the rerequest")
				require.True(t, result.Reused, "losing takeover should reuse the winning replacement")
				require.Equal(t, metadata.latestAfterFail.SessionID, result.SessionID, "result should point to the winning replacement session")
				require.Equal(t, 1, metadata.failCalls, "service should attempt to claim the stranded record once")
				require.Equal(t, 0, sessions.updateStatusCalls, "losing takeover must not fail the winning replacement session")
				require.Equal(t, 0, sessions.createCalls, "losing takeover must not create another replacement")
				require.Equal(t, 0, jobs.enqueueCalls, "the winning request remains responsible for enqueueing")
			},
		},
		{
			name:  "creates fresh review after failed attempt",
			input: newReviewRequestedInput(nil),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.latest = models.CodeReviewSessionMetadata{
					ID:        uuid.New(),
					SessionID: uuid.New(),
					Status:    models.CodeReviewSessionStatusFailed,
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "failed review rerequest should be processed")
				require.False(t, result.Reused, "failed review should get a fresh attempt")
				require.Equal(t, 1, sessions.createCalls, "failed review rerequest should create a fresh session")
				require.Equal(t, 1, jobs.enqueueCalls, "failed review rerequest should enqueue a fresh job")
				require.Contains(t, metadata.created.ReviewOutputKey, metadata.latest.ID.String(), "retry output key should advance from the failed attempt")
			},
		},
		{
			name:  "does not enqueue when metadata creation races with same output key",
			input: newReviewRequestedInput(nil),
			setup: func(_ *policyStub, m *metadataStub, _ *triggerStub) {
				m.createResult = models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusQueued}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, policies *policyStub, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "raced duplicate request should be processed")
				require.True(t, result.Reused, "raced duplicate request should reuse the winning metadata row")
				require.Equal(t, metadata.createResult.SessionID, result.SessionID, "result should point to winning session metadata")
				require.Equal(t, 1, sessions.createCalls, "raced duplicate request may have created a loser session before conflict detection")
				require.Equal(t, 1, metadata.createCalls, "raced duplicate request should attempt metadata creation once")
				require.Equal(t, 0, jobs.enqueueCalls, "raced duplicate request should not enqueue a job for the loser session")
			},
		},
		{
			name:  "honors disabled policy",
			input: newReviewRequestedInput(nil),
			setup: func(p *policyStub, _ *metadataStub, _ *triggerStub) {
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
			metadata := &metadataStub{}
			triggers := &triggerStub{orgID: tt.input.OrgID, repositoryID: tt.input.RepositoryID, err: pgx.ErrNoRows}
			if tt.setup != nil {
				tt.setup(policies, metadata, triggers)
			}
			sessions := &sessionStub{}
			jobs := &jobStub{jobID: uuid.New(), active: true}
			if tt.activeJob != nil {
				jobs.active = *tt.activeJob
			}
			svc := NewService(policies, metadata, sessions, jobs, zerolog.Nop(), Config{AppReviewerLogins: []string{"143-code-reviewer"}})
			svc.SetGitHubTriggerStore(triggers)

			result, err := svc.HandleReviewRequested(context.Background(), tt.input)

			require.NoError(t, err, "HandleReviewRequested should not error for valid test input")
			tt.expected(t, result, policies, metadata, sessions, jobs)
		})
	}
}

type triggerStub struct {
	orgID        uuid.UUID
	repositoryID uuid.UUID
	setting      models.CodeReviewGitHubTriggerSetting
	err          error
}

func (s *triggerStub) GetActiveGitHubTrigger(_ context.Context, orgID, repositoryID uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	if s.setting.ID != uuid.Nil || s.setting.TeamSlug != "" {
		s.orgID = orgID
		s.repositoryID = repositoryID
		return s.setting, nil
	}
	if s.err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, s.err
	}
	return models.CodeReviewGitHubTriggerSetting{}, pgx.ErrNoRows
}

func (s *triggerStub) SaveGitHubTrigger(context.Context, uuid.UUID, db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, nil
}

func (s *triggerStub) DeactivateGitHubTrigger(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
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
	createCalls     int
	staleCalls      int
	supersededBy    *uuid.UUID
	created         models.CodeReviewSessionMetadata
	createResult    models.CodeReviewSessionMetadata
	latest          models.CodeReviewSessionMetadata
	latestAfterFail models.CodeReviewSessionMetadata
	failCalls       int
	failErr         error
}

func (s *metadataStub) CreateSessionMetadata(_ context.Context, metadata *models.CodeReviewSessionMetadata) error {
	s.createCalls++
	if s.createResult.ID != uuid.Nil {
		*metadata = s.createResult
		s.created = *metadata
		return nil
	}
	metadata.ID = uuid.New()
	s.created = *metadata
	return nil
}

func (s *metadataStub) GetLatestByPullRequestHead(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.latest.ID != uuid.Nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) FailReview(_ context.Context, _ uuid.UUID, _ uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	s.failCalls++
	if s.failErr != nil {
		err := s.failErr
		s.failErr = nil
		if s.latestAfterFail.ID != uuid.Nil {
			s.latest = s.latestAfterFail
		}
		return models.CodeReviewSessionMetadata{}, err
	}
	failed := s.latest
	failed.Status = models.CodeReviewSessionStatusFailed
	failed.FailureReason = &reason
	s.latest = failed
	return failed, nil
}

func (s *metadataStub) MarkStaleForPullRequestExceptHead(_ context.Context, _, _ uuid.UUID, _ string, supersededBySessionID *uuid.UUID) (int64, error) {
	s.staleCalls++
	s.supersededBy = supersededBySessionID
	return 1, nil
}

type sessionStub struct {
	createCalls        int
	updateStatusCalls  int
	updateFailureCalls int
	updatedStatus      models.SessionStatus
	created            models.Session
}

func (s *sessionStub) Create(_ context.Context, session *models.Session) error {
	s.createCalls++
	session.ID = uuid.New()
	s.created = *session
	return nil
}

func (s *sessionStub) UpdateStatus(_ context.Context, _, _ uuid.UUID, status models.SessionStatus) error {
	s.updateStatusCalls++
	s.updatedStatus = status
	return nil
}

func (s *sessionStub) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	s.updateFailureCalls++
	return nil
}

type jobStub struct {
	enqueueCalls int
	jobID        uuid.UUID
	jobType      string
	dedupeKey    string
	payload      RunCodeReviewJobPayload
	opts         db.EnqueueOpts
	active       bool
}

func (s *jobStub) EnqueueWithOpts(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	s.enqueueCalls++
	s.opts = opts
	s.jobType = opts.JobType
	if typed, ok := opts.Payload.(RunCodeReviewJobPayload); ok {
		s.payload = typed
	}
	if opts.DedupeKey != nil {
		s.dedupeKey = *opts.DedupeKey
	}
	return s.jobID, nil
}

func (s *jobStub) HasActiveByDedupeKey(context.Context, uuid.UUID, string, string) (bool, error) {
	return s.active, nil
}

func boolPtr(value bool) *bool {
	return &value
}
