package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type endedRecoveryUnitContextKey struct{}

// These CPU cases verify supplied-unit composition, not actual rollback,
// commit acknowledgment, PostgreSQL lock scheduling or Redis event delivery.
type endedRecoveryContextTx struct {
	pgx.Tx
	t       *testing.T
	unitCtx context.Context
}

func (tx *endedRecoveryContextTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	require.Same(tx.t, tx.unitCtx, ctx, "every recovery write and lock must use the existing Run unit context")
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx *endedRecoveryContextTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	require.Same(tx.t, tx.unitCtx, ctx, "every recovery row stream must use only the supplied unit context")
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *endedRecoveryContextTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	require.Same(tx.t, tx.unitCtx, ctx, "every recovery scalar read must use only the supplied unit context")
	return tx.Tx.QueryRow(ctx, sql, args...)
}

type endedRecoveryCPUFixture struct {
	org        uuid.UUID
	in         EndedFullReviewParams
	assessment uuid.UUID
	state      models.CodeReviewPRState
	results    []models.CodeReviewAgentResult
	at         time.Time
}

func newEndedRecoveryCPUFixture() endedRecoveryCPUFixture {
	f := endedRecoveryCPUFixture{org: uuid.New(), assessment: uuid.New(), at: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
	f.in = EndedFullReviewParams{RepositoryID: uuid.New(), PullRequestID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New(), PolicyID: uuid.New(), JobID: uuid.New(), JobLockToken: uuid.New(), HeadSHA: "synthetic-head", OutputKey: "synthetic-original-output", SessionStatus: models.SessionStatusCompleted, SessionTurn: 2}
	f.state = models.CodeReviewPRState{ID: uuid.New(), OrgID: f.org, RepositoryID: f.in.RepositoryID, PullRequestID: f.in.PullRequestID, Generation: 3, HeadSHA: f.in.HeadSHA, BaseSHA: "synthetic-base", BaseRef: "main", ActiveSessionID: &f.in.SessionID, ActiveAssessmentID: &f.assessment, State: models.CodeReviewScheduleRunning, WaitReason: models.CodeReviewWaitNone, CreatedAt: f.at, UpdatedAt: f.at}
	output := "synthetic saved reviewer output"
	f.results = []models.CodeReviewAgentResult{{ID: uuid.New(), OrgID: f.org, SessionID: f.in.SessionID, AgentProvider: "claude_code", Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, RawOutput: &output, StructuredResult: json.RawMessage(`{"synthetic":"saved"}`), CreatedAt: f.at}}
	return f
}

func endedRecoveryCPUStateRows(state models.CodeReviewPRState) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "repository_id", "pull_request_id", "generation", "automatic_paused", "head_sha", "base_sha", "base_ref", "is_draft", "snapshot_observed_at", "last_material_change_at", "first_pending_at", "last_agent_start_at", "eligible_at", "retry_at", "active_session_id", "active_assessment_id", "current_assessment_id", "pending_request_id", "pending_input", "state", "wait_reason", "created_at", "updated_at"}).AddRow(state.ID, state.OrgID, state.RepositoryID, state.PullRequestID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft, state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt, state.ActiveSessionID, state.ActiveAssessmentID, state.CurrentAssessmentID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason, state.CreatedAt, state.UpdatedAt)
}

