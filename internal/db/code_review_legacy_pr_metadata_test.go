package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type legacyPRMetadataObservedTx struct {
	pgx.Tx
	contexts    []context.Context
	settlements []string
}

func (tx *legacyPRMetadataObservedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *legacyPRMetadataObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *legacyPRMetadataObservedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx *legacyPRMetadataObservedTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return nil, errors.New("legacy PR metadata cannot begin")
}

func (tx *legacyPRMetadataObservedTx) Commit(context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return errors.New("legacy PR metadata cannot commit")
}

func (tx *legacyPRMetadataObservedTx) Rollback(context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return errors.New("legacy PR metadata cannot roll back")
}

func callLegacyPRMetadata(store *LegacyPRCodeReviewMetadata, ctx context.Context, method string, org, session, pullRequest, replacement uuid.UUID) ([]models.CodeReviewSessionMetadata, error) {
	switch method {
	case "default failure":
		metadata, err := store.FailReview(ctx, org, session, " synthetic failure ")
		if err != nil {
			return nil, err
		}
		return []models.CodeReviewSessionMetadata{metadata}, nil
	case "explicit failure":
		metadata, err := store.FailReviewWithStatus(ctx, org, FailCodeReviewParams{SessionID: session, Reason: " synthetic failure ", Code: models.CodeReviewStatusCodeGitHubUnavailable, Message: " provider unavailable ", Retryable: true})
		if err != nil {
			return nil, err
		}
		return []models.CodeReviewSessionMetadata{metadata}, nil
	case "supersession":
		return store.MarkStaleForPullRequestExceptHead(ctx, org, pullRequest, "new-head", &replacement)
	default:
		return nil, errors.New("unknown legacy PR metadata test method")
	}
}

func TestLegacyPRCodeReviewMetadataConstructionAndSurface(t *testing.T) {
	t.Parallel()
	var typedNil *pgxpool.Tx
	for _, tt := range []struct {
		name string
		tx   pgx.Tx
	}{{name: "nil"}, {name: "typed nil", tx: typedNil}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := NewLegacyPRCodeReviewMetadataInTx(tt.tx)
			require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "the explicit legacy exception must reject missing supplied transaction authority")
			require.Nil(t, store, "construction refusal must not return a usable facade")
		})
	}
	typ := reflect.TypeOf((*LegacyPRCodeReviewMetadata)(nil))
	methods := make([]string, typ.NumMethod())
	for i := range methods {
		methods[i] = typ.Method(i).Name
	}
	require.Equal(t, []string{"FailReview", "FailReviewWithStatus", "MarkStaleForPullRequestExceptHead"}, methods, "the legacy exception must not expose completion cancellation retirement or general store authority")
}

func TestLegacyPRCodeReviewMetadataRejectsEmptyFacade(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		store *LegacyPRCodeReviewMetadata
	}{{name: "nil receiver"}, {name: "zero facade", store: &LegacyPRCodeReviewMetadata{}}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{"default failure", "explicit failure", "supersession"} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()
					actual, err := callLegacyPRMetadata(tt.store, context.Background(), method, uuid.New(), uuid.New(), uuid.New(), uuid.New())
					require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "a missing legacy facade must refuse rather than falling back to a plain store or runner")
					require.Nil(t, actual, "capability refusal must return no provisional metadata")
				})
			}
		})
	}
}

