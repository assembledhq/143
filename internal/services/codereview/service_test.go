package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
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
			name:  "does not rereview after approval",
			input: newReviewRequestedInput(nil),
			setup: func(_ *policyStub, metadata *metadataStub, _ *triggerStub) {
				metadata.approved = true
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *policyStub, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "approved pull request should not be reviewed again")
				require.Equal(t, "already_approved", result.IgnoredReason, "rerequest should preserve the existing approval")
				require.Equal(t, 0, sessions.createCalls, "approved rerequest should not create a new session")
				require.Equal(t, 0, jobs.enqueueCalls, "approved rerequest should not enqueue new review work")
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
				require.NotNil(t, sessions.created.ReasoningEffort, "review sessions should carry the policy reasoning level")
				require.Equal(t, models.ReasoningEffortHigh, *sessions.created.ReasoningEffort, "review sessions should default to high reasoning")
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
				require.Equal(t, "143-code-reviewer", revisionContext["requested_reviewer_login"], "session revision context should preserve reviewer identity for replacement cleanup")
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
				var revisionContext map[string]any
				require.NoError(t, json.Unmarshal(sessions.created.RevisionContext, &revisionContext), "team review revision context should be valid JSON")
				require.Equal(t, "143-code-reviewer", revisionContext["requested_team_slug"], "session revision context should preserve team identity for replacement cleanup")
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

