package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPRFeedbackReadFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		run   func(context.Context, *PullRequestFeedbackStore, uuid.UUID, uuid.UUID) error
		query string
		extra []any
	}{
		{name: "batch", query: "SELECT .* FROM pull_request_feedback_batches WHERE org_id=", run: func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.GetBatch(ctx, o, id)
			return err
		}},
		{name: "active batch", query: "SELECT .* FROM pull_request_feedback_batches WHERE org_id=", run: func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.GetActiveBatch(ctx, o, id)
			return err
		}},
		{name: "batch items", query: "SELECT .* FROM pull_request_feedback_items WHERE org_id=", run: func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.ListBatchItems(ctx, o, id)
			return err
		}},
		{name: "recent items", query: "SELECT .* FROM pull_request_feedback_items WHERE org_id=", extra: []any{20}, run: func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.ListRecentItems(ctx, o, id, -1)
			return err
		}},
		{name: "pending items", query: "SELECT .* FROM pull_request_feedback_items WHERE org_id=", extra: []any{100}, run: func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.ListPendingItems(ctx, o, id, 101)
			return err
		}},
	}
	for _, tt := range tests {
		for _, stage := range []string{"query unavailable", "malformed result"} {
			t.Run(tt.name+"/"+stage, func(t *testing.T) {
				t.Parallel()
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "create isolated feedback read fixture")
				defer mock.Close()
				org, id := uuid.New(), uuid.New()
				args := append([]any{org, id}, tt.extra...)
				query := mock.ExpectQuery(tt.query).WithArgs(args...)
				if stage == "query unavailable" {
					query.WillReturnError(errors.New("database unavailable"))
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("not-a-uuid"))
				}
				err = tt.run(context.Background(), NewPullRequestFeedbackStore(mock), org, id)
				require.Error(t, err, "unavailable or malformed database result must not be treated as empty successful feedback")
				require.NoError(t, mock.ExpectationsWereMet(), "read must retain exact org fence and bounded limit")
			})
		}
	}
}

func TestPRFeedbackTransactionsFailBeforeMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(context.Context, *PullRequestFeedbackStore, uuid.UUID, uuid.UUID) error
	}{
		{"collect", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, _, err := s.EnsureCollectingBatch(ctx, o, id, time.Now())
			return err
		}},
		{"finalize", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.FinalizeCollectingBatch(ctx, o, id, time.Now(), nil)
			return err
		}},
		{"claim", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.ClaimBatch(ctx, o, id, time.Now())
			return err
		}},
		{"continue", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.QueueContinuation(ctx, o, id, "summary", "prompt")
			return err
		}},
		{"retry", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.RetryBatch(ctx, o, id, id, "verified-head")
			return err
		}},
		{"resolved delivery", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			_, err := s.CancelPendingThreadWithDelivery(ctx, &models.WebhookDelivery{OrgID: o}, id, 12)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create transaction failure fixture")
			defer mock.Close()
			failure := errors.New("cannot begin transaction")
			mock.ExpectBegin().WillReturnError(failure)
			store := NewPullRequestFeedbackStore(mock)
			store.SetJobStore(NewJobStore(mock))
			err = tt.run(context.Background(), store, uuid.New(), uuid.New())
			require.ErrorIs(t, err, failure, "failed transaction creation must preserve cause and perform no partial writes")
			require.NoError(t, mock.ExpectationsWereMet(), "failed transaction must not enqueue, write or commit anything")
		})
	}
}

func TestPRFeedbackMutationFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		run   func(context.Context, *PullRequestFeedbackStore, uuid.UUID, uuid.UUID) error
		query string
		args  func(uuid.UUID, uuid.UUID) []any
	}{
		{"decision", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			return s.SetItemDecision(ctx, o, id, models.PRFeedbackIntentQuestion, models.PRFeedbackItemStatusIgnored, nil, nil, models.PRFeedbackBotEligibilitySource(""))
		}, "UPDATE pull_request_feedback_items SET intent=", func(o, id uuid.UUID) []any {
			return []any{models.PRFeedbackIntentQuestion, models.PRFeedbackItemStatusIgnored, (*string)(nil), (*string)(nil), models.PRFeedbackBotEligibilitySource(""), o, id}
		}},
		{"monitoring", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			return s.UpdateMonitoring(ctx, o, id, models.PRFeedbackMonitoringDisabled)
		}, "UPDATE pull_requests SET feedback_monitoring=", func(o, id uuid.UUID) []any { return []any{models.PRFeedbackMonitoringDisabled, o, id} }},
		{"execution", func(ctx context.Context, s *PullRequestFeedbackStore, o, id uuid.UUID) error {
			return s.SetBatchExecution(ctx, o, id, id, models.PRFeedbackWorkspaceModePRHeadReconstruction)
		}, "UPDATE pull_request_feedback_batches SET thread_id=", func(o, id uuid.UUID) []any {
			return []any{id, models.PRFeedbackWorkspaceModePRHeadReconstruction, o, id}
		}},
	}
	for _, tt := range tests {
		for _, stage := range []string{"database unavailable", "ownership missing"} {
			t.Run(tt.name+"/"+stage, func(t *testing.T) {
				t.Parallel()
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "create mutation failure fixture")
				defer mock.Close()
				org, id := uuid.New(), uuid.New()
				query := mock.ExpectExec(tt.query).WithArgs(tt.args(org, id)...)
				if stage == "database unavailable" {
					query.WillReturnError(errors.New("database unavailable"))
				} else {
					query.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				}
				err = tt.run(context.Background(), NewPullRequestFeedbackStore(mock), org, id)
				require.Error(t, err, "mutation must fail when persistence or tenant/state fence fails")
				require.NoError(t, mock.ExpectationsWereMet(), "mutation must retain exact tenant and complete update arguments")
			})
		}
	}
}
