package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type codeReviewTerminalCapabilityTestStore struct{ calls int }

func (s *codeReviewTerminalCapabilityTestStore) DeadLetterCodeReviewWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, db.CodeReviewTerminalFailure) (db.CodeReviewTerminalOutcome, error) {
	s.calls++
	return db.CodeReviewTerminalOutcome{}, nil
}

func TestCodeReviewTerminalStoreCapabilityBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		typedNil, directNil, supplied bool
	}{
		{name: "nil capability"},
		{name: "typed nil option", typedNil: true},
		{name: "typed nil at defensive runtime boundary", directNil: true},
		{name: "exact supplied pointer", supplied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var supplied CodeReviewTerminalStore
			var typedNil *codeReviewTerminalCapabilityTestStore
			actual := &codeReviewTerminalCapabilityTestStore{}
			if tt.typedNil {
				supplied = typedNil
			}
			if tt.supplied {
				supplied = actual
			}
			w := New(nil, zerolog.Nop(), "synthetic-node", WithCodeReviewTerminalStore(supplied))
			if tt.supplied {
				require.Same(t, actual, w.codeReviewTerminal, "explicit worker composition must preserve the exact supplied capability, not infer one from the queue")
			} else {
				require.Nil(t, w.codeReviewTerminal, "nil and typed-nil options must normalize to a missing capability before any job work")
			}
			if tt.directNil {
				w.codeReviewTerminal = typedNil
			}
			genericCalls, hooks := 0, 0
			w.jobs = &terminalLeaseStoreStub{deadLetterFn: func(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) { genericCalls++; return true, nil }}
			ctx := jobctx.WithDeadLetterHooks(context.Background())
			jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) { hooks++ })
			token := uuid.New()
			w.deadLetterJob(ctx, ctx, &models.Job{ID: uuid.New(), OrgID: uuid.New(), JobType: models.JobTypeRunCodeReview, LockToken: &token}, errors.New("synthetic controller failure"))
			wantCalls := 0
			if tt.supplied {
				wantCalls = 1
			}
			require.Equal(t, wantCalls, actual.calls, "only a nonnil explicitly supplied capability may be invoked")
			require.Zero(t, genericCalls, "a missing review capability must never fall back to ordinary job retirement")
			require.Zero(t, hooks, "missing capability or an unconfirmed zero outcome cannot release any hook")
		})
	}
}

func newCodeReviewTerminalScopeFixture() (db.CodeReviewTerminalScope, runCodeReviewPayload, models.CodeReviewAssessment) {
	job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New(), RepositoryID: uuid.New(),
		PullRequestID: uuid.New(), PolicyID: uuid.New(), HeadSHA: "synthetic-head", OutputKey: "synthetic-output"}
	assessment := models.CodeReviewAssessment{ID: uuid.New(), OrgID: job.OrgID, SessionID: job.SessionID, MetadataID: job.MetadataID,
		RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, PolicyID: job.PolicyID, HeadSHA: job.HeadSHA, PublicationKey: job.OutputKey, ReviewScope: models.CodeReviewScopeFull}
	scope := db.CodeReviewTerminalScope{JobType: models.JobTypeRunCodeReview, OrgID: job.OrgID, JobID: uuid.New(), LockToken: uuid.New(),
		SessionID: job.SessionID, MetadataID: job.MetadataID, RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID,
		PolicyID: job.PolicyID, HeadSHA: job.HeadSHA, OutputKey: job.OutputKey, AssessmentID: assessment.ID, ReviewScope: models.CodeReviewScopeFull}
	return scope, job, assessment
}

func TestCodeReviewTerminalScopeMatchesExactOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*db.CodeReviewTerminalScope)
		want   bool
	}{
		{name: "exact original owner", want: true},
		{name: "other org", mutate: func(s *db.CodeReviewTerminalScope) { s.OrgID = uuid.New() }},
		{name: "other session", mutate: func(s *db.CodeReviewTerminalScope) { s.SessionID = uuid.New() }},
		{name: "other metadata", mutate: func(s *db.CodeReviewTerminalScope) { s.MetadataID = uuid.New() }},
		{name: "other repository", mutate: func(s *db.CodeReviewTerminalScope) { s.RepositoryID = uuid.New() }},
		{name: "other pull request", mutate: func(s *db.CodeReviewTerminalScope) { s.PullRequestID = uuid.New() }},
		{name: "other policy", mutate: func(s *db.CodeReviewTerminalScope) { s.PolicyID = uuid.New() }},
		{name: "other head", mutate: func(s *db.CodeReviewTerminalScope) { s.HeadSHA = "other-head" }},
		{name: "other publication key", mutate: func(s *db.CodeReviewTerminalScope) { s.OutputKey = "other-output" }},
		{name: "other review scope", mutate: func(s *db.CodeReviewTerminalScope) { s.ReviewScope = models.CodeReviewScopeEvidenceOnly }},
		{name: "unrelated job type", mutate: func(s *db.CodeReviewTerminalScope) { s.JobType = "run_agent" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope, job, assessment := newCodeReviewTerminalScopeFixture()
			if tt.mutate != nil {
				tt.mutate(&scope)
			}
			require.Equal(t, tt.want, codeReviewTerminalScopeMatchesFull(scope, job), "full-review hooks must bind every original owner field, never merely the shared session")
			if scope.JobType == models.JobTypeRunCodeReview {
				scope.JobType = models.JobTypeRunCodeReviewRecheck
			}
			require.Equal(t, tt.want, codeReviewTerminalScopeMatchesAssessment(scope, assessment), "recheck hooks must bind the exact original assessment and complete owner association")
		})
	}
}

