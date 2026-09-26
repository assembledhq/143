package db

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func expectPublicationTransaction(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	for _, setting := range []string{"lock_timeout", "statement_timeout", "idle_in_transaction_session_timeout"} {
		mock.ExpectExec("SET LOCAL " + setting).WillReturnResult(pgxmock.NewResult("SET", 0))
	}
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func TestCodeReviewPublicationAdmissionReleasedAfterFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stage string
	}{
		{name: "begin failure", stage: "begin"},
		{name: "callback failure", stage: "callback"},
		{name: "commit failure", stage: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated database mock")
			defer mock.Close()
			store := NewCodeReviewStore(mock)
			failure := errors.New("publication failed")
			if tt.stage == "begin" {
				mock.ExpectBegin().WillReturnError(failure)
			} else {
				expectPublicationTransaction(mock)
				if tt.stage == "commit" {
					mock.ExpectCommit().WillReturnError(failure)
				}
				mock.ExpectRollback()
			}
			err = store.RunWithGitHubPublicationLock(context.Background(), uuid.New(), uuid.New(), func(context.Context, DBTX) error {
				if tt.stage == "callback" {
					return failure
				}
				return nil
			})
			require.ErrorIs(t, err, failure, "the original publication error should be preserved")
			expectPublicationTransaction(mock)
			mock.ExpectCommit()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = store.RunWithGitHubPublicationLock(ctx, uuid.New(), uuid.New(), func(context.Context, DBTX) error { return nil })
			require.NoError(t, err, "a failed publisher must release admission for the next job")
			require.NoError(t, mock.ExpectationsWereMet(), "the failed transaction must roll back before the next publisher begins")
		})
	}
}

func TestCodeReviewPublicationAdmissionWaitsWithoutTransaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		parentWait time.Duration
		canceled   bool
		wantBusy   bool
		wantError  error
	}{
		{name: "local contention uses retryable wait budget", parentWait: time.Minute, wantBusy: true, wantError: context.DeadlineExceeded},
		{name: "parent deadline is preserved", parentWait: 5 * time.Second, wantError: context.DeadlineExceeded},
		{name: "parent cancellation is preserved", parentWait: time.Minute, canceled: true, wantError: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "create isolated database mock")
				defer mock.Close()
				store := NewCodeReviewStore(mock)
				expectPublicationTransaction(mock)
				mock.ExpectCommit()
				started := make(chan struct{})
				release := make(chan struct{})
				defer close(release)
				done := make(chan error, 1)
				go func() {
					done <- store.RunWithGitHubPublicationLock(context.Background(), uuid.New(), uuid.New(), func(context.Context, DBTX) error {
						close(started)
						<-release
						return nil
					})
				}()
				<-started
				ctx, cancel := context.WithTimeout(context.Background(), tt.parentWait)
				defer cancel()
				if tt.canceled {
					cancel()
				}
				err = store.RunWithGitHubPublicationLock(ctx, uuid.New(), uuid.New(), func(context.Context, DBTX) error {
					t.Error("a second publication must wait outside the connection pool")
					return nil
				})
				require.ErrorIs(t, err, tt.wantError, "queued publication should respect cancellation and deadlines without beginning a transaction")
				if tt.wantBusy {
					require.ErrorIs(t, err, ErrCodeReviewPublicationLockBusy, "local admission timeout should use the existing non-consuming retry path")
				} else {
					require.NotErrorIs(t, err, ErrCodeReviewPublicationLockBusy, "parent cancellation must not become a contention retry")
				}
				release <- struct{}{}
				require.NoError(t, <-done, "the active publisher should complete after the waiter leaves")
				expectPublicationTransaction(mock)
				mock.ExpectCommit()
				err = store.RunWithGitHubPublicationLock(context.Background(), uuid.New(), uuid.New(), func(context.Context, DBTX) error { return nil })
				require.NoError(t, err, "canceled waiters must not prevent the next publication")
				require.NoError(t, mock.ExpectationsWereMet(), "waiting must not borrow a connection or start a transaction")
			})
		})
	}
}
