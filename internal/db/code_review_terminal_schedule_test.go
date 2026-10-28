package db

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewTerminalSchedulerRequiresCommittedOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                         string
		owned, hasOwner, committed, foreign, absentAssessment, nilRunner, noCallback bool
		wantErr                                                                      bool
	}{
		{name: "zero outcome", wantErr: true},
		{name: "provisional owner", owned: true, hasOwner: true, wantErr: true},
		{name: "job only has no scheduler authority", owned: true, committed: true, wantErr: true},
		{name: "foreign committed owner", owned: true, hasOwner: true, committed: true, foreign: true, wantErr: true},
		{name: "missing explicit Run", owned: true, hasOwner: true, committed: true, nilRunner: true, wantErr: true},
		{name: "known legacy absence is a no-op", owned: true, hasOwner: true, committed: true, absentAssessment: true},
		{name: "nil Run without callback is not cleanup", owned: true, hasOwner: true, committed: true, noCallback: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := codeReviewTerminalTestScope(models.JobTypeRunCodeReview)
			scope.AssessmentID = uuid.New()
			if tt.absentAssessment {
				scope.AssessmentID = uuid.Nil
			}
			outcome := CodeReviewTerminalOutcome{owned: tt.owned, hasOwner: tt.hasOwner, committed: tt.committed, scope: scope}
			orgID := scope.OrgID
			if tt.foreign {
				orgID = uuid.New()
			}
			calls := 0
			var runner CodeReviewMetadataTransactions = &codeReviewMetadataTestRun{run: func(context.Context, func(context.Context, pgx.Tx) error) error { calls++; return nil }}
			if tt.nilRunner {
				var typedNil *codeReviewMetadataTestRun
				runner = typedNil
			}
			err := SettleCodeReviewTerminalAssessment(context.Background(), orgID, runner, outcome)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrCodeReviewTerminalCapability, "only an exact known-committed owner and actual supplied unit can attempt scheduler cleanup")
			} else {
				require.NoError(t, err, "a genuine no-assessment owner must remain a no-op, never adopt a new assessment")
			}
			wantCalls := 0
			if tt.noCallback {
				wantCalls = 1
			}
			require.Equal(t, wantCalls, calls, "capability refusal and legacy absence must issue no database unit")
		})
	}
}