func TestCodeReviewTerminalScopeBindsAssessmentIdentityAndKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*db.CodeReviewTerminalScope, *models.CodeReviewAssessment)
		want   bool
	}{
		{"exact full fallback owner", func(*db.CodeReviewTerminalScope, *models.CodeReviewAssessment) {}, true},
		{"exact evidence-only owner", func(s *db.CodeReviewTerminalScope, a *models.CodeReviewAssessment) {
			s.ReviewScope, a.ReviewScope = models.CodeReviewScopeEvidenceOnly, models.CodeReviewScopeEvidenceOnly
		}, true},
		{"different assessment", func(s *db.CodeReviewTerminalScope, _ *models.CodeReviewAssessment) { s.AssessmentID = uuid.New() }, false},
		{"missing assessment", func(s *db.CodeReviewTerminalScope, a *models.CodeReviewAssessment) {
			s.AssessmentID, a.ID = uuid.Nil, uuid.Nil
		}, false},
		{"missing review scope", func(s *db.CodeReviewTerminalScope, a *models.CodeReviewAssessment) {
			s.ReviewScope, a.ReviewScope = "", ""
		}, false},
		{"invalid shared review scope", func(s *db.CodeReviewTerminalScope, a *models.CodeReviewAssessment) {
			s.ReviewScope, a.ReviewScope = "invalid", "invalid"
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope, _, assessment := newCodeReviewTerminalScopeFixture()
			scope.JobType = models.JobTypeRunCodeReviewRecheck
			tt.mutate(&scope, &assessment)
			require.Equal(t, tt.want, codeReviewTerminalScopeMatchesAssessment(scope, assessment), "only the exact original assessment and valid matching review kind can authorize the registered recheck hook")
		})
	}
}

func TestCodeReviewTerminalScopeRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*runCodeReviewPayload, *models.CodeReviewAssessment)
	}{
		{"org", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) { j.OrgID, a.OrgID = uuid.Nil, uuid.Nil }},
		{"session", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) {
			j.SessionID, a.SessionID = uuid.Nil, uuid.Nil
		}},
		{"metadata", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) {
			j.MetadataID, a.MetadataID = uuid.Nil, uuid.Nil
		}},
		{"repository", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) {
			j.RepositoryID, a.RepositoryID = uuid.Nil, uuid.Nil
		}},
		{"pull request", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) {
			j.PullRequestID, a.PullRequestID = uuid.Nil, uuid.Nil
		}},
		{"policy", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) {
			j.PolicyID, a.PolicyID = uuid.Nil, uuid.Nil
		}},
		{"head", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) { j.HeadSHA, a.HeadSHA = "", "" }},
		{"publication key", func(j *runCodeReviewPayload, a *models.CodeReviewAssessment) { j.OutputKey, a.PublicationKey = "", "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope, job, assessment := newCodeReviewTerminalScopeFixture()
			tt.mutate(&job, &assessment)
			scope.OrgID, scope.SessionID, scope.MetadataID, scope.RepositoryID = job.OrgID, job.SessionID, job.MetadataID, job.RepositoryID
			scope.PullRequestID, scope.PolicyID, scope.HeadSHA, scope.OutputKey = job.PullRequestID, job.PolicyID, job.HeadSHA, job.OutputKey
			require.False(t, codeReviewTerminalScopeMatchesFull(scope, job), "even equal absent scope cannot authorize full-review hook targets")
			scope.JobType = models.JobTypeRunCodeReviewRecheck
			require.False(t, codeReviewTerminalScopeMatchesAssessment(scope, assessment), "even equal absent scope cannot authorize recheck hook targets")
		})
	}
}

func TestCommittedCodeReviewTerminalOutcomeRejectsUnconfirmedContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		context func() context.Context
	}{
		{"nil context", func() context.Context { return nil }},
		{"no outcome", context.Background},
		{"zero outcome", func() context.Context {
			return context.WithValue(context.Background(), codeReviewTerminalOutcomeKey{}, db.CodeReviewTerminalOutcome{})
		}},
		{"zero outcome with unrelated exact-looking lease", func() context.Context {
			ctx := jobctx.WithJobID(context.Background(), uuid.New())
			ctx = jobctx.WithLockToken(ctx, uuid.New())
			return context.WithValue(ctx, codeReviewTerminalOutcomeKey{}, db.CodeReviewTerminalOutcome{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome, scope, valid := committedCodeReviewTerminalOutcome(tt.context(), uuid.New(), models.JobTypeRunCodeReview)
			require.False(t, valid, "direct or unconfirmed hooks cannot infer a committed retirement from context shape")
			require.Equal(t, db.CodeReviewTerminalOutcome{}, outcome, "refusal must discard every provisional or unrelated outcome descriptor")
			require.Equal(t, db.CodeReviewTerminalScope{}, scope, "refusal must return no target owner identity")
		})
	}
}
