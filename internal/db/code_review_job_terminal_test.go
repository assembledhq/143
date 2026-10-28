package db

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func codeReviewTerminalTestFailure() CodeReviewTerminalFailure {
	return CodeReviewTerminalFailure{JobError: "synthetic worker failure", Reason: "code review job failed: synthetic worker failure", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic terminal failure", Retryable: true}
}

func codeReviewTerminalTestScope(kind string) CodeReviewTerminalScope {
	return CodeReviewTerminalScope{OrgID: uuid.New(), JobID: uuid.New(), LockToken: uuid.New(), JobType: kind, SessionID: uuid.New(), MetadataID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(), PolicyID: uuid.New(), HeadSHA: "synthetic-head", OutputKey: "synthetic-output", ReviewScope: models.CodeReviewScopeFull}
}

func codeReviewTerminalTestCandidate(s CodeReviewTerminalScope) codeReviewTerminalCandidate {
	return codeReviewTerminalCandidate{kind: s.JobType, object: true, raw: [9]string{s.OrgID.String(), s.SessionID.String(), s.MetadataID.String(), s.RepositoryID.String(), s.PullRequestID.String(), s.PolicyID.String(), s.HeadSHA, s.OutputKey, s.AssessmentID.String()}}
}

func codeReviewTerminalTestRows(c codeReviewTerminalCandidate) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"job_type", "object", "org", "session", "metadata", "repository", "pr", "policy", "head", "output", "assessment"}).
		AddRow(c.kind, c.object, c.raw[0], c.raw[1], c.raw[2], c.raw[3], c.raw[4], c.raw[5], c.raw[6], c.raw[7], c.raw[8])
}

func codeReviewTerminalTestMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "construct a private ordered CPU SQL mock, not a production settlement runner")
	t.Cleanup(mock.Close)
	return mock
}

func codeReviewTerminalTestCandidateRead(mock pgxmock.PgxPoolIface, s CodeReviewTerminalScope, c codeReviewTerminalCandidate, locked bool) {
	q := codeReviewTerminalCandidateQuery
	if locked {
		q += ` FOR UPDATE`
	}
	mock.ExpectQuery(regexp.QuoteMeta(q)).WithArgs(s.OrgID, s.JobID, s.LockToken, codeReviewDispatchPreparationMaxBytes).WillReturnRows(codeReviewTerminalTestRows(c))
}

func TestCodeReviewTerminalCandidateClosedScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		kind   string
		mutate func(*codeReviewTerminalCandidate)
		valid  bool
	}{
		{name: "full exact owner", kind: models.JobTypeRunCodeReview, valid: true},
		{name: "recheck exact assessment only", kind: models.JobTypeRunCodeReviewRecheck, valid: true},
		{name: "nonobject", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.object = false }},
		{name: "foreign payload org", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[0] = uuid.NewString() }},
		{name: "nil metadata", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[2] = uuid.Nil.String() }},
		{name: "malformed metadata", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[2] = "not-a-uuid" }},
		{name: "noncanonical UUID", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[2] = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }},
		{name: "missing head", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[6] = "" }},
		{name: "missing output", kind: models.JobTypeRunCodeReview, mutate: func(c *codeReviewTerminalCandidate) { c.raw[7] = "" }},
		{name: "missing recheck id", kind: models.JobTypeRunCodeReviewRecheck, mutate: func(c *codeReviewTerminalCandidate) { c.raw[8] = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := codeReviewTerminalTestScope(tt.kind)
			if tt.kind == models.JobTypeRunCodeReviewRecheck {
				scope.AssessmentID = uuid.New()
			}
			candidate := codeReviewTerminalTestCandidate(scope)
			if tt.mutate != nil {
				tt.mutate(&candidate)
			}
			actual, valid := candidate.owner(scope.OrgID, scope.JobID, scope.LockToken)
			require.Equal(t, tt.valid, valid, "only complete canonical original owner identity may select domain work")
			if tt.valid {
				expected := scope
				if tt.kind == models.JobTypeRunCodeReviewRecheck {
					expected = CodeReviewTerminalScope{OrgID: scope.OrgID, JobID: scope.JobID, LockToken: scope.LockToken, JobType: tt.kind, AssessmentID: scope.AssessmentID}
				}
				require.Equal(t, expected, actual, "full and recheck projections must preserve their different exact owner domains")
			}
		})
	}
}

func TestCodeReviewTerminalFailureAndConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CodeReviewTerminalFailure)
		valid  bool
	}{
		{name: "bounded existing classification", valid: true},
		{name: "empty reason", mutate: func(f *CodeReviewTerminalFailure) { f.Reason = " \t" }},
		{name: "unknown typed code", mutate: func(f *CodeReviewTerminalFailure) { f.Code = "synthetic-unknown" }},
		{name: "reason beyond existing bound", mutate: func(f *CodeReviewTerminalFailure) { f.Reason = strings.Repeat("x", 2001) }},
		{name: "invalid UTF8", mutate: func(f *CodeReviewTerminalFailure) { f.JobError = string([]byte{0xff}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := codeReviewTerminalTestFailure()
			if tt.mutate != nil {
				tt.mutate(&f)
			}
			require.Equal(t, tt.valid, f.valid(), "failure validation must retain the finite existing classification and safe text shape")
		})
	}
	construction := []struct {
		name string
		make func(pgxmock.PgxPoolIface) (*JobStore, error)
	}{
		{name: "nil runner", make: func(m pgxmock.PgxPoolIface) (*JobStore, error) {
			return NewJobStoreWithCodeReviewTerminalTransactions(m, nil)
		}},
		{name: "typed nil runner", make: func(m pgxmock.PgxPoolIface) (*JobStore, error) {
			var r *codeReviewMetadataTestRun
			return NewJobStoreWithCodeReviewTerminalTransactions(m, r)
		}},
		{name: "nil database", make: func(m pgxmock.PgxPoolIface) (*JobStore, error) {
			return NewJobStoreWithCodeReviewTerminalTransactions(nil, &codeReviewMetadataTestRun{})
		}},
		{name: "typed nil database", make: func(m pgxmock.PgxPoolIface) (*JobStore, error) {
			var p *pgxpool.Pool
			return NewJobStoreWithCodeReviewTerminalTransactions(p, &codeReviewMetadataTestRun{})
		}},
	}
	for _, tt := range construction {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s, err := tt.make(m)
			require.ErrorIs(t, err, ErrCodeReviewTerminalCapability, "construction must require explicit existing Run without DBTX inference")
			require.Nil(t, s, "failed construction cannot expose a partially aware store")
			require.NoError(t, m.ExpectationsWereMet(), "capability validation must not borrow a connection or issue SQL")
		})
	}
}