func TestService_HandleReviewChanged(t *testing.T) {
	t.Parallel()

	priorReviewID := int64(143)
	priorReviewURL := "https://github.com/acme/repo/pull/42#pullrequestreview-143"
	tests := []struct {
		name              string
		usePriorSessionID bool
		setup             func(*metadataStub, *sessionStub)
		expected          func(*testing.T, ReviewRequestedResult, *metadataStub, *sessionStub, *jobStub)
	}{
		{
			name: "ignores pull requests without prior 143 review history",
			expected: func(t *testing.T, result ReviewRequestedResult, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "unreviewed pull request changes should not start always-on review")
				require.Equal(t, "review_not_previously_requested", result.IgnoredReason, "ignored change should explain missing reviewer request")
				require.Equal(t, 0, sessions.createCalls, "unreviewed pull request change should not create a session")
				require.Equal(t, 0, jobs.enqueueCalls, "unreviewed pull request change should not enqueue work")
			},
		},
		{
			name: "stops reassessing after 143 has approved",
			setup: func(metadata *metadataStub, _ *sessionStub) {
				metadata.approved = true
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.False(t, result.Processed, "approved pull request changes should not start another review")
				require.Equal(t, "already_approved", result.IgnoredReason, "ignored change should preserve the monotonic approval")
				require.Equal(t, 0, sessions.createCalls, "approved pull request change should not create a session")
				require.Equal(t, 0, jobs.enqueueCalls, "approved pull request change should not enqueue work")
			},
		},
		{
			name: "creates a fresh assessment and carries the existing GitHub review",
			setup: func(metadata *metadataStub, sessions *sessionStub) {
				metadata.latest = models.CodeReviewSessionMetadata{
					ID:              uuid.New(),
					SessionID:       uuid.New(),
					PullRequestID:   uuid.New(),
					PolicyID:        uuid.New(),
					HeadSHA:         "head",
					FromFork:        true,
					TriggerSource:   models.CodeReviewTriggerSourceTeamReviewer,
					Status:          models.CodeReviewSessionStatusCompleted,
					ReviewOutputKey: "prior-output-key",
					GitHubReviewID:  &priorReviewID,
					GitHubReviewURL: &priorReviewURL,
				}
				sessions.getResult = models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya"}`)}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, metadata *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "external PR change should start reassessment")
				require.False(t, result.Reused, "new change delivery should create a new auditable assessment")
				require.Equal(t, 1, sessions.createCalls, "reassessment should create a code review session")
				require.Equal(t, 1, jobs.enqueueCalls, "reassessment should enqueue code review work")
				require.Equal(t, models.CodeReviewTriggerSourceTeamReviewer, metadata.created.TriggerSource, "reassessment should preserve the original trigger source")
				require.Contains(t, metadata.created.ReviewOutputKey, ":change:", "reassessment output should be keyed by the external change")
				require.Equal(t, &priorReviewID, jobs.payload.ExistingGitHubReviewID, "worker should update the existing GitHub review")
				require.Equal(t, &priorReviewURL, jobs.payload.ExistingGitHubReviewURL, "worker should retain the existing review URL")
				require.Equal(t, "prior-output-key", jobs.payload.PreviousOutputKey, "worker should be able to match prior inline findings")
				require.Equal(t, "anya", jobs.payload.PullRequestAuthor, "reassessment should retain author eligibility context")
				require.True(t, jobs.payload.FromFork, "reassessment should retain fork eligibility context")
			},
		},
		{
			name: "defers behind an active assessment while it refreshes mutable gates",
			setup: func(metadata *metadataStub, sessions *sessionStub) {
				metadata.latest = models.CodeReviewSessionMetadata{
					ID: uuid.New(), SessionID: uuid.New(), HeadSHA: "head",
					TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
					Status:        models.CodeReviewSessionStatusRunning, ReviewOutputKey: "active-output-key",
				}
				sessions.getResult = models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya"}`)}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Reused, "change during an active review should reuse the running assessment")
				require.True(t, result.Deferred, "change during an older active review should remain pending for a follow-up")
				require.Equal(t, 0, sessions.createCalls, "active reassessment should not create concurrent reviewer sessions")
				require.Equal(t, 0, jobs.enqueueCalls, "active reassessment should not enqueue duplicate work")
			},
		},
		{
			name:              "coalesces a change already covered by a newer assessment",
			usePriorSessionID: true,
			setup: func(metadata *metadataStub, _ *sessionStub) {
				metadata.latest = models.CodeReviewSessionMetadata{
					ID: uuid.New(), SessionID: uuid.New(), HeadSHA: "head",
					TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
					Status:        models.CodeReviewSessionStatusRunning, ReviewOutputKey: "newer-output-key",
				}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Reused, "newer assessment should cover the queued change")
				require.False(t, result.Deferred, "covered change should not keep retrying")
				require.Equal(t, "change_already_reassessed", result.IgnoredReason, "coalesced result should explain why no new review started")
				require.Equal(t, 0, sessions.createCalls, "covered change should not create another session")
				require.Equal(t, 0, jobs.enqueueCalls, "covered change should not enqueue another review")
			},
		},
		{
			name:              "retries a change after a newer assessment failed",
			usePriorSessionID: true,
			setup: func(metadata *metadataStub, sessions *sessionStub) {
				metadata.latest = models.CodeReviewSessionMetadata{
					ID: uuid.New(), SessionID: uuid.New(), HeadSHA: "head",
					TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
					Status:        models.CodeReviewSessionStatusFailed, ReviewOutputKey: "failed-newer-output-key",
				}
				sessions.getResult = models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya"}`)}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Processed, "failed newer assessment should not consume the queued change")
				require.False(t, result.Reused, "queued change should receive a fresh attempt after failure")
				require.Equal(t, 1, sessions.createCalls, "failed coverage should create a replacement assessment")
				require.Equal(t, 1, jobs.enqueueCalls, "failed coverage should enqueue replacement review work")
			},
		},
		{
			name: "reuses a completed assessment for a redelivered change",
			setup: func(metadata *metadataStub, sessions *sessionStub) {
				metadata.latest = models.CodeReviewSessionMetadata{
					ID: uuid.New(), SessionID: uuid.New(), HeadSHA: "head",
					TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
					Status:        models.CodeReviewSessionStatusCompleted,
				}
				metadata.createResult = models.CodeReviewSessionMetadata{
					ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusCompleted,
				}
				sessions.getResult = models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya"}`)}
			},
			expected: func(t *testing.T, result ReviewRequestedResult, _ *metadataStub, sessions *sessionStub, jobs *jobStub) {
				require.True(t, result.Reused, "redelivered change should reuse its completed assessment")
				require.Equal(t, 0, sessions.createCalls, "redelivery should not create an orphan session")
				require.Equal(t, 0, jobs.enqueueCalls, "redelivery should not enqueue duplicate work")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policies := newPolicyStub()
			metadata := &metadataStub{}
			sessions := &sessionStub{}
			jobs := &jobStub{jobID: uuid.New(), active: true}
			if tt.setup != nil {
				tt.setup(metadata, sessions)
			}
			svc := NewService(policies, metadata, sessions, jobs, zerolog.Nop(), Config{})
			input := newReviewChangedInput()
			if tt.usePriorSessionID {
				input.PriorSessionID = uuid.New()
			}

			result, err := svc.HandleReviewChanged(context.Background(), input)

			require.NoError(t, err, "HandleReviewChanged should process valid reassessment input")
			tt.expected(t, result, metadata, sessions, jobs)
		})
	}
}

