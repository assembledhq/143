package codereview

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type scheduledSessionObservedTx struct {
	pgx.Tx
	settlements []string
}

func (tx *scheduledSessionObservedTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return nil, errors.New("scheduled creation may not begin a nested transaction")
}

func (tx *scheduledSessionObservedTx) Commit(context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return errors.New("scheduled creation may not commit")
}

func (tx *scheduledSessionObservedTx) Rollback(context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return errors.New("scheduled creation may not roll back")
}

func TestScheduledSessionStoreCreate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name string }{{"defaults"}, {"review modes"}, {"session failure"}, {"primary thread failure"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "use a fresh supplied transaction mock for each adapter branch")
			t.Cleanup(mock.Close)
			tx := &scheduledSessionObservedTx{Tx: mock}
			store := &scheduledSessionStore{SessionStore: db.NewSessionStore(nil), tx: tx}
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			id, threadID := uuid.New(), uuid.New()
			session := &models.Session{OrgID: uuid.New(), AgentType: models.AgentTypeCodex, Status: models.SessionStatusIdle}
			if tt.name == "review modes" {
				session.Origin = models.SessionOriginCodeReview
			}
			expected := *session
			expected.Origin = models.SessionOriginIssueTrigger
			mode, filesystem := models.ThreadExecutionModeWork, models.ThreadFilesystemModeReadWrite
			if session.Origin == models.SessionOriginCodeReview {
				expected.Origin = models.SessionOriginCodeReview
				mode, filesystem = models.ThreadExecutionModeReview, models.ThreadFilesystemModeReadOnly
			}
			expected.InteractionMode, expected.ValidationPolicy = models.SessionInteractionModeSingleRun, models.SessionValidationPolicyOnTurnComplete
			expected.LinearPrepareState = models.LinearPrepareStateNone
			expected.PRCreationState, expected.PRPushState, expected.BranchCreationState = models.PRCreationStateIdle, models.PRPushStateIdle, models.BranchCreationStateIdle
			expected.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{}
			failure := errors.New("synthetic scheduled create failure")
			insert := mock.ExpectQuery("WITH inserted AS").WithArgs(scheduledCreateArguments(expected))
			if tt.name == "session failure" {
				insert.WillReturnError(failure)
			} else {
				insert.WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(id, at, at))
				expected.ID, expected.CreatedAt, expected.LastActivityAt = id, at, at
				primary := mock.ExpectQuery("INSERT INTO session_threads").WithArgs(pgx.NamedArgs{"session_id": id, "org_id": session.OrgID, "agent_type": session.AgentType, "model_override": session.ModelOverride, "label": "Main", "status": models.ThreadStatusIdle, "execution_mode": mode, "filesystem_mode": filesystem})
				if tt.name == "primary thread failure" {
					primary.WillReturnError(failure)
				} else {
					primary.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
					expected.PrimaryThreadID = &threadID
				}
			}
			err = store.Create(context.Background(), session)
			if tt.name == "session failure" || tt.name == "primary thread failure" {
				require.ErrorIs(t, err, failure, "the adapter must retain original row or primary-thread errors for the outer owner")
			} else {
				require.NoError(t, err, "the adapter must use the existing CreateInTx sequence without entering the wrapper")
			}
			require.Equal(t, expected, *session, "the adapter must preserve all existing defaults and returned mutation of the supplied session record")
			require.Empty(t, tx.settlements, "only the outer scheduler may begin or settle the transaction")
			require.NoError(t, mock.ExpectationsWereMet(), "all creation SQL must use only the supplied transaction, never the deliberately absent receiver database")
		})
	}
}

func scheduledCreateArguments(session models.Session) pgx.NamedArgs {
	return pgx.NamedArgs{
		"org_id": session.OrgID, "agent_type": session.AgentType, "status": session.Status,
		"autonomy_level": session.AutonomyLevel, "token_mode": session.TokenMode, "complexity_tier": session.ComplexityTier,
		"parent_session_id": session.ParentSessionID, "revision_context": session.RevisionContext, "title": session.Title,
		"execution_brief": session.ExecutionBrief, "planning_reasoning": session.PlanningReasoning, "project_task_id": session.ProjectTaskID,
		"has_execution_context": session.ExecutionBrief != nil || session.PlanningReasoning != nil || session.ProjectTaskID != nil,
		"model_override":        session.ModelOverride, "reasoning_effort": session.ReasoningEffort, "triggered_by_user_id": session.TriggeredByUserID,
		"target_branch": session.TargetBranch, "base_commit_sha": session.BaseCommitSHA, "repository_id": session.RepositoryID,
		"input_manifest": session.InputManifest, "automation_run_id": session.AutomationRunID, "origin": session.Origin,
		"interaction_mode": session.InteractionMode, "validation_policy": session.ValidationPolicy,
		"linear_private": session.LinearPrivate, "linear_state_sync_disabled": session.LinearStateSyncDisabled,
		"linear_identifier_hint": session.LinearIdentifierHint, "linear_prepare_state": session.LinearPrepareState,
		"pr_creation_state": session.PRCreationState, "pr_creation_error": session.PRCreationError,
		"pr_push_state": session.PRPushState, "pr_push_error": session.PRPushError, "pr_push_error_code": session.PRPushErrorCode,
		"branch_creation_state": session.BranchCreationState, "branch_creation_error": session.BranchCreationError,
		"branch_url": session.BranchURL, "capability_snapshot": "[]", "git_identity_source": session.GitIdentitySource,
		"git_identity_user_id": session.GitIdentityUserID,
	}
}

func TestScheduledSessionStoreRejectsMissingTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *scheduledSessionObservedTx
	for _, tt := range []struct {
		name  string
		store *scheduledSessionStore
	}{{name: "nil adapter"}, {name: "nil transaction", store: &scheduledSessionStore{}}, {name: "typed nil transaction", store: &scheduledSessionStore{tx: typedNil}}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.store.Create(context.Background(), nil)
			require.EqualError(t, err, "scheduled session create: nil transaction", "missing transaction must fail before touching the session or falling back to any database")
		})
	}
}

func TestScheduledSessionStoreInheritedMethods(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read", "status", "failure"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each inherited method must retain its own scoped mock")
			t.Cleanup(mock.Close)
			tx := &scheduledSessionObservedTx{Tx: mock}
			original := &Service{sessions: &sessionStub{}}
			scoped := original.transactionalService(tx)
			adapter, ok := scoped.sessions.(*scheduledSessionStore)
			require.True(t, ok, "the actual transaction-scoped service must install the narrow creation adapter")
			require.Same(t, tx, adapter.tx, "the adapter must retain exactly the PR owner's transaction")
			require.Same(t, tx, scoped.admissionTx, "sandbox admission and session creation must share the same original owner")
			failure := errors.New("synthetic inherited row failure")
			org, session := uuid.New(), uuid.New()
			switch name {
			case "read":
				mock.ExpectQuery("SELECT").WithArgs(pgx.NamedArgs{"org_id": org, "id": session}).WillReturnError(failure)
				_, err = scoped.sessions.GetByID(context.Background(), org, session)
			case "status":
				mock.ExpectQuery("UPDATE sessions SET status").WithArgs(pgx.NamedArgs{"org_id": org, "id": session, "status": "failed"}).WillReturnError(failure)
				err = scoped.sessions.UpdateStatus(context.Background(), org, session, models.SessionStatusFailed)
			case "failure":
				mock.ExpectExec("UPDATE sessions SET failure_explanation").WithArgs(pgx.NamedArgs{"org_id": org, "id": session, "failure_explanation": "synthetic", "failure_category": "synthetic", "failure_next_steps": []string{"retry"}, "failure_retry_advised": true}).WillReturnError(failure)
				err = scoped.sessions.UpdateFailure(context.Background(), org, session, "synthetic", "synthetic", []string{"retry"}, true)
			}
			require.ErrorIs(t, err, failure, "the narrow adapter must retain the existing inherited read and row-only update paths")
			require.Empty(t, tx.settlements, "inherited methods must not acquire or settle another transaction")
			require.IsType(t, &sessionStub{}, original.sessions, "creating the scoped adapter must not replace the standalone service's original session port")
			require.NoError(t, mock.ExpectationsWereMet(), "inherited methods must keep exact tenant arguments and the fresh store's no-publisher Exec path")
		})
	}
}

func TestScheduledSessionStoreWiring(t *testing.T) {
	t.Parallel()
	files := token.NewFileSet()
	source, err := parser.ParseFile(files, "scheduling.go", nil, 0)
	require.NoError(t, err, "parse the actual scheduler adapter and service binding")
	var methods, bindings, createCalls, forbidden []string
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Recv != nil {
			var receiver bytes.Buffer
			require.NoError(t, format.Node(&receiver, files, function.Recv.List[0].Type), "read the concrete adapter receiver without inferring type resolution")
			if receiver.String() == "*scheduledSessionStore" {
				methods = append(methods, function.Name.Name)
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if selector.Sel.Name == "CreateInTx" {
						var expression bytes.Buffer
						require.NoError(t, format.Node(&expression, files, call), "retain the adapter's exact supplied transaction call")
						createCalls = append(createCalls, expression.String())
					}
					if selector.Sel.Name == "Create" || selector.Sel.Name == "Begin" || selector.Sel.Name == "Commit" || selector.Sel.Name == "Rollback" || selector.Sel.Name == "SetStreams" {
						forbidden = append(forbidden, selector.Sel.Name)
					}
					return true
				})
			}
		}
		if function.Name.Name == "transactionalService" {
			ast.Inspect(function.Body, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok || len(assignment.Lhs) != 1 {
					return true
				}
				var expression bytes.Buffer
				require.NoError(t, format.Node(&expression, files, assignment.Lhs[0]), "inspect the existing transaction-scoped session binding")
				if expression.String() == "scoped.sessions" {
					expression.Reset()
					require.NoError(t, format.Node(&expression, files, assignment.Rhs[0]), "retain exact fresh-store and supplied-transaction construction")
					bindings = append(bindings, expression.String())
				}
				return true
			})
		}
	}
	require.Equal(t, []string{"Create"}, methods, "the private adapter must override creation only, retaining the existing interface and inherited semantics")
	require.Equal(t, []string{"s.SessionStore.CreateInTx(ctx, s.tx, session)"}, createCalls, "creation must delegate to the existing explicit transaction seam")
	require.Equal(t, []string{"&scheduledSessionStore{SessionStore: db.NewSessionStore(tx), tx: tx}"}, bindings, "the real scheduler must install only a fresh nil-publisher store bound to its same transaction")
	require.Empty(t, forbidden, "the adapter must not settle, enter the transaction-owning wrapper, or introduce publisher wiring")
}