// These adapters test wrapper control flow only; they manufacture no claims
// about real PostgreSQL retries, commits, ACK loss or runtime convergence.
func TestCodeReviewTerminalPoolResultSettlement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                           string
		replay, finalError, noCallback bool
	}{
		{name: "known nil final settlement"}, {name: "later empty callback resets positive", replay: true}, {name: "error after positive callback discards it", finalError: true}, {name: "nil without callback refuses", noCallback: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			scope := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			c := codeReviewTerminalTestCandidate(scope)
			c.object = false
			if !tt.noCallback {
				m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
				codeReviewTerminalTestCandidateRead(m, scope, c, false)
				codeReviewTerminalTestCandidateRead(m, scope, c, true)
				m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(scope.OrgID, scope.JobID, scope.LockToken, codeReviewTerminalTestFailure().JobError, scope.JobType).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				if tt.replay {
					m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
					m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalCandidateQuery)).WithArgs(scope.OrgID, scope.JobID, scope.LockToken, codeReviewDispatchPreparationMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"job_type"}))
				}
			}
			cause := errors.New("synthetic final settlement uncertainty")
			calls := 0
			r := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				calls++
				if tt.noCallback {
					return nil
				}
				if err := work(ctx, m); err != nil {
					return err
				}
				if tt.replay {
					if err := work(ctx, m); err != nil {
						return err
					}
				}
				if tt.finalError {
					return cause
				}
				return nil
			}}
			s, err := NewJobStoreWithCodeReviewTerminalTransactions(m, r)
			require.NoError(t, err, "CPU wrapper receives one explicit Run capability")
			actual, err := s.DeadLetterCodeReviewWithLease(context.Background(), scope.OrgID, scope.JobID, scope.LockToken, codeReviewTerminalTestFailure())
			require.Equal(t, 1, calls, "the pool wrapper must not add an outer retry or alternate runner")
			if tt.finalError {
				require.ErrorIs(t, err, cause, "outer uncertainty must retain its cause without acceptance")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "no provisional result survives a final error")
			} else if tt.noCallback {
				require.ErrorIs(t, err, ErrCodeReviewTerminalCapability, "nil Run without callback is not commit proof")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "an uncalled callback cannot manufacture retirement")
			} else if tt.replay {
				require.NoError(t, err, "final nil callback may be a safe lease miss")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "final miss must erase earlier provisional ownership")
			} else {
				require.NoError(t, err, "only known nil final Run releases its actual outcome")
				require.True(t, actual.Matches(scope.OrgID, scope.JobID, scope.LockToken), "the known-commit result must bind exactly the original leased job")
				returned, hasOwner := actual.Scope()
				require.False(t, hasOwner, "malformed-scope retirement has no target authority")
				require.Equal(t, CodeReviewTerminalScope{OrgID: scope.OrgID, JobID: scope.JobID, LockToken: scope.LockToken, JobType: scope.JobType}, returned, "job-only result must strip every untrusted target field")
			}
			require.NoError(t, m.ExpectationsWereMet(), "only supplied-tx scalar reads and exact job update may occur on this job-only path")
		})
	}
}

func TestCodeReviewTerminalSuppliedTxRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                      string
		initialMiss, lockedMiss, changed, queryFailure, cancelled bool
	}{
		{name: "initial lease miss", initialMiss: true}, {name: "lease lost before lock", lockedMiss: true}, {name: "retained identity changed", changed: true}, {name: "candidate storage error", queryFailure: true}, {name: "cancelled before reads", cancelled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			c := codeReviewTerminalTestCandidate(s)
			c.object = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("synthetic private query failure")
			if tt.cancelled {
				cancel()
			} else {
				m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
				q := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalCandidateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, codeReviewDispatchPreparationMaxBytes)
				if tt.queryFailure {
					q.WillReturnError(cause)
				} else if tt.initialMiss {
					q.WillReturnRows(pgxmock.NewRows([]string{"job_type"}))
				} else {
					q.WillReturnRows(codeReviewTerminalTestRows(c))
					locked := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalCandidateQuery+` FOR UPDATE`)).WithArgs(s.OrgID, s.JobID, s.LockToken, codeReviewDispatchPreparationMaxBytes)
					if tt.lockedMiss {
						locked.WillReturnRows(pgxmock.NewRows([]string{"job_type"}))
					} else {
						c.raw[6] = "different-retained-head"
						locked.WillReturnRows(codeReviewTerminalTestRows(c))
					}
				}
			}
			actual, err := DeadLetterCodeReviewInTx(ctx, s.OrgID, m, s.JobID, s.LockToken, codeReviewTerminalTestFailure())
			require.Equal(t, CodeReviewTerminalOutcome{}, actual, "lease miss, invalidated identity and storage errors must expose no provisional target or retirement")
			if tt.cancelled {
				require.ErrorIs(t, err, context.Canceled, "cancellation must retain its original cause before source reads")
			} else if tt.queryFailure {
				require.ErrorIs(t, err, ErrCodeReviewTerminalStorage, "query failures may not select the job-only fallback")
				require.ErrorIs(t, err, cause, "safe storage wrapper must preserve the private underlying cause")
				require.Equal(t, ErrCodeReviewTerminalStorage.Error(), err.Error(), "surface error text must not expose SQL or payload data")
			} else if tt.changed {
				require.ErrorIs(t, err, ErrCodeReviewTerminalScope, "later changed scope cannot be downgraded to job-only")
			} else {
				require.NoError(t, err, "a missing current lease is an ordinary nonowned miss")
			}
			require.NoError(t, m.ExpectationsWereMet(), "refusal must add no writes Begin settlement fallback or target read")
		})
	}
}