func TestService_QueueReviewChanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(*metadataStub)
		expectedQueue bool
		expectedWhy   string
	}{
		{
			name: "queues a durable reassessment behind the current session",
			setup: func(metadata *metadataStub) {
				metadata.latest = models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusRunning}
			},
			expectedQueue: true,
		},
		{
			name:        "ignores pull requests without review history",
			expectedWhy: "review_not_previously_requested",
		},
		{
			name: "does not queue after approval",
			setup: func(metadata *metadataStub) {
				metadata.approved = true
			},
			expectedWhy: "already_approved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metadata := &metadataStub{}
			if tt.setup != nil {
				tt.setup(metadata)
			}
			jobs := &jobStub{jobID: uuid.New()}
			svc := NewService(newPolicyStub(), metadata, &sessionStub{}, jobs, zerolog.Nop(), Config{})
			input := newReviewChangedInput()

			result, err := svc.QueueReviewChanged(context.Background(), input)

			require.NoError(t, err, "QueueReviewChanged should handle valid event input")
			if !tt.expectedQueue {
				require.False(t, result.Processed, "ignored event should not report queued work")
				require.Equal(t, tt.expectedWhy, result.IgnoredReason, "ignored event should explain why no work was queued")
				require.Equal(t, 0, jobs.enqueueCalls, "ignored event should not enqueue a reassessment starter")
				return
			}
			require.True(t, result.Processed, "reviewed pull request event should be durably queued")
			require.Equal(t, 1, jobs.enqueueCalls, "event should enqueue one reassessment starter")
			require.Equal(t, models.JobTypeStartCodeReviewReassessment, jobs.jobType, "event should use the reassessment starter job type")
			require.Equal(t, metadata.latest.SessionID, jobs.reassessmentPayload.PriorSessionID, "queued event should remember the assessment current when it arrived")
			require.Contains(t, jobs.dedupeKey, input.ChangeKey, "starter job should dedupe webhook redeliveries")
		})
	}
}

