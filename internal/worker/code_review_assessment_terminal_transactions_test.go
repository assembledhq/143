package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type assessmentTerminalTestRun struct {
	calls int
	err   error
}

func (r *assessmentTerminalTestRun) Run(context.Context, func(context.Context, pgx.Tx) error) error {
	r.calls++
	return r.err
}

func TestFullAssessmentMatchesMetadataOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*models.CodeReviewAssessment, *models.CodeReviewSessionMetadata)
		expected bool
	}{
		{name: "exact composite owner association", expected: true},
		{name: "absent assessment", change: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) { a.ID = uuid.Nil }},
		{name: "different review scope", change: func(a *models.CodeReviewAssessment, _ *models.CodeReviewSessionMetadata) {
			a.ReviewScope = models.CodeReviewScopeEvidenceOnly
		}},
		{name: "missing org", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.OrgID, m.OrgID = uuid.Nil, uuid.Nil
		}},
		{name: "different org", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) { m.OrgID = uuid.New() }},
		{name: "missing session", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.SessionID, m.SessionID = uuid.Nil, uuid.Nil
		}},
		{name: "different session", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) { m.SessionID = uuid.New() }},
		{name: "missing metadata", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.MetadataID, m.ID = uuid.Nil, uuid.Nil
		}},
		{name: "different metadata", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) { m.ID = uuid.New() }},
		{name: "missing PR", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.PullRequestID, m.PullRequestID = uuid.Nil, uuid.Nil
		}},
		{name: "different PR", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			m.PullRequestID = uuid.New()
		}},
		{name: "missing repository", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.RepositoryID, m.RepositoryID = uuid.Nil, uuid.Nil
		}},
		{name: "different repository", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) { m.RepositoryID = uuid.New() }},
		{name: "missing policy", change: func(a *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) {
			a.PolicyID, m.PolicyID = uuid.Nil, uuid.Nil
		}},
		{name: "different policy", change: func(_ *models.CodeReviewAssessment, m *models.CodeReviewSessionMetadata) { m.PolicyID = uuid.New() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assessment := models.CodeReviewAssessment{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New(), PullRequestID: uuid.New(), RepositoryID: uuid.New(), PolicyID: uuid.New(), ReviewScope: models.CodeReviewScopeFull}
			metadata := models.CodeReviewSessionMetadata{ID: assessment.MetadataID, OrgID: assessment.OrgID, SessionID: assessment.SessionID, PullRequestID: assessment.PullRequestID, RepositoryID: assessment.RepositoryID, PolicyID: assessment.PolicyID}
			if tt.change != nil {
				tt.change(&assessment, &metadata)
			}
			require.Equal(t, tt.expected, fullAssessmentMatchesMetadataOwner(assessment, metadata), "only a complete positively matching independent metadata association may choose the PR domain for reconciliation")
		})
	}
}

func TestFullAssessmentCompletionRequiresBoundedOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		missing        bool
		typedNil       bool
		invalidScope   bool
		invalidSession bool
		invalidStatus  bool
		expectedCalls  int
	}{
		{name: "absent owner", missing: true},
		{name: "typed nil owner", typedNil: true},
		{name: "different review scope", invalidScope: true},
		{name: "different session", invalidSession: true},
		{name: "already completed assessment", invalidStatus: true},
		{name: "run error returned without fallback", expectedCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("synthetic bounded owner refusal")
			runner := &assessmentTerminalTestRun{err: cause}
			var transactions db.CodeReviewMetadataTransactions = runner
			if tt.missing {
				transactions = nil
			} else if tt.typedNil {
				var absent *assessmentTerminalTestRun
				transactions = absent
			}
			sessionID := uuid.New()
			assessment := models.CodeReviewAssessment{
				SessionID:   sessionID,
				ReviewScope: models.CodeReviewScopeFull,
				Status:      models.CodeReviewAssessmentRunning,
			}
			if tt.invalidScope {
				assessment.ReviewScope = "synthetic_invalid_scope"
			} else if tt.invalidSession {
				assessment.SessionID = uuid.New()
			} else if tt.invalidStatus {
				assessment.Status = models.CodeReviewAssessmentCompleted
			}
			err := completeFullAssessment(context.Background(), transactions, assessment, db.CompleteCodeReviewParams{SessionID: sessionID}, nil, false, "synthetic assessment body")
			switch {
			case tt.missing || tt.typedNil:
				require.EqualError(t, err, "assessment completion requires bounded transaction ownership", "completion must refuse absent ownership before a callback or database fallback")
			case tt.invalidScope || tt.invalidSession || tt.invalidStatus:
				require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "incompatible assessment identity must refuse before entering the supplied owner")
			default:
				require.ErrorIs(t, err, cause, "the actual supplied owner error must propagate without another transaction or completion attempt")
			}
			require.Equal(t, tt.expectedCalls, runner.calls, "only a valid completion may invoke exactly one supplied bounded owner")
		})
	}
}

func TestFullAssessmentReconciliationRequiresBoundedOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		typedNil bool
	}{
		{name: "absent owner"},
		{name: "typed nil owner", typedNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated no-query assessment store fixture")
			defer mock.Close()
			stores := &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}
			if tt.typedNil {
				var absent *assessmentTerminalTestRun
				stores.ThreadSendDependencies.Transactions = absent
			}
			err = reconcileCompletedFullAssessment(context.Background(), stores, models.CodeReviewSessionMetadata{OrgID: uuid.New(), SessionID: uuid.New()})
			require.EqualError(t, err, "assessment reconciliation requires bounded transaction ownership", "reconciliation must refuse missing ownership before any state lookup or publication mutation")
			require.NoError(t, mock.ExpectationsWereMet(), "missing reconciliation ownership must not query or begin a fallback transaction")
		})
	}
}