func codeReviewTerminalTestDomainPrefix(m pgxmock.PgxPoolIface, s CodeReviewTerminalScope, c codeReviewTerminalCandidate, initialPointer *uuid.UUID) {
	m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
	codeReviewTerminalTestCandidateRead(m, s, c, false)
	if s.JobType == models.JobTypeRunCodeReview {
		codeReviewTerminalTestInitialMetadataRead(m, s, initialPointer)
		// A full controller initially has no resolved assessment ID, even
		// when its retained unique publication-key read will discover A.
		rows := pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"})
		if s.AssessmentID != uuid.Nil {
			rows.AddRow(s.AssessmentID, s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope)
		}
		m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, uuid.Nil, codeReviewDispatchPreparationMaxBytes).WillReturnRows(rows)
	} else {
		m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalRecheckScopeQuery)).WithArgs(s.OrgID, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"session", "metadata", "repository", "pr", "policy", "head", "key", "scope"}).AddRow(s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope))
	}
	m.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`)).WithArgs("code_review_pr:" + s.OrgID.String() + ":" + s.PullRequestID.String()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
	m.ExpectQuery(regexp.QuoteMeta(codeReviewMetadataTerminalSessionLocks)).WithArgs(pgx.NamedArgs{"org_id": s.OrgID, "session_id": s.SessionID, "active_only": false}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(s.MetadataID))
	codeReviewTerminalTestMetadataMatch(m, s)
	codeReviewTerminalTestCandidateRead(m, s, c, true)
}

func codeReviewTerminalTestInitialMetadataRead(m pgxmock.PgxPoolIface, s CodeReviewTerminalScope, pointer *uuid.UUID) {
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalInitialMetadataQuery)).WithArgs(s.OrgID, s.MetadataID, s.SessionID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey).WillReturnRows(pgxmock.NewRows([]string{"assessment_id"}).AddRow(pointer))
}

func codeReviewTerminalTestMetadataMatch(m pgxmock.PgxPoolIface, s CodeReviewTerminalScope) {
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalMetadataMatchesQuery)).WithArgs(s.OrgID, s.MetadataID, s.SessionID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.JobType, s.HeadSHA, s.OutputKey).WillReturnRows(pgxmock.NewRows([]string{"matches"}).AddRow(true))
}

func TestCodeReviewTerminalFullOwnerMetadataAndFreshPending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                      string
		unresolved, childFailure, retainedAssessment, selfPointer bool
	}{
		{name: "ordinary no-assessment full owner"},
		{name: "broad retained due overrides retry advice", unresolved: true},
		{name: "child failure exposes no partial result", unresolved: true, childFailure: true},
		{name: "retained A with reported compatible NULL pointer", retainedAssessment: true},
		{name: "retained A with reported compatible self pointer", retainedAssessment: true, selfPointer: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			if tt.retainedAssessment {
				s.AssessmentID = uuid.New()
			}
			c := codeReviewTerminalTestCandidate(s)
			f := codeReviewTerminalTestFailure()
			var initialPointer *uuid.UUID
			if tt.selfPointer {
				initialPointer = &s.AssessmentID
			}
			codeReviewTerminalTestDomainPrefix(m, s, c, initialPointer)
			assessmentRows := pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"})
			if tt.retainedAssessment {
				assessmentRows.AddRow(s.AssessmentID, s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope)
			}
			m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentLockQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(assessmentRows)
			// EXISTS reports compatibility here. The exact query pin below and
			// real PostgreSQL fixtures, not a mock, establish NULL/self semantics.
			m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalMetadataAssessmentMatchesQuery)).WithArgs(s.OrgID, s.MetadataID, s.AssessmentID).WillReturnRows(pgxmock.NewRows([]string{"matches"}).AddRow(true))
			if tt.retainedAssessment {
				// A reported negative proof retains the ordinary failure contract;
				// actual receipt-field SQL semantics require the real-PG matrix.
				codeReviewTerminalTestCompletionPendingRead(m, s).WillReturnRows(codeReviewTerminalTestCompletionPendingRows(s, false))
			}
			m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalUnresolvedQuery)).WithArgs(s.OrgID, s.SessionID, s.MetadataID.String(), s.OutputKey).WillReturnRows(pgxmock.NewRows([]string{"unresolved"}).AddRow(tt.unresolved))
			m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, f.JobError, s.JobType).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			if tt.retainedAssessment {
				m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentUpdateQuery)).WithArgs(s.OrgID, s.AssessmentID, f.Reason).WillReturnRows(pgxmock.NewRows([]string{"id"}))
			}
			code, message, retryable := f.Code, f.Message, f.Retryable
			if tt.unresolved {
				code = models.CodeReviewStatusCodeDispatchUnresolved
				message = "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command."
				retryable = false
			}
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			reason := f.Reason
			expected := models.CodeReviewSessionMetadata{ID: s.MetadataID, OrgID: s.OrgID, SessionID: s.SessionID, RepositoryID: s.RepositoryID, PullRequestID: s.PullRequestID, PolicyID: s.PolicyID, BaseSHA: "base", HeadSHA: s.HeadSHA, TriggerSource: models.CodeReviewTriggerSourceAppReviewer, Status: models.CodeReviewSessionStatusFailed, StatusCode: &code, StatusMessage: &message, LastErrorAt: &now, RetryableFailure: retryable, ReviewOutputKey: s.OutputKey, FailureReason: &reason, CompletedAt: &now, CreatedAt: now}
			rows := codeReviewMetadataRowsForTest().AddRow(s.MetadataID, s.OrgID, s.SessionID, s.RepositoryID, s.PullRequestID, s.PolicyID, "base", s.HeadSHA, false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusFailed, nil, &code, &message, nil, &now, retryable, nil, nil, false, nil, s.OutputKey, nil, nil, nil, nil, &reason, &now, now)
			m.ExpectQuery("UPDATE code_review_session_metadata SET status = 'failed'.*RETURNING").WithArgs(pgx.NamedArgs{"org_id": s.OrgID, "session_id": s.SessionID, "failure_reason": f.Reason, "status_code": code, "status_message": message, "retryable_failure": retryable, "terminal_metadata_ids": []uuid.UUID{s.MetadataID}}).WillReturnRows(rows)
			cause := errors.New("synthetic later child write failure")
			child := m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalPendingCancelQuery)).WithArgs(s.OrgID, s.SessionID, s.MetadataID.String(), s.OutputKey)
			if tt.childFailure {
				child.WillReturnError(cause)
			} else {
				child.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			actual, err := DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, f)
			if tt.childFailure {
				require.ErrorIs(t, err, cause, "a later result write failure requires whole outer rollback")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "failed child mutation cannot release job or metadata descriptors")
			} else {
				require.NoError(t, err, "a pointer-compatible full owner must change exact metadata and pending in one supplied unit")
				actualScope, hasOwner := actual.Scope()
				require.True(t, hasOwner, "only the retained compatible full owner may release a provisional domain descriptor")
				require.Equal(t, s, actualScope, "legacy absence or retained full A must not be replaced with another assessment")
				metadata, ok := actual.Metadata()
				require.True(t, ok, "only the actual changed exact metadata is retained")
				require.Equal(t, expected, metadata, "existing failure fields and uncertainty override must match the full returned value")
				require.Equal(t, tt.unresolved, actual.UnresolvedDispatch(), "the broad pre-cancellation due fact must survive the later result statement")
				require.False(t, actual.Matches(s.OrgID, s.JobID, s.LockToken), "a supplied-tx result remains provisional without known pool commit")
			}
			require.NoError(t, m.ExpectationsWereMet(), "actual order must be PR then ordered metadata then exact job then assessment before fresh child statement")
		})
	}
}

func codeReviewTerminalTestCompletionPendingRead(m pgxmock.PgxPoolIface, s CodeReviewTerminalScope) *pgxmock.ExpectedQuery {
	return m.ExpectQuery(regexp.QuoteMeta(codeReviewConfirmedCompletionObservationQuery)).WithArgs(s.OrgID, s.MetadataID, codeReviewConfirmedCompletionObservationMaxBytes)
}

func codeReviewTerminalTestCompletionPendingRows(s CodeReviewTerminalScope, pending bool) *pgxmock.Rows {
	return codeReviewConfirmedCompletionTestRows(&s.AssessmentID, pending, true, codeReviewConfirmedCompletionTestReceipt(s.AssessmentID))
}

func codeReviewTerminalTestConfirmedPrefix(m pgxmock.PgxPoolIface, s CodeReviewTerminalScope) {
	codeReviewTerminalTestDomainPrefix(m, s, codeReviewTerminalTestCandidate(s), &s.AssessmentID)
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentLockQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"}).AddRow(s.AssessmentID, s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope))
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalMetadataAssessmentMatchesQuery)).WithArgs(s.OrgID, s.MetadataID, s.AssessmentID).
		WillReturnRows(pgxmock.NewRows([]string{"matches"}).AddRow(true))
}

// This SQL mock supplies a bounded structural projection and real decoder
// bytes. It covers control flow/sealing, not PostgreSQL snapshot semantics.
func TestCodeReviewTerminalCompletionPendingSettlement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                 string
		provisional, proofError, markerError, childError, finalError, replay bool
	}{
		{name: "known commit preserves confirmed outcome"},
		{name: "supplied transaction is provisional", provisional: true},
		{name: "proof read failure refuses before writes", proofError: true},
		{name: "diagnostic write error discards classification", markerError: true},
		{name: "later cancellation error discards classification", childError: true},
		{name: "final uncertainty discards positive callback", finalError: true},
		{name: "later callback resets positive pending proof", replay: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			s.AssessmentID = uuid.New()
			f := codeReviewTerminalTestFailure()
			cause := errors.New("synthetic bounded completion-pending failure")
			codeReviewTerminalTestConfirmedPrefix(m, s)
			proof := codeReviewTerminalTestCompletionPendingRead(m, s)
			if tt.proofError {
				proof.WillReturnError(cause)
			} else {
				proof.WillReturnRows(codeReviewTerminalTestCompletionPendingRows(s, true))
				m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalUnresolvedQuery)).WithArgs(s.OrgID, s.SessionID, s.MetadataID.String(), s.OutputKey).
					WillReturnRows(pgxmock.NewRows([]string{"unresolved"}).AddRow(true))
				m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, f.JobError, s.JobType).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				marker := m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalCompletionRecoveryMarkerQuery)).
					WithArgs(s.OrgID, s.AssessmentID, CodeReviewConfirmedCompletionRecoveryPending, CodeReviewConfirmedCompletionOperatorRequired)
				if tt.markerError {
					marker.WillReturnError(cause)
				} else {
					marker.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					child := m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalPendingCancelQuery)).WithArgs(s.OrgID, s.SessionID, s.MetadataID.String(), s.OutputKey)
					if tt.childError {
						child.WillReturnError(cause)
					} else {
						child.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					}
				}
				if tt.replay {
					m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
					m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalCandidateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, codeReviewDispatchPreparationMaxBytes).
						WillReturnRows(pgxmock.NewRows([]string{"job_type"}))
				}
			}
			var actual CodeReviewTerminalOutcome
			var err error
			if tt.provisional {
				actual, err = DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, f)
			} else {
				calls := 0
				runner := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
					calls++
					if callbackErr := work(ctx, m); callbackErr != nil {
						return callbackErr
					}
					if tt.replay {
						if callbackErr := work(ctx, m); callbackErr != nil {
							return callbackErr
						}
					}
					if tt.finalError {
						return cause
					}
					return nil
				}}
				store, constructionErr := NewJobStoreWithCodeReviewTerminalTransactions(m, runner)
				require.NoError(t, constructionErr, "completion-pending wrapper must receive an explicit existing transaction capability")
				actual, err = store.DeadLetterCodeReviewWithLease(context.Background(), s.OrgID, s.JobID, s.LockToken, f)
				require.Equal(t, 1, calls, "positive completion proof adds no new outer runner or retry policy")
			}
			if tt.proofError || tt.markerError || tt.childError || tt.finalError {
				require.ErrorIs(t, err, cause, "proof, cancellation or outer uncertainty must preserve the exact private cause")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "no pending disposition or target descriptor survives any final error")
			} else if tt.replay {
				require.NoError(t, err, "a later safe lease miss may settle without previous callback authority")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "every replay resets the positive pending proof and exact owner captures")
			} else {
				require.NoError(t, err, "exact confirmed completion-pending retirement must preserve its domain while retiring the job")
				require.True(t, actual.Owned(), "the exact still-leased job is retired even though its confirmed review remains active")
				require.Equal(t, !tt.provisional, actual.CompletionRecoveryPending(), "only a known successful outer commit releases the positive pending classification")
				require.Equal(t, !tt.provisional, actual.Matches(s.OrgID, s.JobID, s.LockToken), "pending classification remains correlated to the exact committed lease")
				require.True(t, actual.UnresolvedDispatch(), "original due fact is observed before pending cancellation and remains independent of metadata failure")
				metadata, metadataChanged := actual.Metadata()
				require.False(t, metadataChanged, "preserved active metadata must not fabricate a changed failure descriptor")
				require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "no unchanged active metadata row is published as a terminal event")
				assessment, assessmentChanged := actual.Assessment()
				require.False(t, assessmentChanged, "a saved confirmed publication is not converted into a failed assessment")
				require.Equal(t, models.CodeReviewAssessment{}, assessment, "no assessment descriptor is fabricated merely to communicate recovery pending")
			}
			require.NoError(t, m.ExpectationsWereMet(), "positive classification must follow exact locks and due observation, retire the job, record only the exact diagnostic, and cancel pending children in a later statement")
		})
	}
}

func TestCodeReviewTerminalCompletionPendingSealedClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CodeReviewTerminalOutcome)
		want   bool
	}{
		{name: "positive exact committed full owner", want: true},
		{name: "no ownership", mutate: func(o *CodeReviewTerminalOutcome) { o.owned = false }},
		{name: "no domain owner", mutate: func(o *CodeReviewTerminalOutcome) { o.hasOwner = false }},
		{name: "provisional callback only", mutate: func(o *CodeReviewTerminalOutcome) { o.committed = false }},
		{name: "missing positive proof", mutate: func(o *CodeReviewTerminalOutcome) { o.completionPending = false }},
		{name: "recheck cannot inherit full exception", mutate: func(o *CodeReviewTerminalOutcome) { o.scope.JobType = models.JobTypeRunCodeReviewRecheck }},
		{name: "evidence-only cannot inherit full exception", mutate: func(o *CodeReviewTerminalOutcome) { o.scope.ReviewScope = models.CodeReviewScopeEvidenceOnly }},
		{name: "legacy review ID alone lacks original A", mutate: func(o *CodeReviewTerminalOutcome) { o.scope.AssessmentID = uuid.Nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			s.AssessmentID = uuid.New()
			outcome := CodeReviewTerminalOutcome{owned: true, hasOwner: true, committed: true, completionPending: true, scope: s}
			if tt.mutate != nil {
				tt.mutate(&outcome)
			}
			require.Equal(t, tt.want, outcome.CompletionRecoveryPending(), "completion-pending suppression requires positive exact full-owner proof and a known committed retirement")
			raw, err := json.Marshal(outcome) //nolint:staticcheck // Intentionally verify sealed persistence capabilities serialize without private fields.
			require.NoError(t, err, "the sealed pending bit remains serializable only as an empty internal value")
			require.JSONEq(t, `{}`, string(raw), "pending completion is not a new public receipt or business status")
		})
	}
}

func TestCodeReviewTerminalCompletionPendingProofRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(*CodeReviewTerminalScope)
		query     bool
		readError bool
	}{
		{name: "full original assessment proof reports false", query: true},
		{name: "proof read failure is not ordinary absence", query: true, readError: true},
		{name: "no original assessment never infers from metadata review ID", mutate: func(s *CodeReviewTerminalScope) { s.AssessmentID = uuid.Nil }},
		{name: "recheck cannot request full publication exemption", mutate: func(s *CodeReviewTerminalScope) { s.JobType = models.JobTypeRunCodeReviewRecheck }},
		{name: "evidence-only owner cannot request full publication exemption", mutate: func(s *CodeReviewTerminalScope) { s.ReviewScope = models.CodeReviewScopeEvidenceOnly }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			s.AssessmentID = uuid.New()
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			cause := errors.New("synthetic exact receipt proof read error")
			if tt.query {
				read := codeReviewTerminalTestCompletionPendingRead(m, s)
				if tt.readError {
					read.WillReturnError(cause)
				} else {
					read.WillReturnRows(codeReviewTerminalTestCompletionPendingRows(s, false))
				}
			}
			pending, err := codeReviewTerminalCompletionPending(context.Background(), m, s)
			require.False(t, pending, "a negative proof, inapplicable original owner or storage error never grants pending-completion authority")
			if tt.readError {
				require.ErrorIs(t, err, ErrCodeReviewTerminalStorage, "a query failure must remain an explicit safe storage refusal")
				require.ErrorIs(t, err, cause, "the exact private query cause must not be mistaken for a missing receipt")
			} else {
				require.NoError(t, err, "known inapplicability does not itself mutate the ordinary failure contract")
			}
			require.NoError(t, m.ExpectationsWereMet(), "only an exact retained full assessment may issue the supplied-tx scalar proof and no branch may write or check out another connection")
		})
	}
}

func TestCodeReviewTerminalCompletionPendingQueryProofBoundary(t *testing.T) {
	t.Parallel()
	// These CPU pins cover exact structural proof only; the leaf decoder owns
	// receipt semantics and actual PostgreSQL owns stored JSONB/snapshot proof.
	tests := []struct{ fragment string }{
		{"m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id"},
		{"m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id"},
		{"m.head_sha=a.head_sha AND m.review_output_key=a.publication_key"},
		{"(m.assessment_id IS NULL OR m.assessment_id=a.id) AND m.status IN ('queued','running')"},
		{"a.review_scope='full' AND a.status='publishing' AND a.publication_state='confirmed'"},
		{"a.result_origin IS NOT NULL AND a.decision IS NOT NULL AND a.acceptable IS NOT NULL"},
		{"a.rendered_body IS NOT NULL AND a.structured_outcome IS NOT NULL"},
		{"a.github_review_id IS NOT NULL AND a.submitted_commit_sha=a.head_sha"},
		{"(m.github_review_id IS NULL OR m.github_review_id=a.github_review_id)"},
	}
	for _, tt := range tests {
		t.Run(tt.fragment, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, codeReviewConfirmedFullCompletionStructure, tt.fragment, "every original owner and staged/confirmed condition remains separate from the shared receipt decoder")
		})
	}
	require.NotContains(t, codeReviewConfirmedFullCompletionStructure, "coverage_complete", "incomplete staged coverage retains the existing receipt-recovery predicate")
	require.NotContains(t, codeReviewConfirmedFullCompletionStructure, "publication_receipt->", "SQL cannot silently substitute a second canonical-key receipt decoder")
	require.NotContains(t, codeReviewConfirmedCompletionObservationQuery, "SELECT *", "the shared proof cannot fetch an unbounded assessment/body model")
	require.Contains(t, codeReviewTerminalCompletionRecoveryMarkerQuery, "WHEN a.failure_detail=$3 THEN $4 ELSE $3 END", "exact committed first retirement is the only automatic repair allowance")
	require.Contains(t, codeReviewTerminalCompletionRecoveryMarkerQuery, "WHEN a.failure_detail LIKE 'operator_reconciliation_required:%' THEN a.failure_detail", "repeated or manually queued retirements cannot erase an existing operator pause")
	require.Contains(t, codeReviewTerminalCompletionRecoveryMarkerQuery, "WHERE a.org_id=$1 AND a.id=$2", "the diagnostic belongs only to the exact locked tenant assessment")
}

func TestCodeReviewTerminalFullMetadataPointerRefusesBeforeEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                            string
		legacy, missing, storageFailure bool
	}{
		{name: "retained A rejects conflicting pointer"},
		{name: "legacy absence rejects nonnull pointer", legacy: true},
		{name: "retained A rejects missing metadata", missing: true},
		{name: "legacy absence rejects missing metadata", legacy: true, missing: true},
		{name: "retained A pointer read preserves storage cause", storageFailure: true},
		{name: "legacy pointer read preserves storage cause", legacy: true, storageFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			if !tt.legacy {
				s.AssessmentID = uuid.New()
			}
			c := codeReviewTerminalTestCandidate(s)
			codeReviewTerminalTestDomainPrefix(m, s, c, nil)
			rows := pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"})
			if !tt.legacy {
				rows.AddRow(s.AssessmentID, s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope)
			}
			m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentLockQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(rows)
			pointer := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalMetadataAssessmentMatchesQuery)).WithArgs(s.OrgID, s.MetadataID, s.AssessmentID)
			cause := errors.New("synthetic retained pointer read failure")
			if tt.storageFailure {
				pointer.WillReturnError(cause)
			} else {
				// EXISTS is false for both a missing row and a conflicting
				// pointer; actual SQL/constraint semantics are a separate PG gate.
				pointer.WillReturnRows(pgxmock.NewRows([]string{"matches"}).AddRow(false))
			}
			actual, err := DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, codeReviewTerminalTestFailure())
			require.Equal(t, CodeReviewTerminalOutcome{}, actual, "a pointer conflict, missing metadata or storage failure must expose no provisional retirement or target")
			if tt.storageFailure {
				require.ErrorIs(t, err, ErrCodeReviewTerminalStorage, "pointer read errors cannot downgrade the retained owner to job-only retirement")
				require.ErrorIs(t, err, cause, "the fixed safe storage wrapper must preserve the actual private read cause")
				require.Equal(t, ErrCodeReviewTerminalStorage.Error(), err.Error(), "storage error text must not reveal retained metadata or pointer values")
			} else {
				require.ErrorIs(t, err, ErrCodeReviewTerminalScope, "missing or conflicting current association requires whole-unit rollback without selecting another assessment")
			}
			require.NoError(t, m.ExpectationsWereMet(), "the full pointer recheck must follow PR/ordered metadata/exact job/retained assessment fences and precede unresolved reads or any DML")
		})
	}
}

func TestCodeReviewTerminalFullMetadataPointerQueryBoundary(t *testing.T) {
	t.Parallel()
	expected := `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2
 AND (assessment_id IS NULL OR ($3<>'00000000-0000-0000-0000-000000000000'::uuid AND assessment_id=$3)))`
	require.Equal(t, expected, codeReviewTerminalMetadataAssessmentMatchesQuery, "the final tenant-scoped pointer read permits NULL/self only and true legacy absence permits only NULL")
	require.Equal(t, `SELECT assessment_id FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND repository_id=$4 AND pull_request_id=$5 AND policy_id=$6
 AND head_sha=$7 AND review_output_key=$8`, codeReviewTerminalInitialMetadataQuery, "the initial full scope and nullable pointer must be read together before A is resolved, without a premature NULL-only filter")
	require.NotContains(t, codeReviewTerminalMetadataMatchesQuery, "assessment_id", "the later common tuple fence must preserve rechecks' distinct baseline semantics")
}

func TestCodeReviewTerminalRecheckNeverMutatesFullMetadata(t *testing.T) {
	t.Parallel()
	m := codeReviewTerminalTestMock(t)
	s := codeReviewTerminalTestScope(models.JobTypeRunCodeReviewRecheck)
	s.AssessmentID = uuid.New()
	s.ReviewScope = models.CodeReviewScopeEvidenceOnly
	c := codeReviewTerminalTestCandidate(s)
	codeReviewTerminalTestDomainPrefix(m, s, c, nil)
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentLockQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"}).AddRow(s.AssessmentID, s.SessionID, s.MetadataID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey, s.ReviewScope))
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalUnresolvedQuery)).WithArgs(s.OrgID, s.SessionID, s.MetadataID.String(), s.OutputKey).WillReturnRows(pgxmock.NewRows([]string{"unresolved"}).AddRow(false))
	f := codeReviewTerminalTestFailure()
	m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, f.JobError, s.JobType).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentUpdateQuery)).WithArgs(s.OrgID, s.AssessmentID, f.Reason).WillReturnRows(pgxmock.NewRows([]string{"id"}))
	actual, err := DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, f)
	require.NoError(t, err, "protected recheck assessment may remain unchanged while exact job retires")
	scope, ok := actual.Scope()
	require.True(t, ok, "recheck correlation must retain only its exact actual assessment owner")
	require.Equal(t, s, scope, "recheck target may not be substituted with the source full baseline")
	metadata, changed := actual.Metadata()
	require.False(t, changed, "a recheck must never fail its shared full-review metadata")
	require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "no full metadata event descriptor may be manufactured")
	require.NoError(t, m.ExpectationsWereMet(), "recheck must issue no metadata UPDATE or pending cancellation for the full controller")
}

func TestCodeReviewTerminalOutcomeCopiesAndCommitFence(t *testing.T) {
	t.Parallel()
	s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
	s.AssessmentID = uuid.New()
	code := models.CodeReviewStatusCodeDispatchUnresolved
	message := "Synthetic safe message"
	body := "Synthetic retained output"
	m := models.CodeReviewSessionMetadata{ID: s.MetadataID, OrgID: s.OrgID, SessionID: s.SessionID, StatusCode: &code, StatusMessage: &message}
	a := models.CodeReviewAssessment{ID: s.AssessmentID, OrgID: s.OrgID, SessionID: s.SessionID, RenderedBody: &body, InputManifest: json.RawMessage(`{"synthetic":true}`), StructuredOutcome: json.RawMessage(`{"coverage_complete":false}`)}
	o := CodeReviewTerminalOutcome{owned: true, hasOwner: true, scope: s, metadata: &m, assessment: &a, unresolved: true}
	require.False(t, o.Matches(s.OrgID, s.JobID, s.LockToken), "positive InTx result cannot authorize known-commit hooks")
	actualM, ok := o.Metadata()
	require.True(t, ok, "provisional descriptor may be inspected only by its explicit outer owner")
	*actualM.StatusCode = models.CodeReviewStatusCodeWorkerFailed
	*actualM.StatusMessage = "mutated copy"
	againM, ok := o.Metadata()
	require.True(t, ok, "copy mutation must not invalidate original descriptor availability")
	require.Equal(t, m, againM, "metadata pointers must be defensively copied")
	actualA, ok := o.Assessment()
	require.True(t, ok, "actual assessment descriptor is retained independently")
	*actualA.RenderedBody = "mutated copy"
	actualA.InputManifest[0] = 'x'
	actualA.StructuredOutcome[0] = 'x'
	againA, ok := o.Assessment()
	require.True(t, ok, "copy mutation cannot alter assessment presence")
	require.Equal(t, a, againA, "assessment pointer and raw JSON fields must be defensively copied")
	o.committed = true
	require.True(t, o.Matches(s.OrgID, s.JobID, s.LockToken), "known nil pool settlement may release exact job correlation")
	require.False(t, o.Matches(uuid.New(), s.JobID, s.LockToken), "foreign org may not reuse a committed descriptor")
	require.False(t, o.Matches(s.OrgID, uuid.New(), s.LockToken), "another job may not reuse a committed descriptor")
	require.False(t, o.Matches(s.OrgID, s.JobID, uuid.New()), "another lease may not reuse a committed descriptor")
	for _, tt := range []struct {
		name  string
		value any
	}{{"outcome", o}, {"scope", s}, {"failure", codeReviewTerminalTestFailure()}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.value)
			require.NoError(t, err, "internal descriptors must not leak raw owner/failure data when serialized")
			require.JSONEq(t, `{}`, string(raw), "private retirement correlation is never a public JSON receipt")
		})
	}
}

func TestCodeReviewTerminalMissingOwnerStillRetiresOnlyJob(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, kind string }{{"full exact metadata absent", models.JobTypeRunCodeReview}, {"recheck exact assessment absent", models.JobTypeRunCodeReviewRecheck}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(tt.kind)
			if tt.kind == models.JobTypeRunCodeReviewRecheck {
				s.AssessmentID = uuid.New()
			}
			c := codeReviewTerminalTestCandidate(s)
			m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
			codeReviewTerminalTestCandidateRead(m, s, c, false)
			if tt.kind == models.JobTypeRunCodeReview {
				m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalInitialMetadataQuery)).WithArgs(s.OrgID, s.MetadataID, s.SessionID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey).WillReturnRows(pgxmock.NewRows([]string{"assessment_id"}))
			} else {
				m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalRecheckScopeQuery)).WithArgs(s.OrgID, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"session"}))
			}
			codeReviewTerminalTestCandidateRead(m, s, c, true)
			f := codeReviewTerminalTestFailure()
			m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, f.JobError, s.JobType).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			actual, err := DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, f)
			require.NoError(t, err, "a positively leased poison/missing-owner job remains safely retireable")
			require.True(t, actual.Owned(), "missing domain scope must not strand the exact running leased job")
			scope, hasOwner := actual.Scope()
			require.False(t, hasOwner, "missing owner cannot authorize metadata assessment result or provider targets")
			require.Equal(t, CodeReviewTerminalScope{OrgID: s.OrgID, JobID: s.JobID, LockToken: s.LockToken, JobType: s.JobType}, scope, "all target fields must be erased rather than inferred from an unrelated session")
			require.False(t, actual.UnresolvedDispatch(), "missing exact owner must not inherit another controller's unresolved classification")
			require.NoError(t, m.ExpectationsWereMet(), "job-only path must issue no PR metadata result or assessment mutation")
		})
	}
}

func TestCodeReviewTerminalDispatchQueriesBindExactFrozenWake(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, query string }{{"unresolved read", codeReviewTerminalUnresolvedQuery}, {"pending cancellation", codeReviewTerminalPendingCancelQuery}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, tt.query, codeReviewTerminalDispatchOwner, "both statements must use the identical immutable original-controller predicate, not session alone")
			for _, fragment := range []string{"r.org_id=$1", "r.session_id=$2", "r.role='reviewer'", "r.dispatch_version=1", "r.dispatch_wake->>'job_type'='run_code_review'", "r.dispatch_wake->>'queue'='agent'", "r.dispatch_wake->>'dedupe_key'='code_review:'||$4", "r.dispatch_wake->'payload'->>'org_id'=$1::text", "r.dispatch_wake->'payload'->>'session_id'=$2::text", "r.dispatch_wake->'payload'->>'metadata_id'=$3", "r.dispatch_wake->'payload'->>'review_output_key'=$4"} {
				require.Contains(t, tt.query, fragment, "a same-session sibling must not be treated as the retained controller's dispatch owner")
			}
			if tt.name == "unresolved read" {
				require.Contains(t, tt.query, "r.dispatch_due_at IS NOT NULL", "unresolved classification remains the adopted broad due predicate before cancellation")
				require.NotContains(t, tt.query, "dispatch_state='pending'", "unresolved read must not narrow away attempted unknown or attention intent")
			} else {
				require.Contains(t, tt.query, "AND r.dispatch_state='pending' ORDER BY r.id FOR UPDATE OF r", "only ordered supported pending results may be cancelled")
				require.Contains(t, tt.query, "dispatch_resolution='owner_cancelled'", "pending-only retirement must preserve the adopted finite resolution")
				require.NotContains(t, tt.query, "dispatch_attempt_id=", "original attempted history may never be cleared by owner cancellation")
			}
		})
	}
}

func TestCodeReviewTerminalUnconfiguredWrapperRefuses(t *testing.T) {
	t.Parallel()
	m := codeReviewTerminalTestMock(t)
	s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
	actual, err := NewJobStore(m).DeadLetterCodeReviewWithLease(context.Background(), s.OrgID, s.JobID, s.LockToken, codeReviewTerminalTestFailure())
	require.ErrorIs(t, err, ErrCodeReviewTerminalCapability, "DBTX alone must not imply Run or a legacy-blind terminal SQL fallback")
	require.Equal(t, CodeReviewTerminalOutcome{}, actual, "an unconfigured wrapper cannot acknowledge any retirement")
	require.NoError(t, m.ExpectationsWereMet(), "capability refusal precedes all SQL")
}

func TestCodeReviewTerminalSuppliedTxInputGuards(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		mutate        func(*CodeReviewTerminalScope, *CodeReviewTerminalFailure)
		nilCtx, nilTx bool
		expected      error
	}{
		{name: "nil context", nilCtx: true, expected: ErrCodeReviewTerminalCapability}, {name: "nil supplied transaction", nilTx: true, expected: ErrCodeReviewTerminalCapability},
		{name: "zero org", mutate: func(s *CodeReviewTerminalScope, _ *CodeReviewTerminalFailure) { s.OrgID = uuid.Nil }, expected: ErrCodeReviewTerminalInput},
		{name: "zero job", mutate: func(s *CodeReviewTerminalScope, _ *CodeReviewTerminalFailure) { s.JobID = uuid.Nil }, expected: ErrCodeReviewTerminalInput},
		{name: "zero lease", mutate: func(s *CodeReviewTerminalScope, _ *CodeReviewTerminalFailure) { s.LockToken = uuid.Nil }, expected: ErrCodeReviewTerminalInput},
		{name: "unsupported classification", mutate: func(_ *CodeReviewTerminalScope, f *CodeReviewTerminalFailure) { f.Code = "unknown" }, expected: ErrCodeReviewTerminalInput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			f := codeReviewTerminalTestFailure()
			if tt.mutate != nil {
				tt.mutate(&s, &f)
			}
			ctx := context.Background()
			if tt.nilCtx {
				ctx = nil
			}
			var tx pgx.Tx = m
			if tt.nilTx {
				tx = nil
			}
			actual, err := DeadLetterCodeReviewInTx(ctx, s.OrgID, tx, s.JobID, s.LockToken, f)
			require.ErrorIs(t, err, tt.expected, "invalid capability or identity must refuse before source reads")
			require.Equal(t, CodeReviewTerminalOutcome{}, actual, "input refusal cannot expose a target or owned result")
			require.NoError(t, m.ExpectationsWereMet(), "invalid input must not install settings or issue SQL")
		})
	}
}

func TestCodeReviewTerminalAssessmentRetainedIdentity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                                    string
		missing, newAssessment, changedMetadata bool
		valid                                   bool
	}{
		{name: "same exact retained full assessment", valid: true}, {name: "retained assessment disappeared", missing: true}, {name: "metadata association changed", changedMetadata: true}, {name: "legacy stays without assessment", missing: true, newAssessment: true, valid: true}, {name: "legacy cannot adopt a new matching assessment", newAssessment: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			s.AssessmentID = uuid.New()
			if tt.newAssessment {
				s.AssessmentID = uuid.Nil
			}
			actual := s
			if actual.AssessmentID == uuid.Nil {
				actual.AssessmentID = uuid.New()
			}
			if tt.changedMetadata {
				actual.MetadataID = uuid.New()
			}
			q := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentLockQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes)
			if tt.missing {
				q.WillReturnRows(pgxmock.NewRows([]string{"id"}))
			} else {
				q.WillReturnRows(pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"}).AddRow(actual.AssessmentID, actual.SessionID, actual.MetadataID, actual.RepositoryID, actual.PullRequestID, actual.PolicyID, actual.HeadSHA, actual.OutputKey, actual.ReviewScope))
			}
			locked, err := lockCodeReviewTerminalAssessment(context.Background(), m, s)
			if tt.valid {
				require.NoError(t, err, "only unchanged retained identity or unchanged legacy absence may pass")
				require.Equal(t, s, locked, "the lock cannot replace original identity with a current latest owner")
			} else {
				require.ErrorIs(t, err, ErrCodeReviewTerminalScope, "changed association or newly discovered assessment requires whole rollback")
				require.Equal(t, CodeReviewTerminalScope{}, locked, "failed revalidation exposes no replacement owner")
			}
			require.NoError(t, m.ExpectationsWereMet(), "assessment revalidation is one exact tenant-scoped lock read, without writes or discovery retries")
		})
	}
}

func TestCodeReviewTerminalCandidateRetainsFullAssessmentID(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name            string
		foreignMetadata bool
	}{{name: "exact optional assessment retained"}, {name: "inconsistent original assessment grants only job retirement", foreignMetadata: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			actual := s
			actual.AssessmentID = uuid.New()
			if tt.foreignMetadata {
				actual.MetadataID = uuid.New()
			}
			codeReviewTerminalTestInitialMetadataRead(m, s, nil)
			m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, s.AssessmentID, codeReviewDispatchPreparationMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"}).AddRow(actual.AssessmentID, actual.SessionID, actual.MetadataID, actual.RepositoryID, actual.PullRequestID, actual.PolicyID, actual.HeadSHA, actual.OutputKey, actual.ReviewScope))
			retained, valid, err := resolveCodeReviewTerminalOwner(context.Background(), m, s)
			require.NoError(t, err, "initial bounded owner projection must distinguish positive invalid scope from a storage error")
			require.Equal(t, !tt.foreignMetadata, valid, "only the complete original metadata/assessment tuple may authorize domain work")
			if valid {
				expected := s
				expected.AssessmentID = actual.AssessmentID
				require.Equal(t, expected, retained, "original full assessment ID must be retained before PR/metadata/job waits")
			}
			require.NoError(t, m.ExpectationsWereMet(), "initial resolution must not lock or mutate job metadata assessment or result rows")
		})
	}
}

func TestCodeReviewTerminalInitialPointerConflictRetiresOnlyJob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                string
		legacy, mismatchedAssessment, initialReadError, assessmentReadError bool
	}{
		{name: "different pointer than retained A"},
		{name: "nonnull pointer with genuine original A absence", legacy: true},
		{name: "nonnull pointer cannot rescue inconsistent original A", mismatchedAssessment: true},
		{name: "initial pointer storage failure never falls back", initialReadError: true},
		{name: "optional assessment storage failure never falls back", assessmentReadError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := codeReviewTerminalTestMock(t)
			s := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			c := codeReviewTerminalTestCandidate(s)
			other := uuid.New()
			cause := errors.New("synthetic original scope storage failure")
			m.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
			codeReviewTerminalTestCandidateRead(m, s, c, false)
			initial := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalInitialMetadataQuery)).WithArgs(s.OrgID, s.MetadataID, s.SessionID, s.RepositoryID, s.PullRequestID, s.PolicyID, s.HeadSHA, s.OutputKey)
			if tt.initialReadError {
				initial.WillReturnError(cause)
			} else {
				initial.WillReturnRows(pgxmock.NewRows([]string{"assessment_id"}).AddRow(&other))
				original := s
				original.AssessmentID = uuid.New()
				if tt.mismatchedAssessment {
					original.MetadataID = uuid.New()
				}
				rows := pgxmock.NewRows([]string{"id", "session", "metadata", "repository", "pr", "policy", "head", "key", "scope"})
				if !tt.legacy {
					rows.AddRow(original.AssessmentID, original.SessionID, original.MetadataID, original.RepositoryID, original.PullRequestID, original.PolicyID, original.HeadSHA, original.OutputKey, original.ReviewScope)
				}
				assessment := m.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalAssessmentQuery)).WithArgs(s.OrgID, s.JobType, s.SessionID, s.OutputKey, uuid.Nil, codeReviewDispatchPreparationMaxBytes)
				if tt.assessmentReadError {
					assessment.WillReturnError(cause)
				} else {
					assessment.WillReturnRows(rows)
					codeReviewTerminalTestCandidateRead(m, s, c, true)
					m.ExpectExec(regexp.QuoteMeta(codeReviewTerminalJobUpdateQuery)).WithArgs(s.OrgID, s.JobID, s.LockToken, codeReviewTerminalTestFailure().JobError, s.JobType).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
			}
			actual, err := DeadLetterCodeReviewInTx(context.Background(), s.OrgID, m, s.JobID, s.LockToken, codeReviewTerminalTestFailure())
			if tt.initialReadError || tt.assessmentReadError {
				require.ErrorIs(t, err, ErrCodeReviewTerminalStorage, "initial read errors must preserve storage refusal rather than infer a poison owner")
				require.ErrorIs(t, err, cause, "the exact initial read cause must remain available privately")
				require.Equal(t, CodeReviewTerminalOutcome{}, actual, "storage failure cannot acknowledge provisional retirement")
			} else {
				require.NoError(t, err, "a positively observed initial conflict must retire only the exact leased poison job")
				require.True(t, actual.Owned(), "initial pointer poison must not remain available for endless lease recovery")
				actualScope, hasOwner := actual.Scope()
				require.False(t, hasOwner, "no target-derived hook authority may survive initial positive inconsistency")
				require.Equal(t, CodeReviewTerminalScope{OrgID: s.OrgID, JobID: s.JobID, LockToken: s.LockToken, JobType: s.JobType}, actualScope, "all projected target fields must be scrubbed after job-only classification")
				require.False(t, actual.UnresolvedDispatch(), "another owner's dispatch may not influence a poison job's descriptor")
			}
			require.NoError(t, m.ExpectationsWereMet(), "capture must precede optional A lookup; initial poison must take no PR, metadata, assessment or result locks and no target read or write")
		})
	}
}