func TestService_RetryReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mutate           func(*models.CodeReviewSessionMetadata, *metadataStub, *policyStub, *pullRequestStub)
		enqueueErr       error
		syncErr          error
		expectedCode     RetryReviewConflictCode
		expectStart      bool
		unconfirmedReuse bool
	}{
		{name: "creates a fresh replacement attempt", expectStart: true},
		{name: "uses persisted health while mergeability is pending", syncErr: ghservice.ErrPullRequestMergeabilityPending, expectStart: true},
		{name: "terminalizes an unqueued replacement for recovery", enqueueErr: errors.New("queue unavailable")},
		{
			name: "keeps source retryable while concurrent replacement dispatch is unconfirmed",
			mutate: func(source *models.CodeReviewSessionMetadata, metadata *metadataStub, _ *policyStub, _ *pullRequestStub) {
				replacement := *source
				replacement.ID = uuid.New()
				replacement.SessionID = uuid.New()
				replacement.Status = models.CodeReviewSessionStatusQueued
				replacement.RetryableFailure = false
				replacement.ReviewOutputKey = "concurrent-replacement"
				replacement.CreatedAt = time.Now()
				metadata.latestByHead = replacement
			},
			expectedCode:     RetryReviewConflictNewerAttempt,
			unconfirmedReuse: true,
		},
		{
			name: "rejects a pull request with an existing approval",
			mutate: func(_ *models.CodeReviewSessionMetadata, metadata *metadataStub, _ *policyStub, _ *pullRequestStub) {
				metadata.approved = true
			},
			expectedCode: RetryReviewConflictCompleted,
		},
		{
			name: "rejects a completed attempt",
			mutate: func(source *models.CodeReviewSessionMetadata, _ *metadataStub, _ *policyStub, _ *pullRequestStub) {
				source.Status = models.CodeReviewSessionStatusCompleted
			},
			expectedCode: RetryReviewConflictCompleted,
		},
		{
			name: "rejects a non-retryable failure",
			mutate: func(source *models.CodeReviewSessionMetadata, _ *metadataStub, _ *policyStub, _ *pullRequestStub) {
				source.RetryableFailure = false
			},
			expectedCode: RetryReviewConflictNotRetryable,
		},
		{
			name: "rejects a superseded failure",
			mutate: func(source *models.CodeReviewSessionMetadata, _ *metadataStub, _ *policyStub, _ *pullRequestStub) {
				replacementID := uuid.New()
				source.SupersededBySessionID = &replacementID
			},
			expectedCode: RetryReviewConflictSuperseded,
		},
		{
			name: "rejects a changed pull request head",
			mutate: func(_ *models.CodeReviewSessionMetadata, _ *metadataStub, _ *policyStub, pullRequests *pullRequestStub) {
				pullRequests.health.HeadSHA = "new-head"
			},
			expectedCode: RetryReviewConflictHeadChanged,
		},
		{
			name: "rejects a closed pull request",
			mutate: func(_ *models.CodeReviewSessionMetadata, _ *metadataStub, _ *policyStub, pullRequests *pullRequestStub) {
				pullRequests.result.Status = models.PullRequestStatusClosed
			},
			expectedCode: RetryReviewConflictPRClosed,
		},
		{
			name: "rejects a failure with a newer attempt",
			mutate: func(source *models.CodeReviewSessionMetadata, metadata *metadataStub, _ *policyStub, _ *pullRequestStub) {
				newer := *source
				newer.ID = uuid.New()
				newer.SessionID = uuid.New()
				newer.CreatedAt = source.CreatedAt.Add(time.Second)
				metadata.latestByPullRequest = newer
			},
			expectedCode: RetryReviewConflictNewerAttempt,
		},
		{
			name: "rejects a disabled current policy",
			mutate: func(_ *models.CodeReviewSessionMetadata, _ *metadataStub, policies *policyStub, _ *pullRequestStub) {
				config := models.DefaultCodeReviewPolicyConfig()
				config.Enabled = false
				policies.resolved = models.CodeReviewResolvedPolicy{
					Config: config,
					Source: "organization",
					Policy: &models.CodeReviewPolicyRecord{ID: uuid.New(), Version: 1, Enabled: false},
				}
			},
			expectedCode: RetryReviewConflictPolicyOff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			pullRequestID := uuid.New()
			githubReviewID := int64(9182)
			githubReviewURL := "https://github.com/acme/repo/pull/42#pullrequestreview-9182"
			source := models.CodeReviewSessionMetadata{
				ID: uuid.New(), OrgID: orgID, SessionID: uuid.New(), RepositoryID: repoID,
				PullRequestID: pullRequestID, PolicyID: uuid.New(), BaseSHA: "base", HeadSHA: "head",
				TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
				Status:        models.CodeReviewSessionStatusFailed, RetryableFailure: true,
				ReviewOutputKey: "failed-output", GitHubReviewID: &githubReviewID, GitHubReviewURL: &githubReviewURL,
				CreatedAt: time.Now().Add(-time.Minute),
			}
			metadata := &metadataStub{latest: source, submitted: source}
			policies := newPolicyStub()
			head := "head"
			base := "base"
			pullRequests := &pullRequestStub{result: models.PullRequest{
				ID: pullRequestID, OrgID: orgID, GitHubRepo: "acme/repo", GitHubPRNumber: 42,
				GitHubPRURL: "https://github.com/acme/repo/pull/42", Title: "Fix rounding",
				Status: models.PullRequestStatusOpen, HeadSHA: &head, BaseSHA: &base,
			}, health: models.PullRequestHealthCurrent{
				PullRequestID: pullRequestID, OrgID: orgID, HeadSHA: "head", BaseSHA: "base",
			}}
			if tt.mutate != nil {
				tt.mutate(&source, metadata, policies, pullRequests)
				metadata.latest = source
			}
			metadata.getBySession = source
			sessions := &sessionStub{getResult: models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya","requested_reviewer_login":"143-code-reviewer"}`)}}
			jobs := &jobStub{jobID: uuid.New(), active: !tt.unconfirmedReuse, err: tt.enqueueErr}
			svc := NewService(policies, metadata, sessions, jobs, zerolog.Nop(), Config{})
			svc.SetRetryDependencies(pullRequests, &pullRequestSyncerStub{err: tt.syncErr})

			result, err := svc.RetryReview(context.Background(), RetryReviewInput{OrgID: orgID, SessionID: source.SessionID})

			if tt.expectedCode != "" {
				var conflict *RetryReviewConflictError
				require.ErrorAs(t, err, &conflict, "ineligible retries should return a typed conflict")
				require.Equal(t, tt.expectedCode, conflict.Code, "retry conflict should identify the rejected state")
				require.Equal(t, 0, jobs.enqueueCalls, "rejected retries should not enqueue review work")
				if tt.unconfirmedReuse {
					require.Nil(t, metadata.latest.SupersededBySessionID, "unconfirmed replacement must not hide the source retry action")
				}
				return
			}
			if tt.enqueueErr != nil {
				require.ErrorContains(t, err, tt.enqueueErr.Error(), "enqueue failures should be returned to the caller")
				require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.created.Status, "unqueued replacement should become terminal")
				require.True(t, metadata.created.RetryableFailure, "unqueued replacement should remain manually recoverable")
				require.Equal(t, metadata.created.SessionID, result.SessionID, "failed dispatch should identify its durable replacement for auditing")
				require.Equal(t, metadata.created.SessionID, *metadata.latest.SupersededBySessionID, "failed source should link to the recoverable replacement")
				require.Equal(t, 1, sessions.updateStatusCalls, "unqueued replacement session should be terminalized")

				failedReplacement := metadata.created
				metadata.getBySession = failedReplacement
				metadata.latest = failedReplacement
				metadata.latestByPullRequest = models.CodeReviewSessionMetadata{}
				metadata.created = models.CodeReviewSessionMetadata{}
				jobs.err = nil
				jobs.jobID = uuid.New()
				recovered, retryErr := svc.RetryReview(context.Background(), RetryReviewInput{OrgID: orgID, SessionID: failedReplacement.SessionID})

				require.NoError(t, retryErr, "compensated replacement should itself be retryable")
				require.NotEqual(t, failedReplacement.SessionID, recovered.SessionID, "recovery should create a fresh immutable attempt")
				require.Equal(t, recovered.SessionID, *metadata.latest.SupersededBySessionID, "compensated failure should link to the recovered attempt")
				require.Equal(t, 2, jobs.enqueueCalls, "recovery should make a second queue attempt")
				require.Equal(t, source.GitHubReviewID, jobs.payload.ExistingGitHubReviewID, "recovery should still update the original GitHub evidence")
				return
			}
			require.NoError(t, err, "eligible retry should create a replacement attempt")
			require.True(t, tt.expectStart, "success case should be explicitly marked")
			require.Equal(t, source.SessionID, result.PreviousSessionID, "retry should identify the failed source attempt")
			require.NotEqual(t, source.SessionID, result.SessionID, "retry should create an immutable replacement session")
			require.Equal(t, result.SessionID, *metadata.latest.SupersededBySessionID, "failed attempt should link to its replacement")
			require.Equal(t, 1, jobs.enqueueCalls, "retry should enqueue one replacement job")
			require.Equal(t, models.CodeReviewPhaseSyncingGitHub, *metadata.created.Phase, "replacement should start in the GitHub sync phase")
			require.Equal(t, "anya", jobs.payload.PullRequestAuthor, "replacement should preserve author context")
			require.Equal(t, "143-code-reviewer", jobs.payload.RequestedReviewerLogin, "replacement should preserve reviewer identity for GitHub cleanup")
			require.Equal(t, source.ReviewOutputKey, jobs.payload.PreviousOutputKey, "replacement should preserve the prior output key")
			require.Equal(t, source.GitHubReviewID, jobs.payload.ExistingGitHubReviewID, "replacement should update the submitted GitHub review")
			require.Equal(t, source.GitHubReviewURL, jobs.payload.ExistingGitHubReviewURL, "replacement should preserve the submitted GitHub review URL")
			require.Equal(t, orgID, pullRequests.healthOrgID, "live head lookup should preserve org tenancy")
			require.Equal(t, pullRequestID, pullRequests.healthPullRequestID, "live head lookup should target the retried pull request")
		})
	}
}