func TestLegacyPRCodeReviewMetadataDelegatesOnSameTransaction(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"default failure", "explicit failure", "supersession"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			tx := &legacyPRMetadataObservedTx{Tx: mock}
			store, err := NewLegacyPRCodeReviewMetadataInTx(tx)
			require.NoError(t, err, "the named owner may compose its exact supplied transaction without issuing SQL at construction")
			org, session, pullRequest, replacement := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			code, message, reason := models.CodeReviewStatusCodeWorkerFailed, "The code review stopped before it could finish.", "synthetic failure"
			expected := models.CodeReviewSessionMetadata{ID: uuid.New(), OrgID: org, SessionID: session, RepositoryID: uuid.New(), PullRequestID: pullRequest, PolicyID: uuid.New(), BaseSHA: "base", HeadSHA: "old-head", TriggerSource: models.CodeReviewTriggerSourceAppReviewer, Status: models.CodeReviewSessionStatusFailed, StatusCode: &code, StatusMessage: &message, FailureReason: &reason, LastErrorAt: &now, CompletedAt: &now, CreatedAt: now, ReviewOutputKey: "synthetic-output"}
			if method == "supersession" {
				expected.Status, expected.Stale, expected.SupersededBySessionID = models.CodeReviewSessionStatusStale, true, &replacement
				expected.StatusCode, expected.StatusMessage, expected.FailureReason, expected.LastErrorAt = nil, nil, nil, nil
				expectCodeReviewTerminalCPUOwners(mock, codeReviewTerminalCPUBatchLocks, pgx.NamedArgs{"org_id": org, "pull_request_id": pullRequest, "current_head_sha": "new-head"}, []uuid.UUID{expected.ID})
				mock.ExpectQuery(codeReviewTerminalCPUStale).WithArgs(pgx.NamedArgs{"org_id": org, "pull_request_id": pullRequest, "current_head_sha": "new-head", "superseded_by_session_id": &replacement, "terminal_metadata_ids": []uuid.UUID{expected.ID}}).WillReturnRows(codeReviewTerminalCPUMetadataRows(expected)).RowsWillBeClosed()
			} else {
				if method == "explicit failure" {
					code, message, expected.RetryableFailure = models.CodeReviewStatusCodeGitHubUnavailable, "provider unavailable", true
				}
				expectCodeReviewTerminalCPUOwners(mock, codeReviewTerminalCPUSessionLocks, pgx.NamedArgs{"org_id": org, "session_id": session, "active_only": true}, []uuid.UUID{expected.ID})
				mock.ExpectQuery(legacyPRMetadataFailureSQL).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session, "failure_reason": reason, "status_code": code, "status_message": message, "retryable_failure": expected.RetryableFailure, "terminal_metadata_ids": []uuid.UUID{expected.ID}}).WillReturnRows(codeReviewTerminalCPUMetadataRows(expected)).RowsWillBeClosed()
			}
			mock.ExpectExec(codeReviewTerminalCPUCancelPending).WithArgs(org, []uuid.UUID{expected.ID}, []uuid.UUID{session}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mr := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "use an available synthetic publisher so the no-effect assertion is meaningful")
			t.Cleanup(func() {
				require.NoError(t, client.Close(), "close the synthetic publisher after its provisional-effect observation")
			})
			streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
			sub, err := streams.Subscribe(org)
			require.NoError(t, err, "observe a real synthetic stream before the provisional mutation")
			t.Cleanup(sub.Close)
			store.store.SetStreams(streams)
			ctx := context.WithValue(context.Background(), struct{ owner string }{"legacy PR"}, "caller context")
			actual, err := callLegacyPRMetadata(store, ctx, method, org, session, pullRequest, replacement)
			require.NoError(t, err, "all three facade methods must reuse the existing pending-safe terminal primitives")
			require.Equal(t, []models.CodeReviewSessionMetadata{expected}, actual, "delegation must preserve every returned field and the existing default failure policy")
			require.Equal(t, []context.Context{ctx, ctx, ctx, ctx}, tx.contexts, "isolation lock mutation and later child cancellation must use the exact original caller context and transaction")
			require.Empty(t, tx.settlements, "the legacy facade must not begin settle retry configure or acquire another owner")
			requireCodeReviewTerminalCPUNoEvent(t, sub.C)
			require.NoError(t, mock.ExpectationsWereMet(), "terminal SQL must retain exact tenant scope ordered metadata membership and pending-only cancellation without extra owner operations")
		})
	}
}

func TestLegacyPRCodeReviewMetadataIsolationRefusal(t *testing.T) {
	t.Parallel()
	for _, isolation := range []string{string(pgx.RepeatableRead), string(pgx.Serializable)} {
		t.Run(isolation, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{"default failure", "explicit failure", "supersession"} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()
					mock := newCodeReviewMetadataMock(t)
					tx := &legacyPRMetadataObservedTx{Tx: mock}
					store, err := NewLegacyPRCodeReviewMetadataInTx(tx)
					require.NoError(t, err, "legacy construction must not claim or configure actual transaction isolation")
					mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(isolation)).RowsWillBeClosed()
					actual, err := callLegacyPRMetadata(store, context.Background(), method, uuid.New(), uuid.New(), uuid.New(), uuid.New())
					require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalIsolation, "unsupported isolation must propagate to the outer legacy owner before metadata or child writes")
					require.Nil(t, actual, "unsupported isolation must return no provisional terminal result")
					require.Empty(t, tx.settlements, "isolation refusal must not replace the outer owner's settlement policy")
					require.NoError(t, mock.ExpectationsWereMet(), "the legacy exception must never fix isolation by configuring retiming or starting another transaction")
				})
			}
		})
	}
}

func TestLegacyPRCodeReviewMetadataContextRefusal(t *testing.T) {
	t.Parallel()
	for _, contextName := range []string{"nil", "cancelled"} {
		t.Run(contextName, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{"default failure", "explicit failure", "supersession"} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()
					mock := newCodeReviewMetadataMock(t)
					store, err := NewLegacyPRCodeReviewMetadataInTx(mock)
					require.NoError(t, err, "context refusal must not require a replacement transaction owner")
					var ctx context.Context
					expected := ErrCodeReviewMetadataTerminalCapability
					if contextName == "cancelled" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(context.Background())
						cancel()
						expected = context.Canceled
					}
					actual, err := callLegacyPRMetadata(store, ctx, method, uuid.New(), uuid.New(), uuid.New(), uuid.New())
					require.ErrorIs(t, err, expected, "the legacy facade must preserve the supplied context refusal without installing its own deadline or retry")
					require.Nil(t, actual, "context refusal must return no provisional metadata")
					require.NoError(t, mock.ExpectationsWereMet(), "a missing or cancelled caller context must refuse before isolation queries locks mutations or transaction control")
				})
			}
		})
	}
}

const legacyPRMetadataFailureSQL = `UPDATE code_review_session_metadata
 SET status = 'failed', phase = NULL,
 decision = CASE WHEN github_review_id IS NOT NULL THEN decision ELSE NULL END,
 acceptable = CASE WHEN github_review_id IS NOT NULL THEN acceptable ELSE NULL END,
 failure_reason = @failure_reason, status_code = @status_code, status_message = @status_message,
 retry_at = NULL, last_error_at = now(), retryable_failure = @retryable_failure, completed_at = now()
 WHERE org_id = @org_id AND session_id = @session_id AND status IN ('queued', 'running')
 AND id = ANY(@terminal_metadata_ids)
 RETURNING ` + codeReviewMetadataColumns