func TestCodeReviewTerminalSchedulerRechecksExactOwnerBeforeWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		jobType                string
		mismatch, queryFailure bool
	}{
		{name: "full changed association or protected nonterminal owner", jobType: models.JobTypeRunCodeReview},
		{name: "full wrong returned identity refuses", jobType: models.JobTypeRunCodeReview, mismatch: true},
		{name: "full storage failure is not cleanup", jobType: models.JobTypeRunCodeReview, queryFailure: true},
		{name: "recheck changed association or protected nonterminal owner", jobType: models.JobTypeRunCodeReviewRecheck},
		{name: "recheck wrong returned identity refuses", jobType: models.JobTypeRunCodeReviewRecheck, mismatch: true},
		{name: "recheck storage failure is not cleanup", jobType: models.JobTypeRunCodeReviewRecheck, queryFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := codeReviewTerminalTestMock(t)
			scope := codeReviewTerminalTestScope(tt.jobType)
			scope.AssessmentID = uuid.New()
			// Deliberately no changed-assessment descriptor: an already-terminal
			// owner must still reach guarded scheduler-only reconciliation.
			outcome := CodeReviewTerminalOutcome{owned: true, hasOwner: true, committed: true, scope: scope}
			mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`)).WithArgs("code_review_pr:" + scope.OrgID.String() + ":" + scope.PullRequestID.String()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"isolation"}).AddRow("read committed"))
			mock.ExpectQuery(regexp.QuoteMeta(codeReviewMetadataTerminalSessionLocks)).WithArgs(pgx.NamedArgs{"org_id": scope.OrgID, "session_id": scope.SessionID, "active_only": false}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(scope.MetadataID))
			query := mock.ExpectQuery(regexp.QuoteMeta(codeReviewTerminalScheduleOwnerQuery)).WithArgs(scope.OrgID, scope.AssessmentID, scope.SessionID, scope.MetadataID, scope.RepositoryID, scope.PullRequestID, scope.PolicyID, scope.HeadSHA, scope.OutputKey, scope.ReviewScope, scope.JobType)
			cause := errors.New("synthetic owner read failure")
			if tt.queryFailure {
				query.WillReturnError(cause)
			} else if tt.mismatch {
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			} else {
				query.WillReturnError(pgx.ErrNoRows)
			}
			calls := 0
			runner := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				calls++
				return work(ctx, mock)
			}}
			err := SettleCodeReviewTerminalAssessment(context.Background(), scope.OrgID, runner, outcome)
			if tt.queryFailure {
				require.ErrorIs(t, err, cause, "the scheduler attempt must propagate a real read failure without mutation")
			} else if tt.mismatch {
				require.ErrorIs(t, err, ErrCodeReviewTerminalScope, "the original assessment identity cannot be substituted")
			} else {
				require.NoError(t, err, "a changed or nonterminal owner must safely skip rather than failing or adopting it")
			}
			require.Equal(t, 1, calls, "an already-terminal descriptor absence must not suppress the actual explicit cleanup unit")
			require.NoError(t, mock.ExpectationsWereMet(), "the original PR, ordered metadata and exact assessment must be checked before any request or scheduler write")
		})
	}
}

func TestCodeReviewTerminalSchedulerRejectsUnsupportedJobTypeBeforeSQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, jobType    string
		absentAssessment bool
	}{
		{name: "empty kind", jobType: ""},
		{name: "foreign job kind", jobType: "synthetic_other_job"},
		{name: "full kind is exact", jobType: "run_code_review "},
		{name: "recheck kind is exact", jobType: "RUN_CODE_REVIEW_RECHECK"},
		{name: "legacy absence cannot bypass closed kind", jobType: "synthetic_other_job", absentAssessment: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := codeReviewTerminalTestMock(t)
			scope := codeReviewTerminalTestScope(tt.jobType)
			if !tt.absentAssessment {
				scope.AssessmentID = uuid.New()
			}
			outcome := CodeReviewTerminalOutcome{owned: true, hasOwner: true, committed: true, scope: scope}
			calls := 0
			runner := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				calls++
				return work(ctx, mock)
			}}
			err := SettleCodeReviewTerminalAssessment(context.Background(), scope.OrgID, runner, outcome)
			require.ErrorIs(t, err, ErrCodeReviewTerminalScope, "only the two exact supported job kinds may select scheduler reconciliation")
			require.Equal(t, 0, calls, "unsupported kinds must be refused before opening a supplied unit or issuing SQL")
			require.NoError(t, mock.ExpectationsWereMet(), "unsupported kinds must not query even when their retained assessment is absent")
		})
	}
}

func TestCodeReviewTerminalSchedulerOwnerQueryQualifiesFullMetadataOnly(t *testing.T) {
	t.Parallel()
	// This CPU boundary pin describes the intended SQL, not execution proof of
	// legal late metadata changes or positive recheck scheduler settlement.
	expected := `SELECT a.id FROM code_review_revision_assessments a
 JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.session_id=$3 AND a.metadata_id=$4
 AND a.repository_id=$5 AND a.pull_request_id=$6 AND a.policy_id=$7
 AND a.head_sha=$8 AND a.publication_key=$9 AND a.review_scope=$10
 AND ($11='run_code_review_recheck' OR ($11='run_code_review'
 AND m.head_sha=$8 AND m.review_output_key=$9
 AND (m.assessment_id IS NULL OR m.assessment_id=a.id)))
 AND a.status IN ('failed','completed','cancelled','superseded') FOR UPDATE OF a`
	require.Equal(t, expected, codeReviewTerminalScheduleOwnerQuery, "full owners must retain metadata head/key/pointer binding without applying baseline-only fields to rechecks")
}
