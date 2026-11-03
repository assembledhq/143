package codereview

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

type scheduledMetadataObservedTx struct {
	*scheduledSessionObservedTx
	contexts []context.Context
}

func (tx *scheduledMetadataObservedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *scheduledMetadataObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func callScheduledMetadataTerminal(store *scheduledMetadataStore, ctx context.Context, method string, org, session, pullRequest, replacement uuid.UUID) error {
	switch method {
	case "default failure":
		_, err := store.FailReview(ctx, org, session, " synthetic failure ")
		return err
	case "explicit failure":
		_, err := store.FailReviewWithStatus(ctx, org, db.FailCodeReviewParams{SessionID: session, Reason: " synthetic failure ", Code: models.CodeReviewStatusCodeGitHubUnavailable, Message: " provider unavailable ", Retryable: true})
		return err
	case "supersession":
		_, err := store.MarkStaleForPullRequestExceptHead(ctx, org, pullRequest, "new-head", &replacement)
		return err
	default:
		return errors.New("unknown scheduled metadata test method")
	}
}

func TestScheduledMetadataStoreTerminalComposition(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"default failure", "explicit failure", "supersession"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each scheduler terminal method must use its own exact supplied transaction")
			t.Cleanup(mock.Close)
			tx := &scheduledMetadataObservedTx{scheduledSessionObservedTx: &scheduledSessionObservedTx{Tx: mock}}
			outer := &metadataStub{}
			original := &Service{metadata: outer}
			scoped := original.transactionalService(tx)
			adapter, ok := scoped.metadata.(*scheduledMetadataStore)
			require.True(t, ok, "the actual scheduler service copy must install the named legacy PR adapter")
			require.Same(t, tx, adapter.tx, "the adapter must preserve the PR owner's exact transaction")
			require.Same(t, adapter.MetadataStore, scoped.policies, "policy resolution and default materialization must retain the plain transaction-bound store rather than the terminal exception")
			require.IsType(t, &db.CodeReviewStore{}, scoped.policies, "the new exception must not replace policy ownership with a broad transaction wrapper")
			org, session, pullRequest, replacement := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			failure := errors.New("synthetic metadata mutation failure")
			mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted))).RowsWillBeClosed()
			if method == "supersession" {
				mock.ExpectQuery(`SELECT id FROM code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": org, "pull_request_id": pullRequest, "current_head_sha": "new-head"}).WillReturnRows(pgxmock.NewRows([]string{"id"})).RowsWillBeClosed()
				mock.ExpectQuery(`UPDATE code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": org, "pull_request_id": pullRequest, "current_head_sha": "new-head", "superseded_by_session_id": &replacement, "terminal_metadata_ids": []uuid.UUID{}}).WillReturnError(failure)
			} else {
				code, message, retryable := models.CodeReviewStatusCodeWorkerFailed, "The code review stopped before it could finish.", false
				if method == "explicit failure" {
					code, message, retryable = models.CodeReviewStatusCodeGitHubUnavailable, "provider unavailable", true
				}
				mock.ExpectQuery(`SELECT id FROM code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session, "active_only": true}).WillReturnRows(pgxmock.NewRows([]string{"id"})).RowsWillBeClosed()
				mock.ExpectQuery(`UPDATE code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session, "failure_reason": "synthetic failure", "status_code": code, "status_message": message, "retryable_failure": retryable, "terminal_metadata_ids": []uuid.UUID{}}).WillReturnError(failure)
			}
			ctx := context.WithValue(context.Background(), struct{ owner string }{"legacy PR"}, "caller context")
			err = callScheduledMetadataTerminal(adapter, ctx, method, org, session, pullRequest, replacement)
			require.ErrorIs(t, err, failure, "the real scoped service must propagate the original mutation error so its entire outer PR unit rolls back")
			require.Equal(t, []context.Context{ctx, ctx, ctx}, tx.contexts, "isolation locks and mutation must retain the original caller context rather than installing a new budget")
			require.Empty(t, tx.settlements, "terminal composition must leave Begin Commit Rollback and retry to the existing PR owner")
			require.Same(t, outer, original.metadata, "creating a scoped service must not mutate the original service's metadata capability")
			require.Equal(t, 0, outer.failCalls, "scoped terminal work must not escape to the original possibly pool-backed metadata port")
			require.NoError(t, mock.ExpectationsWereMet(), "the three reached methods must preserve default policy and exact tenant arguments without another acquisition or terminal fallback")
		})
	}
}

func TestScheduledMetadataStoreInheritedMethodsAndPlainPolicy(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"metadata read", "row-only supersession", "policy read", "plain policy terminal refusal"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each unchanged port must retain an isolated transaction fixture")
			t.Cleanup(mock.Close)
			tx := &scheduledSessionObservedTx{Tx: mock}
			scoped := (&Service{}).transactionalService(tx)
			org, session, replacement := uuid.New(), uuid.New(), uuid.New()
			failure := errors.New("synthetic existing port error")
			switch method {
			case "metadata read":
				mock.ExpectQuery(`SELECT`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session}).WillReturnError(failure)
				_, err = scoped.metadata.GetBySessionID(context.Background(), org, session)
			case "row-only supersession":
				mock.ExpectQuery(`UPDATE code_review_session_metadata`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session, "replacement_session_id": replacement}).WillReturnError(failure)
				_, err = scoped.metadata.MarkSupersededBy(context.Background(), org, session, replacement)
			case "policy read":
				mock.ExpectQuery(`FROM code_review_policies`).WithArgs(pgx.NamedArgs{"org_id": org}).WillReturnError(failure)
				_, err = scoped.policies.ResolvePolicy(context.Background(), org)
			case "plain policy terminal refusal":
				plain, ok := scoped.policies.(*db.CodeReviewStore)
				require.True(t, ok, "the unchanged policy port must still use the plain transaction-bound store")
				_, err = plain.FailReview(context.Background(), org, session, "synthetic")
				failure = db.ErrCodeReviewMetadataTerminalCapability
			}
			require.ErrorIs(t, err, failure, "unchanged ports must preserve their original operation and error rather than silently gaining terminal authority")
			require.Empty(t, tx.settlements, "inherited row-only methods must not add an owner or settle the original transaction")
			require.NoError(t, mock.ExpectationsWereMet(), "the adapter must inherit exact same-transaction reads and row-only writes without capability probes")
		})
	}
}

func TestScheduledMetadataStoreRejectsMissingTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *scheduledSessionObservedTx
	for _, tt := range []struct {
		name  string
		store *scheduledMetadataStore
	}{{name: "nil adapter"}, {name: "zero adapter", store: &scheduledMetadataStore{}}, {name: "typed nil transaction", store: &scheduledMetadataStore{tx: typedNil}}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{"default failure", "explicit failure", "supersession"} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()
					err := callScheduledMetadataTerminal(tt.store, context.Background(), method, uuid.New(), uuid.New(), uuid.New(), uuid.New())
					require.ErrorIs(t, err, db.ErrCodeReviewMetadataTerminalCapability, "a missing transaction must propagate the named constructor error without inherited-store or runner fallback")
				})
			}
		})
	}
}