func TestService_RetryReviewRecoversUnqueuedNewerAttempt(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	githubReviewID := int64(9182)
	githubReviewURL := "https://github.com/acme/repo/pull/42#pullrequestreview-9182"
	source := models.CodeReviewSessionMetadata{
		ID: uuid.New(), OrgID: orgID, SessionID: uuid.New(), RepositoryID: repositoryID,
		PullRequestID: pullRequestID, PolicyID: uuid.New(), BaseSHA: "base", HeadSHA: "head",
		TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
		Status:        models.CodeReviewSessionStatusFailed, RetryableFailure: true,
		ReviewOutputKey: "failed-output", GitHubReviewID: &githubReviewID, GitHubReviewURL: &githubReviewURL,
		CreatedAt: time.Now().Add(-3 * time.Minute),
	}
	unqueued := source
	unqueued.ID = uuid.New()
	unqueued.SessionID = uuid.New()
	unqueued.Status = models.CodeReviewSessionStatusQueued
	unqueued.RetryableFailure = false
	unqueued.GitHubReviewID = nil
	unqueued.GitHubReviewURL = nil
	unqueued.ReviewOutputKey = "unqueued-output"
	unqueued.CreatedAt = time.Now().Add(-2 * codeReviewJobEnqueueGracePeriod)
	metadata := &metadataStub{
		getBySession: source, latest: source, latestByPullRequest: unqueued, submitted: source,
	}
	head := "head"
	base := "base"
	pullRequests := &pullRequestStub{
		result: models.PullRequest{
			ID: pullRequestID, OrgID: orgID, GitHubRepo: "acme/repo", GitHubPRNumber: 42,
			GitHubPRURL: "https://github.com/acme/repo/pull/42", Title: "Fix rounding",
			Status: models.PullRequestStatusOpen, HeadSHA: &head, BaseSHA: &base,
		},
		health: models.PullRequestHealthCurrent{
			PullRequestID: pullRequestID, OrgID: orgID, HeadSHA: head, BaseSHA: base,
		},
	}
	sessions := &sessionStub{getResult: models.Session{RevisionContext: json.RawMessage(`{"pull_request_author":"anya"}`)}}
	jobs := &jobStub{jobID: uuid.New()}
	svc := NewService(newPolicyStub(), metadata, sessions, jobs, zerolog.Nop(), Config{})
	svc.SetRetryDependencies(pullRequests, &pullRequestSyncerStub{})

	_, err := svc.RetryReview(context.Background(), RetryReviewInput{OrgID: orgID, SessionID: source.SessionID})

	var conflict *RetryReviewConflictError
	require.ErrorAs(t, err, &conflict, "selected source should report the newer replacement")
	require.Equal(t, RetryReviewConflictNewerAttempt, conflict.Code, "recovered orphan should remain a newer-attempt conflict for the old row")
	require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.latestByPullRequest.Status, "orphaned replacement should be terminalized")
	require.True(t, metadata.latestByPullRequest.RetryableFailure, "orphaned replacement should become manually recoverable")
	require.Equal(t, unqueued.SessionID, *metadata.latest.SupersededBySessionID, "old attempt should link to the recovered orphan")
	require.Equal(t, 1, sessions.updateStatusCalls, "orphaned replacement session should be terminalized")

	failedReplacement := metadata.latestByPullRequest
	metadata.getBySession = failedReplacement
	metadata.latest = failedReplacement
	metadata.latestByPullRequest = models.CodeReviewSessionMetadata{}
	jobs.jobID = uuid.New()
	recovered, retryErr := svc.RetryReview(context.Background(), RetryReviewInput{OrgID: orgID, SessionID: failedReplacement.SessionID})

	require.NoError(t, retryErr, "recovered orphan should support a fresh retry")
	require.NotEqual(t, failedReplacement.SessionID, recovered.SessionID, "retry should create a new immutable session")
	require.Equal(t, recovered.SessionID, *metadata.latest.SupersededBySessionID, "recovered orphan should link to its queued replacement")
	require.Equal(t, 1, jobs.enqueueCalls, "retrying the recovered orphan should enqueue one job")
}