func expectEndedRecoveryCPUPR(mock pgxmock.PgxPoolIface, f endedRecoveryCPUFixture) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`).WithArgs("code_review_pr:" + f.org.String() + ":" + f.in.PullRequestID.String()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT EXISTS(SELECT 1 FROM pull_requests p JOIN repositories r ON r.org_id=p.org_id AND r.full_name=p.github_repo WHERE p.org_id=$1 AND p.id=$2 AND r.id=$3)`).WithArgs(f.org, f.in.PullRequestID, f.in.RepositoryID).WillReturnRows(pgxmock.NewRows([]string{"owned"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id) VALUES($1,$2,$3) ON CONFLICT(org_id,pull_request_id) DO NOTHING`).WithArgs(f.org, f.in.RepositoryID, f.in.PullRequestID).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery(`SELECT * FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2 FOR UPDATE`).WithArgs(f.org, f.in.PullRequestID).WillReturnRows(endedRecoveryCPUStateRows(f.state)).RowsWillBeClosed()
}

const endedRecoveryCPUParentLock = `SELECT COALESCE(s.failure_explanation,'') FROM sessions s
 WHERE s.org_id=$1 AND s.id=$2 AND s.origin='code_review' AND s.repository_id=$3
 AND s.status=$4 AND s.current_turn=$5
 AND (s.code_review_owner_pr_id IS NULL OR s.code_review_owner_pr_id=$6)
 AND (` + codeReviewSessionExecutionDrainedSQL + `)
 AND NOT EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status IN ('pending','running','awaiting_input'))
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=s.org_id AND j.status IN ('pending','running')
 AND j.payload->>'session_id'=s.id::text AND j.id<>$7
 AND j.job_type IN ('run_code_review','run_agent','continue_session','fork_session_thread','revert_session_thread','deliver_thread_inbox','prepare_code_review_workspace'))
 AND NOT EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=s.org_id AND d.session_id=s.id AND d.status IN ('pending','running'))
 FOR UPDATE OF s`

const endedRecoveryCPUAssessmentLock = `SELECT a.id FROM code_review_revision_assessments a
 JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 WHERE a.org_id=$1 AND a.session_id=$2 AND a.metadata_id=$3 AND a.repository_id=$4 AND a.pull_request_id=$5
 AND a.policy_id=$6 AND a.head_sha=$7 AND a.publication_key=$8
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 AND m.head_sha=a.head_sha AND m.review_output_key=a.publication_key AND m.status IN ('queued','running')
 AND m.github_review_id IS NULL AND a.review_scope='full' AND a.status='running' AND a.result_origin IS NULL
 AND a.publication_state='not_started' AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM code_review_revision_assessments newer WHERE newer.org_id=a.org_id AND newer.pull_request_id=a.pull_request_id AND newer.generation>a.generation)
 FOR UPDATE OF a,m`

func expectEndedRecoveryCPURead(mock pgxmock.PgxPoolIface, f endedRecoveryCPUFixture, failure string, cause error) {
	expectEndedRecoveryCPUPR(mock, f)
	if failure == "parent status" {
		return
	}
	mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted)))
	prelock := mock.ExpectQuery(codeReviewMetadataTerminalSessionLocks).WithArgs(pgx.NamedArgs{"org_id": f.org, "session_id": f.in.SessionID, "active_only": false})
	if failure == "metadata error" {
		prelock.WillReturnError(cause)
		return
	}
	owners := pgxmock.NewRows([]string{"id"})
	if failure != "metadata missing" {
		owners.AddRow(f.in.MetadataID)
	}
	prelock.WillReturnRows(owners).RowsWillBeClosed()
	if failure == "metadata missing" {
		return
	}
	lease := mock.ExpectQuery(`SELECT id FROM jobs WHERE org_id=$1 AND id=$2 AND lock_token=$3 AND status='running' FOR UPDATE`).WithArgs(f.org, f.in.JobID, f.in.JobLockToken)
	if failure == "job lease" {
		lease.WillReturnError(pgx.ErrNoRows)
		return
	}
	lease.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.in.JobID))
	parent := mock.ExpectQuery(endedRecoveryCPUParentLock).WithArgs(f.org, f.in.SessionID, f.in.RepositoryID, f.in.SessionStatus, f.in.SessionTurn, f.in.PullRequestID, f.in.JobID)
	if failure == "parent drain" {
		parent.WillReturnError(pgx.ErrNoRows)
		return
	}
	parent.WillReturnRows(pgxmock.NewRows([]string{"failure_explanation"}).AddRow("synthetic original execution failure"))
	assessment := mock.ExpectQuery(endedRecoveryCPUAssessmentLock).WithArgs(f.org, f.in.SessionID, f.in.MetadataID, f.in.RepositoryID, f.in.PullRequestID, f.in.PolicyID, f.in.HeadSHA, f.in.OutputKey)
	if failure == "publication or identity" {
		assessment.WillReturnError(pgx.ErrNoRows)
		return
	}
	assessment.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.assessment))
	if failure == "scheduler owner" {
		return
	}
	rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "agent_provider", "agent_model", "role", "status", "raw_output", "structured_result", "created_at"})
	for _, result := range f.results {
		rows.AddRow(result.ID, result.OrgID, result.SessionID, result.AgentProvider, result.AgentModel, result.Role, result.Status, result.RawOutput, result.StructuredResult, result.CreatedAt)
	}
	mock.ExpectQuery(`SELECT ` + codeReviewAgentResultColumns + ` FROM code_review_agent_results WHERE org_id = @org_id AND session_id = @session_id ORDER BY created_at ASC, id ASC`).WithArgs(pgx.NamedArgs{"org_id": f.org, "session_id": f.in.SessionID}).WillReturnRows(rows).RowsWillBeClosed()
}

func expectEndedRecoveryCPUFailure(t *testing.T, mock pgxmock.PgxPoolIface, f endedRecoveryCPUFixture, enqueueError error) {
	t.Helper()
	reason, message := "synthetic original execution failure", "Review execution ended before usable output was saved. Retry the review to start a fresh attempt."
	code := models.CodeReviewStatusCodeReviewerFailed
	mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted)))
	mock.ExpectQuery(codeReviewMetadataTerminalSessionLocks).WithArgs(pgx.NamedArgs{"org_id": f.org, "session_id": f.in.SessionID, "active_only": true}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.in.MetadataID)).RowsWillBeClosed()
	mock.ExpectQuery(`UPDATE code_review_session_metadata
 SET status = 'failed', phase = NULL,
 decision = CASE WHEN github_review_id IS NOT NULL THEN decision ELSE NULL END,
 acceptable = CASE WHEN github_review_id IS NOT NULL THEN acceptable ELSE NULL END,
 failure_reason = @failure_reason, status_code = @status_code, status_message = @status_message,
 retry_at = NULL, last_error_at = now(), retryable_failure = @retryable_failure, completed_at = now()
 WHERE org_id = @org_id AND session_id = @session_id AND status IN ('queued', 'running')
 AND id = ANY(@terminal_metadata_ids) RETURNING ` + codeReviewMetadataColumns).WithArgs(pgx.NamedArgs{"org_id": f.org, "session_id": f.in.SessionID, "failure_reason": reason, "status_code": code, "status_message": message, "retryable_failure": true, "terminal_metadata_ids": []uuid.UUID{f.in.MetadataID}}).WillReturnRows(codeReviewMetadataRowsForTest().AddRow(f.in.MetadataID, f.org, f.in.SessionID, f.in.RepositoryID, f.in.PullRequestID, f.in.PolicyID, "synthetic-base", f.in.HeadSHA, false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusFailed, nil, &code, &message, nil, &f.at, true, nil, nil, false, nil, f.in.OutputKey, nil, nil, nil, nil, &reason, &f.at, f.at)).RowsWillBeClosed()
	mock.ExpectExec(codeReviewMetadataCancelPending).WithArgs(f.org, []uuid.UUID{f.in.MetadataID}, []uuid.UUID{f.in.SessionID}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE code_review_revision_assessments SET status='failed',completed_at=now(),failure_detail=$3 WHERE org_id=$1 AND id=$2 AND status='running'`).WithArgs(f.org, f.assessment, reason).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE code_review_requests SET status='failed' WHERE org_id=$1 AND pull_request_id=$2 AND (assessment_id=$4 OR (session_id=$3 AND assessment_id IS NULL)) AND status IN ('pending','joined')`).WithArgs(f.org, f.in.PullRequestID, f.in.SessionID, f.assessment).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	key := "code_review_status_comment:" + f.in.SessionID.String() + ":terminal"
	payload := map[string]uuid.UUID{"org_id": f.org, "repository_id": f.in.RepositoryID, "pull_request_id": f.in.PullRequestID, "session_id": f.in.SessionID}
	if f.in.TriggeringDisputeID != nil {
		payload["triggering_dispute_id"] = *f.in.TriggeringDisputeID
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err, "synthetic terminal comment should preserve its full exact owner and optional dispute payload")
	comment := mock.ExpectQuery(`INSERT INTO jobs (org_id, queue, job_type, payload, priority, dedupe_key, max_attempts) VALUES (@org_id, @queue, @job_type, @payload, @priority, @dedupe_key, @max_attempts) ON CONFLICT DO NOTHING RETURNING id`).WithArgs(pgx.NamedArgs{"org_id": f.org, "queue": "default", "job_type": models.JobTypeSyncCodeReviewStatusComment, "payload": raw, "priority": 3, "dedupe_key": &key, "max_attempts": 3})
	if enqueueError != nil {
		comment.WillReturnError(enqueueError)
	} else {
		comment.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	}
}

func expectEndedRecoveryCPUStateWrite(mock pgxmock.PgxPoolIface, f endedRecoveryCPUFixture, state models.CodeReviewPRState) {
	mock.ExpectExec(`UPDATE code_review_pr_state SET generation=$3,automatic_paused=$4,head_sha=$5,base_sha=$6,base_ref=$7,is_draft=$8,snapshot_observed_at=$9,last_material_change_at=$10,first_pending_at=$11,last_agent_start_at=$12,eligible_at=$13,retry_at=$14,active_session_id=$15,pending_request_id=$16,pending_input=$17,state=$18,wait_reason=$19,active_assessment_id=$20,current_assessment_id=$21,updated_at=now() WHERE org_id=$1 AND pull_request_id=$2`).WithArgs(f.org, f.in.PullRequestID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft, state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt, state.ActiveSessionID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason, state.ActiveAssessmentID, state.CurrentAssessmentID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func TestEndedFullReviewRequiresExplicitTransactions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                              string
		typedNil, nilContext, nilReceiver bool
	}{
		{name: "missing Run capability"},
		{name: "typed nil Run capability", typedNil: true},
		{name: "missing caller context", nilContext: true},
		{name: "missing receiver", nilReceiver: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			store := NewCodeReviewScheduleStore(mock)
			var transactions CodeReviewMetadataTransactions
			if tt.typedNil {
				var absent *codeReviewMetadataTestRun
				transactions = absent
			}
			ctx := context.Background()
			if tt.nilContext {
				ctx = nil
				transactions = &codeReviewMetadataTestRun{run: func(context.Context, func(context.Context, pgx.Tx) error) error {
					t.Fatal("missing caller context must refuse before invoking Run")
					return nil
				}}
			}
			if tt.nilReceiver {
				store = nil
				transactions = &codeReviewMetadataTestRun{run: func(context.Context, func(context.Context, pgx.Tx) error) error {
					t.Fatal("missing receiver must refuse before invoking Run")
					return nil
				}}
			}
			resumed, err := store.ReconcileEndedFullReview(ctx, uuid.New(), transactions, EndedFullReviewParams{}, nil)
			require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "DBTX or an invalid explicit capability must not select an ordinary transaction fallback")
			require.False(t, resumed, "missing construction ownership must never expose resume permission")
			require.NoError(t, mock.ExpectationsWereMet(), "capability refusal must not query begin commit or infer Run from the receiver")
		})
	}
}

func TestEndedFullReviewUsesOneSuppliedUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode, failure string
		wantResume          bool
	}{
		{name: "validated saved work resumes only after known settlement", wantResume: true},
		{name: "failed parent atomically retires with exact comment", mode: "failed"},
		{name: "newer pending work survives exact reservation release", mode: "pending"},
		{name: "last replay cannot inherit earlier resume permission", mode: "replay"},
		{name: "unknown final Run loses provisional resume result", mode: "unknown"},
		{name: "later failed replay loses earlier resume result", mode: "replay failure"},
		{name: "known nil Run without work is not recovery", mode: "no callback"},
		{name: "metadata prelock error precedes session locks", failure: "metadata error"},
		{name: "missing metadata precedes job lock", failure: "metadata missing"},
		{name: "original job lease is required", failure: "job lease"},
		{name: "original parent drain is required", failure: "parent drain"},
		{name: "publication uncertainty and changed identity remain fenced", failure: "publication or identity"},
		{name: "exact active scheduler owner remains required", failure: "scheduler owner"},
		{name: "running parent remains ineligible", failure: "parent status"},
		{name: "terminal comment fault aborts full callback", mode: "failed", failure: "comment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newEndedRecoveryCPUFixture()
			if tt.mode == "failed" {
				f.in.SessionStatus = models.SessionStatusFailed
				dispute := uuid.New()
				f.in.TriggeringDisputeID = &dispute
			} else if tt.mode == "pending" {
				f.in.SessionStatus = models.SessionStatusFailed
				f.state.PendingInput = json.RawMessage(`{"synthetic":"newer pending target"}`)
			}
			if tt.failure == "parent status" {
				f.in.SessionStatus = models.SessionStatusRunning
			} else if tt.failure == "scheduler owner" {
				other := uuid.New()
				f.state.ActiveSessionID = &other
			}
			receiver := newCodeReviewMetadataMock(t)
			mock := newCodeReviewMetadataMock(t)
			parentCtx := context.WithValue(context.Background(), endedRecoveryUnitContextKey{}, "synthetic caller")
			unitCtx := context.WithValue(parentCtx, endedRecoveryUnitContextKey{}, "synthetic unit")
			tx := &endedRecoveryContextTx{Tx: mock, t: t, unitCtx: unitCtx}
			cause := errors.New("synthetic ended recovery storage or settlement fault")
			runCalls, callbacks, predicateCalls := 0, 0, 0
			attempts := 1
			if tt.mode == "replay" || tt.mode == "replay failure" {
				attempts = 2
			} else if tt.mode == "no callback" {
				attempts = 0
			}
			transactions := &codeReviewMetadataTestRun{run: func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				runCalls++
				require.Same(t, parentCtx, ctx, "the explicit owner should receive the original caller context exactly once")
				for attempt := 0; attempt < attempts; attempt++ {
					callbacks++
					failure := tt.failure
					if failure == "comment" {
						failure = ""
					}
					if tt.mode == "replay failure" && attempt == 1 {
						failure = "metadata error"
					}
					expectEndedRecoveryCPURead(mock, f, failure, cause)
					willRead := failure == ""
					willFail := willRead && (f.in.SessionStatus == models.SessionStatusFailed || tt.mode == "replay" && attempt == 1)
					if willFail {
						var enqueueError error
						if tt.failure == "comment" {
							enqueueError = cause
						}
						expectEndedRecoveryCPUFailure(t, mock, f, enqueueError)
					}
					if willRead && tt.failure != "comment" {
						state := f.state
						if willFail {
							state.ActiveAssessmentID, state.ActiveSessionID = nil, nil
							if state.PendingInput == nil {
								state.State, state.WaitReason = models.CodeReviewScheduleIdle, models.CodeReviewWaitNone
								state.RetryAt, state.EligibleAt = nil, nil
							}
						}
						expectEndedRecoveryCPUStateWrite(mock, f, state)
					}
					if err := work(unitCtx, tx); err != nil {
						return err
					}
				}
				if tt.mode == "unknown" {
					return cause
				}
				return nil
			}}
			resumed, err := NewCodeReviewScheduleStore(receiver).ReconcileEndedFullReview(parentCtx, f.org, transactions, f.in, func(results []models.CodeReviewAgentResult) bool {
				predicateCalls++
				require.Equal(t, f.results, results, "the pure resume predicate must receive the complete unchanged original saved evidence")
				return tt.mode != "replay" || predicateCalls == 1
			})
			if tt.failure != "" || tt.mode == "unknown" || tt.mode == "replay failure" || tt.mode == "no callback" {
				require.Error(t, err, "invalid ownership failed work or uncertain outer settlement must remain observable")
				if tt.mode == "no callback" {
					require.ErrorIs(t, err, ErrCodeReviewMetadataTerminalCapability, "a nil outer result without any successful supplied unit must not authorize recovery")
				}
				if tt.mode == "unknown" || tt.mode == "replay failure" || tt.failure == "metadata error" || tt.failure == "comment" {
					require.ErrorIs(t, err, cause, "the outer boundary must preserve the actual known failure or settlement uncertainty")
				}
			} else {
				require.NoError(t, err, "only the final successful supplied unit may return its exact recovery result")
			}
			require.Equal(t, tt.wantResume, resumed, "only final known successful validated saved-work settlement may authorize resume")
			require.Equal(t, 1, runCalls, "ended recovery must invoke one explicit owner without a nested Run")
			require.Equal(t, attempts, callbacks, "only the explicit fake owner may select DB-only callback replay")
			expectedPredicates := 0
			if tt.failure == "" && f.in.SessionStatus == models.SessionStatusCompleted {
				expectedPredicates = attempts
				if tt.mode == "replay failure" {
					expectedPredicates = 1
				}
			}
			require.Equal(t, expectedPredicates, predicateCalls, "failed parents and rejected ownership must not consult or rerender saved work")
			require.NoError(t, mock.ExpectationsWereMet(), "PR metadata job session assessment evidence child comment and reservation work must follow exact supplied-transaction order")
			require.NoError(t, receiver.ExpectationsWereMet(), "the receiver DBTX must never acquire query begin settle or provide a fallback capability")
		})
	}
}
