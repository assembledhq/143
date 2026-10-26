package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type sessionClaimPersistenceTx struct {
	*sessionPersistenceOwnedTx
	queries  []threadPersistenceWrite
	contexts []context.Context
}

func (tx *sessionClaimPersistenceTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, threadPersistenceWrite{SQL: strings.Join(strings.Fields(query), " "), Args: args})
	tx.contexts = append(tx.contexts, ctx)
	return tx.Tx.Query(ctx, query, args...)
}

func callSessionClaimPersistence(store *SessionStore, ctx context.Context, tx pgx.Tx, f sessionPersistenceFixture, kind, adapter string) (models.Session, error) {
	if adapter == "legacy" {
		if kind == "idle" {
			return store.ClaimIdle(ctx, f.orgID, f.sessionID)
		}
		return store.ClaimForResume(ctx, f.orgID, f.sessionID)
	}
	if kind == "idle" {
		return store.ClaimIdleInTx(ctx, f.orgID, tx, f.sessionID)
	}
	return store.ClaimForResumeInTx(ctx, f.orgID, tx, f.sessionID)
}

func expectedSessionClaimStatement(f sessionPersistenceFixture, kind string) threadPersistenceWrite {
	query := `UPDATE sessions SET status = 'running', started_at = now(), completed_at = NULL,
		error = NULL, failure_explanation = NULL, failure_category = NULL,
		failure_next_steps = NULL, failure_retry_advised = false,
		runtime_soft_deadline_at = NULL, runtime_hard_deadline_at = NULL,
		last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'idle'
		AND sandbox_state != 'destroyed' AND code_review_owner_pr_id IS NULL
		RETURNING ` + sessionSelectColumns
	args := pgx.NamedArgs{"id": f.sessionID, "org_id": f.orgID}
	if kind == "resume" {
		query = fmt.Sprintf(`UPDATE sessions SET status = 'running', started_at = now(), completed_at = NULL,
			%s, last_activity_at = now()
			WHERE id = @id AND org_id = @org_id AND status = ANY(@statuses)
			AND sandbox_state != 'destroyed' AND code_review_owner_pr_id IS NULL
			RETURNING `+sessionSelectColumns, sessionResumeRuntimeResetAssignments)
		args["statuses"] = []string{"completed", "pr_created", "failed", "cancelled", "awaiting_input", "needs_human_guidance"}
	}
	return threadPersistenceWrite{SQL: strings.Join(strings.Fields(query), " "), Args: []any{args}}
}

func TestSessionClaimPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, adapter, outcome string }{}
	for _, kind := range []string{"idle", "resume"} {
		for _, adapter := range []string{"legacy", "explicit"} {
			for _, outcome := range []string{"success", "query error", "no row", "scan error", "cancelled", "hydrate defaults"} {
				tests = append(tests, struct{ kind, adapter, outcome string }{kind, adapter, outcome})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.adapter+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionPersistenceFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this claim's isolated database handle")
			t.Cleanup(mock.Close)
			tx := &sessionClaimPersistenceTx{sessionPersistenceOwnedTx: &sessionPersistenceOwnedTx{Tx: mock}}
			var receiver DBTX
			if tt.adapter == "legacy" {
				receiver = tx
			}
			store, redis := sessionPersistenceStoreWithStreams(t, receiver)
			statement := expectedSessionClaimStatement(fixture, tt.kind)
			query := mock.ExpectQuery(regexp.QuoteMeta(statement.SQL)).WithArgs(statement.Args...)
			failure := errors.New("synthetic parent claim failure")
			started := fixture.at.Add(-time.Hour)
			expected := models.Session{
				ID: fixture.sessionID, OrgID: fixture.orgID, Origin: models.SessionOriginIssueTrigger,
				InteractionMode: models.SessionInteractionModeSingleRun, ValidationPolicy: models.SessionValidationPolicyOnTurnComplete,
				AgentType: "claude-code", Status: models.SessionStatusRunning, AutonomyLevel: "supervised", TokenMode: "low",
				StartedAt: &started, LastActivityAt: fixture.at, SandboxState: models.SandboxStateNone,
				WorkspaceRevisionUpdatedAt: fixture.at, PRCreationState: "idle", PRPushState: "idle",
				BranchCreationState: "idle", LinearPrepareState: "none", CreatedAt: fixture.at.Add(-2 * time.Hour),
			}
			row := newAgentSessionRow(fixture.sessionID, uuid.Nil, fixture.orgID, fixture.at)
			setSessionTestColumnValue(row, "status", "running")
			setSessionTestColumnValue(row, "completed_at", nil)
			switch tt.outcome {
			case "query error":
				query.WillReturnError(failure)
			case "cancelled":
				cancel()
				query.WillReturnError(context.Canceled)
			case "no row":
				query.WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			case "scan error":
				setSessionTestColumnValue(row, "current_turn", "synthetic invalid turn")
				query.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(row...))
			default:
				if tt.outcome == "hydrate defaults" {
					setSessionTestColumnValue(row, "origin", "")
					setSessionTestColumnValue(row, "interaction_mode", "")
					setSessionTestColumnValue(row, "validation_policy", "")
				}
				query.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(row...))
			}
			actual, err := callSessionClaimPersistence(store, ctx, tx, fixture, tt.kind, tt.adapter)
			if tt.outcome == "success" || tt.outcome == "hydrate defaults" {
				require.NoError(t, err, "the caller's handle must claim and hydrate the complete unchanged returned row")
				require.Equal(t, expected, actual, "claim extraction must preserve every returned field and policy default")
			} else {
				require.Error(t, err, "query, eligibility and collection failures must reach the outer owner")
				require.Equal(t, models.Session{}, actual, "failed claims must return no accepted session")
				switch tt.outcome {
				case "query error":
					prefix := "claim idle session"
					if tt.kind == "resume" {
						prefix = "claim terminal session for resume"
					}
					require.EqualError(t, err, prefix+": synthetic parent claim failure", "retain the legacy query error message")
					require.ErrorIs(t, err, failure, "retain the original database failure identity")
				case "no row":
					require.Equal(t, pgx.ErrNoRows, err, "ineligible, foreign-tenant and missing claims retain the missing-row sentinel")
				case "cancelled":
					require.ErrorIs(t, err, context.Canceled, "preserve cancellation through the query wrapper")
				}
			}
			require.Equal(t, []threadPersistenceWrite{statement}, tx.queries, "both adapters must retain exact original claim/reset/ownership and tenancy SQL with no extra read")
			require.Equal(t, []context.Context{ctx}, tx.contexts, "the query must receive the caller's exact context")
			require.Empty(t, tx.settlements, "neither adapter may add a transaction, savepoint, commit or rollback")
			require.Empty(t, redis.Keys(), "enabled session and workspace publishers must remain silent just as in the original claims")
			require.NoError(t, mock.ExpectationsWereMet(), "no claim path may borrow a second connection, retry or publish")
		})
	}
}

func TestSessionClaimPersistenceRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *sessionClaimPersistenceTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil"}, {name: "typed nil", tx: typedNil},
	}
	for _, kind := range []string{"idle", "resume"} {
		for _, tt := range tests {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				store, redis := sessionPersistenceStoreWithStreams(t, nil)
				actual, err := callSessionClaimPersistence(store, context.Background(), tt.tx, newSessionPersistenceFixture(), kind, "explicit")
				require.ErrorContains(t, err, "transaction is required", "missing transaction must reject before any receiver fallback or database call")
				require.Equal(t, models.Session{}, actual, "invalid transactions cannot yield a claimed row")
				require.Empty(t, redis.Keys(), "transaction validation must not publish anything")
			})
		}
	}
}