func newReviewChangedInput() ReviewChangedInput {
	return ReviewChangedInput{
		OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(),
		GitHubRepo: "acme/repo", GitHubPRNumber: 42,
		GitHubPRURL: "https://github.com/acme/repo/pull/42", PullRequestTitle: "Fix rounding",
		BaseSHA: "base", HeadSHA: "head", ChangeKey: "delivery-143", ChangeReason: "pull_request.edited",
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

func (s *policyStub) ResolvePolicy(_ context.Context, _ uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	return s.resolved, nil
}

func (s *policyStub) SavePolicy(_ context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, _ *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	s.saveCalls++
	return models.CodeReviewPolicyRecord{ID: uuid.New(), OrgID: orgID, Version: 1, Enabled: config.Enabled, ApprovalMode: config.ApprovalMode}, nil
}

type metadataStub struct {
	createCalls         int
	staleCalls          int
	supersededBy        *uuid.UUID
	created             models.CodeReviewSessionMetadata
	createResult        models.CodeReviewSessionMetadata
	getBySession        models.CodeReviewSessionMetadata
	latest              models.CodeReviewSessionMetadata
	latestByHead        models.CodeReviewSessionMetadata
	latestByPullRequest models.CodeReviewSessionMetadata
	submitted           models.CodeReviewSessionMetadata
	latestAfterFail     models.CodeReviewSessionMetadata
	failCalls           int
	failErr             error
	approved            bool
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

func (s *metadataStub) GetByOutputKey(context.Context, uuid.UUID, string) (models.CodeReviewSessionMetadata, error) {
	if s.createResult.ID != uuid.Nil {
		return s.createResult, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) GetBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.getBySession.ID != uuid.Nil {
		return s.getBySession, nil
	}
	if s.latest.ID != uuid.Nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) GetLatestByPullRequestHead(_ context.Context, _, _ uuid.UUID, _ string, _ uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.latestByHead.ID != uuid.Nil {
		return s.latestByHead, nil
	}
	if s.latest.ID != uuid.Nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) GetLatestByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.latestByPullRequest.ID != uuid.Nil {
		return s.latestByPullRequest, nil
	}
	if s.latest.ID != uuid.Nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) GetLatestSubmittedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.submitted.GitHubReviewID != nil {
		return s.submitted, nil
	}
	if s.latest.GitHubReviewID != nil {
		return s.latest, nil
	}
	return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
}

func (s *metadataStub) HasApprovedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return s.approved, nil
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

func (s *metadataStub) FailReviewWithStatus(_ context.Context, _ uuid.UUID, params db.FailCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	s.failCalls++
	var failed models.CodeReviewSessionMetadata
	var assign func(models.CodeReviewSessionMetadata)
	switch {
	case s.created.SessionID == params.SessionID:
		failed = s.created
		assign = func(value models.CodeReviewSessionMetadata) { s.created = value }
	case s.latestByPullRequest.SessionID == params.SessionID:
		failed = s.latestByPullRequest
		assign = func(value models.CodeReviewSessionMetadata) { s.latestByPullRequest = value }
	case s.latest.SessionID == params.SessionID:
		failed = s.latest
		assign = func(value models.CodeReviewSessionMetadata) { s.latest = value }
	default:
		return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
	}
	failed.Status = models.CodeReviewSessionStatusFailed
	failed.Phase = nil
	failed.FailureReason = &params.Reason
	failed.StatusCode = &params.Code
	failed.StatusMessage = &params.Message
	failed.RetryableFailure = params.Retryable
	assign(failed)
	return failed, nil
}

func (s *metadataStub) MarkSupersededBy(_ context.Context, _ uuid.UUID, sessionID, replacementSessionID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	if s.latest.SessionID != sessionID {
		return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
	}
	s.latest.SupersededBySessionID = &replacementSessionID
	return s.latest, nil
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
	getResult          models.Session
}

func (s *sessionStub) Create(_ context.Context, session *models.Session) error {
	s.createCalls++
	session.ID = uuid.New()
	s.created = *session
	return nil
}

func (s *sessionStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return s.getResult, nil
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
	enqueueCalls        int
	jobID               uuid.UUID
	jobType             string
	dedupeKey           string
	payload             RunCodeReviewJobPayload
	reassessmentPayload ReviewChangedInput
	opts                db.EnqueueOpts
	active              bool
	err                 error
}

type pullRequestStub struct {
	result              models.PullRequest
	health              models.PullRequestHealthCurrent
	healthOrgID         uuid.UUID
	healthPullRequestID uuid.UUID
	err                 error
}

func (s *pullRequestStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.PullRequest, error) {
	return s.result, s.err
}

func (s *pullRequestStub) GetHealthCurrent(_ context.Context, orgID, pullRequestID uuid.UUID) (models.PullRequestHealthCurrent, error) {
	s.healthOrgID = orgID
	s.healthPullRequestID = pullRequestID
	return s.health, s.err
}

type pullRequestSyncerStub struct {
	err error
}

func (s *pullRequestSyncerStub) SyncPullRequestState(context.Context, uuid.UUID, uuid.UUID) error {
	return s.err
}

func (s *jobStub) EnqueueWithOpts(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	s.enqueueCalls++
	s.opts = opts
	s.jobType = opts.JobType
	if typed, ok := opts.Payload.(RunCodeReviewJobPayload); ok {
		s.payload = typed
	}
	if typed, ok := opts.Payload.(ReviewChangedInput); ok {
		s.reassessmentPayload = typed
	}
	if opts.DedupeKey != nil {
		s.dedupeKey = *opts.DedupeKey
	}
	return s.jobID, s.err
}

func (s *jobStub) HasActiveByDedupeKey(context.Context, uuid.UUID, string, string) (bool, error) {
	return s.active, nil
}

func boolPtr(value bool) *bool {
	return &value
}
