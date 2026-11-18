package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const assessmentPersistenceLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`
const assessmentPersistenceOwnershipSQL = `SELECT EXISTS(SELECT 1 FROM pull_requests p JOIN repositories r ON r.org_id=p.org_id AND r.full_name=p.github_repo WHERE p.org_id=$1 AND p.id=$2 AND r.id=$3)`
const assessmentPersistenceInsertSQL = `INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id) VALUES($1,$2,$3) ON CONFLICT(org_id,pull_request_id) DO NOTHING`
const assessmentPersistenceStateSQL = `SELECT * FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2 FOR UPDATE`
const assessmentPersistenceWriteSQL = `UPDATE code_review_pr_state SET generation=$3,automatic_paused=$4,head_sha=$5,base_sha=$6,base_ref=$7,is_draft=$8,snapshot_observed_at=$9,last_material_change_at=$10,first_pending_at=$11,last_agent_start_at=$12,eligible_at=$13,retry_at=$14,active_session_id=$15,pending_request_id=$16,pending_input=$17,state=$18,wait_reason=$19,active_assessment_id=$20,current_assessment_id=$21,updated_at=now() WHERE org_id=$1 AND pull_request_id=$2`
const assessmentPersistenceIdentitySQL = `SELECT repository_id,pull_request_id FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`
const assessmentPersistenceAssessmentSQL = `SELECT status,head_sha,base_sha,base_ref,session_id,review_scope FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND repository_id=$3 AND pull_request_id=$4`
const assessmentPersistenceRequestSQL = `UPDATE code_review_requests SET status=$3
 WHERE org_id=$1 AND status IN ('pending','joined')
 AND (assessment_id=$2 OR ($3='satisfied' AND $6='full' AND assessment_id IS NULL AND status='joined' AND session_id=$4 AND pull_request_id=$5))`
const assessmentPersistenceFallbackSQL = `SELECT id FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND status='completed' AND superseded_by_assessment_id IS NULL ORDER BY generation DESC LIMIT 1`

type assessmentPersistenceFixture struct {
	state                                models.CodeReviewPRState
	assessmentID, sessionID, completedID uuid.UUID
}

func newAssessmentPersistenceFixture() assessmentPersistenceFixture {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	active, current, request, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	return assessmentPersistenceFixture{
		assessmentID: active, sessionID: session, completedID: uuid.New(),
		state: models.CodeReviewPRState{
			ID: uuid.New(), OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(),
			Generation: 17, AutomaticPaused: true, HeadSHA: "synthetic-head", BaseSHA: "synthetic-base", BaseRef: "synthetic-main", IsDraft: true,
			SnapshotObservedAt: &at, LastMaterialChangeAt: &at, FirstPendingAt: &at, LastAgentStartAt: &at, EligibleAt: &at, RetryAt: &at,
			ActiveSessionID: &session, ActiveAssessmentID: &active, CurrentAssessmentID: &current, PendingRequestID: &request,
			State: models.CodeReviewScheduleRunning, WaitReason: models.CodeReviewWaitActive, CreatedAt: at, UpdatedAt: at,
		},
	}
}

func assessmentPersistenceStateRows(state models.CodeReviewPRState, invalidGeneration bool) *pgxmock.Rows {
	value, typ := reflect.ValueOf(state), reflect.TypeOf(state)
	columns := make([]string, 0, typ.NumField())
	values := make([]any, 0, typ.NumField())
	for i := range typ.NumField() {
		column := typ.Field(i).Tag.Get("db")
		columns = append(columns, column)
		if column == "generation" && invalidGeneration {
			values = append(values, "invalid generation")
		} else {
			values = append(values, value.Field(i).Interface())
		}
	}
	return pgxmock.NewRows(columns).AddRow(values...)
}

type assessmentPersistenceTx struct {
	pgx.Tx
	settlements []string
}

func (tx *assessmentPersistenceTx) Begin(ctx context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return tx.Tx.Begin(ctx)
}

func (tx *assessmentPersistenceTx) Commit(ctx context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return tx.Tx.Commit(ctx)
}

func (tx *assessmentPersistenceTx) Rollback(ctx context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return tx.Tx.Rollback(ctx)
}

type assessmentPersistenceStarter struct {
	TxStarter
	tx     *assessmentPersistenceTx
	begins int
}

func (s *assessmentPersistenceStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx.Tx = tx
	return s.tx, nil
}

func expectAssessmentPersistenceLock(mock pgxmock.PgxPoolIface, f assessmentPersistenceFixture, failureStage string, failure error) bool {
	state := f.state
	lock := mock.ExpectExec(assessmentPersistenceLockSQL).WithArgs("code_review_pr:" + state.OrgID.String() + ":" + state.PullRequestID.String())
	if failureStage == "lock" {
		lock.WillReturnError(failure)
		return false
	}
	lock.WillReturnResult(pgxmock.NewResult("SELECT", 1))
	owned := mock.ExpectQuery(assessmentPersistenceOwnershipSQL).WithArgs(state.OrgID, state.PullRequestID, state.RepositoryID)
	switch failureStage {
	case "ownership query":
		owned.WillReturnError(failure)
		return false
	case "ownership scan":
		owned.WillReturnRows(pgxmock.NewRows([]string{"owned"}).AddRow("invalid ownership"))
		return false
	case "unowned":
		owned.WillReturnRows(pgxmock.NewRows([]string{"owned"}).AddRow(false))
		return false
	default:
		owned.WillReturnRows(pgxmock.NewRows([]string{"owned"}).AddRow(true))
	}
	insert := mock.ExpectExec(assessmentPersistenceInsertSQL).WithArgs(state.OrgID, state.RepositoryID, state.PullRequestID)
	if failureStage == "insert" {
		insert.WillReturnError(failure)
		return false
	}
	insert.WillReturnResult(pgxmock.NewResult("INSERT", 1))
	query := mock.ExpectQuery(assessmentPersistenceStateSQL).WithArgs(state.OrgID, state.PullRequestID)
	switch failureStage {
	case "state query":
		query.WillReturnError(failure)
		return false
	case "state missing":
		query.WillReturnRows(pgxmock.NewRows([]string{"id"}))
		return false
	case "state scan":
		query.WillReturnRows(assessmentPersistenceStateRows(state, true))
		return false
	default:
		query.WillReturnRows(assessmentPersistenceStateRows(state, false))
	}
	return true
}

func expectAssessmentPersistenceWrite(mock pgxmock.PgxPoolIface, state models.CodeReviewPRState, failure error) {
	write := mock.ExpectExec(assessmentPersistenceWriteSQL).WithArgs(
		state.OrgID, state.PullRequestID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft,
		state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt,
		state.ActiveSessionID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason, state.ActiveAssessmentID, state.CurrentAssessmentID,
	)
	if failure != nil {
		write.WillReturnError(failure)
	} else {
		write.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
}

func TestCodeReviewScheduleWithLockedPRInTxPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failureStage, errorText string
		callbackCalls                 int
	}{
		{name: "advisory lock failure", failureStage: "lock", errorText: "synthetic schedule persistence failure"},
		{name: "ownership query failure", failureStage: "ownership query", errorText: "synthetic schedule persistence failure"},
		{name: "ownership scan failure", failureStage: "ownership scan", errorText: "destination kind 'bool' not supported for value kind 'string' of column 'owned'"},
		{name: "unowned PR cannot create scheduler state", failureStage: "unowned", errorText: "no rows in result set"},
		{name: "state insert failure", failureStage: "insert", errorText: "synthetic schedule persistence failure"},
		{name: "locked state query failure", failureStage: "state query", errorText: "synthetic schedule persistence failure"},
		{name: "locked state missing", failureStage: "state missing", errorText: "no rows in result set"},
		{name: "locked state scan failure", failureStage: "state scan", errorText: "destination kind 'int64' not supported for value kind 'string' of column 'generation'"},
		{name: "callback failure returns no state", failureStage: "callback", errorText: "synthetic schedule persistence failure", callbackCalls: 1},
		{name: "invalid callback state returns no state", failureStage: "invalid state", errorText: "invalid review scheduling state \"synthetic-invalid\"", callbackCalls: 1},
		{name: "invalid callback wait reason returns no state", failureStage: "invalid wait", errorText: "invalid review wait reason \"synthetic-invalid\"", callbackCalls: 1},
		{name: "final state write failure returns no state", failureStage: "state write", errorText: "synthetic schedule persistence failure", callbackCalls: 1},
		{name: "callback mutation is returned without settlement", callbackCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "private PR persistence should use an isolated database mock")
			t.Cleanup(mock.Close)
			f := newAssessmentPersistenceFixture()
			failure := errors.New("synthetic schedule persistence failure")
			expectAssessmentPersistenceLock(mock, f, tt.failureStage, failure)
			mutated := f.state
			mutated.Generation = 18
			mutated.AutomaticPaused = false
			mutated.State = models.CodeReviewScheduleWaiting
			mutated.WaitReason = models.CodeReviewWaitCapacity
			if tt.failureStage == "state write" {
				expectAssessmentPersistenceWrite(mock, mutated, failure)
			} else if tt.errorText == "" {
				expectAssessmentPersistenceWrite(mock, mutated, nil)
			}
			tx := &assessmentPersistenceTx{Tx: mock}
			callbackCalls := 0
			actualState, err := NewCodeReviewScheduleStore(nil).withLockedPRInTx(context.Background(), tx, f.state.OrgID, f.state.RepositoryID, f.state.PullRequestID, func(actualTx pgx.Tx, state *models.CodeReviewPRState) error {
				callbackCalls++
				require.Same(t, tx, actualTx, "the callback must receive only the supplied caller transaction")
				require.Equal(t, f.state, *state, "the callback must receive the complete tenant-scoped locked state")
				*state = mutated
				switch tt.failureStage {
				case "callback":
					return failure
				case "invalid state":
					state.State = "synthetic-invalid"
				case "invalid wait":
					state.WaitReason = "synthetic-invalid"
				}
				return nil
			})
			expectedState := models.CodeReviewPRState{}
			if tt.errorText == "" {
				expectedState = mutated
			}
			actualError := ""
			if err != nil {
				actualError = err.Error()
			}
			require.Equal(t, struct {
				state         models.CodeReviewPRState
				errorText     string
				callbackCalls int
			}{expectedState, tt.errorText, tt.callbackCalls}, struct {
				state         models.CodeReviewPRState
				errorText     string
				callbackCalls int
			}{actualState, actualError, callbackCalls}, "the private core should return the complete successful state and no state or effects on failure")
			if tt.errorText == failure.Error() {
				require.ErrorIs(t, err, failure, "database and callback failures must preserve the original cause")
			}
			require.Equal(t, []string(nil), tx.settlements, "private PR persistence must never begin, commit or roll back even after partial writes")
			require.NoError(t, mock.ExpectationsWereMet(), "private PR persistence must use the exact lock, tenant ownership and state SQL without receiver fallback")
		})
	}
}

type assessmentSettlementPersistenceCase struct {
	name, failureStage, owner, current, mismatch string
	status                                       models.CodeReviewAssessmentStatus
	scope                                        models.CodeReviewScope
	pending                                      bool
	expectedPolicy, expectedCurrent, errorText   string
}

func assessmentSettlementPersistenceFixture(tt assessmentSettlementPersistenceCase) assessmentPersistenceFixture {
	f := newAssessmentPersistenceFixture()
	switch tt.owner {
	case "legacy":
		f.state.ActiveAssessmentID = nil
	case "other assessment":
		other := uuid.New()
		f.state.ActiveAssessmentID = &other
	case "other session":
		other := uuid.New()
		f.state.ActiveAssessmentID, f.state.ActiveSessionID = nil, &other
	case "none":
		f.state.ActiveAssessmentID, f.state.ActiveSessionID = nil, nil
	}
	switch tt.current {
	case "self":
		f.state.CurrentAssessmentID = &f.assessmentID
	case "none":
		f.state.CurrentAssessmentID = nil
	}
	if tt.pending {
		f.state.PendingInput = json.RawMessage(`{"synthetic":"pending intent"}`)
	}
	return f
}

func expectedAssessmentSettlementState(f assessmentPersistenceFixture, tt assessmentSettlementPersistenceCase) models.CodeReviewPRState {
	expected := f.state
	if tt.expectedPolicy != "unchanged" {
		expected.ActiveAssessmentID, expected.ActiveSessionID = nil, nil
		if tt.expectedPolicy != "pending" {
			expected.State = models.CodeReviewScheduleIdle
			if tt.expectedPolicy == "covered" {
				expected.State = models.CodeReviewScheduleCovered
			}
			expected.WaitReason, expected.RetryAt, expected.EligibleAt = models.CodeReviewWaitNone, nil, nil
		}
	}
	switch tt.expectedCurrent {
	case "self":
		expected.CurrentAssessmentID = &f.assessmentID
	case "completed":
		expected.CurrentAssessmentID = &f.completedID
	case "none":
		expected.CurrentAssessmentID = nil
	}
	return expected
}

func expectAssessmentSettlementIdentity(mock pgxmock.PgxPoolIface, f assessmentPersistenceFixture, failureStage string, failure error) bool {
	identity := mock.ExpectQuery(assessmentPersistenceIdentitySQL).WithArgs(f.state.OrgID, f.assessmentID)
	rows := pgxmock.NewRows([]string{"repository_id", "pull_request_id"})
	switch failureStage {
	case "identity query":
		identity.WillReturnError(failure)
		return false
	case "identity missing":
		identity.WillReturnRows(rows)
		return false
	case "identity scan":
		identity.WillReturnRows(rows.AddRow(true, f.state.PullRequestID))
		return false
	default:
		identity.WillReturnRows(rows.AddRow(f.state.RepositoryID, f.state.PullRequestID))
		return true
	}
}

func expectAssessmentSettlementPolicy(mock pgxmock.PgxPoolIface, f assessmentPersistenceFixture, tt assessmentSettlementPersistenceCase, failure error) bool {
	assessment := mock.ExpectQuery(assessmentPersistenceAssessmentSQL).WithArgs(f.state.OrgID, f.assessmentID, f.state.RepositoryID, f.state.PullRequestID)
	rows := pgxmock.NewRows([]string{"status", "head_sha", "base_sha", "base_ref", "session_id", "review_scope"})
	head, base, ref := f.state.HeadSHA, f.state.BaseSHA, f.state.BaseRef
	switch tt.mismatch {
	case "head":
		head = "synthetic-other-head"
	case "base":
		base = "synthetic-other-base"
	case "ref":
		ref = "synthetic-other-ref"
	}
	switch tt.failureStage {
	case "assessment query":
		assessment.WillReturnError(failure)
		return false
	case "assessment missing":
		assessment.WillReturnRows(rows)
		return false
	case "assessment scan":
		assessment.WillReturnRows(rows.AddRow(tt.status, true, base, ref, f.sessionID, tt.scope))
		return false
	default:
		assessment.WillReturnRows(rows.AddRow(tt.status, head, base, ref, f.sessionID, tt.scope))
	}
	requestStatus := ""
	switch tt.status {
	case models.CodeReviewAssessmentCompleted:
		requestStatus = "satisfied"
	case models.CodeReviewAssessmentFailed, models.CodeReviewAssessmentCancelled, models.CodeReviewAssessmentSuperseded:
		requestStatus = "failed"
	default:
		return true
	}
	request := mock.ExpectExec(assessmentPersistenceRequestSQL).WithArgs(f.state.OrgID, f.assessmentID, requestStatus, f.sessionID, f.state.PullRequestID, tt.scope)
	if tt.failureStage == "requests" {
		request.WillReturnError(failure)
		return false
	}
	request.WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	if tt.current == "self" && tt.status != models.CodeReviewAssessmentCompleted && tt.expectedPolicy != "unchanged" {
		fallback := mock.ExpectQuery(assessmentPersistenceFallbackSQL).WithArgs(f.state.OrgID, f.state.PullRequestID)
		completed := pgxmock.NewRows([]string{"id"})
		switch tt.failureStage {
		case "fallback query":
			fallback.WillReturnError(failure)
			return false
		case "fallback scan":
			fallback.WillReturnRows(completed.AddRow(true))
			return false
		case "fallback missing":
			fallback.WillReturnRows(completed)
		default:
			fallback.WillReturnRows(completed.AddRow(f.completedID))
		}
	}
	return true
}

func TestCodeReviewScheduleSettleAssessmentPersistence(t *testing.T) {
	t.Parallel()
	tests := []assessmentSettlementPersistenceCase{
		{name: "identity query failure", failureStage: "identity query", errorText: "synthetic schedule persistence failure"},
		{name: "identity absent", failureStage: "identity missing", errorText: "no rows in result set"},
		{name: "identity scan failure", failureStage: "identity scan", errorText: "scanning value error for column 'repository_id': Scan: unable to scan type bool into UUID"},
		{name: "assessment query failure", failureStage: "assessment query", errorText: "synthetic schedule persistence failure"},
		{name: "assessment absent under lock", failureStage: "assessment missing", errorText: "no rows in result set"},
		{name: "assessment scan failure", failureStage: "assessment scan", errorText: "destination kind 'string' not supported for value kind 'bool' of column 'head_sha'"},
		{name: "reserved assessment preserves complete state", status: models.CodeReviewAssessmentReserved, scope: models.CodeReviewScopeFull, expectedPolicy: "unchanged"},
		{name: "running assessment preserves complete state", status: models.CodeReviewAssessmentRunning, scope: models.CodeReviewScopeFull, expectedPolicy: "unchanged"},
		{name: "publishing assessment preserves complete state", status: models.CodeReviewAssessmentPublishing, scope: models.CodeReviewScopeFull, expectedPolicy: "unchanged"},
		{name: "completed owned assessment covers matching target", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, expectedPolicy: "covered", expectedCurrent: "self"},
		{name: "failed owned assessment becomes idle", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, expectedPolicy: "idle"},
		{name: "cancelled owned assessment becomes idle", status: models.CodeReviewAssessmentCancelled, scope: models.CodeReviewScopeFull, expectedPolicy: "idle"},
		{name: "superseded owned assessment becomes idle", status: models.CodeReviewAssessmentSuperseded, scope: models.CodeReviewScopeFull, expectedPolicy: "idle"},
		{name: "request settlement failure preserves original error", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, failureStage: "requests", errorText: "synthetic schedule persistence failure"},
		{name: "completed legacy full session releases scheduler", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, owner: "legacy", expectedPolicy: "covered", expectedCurrent: "self"},
		{name: "failed legacy full session releases scheduler", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, owner: "legacy", expectedPolicy: "idle"},
		{name: "evidence assessment cannot claim legacy session", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeEvidenceOnly, owner: "legacy", expectedPolicy: "unchanged"},
		{name: "different active assessment remains owned", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, owner: "other assessment", expectedPolicy: "unchanged"},
		{name: "different legacy session remains owned", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, owner: "other session", expectedPolicy: "unchanged"},
		{name: "unowned scheduler remains unchanged", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, owner: "none", expectedPolicy: "unchanged"},
		{name: "completed head mismatch becomes idle", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, mismatch: "head", expectedPolicy: "idle", expectedCurrent: "self"},
		{name: "completed base mismatch becomes idle", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, mismatch: "base", expectedPolicy: "idle", expectedCurrent: "self"},
		{name: "completed base ref mismatch becomes idle", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, mismatch: "ref", expectedPolicy: "idle", expectedCurrent: "self"},
		{name: "completed assessment retains newer pending intent and timing", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, pending: true, expectedPolicy: "pending", expectedCurrent: "self"},
		{name: "failed assessment retains newer pending intent and timing", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, pending: true, expectedPolicy: "pending"},
		{name: "failed current assessment restores completed fallback", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "self", expectedPolicy: "idle", expectedCurrent: "completed"},
		{name: "failed current assessment clears missing fallback", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "self", failureStage: "fallback missing", expectedPolicy: "idle", expectedCurrent: "none"},
		{name: "fallback query failure preserves original error", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "self", failureStage: "fallback query", expectedPolicy: "idle", errorText: "synthetic schedule persistence failure"},
		{name: "fallback scan failure preserves original error", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "self", failureStage: "fallback scan", expectedPolicy: "idle", errorText: "scanning value error for column 'id': Scan: unable to scan type bool into UUID"},
		{name: "failed assessment preserves absent current pointer", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "none", expectedPolicy: "idle", expectedCurrent: "none"},
		{name: "fallback precedes retention of newer pending intent", status: models.CodeReviewAssessmentFailed, scope: models.CodeReviewScopeFull, current: "self", pending: true, expectedPolicy: "pending", expectedCurrent: "completed"},
		{name: "final state write failure leaves settlement to owner", status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, failureStage: "state write", expectedPolicy: "covered", expectedCurrent: "self", errorText: "synthetic schedule persistence failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"wrapper", "in tx"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
					require.NoError(t, err, "assessment policy should use an isolated transaction mock")
					t.Cleanup(mock.Close)
					f := assessmentSettlementPersistenceFixture(tt)
					failure := errors.New("synthetic schedule persistence failure")
					tx := &assessmentPersistenceTx{Tx: mock}
					starter := &assessmentPersistenceStarter{TxStarter: mock, tx: tx}
					identityFound := expectAssessmentSettlementIdentity(mock, f, tt.failureStage, failure)
					if identityFound {
						if mode == "wrapper" {
							mock.ExpectBegin()
						}
						expectAssessmentPersistenceLock(mock, f, "", failure)
						if expectAssessmentSettlementPolicy(mock, f, tt, failure) {
							var writeFailure error
							if tt.failureStage == "state write" {
								writeFailure = failure
							}
							expectAssessmentPersistenceWrite(mock, expectedAssessmentSettlementState(f, tt), writeFailure)
						}
						if mode == "wrapper" {
							if tt.errorText == "" {
								mock.ExpectCommit()
							}
							mock.ExpectRollback()
						}
					}
					if mode == "wrapper" {
						err = NewCodeReviewScheduleStore(starter).SettleAssessment(context.Background(), f.state.OrgID, f.assessmentID)
					} else {
						err = NewCodeReviewScheduleStore(nil).SettleAssessmentInTx(context.Background(), f.state.OrgID, tx, f.assessmentID)
					}
					actualError := ""
					if err != nil {
						actualError = err.Error()
					}
					expectedBegins := 0
					var expectedSettlements []string
					if mode == "wrapper" && identityFound {
						expectedBegins = 1
						if tt.errorText == "" {
							expectedSettlements = []string{"commit", "rollback"}
						} else {
							expectedSettlements = []string{"rollback"}
						}
					}
					require.Equal(t, struct {
						errorText   string
						begins      int
						settlements []string
					}{tt.errorText, expectedBegins, expectedSettlements}, struct {
						errorText   string
						begins      int
						settlements []string
					}{actualError, starter.begins, tx.settlements}, "both settlement entry points should preserve policy and errors while only the wrapper owns settlement")
					if tt.errorText == failure.Error() {
						require.ErrorIs(t, err, failure, "settlement should preserve its exact SQL failure for the transaction owner")
					}
					require.NoError(t, mock.ExpectationsWereMet(), "settlement must retain exact tenant, owner, terminal and legacy-join SQL and every unchanged state field")
				})
			}
		})
	}
}

func TestCodeReviewSchedulePersistenceWrapperFailures(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, operation, failureStage string }{
		{name: "PR wrapper begin failure", operation: "lock", failureStage: "begin"},
		{name: "PR wrapper commit failure", operation: "lock", failureStage: "commit"},
		{name: "assessment wrapper reads identity before begin failure", operation: "assessment", failureStage: "begin"},
		{name: "assessment wrapper commit failure", operation: "assessment", failureStage: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "wrapper settlement failure should use an isolated mock")
			t.Cleanup(mock.Close)
			f := newAssessmentPersistenceFixture()
			failure := errors.New("synthetic wrapper settlement failure")
			tx := &assessmentPersistenceTx{Tx: mock}
			starter := &assessmentPersistenceStarter{TxStarter: mock, tx: tx}
			if tt.operation == "assessment" {
				expectAssessmentSettlementIdentity(mock, f, "", failure)
			}
			begin := mock.ExpectBegin()
			var expectedSettlements []string
			if tt.failureStage == "begin" {
				begin.WillReturnError(failure)
			} else {
				expectAssessmentPersistenceLock(mock, f, "", failure)
				expectedState := f.state
				if tt.operation == "assessment" {
					spec := assessmentSettlementPersistenceCase{status: models.CodeReviewAssessmentCompleted, scope: models.CodeReviewScopeFull, expectedPolicy: "covered", expectedCurrent: "self"}
					expectAssessmentSettlementPolicy(mock, f, spec, failure)
					expectedState = expectedAssessmentSettlementState(f, spec)
				}
				expectAssessmentPersistenceWrite(mock, expectedState, nil)
				mock.ExpectCommit().WillReturnError(failure)
				mock.ExpectRollback()
				expectedSettlements = []string{"commit", "rollback"}
			}
			store := NewCodeReviewScheduleStore(starter)
			if tt.operation == "assessment" {
				err = store.SettleAssessment(context.Background(), f.state.OrgID, f.assessmentID)
			} else {
				err = store.WithLockedPR(context.Background(), f.state.OrgID, f.state.RepositoryID, f.state.PullRequestID, func(actual pgx.Tx, state *models.CodeReviewPRState) error {
					require.Same(t, tx, actual, "the wrapper callback should receive its single acquired transaction")
					require.Equal(t, f.state, *state, "the wrapper should preserve the complete state supplied to its callback")
					return nil
				})
			}
			require.ErrorIs(t, err, failure, "the wrapper must preserve its exact begin or commit failure without retrying")
			require.Equal(t, 1, starter.begins, "each wrapper should attempt acquisition exactly once")
			require.Equal(t, expectedSettlements, tx.settlements, "only a successfully acquired outer unit should attempt commit or deferred rollback")
			require.NoError(t, mock.ExpectationsWereMet(), "wrapper failures must retain identity-read order and avoid extra SQL or retries")
		})
	}
}

func TestCodeReviewSchedulePersistenceRejectsNil(t *testing.T) {
	t.Parallel()
	var typedNil *assessmentPersistenceTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil transaction"},
		{name: "typed nil transaction", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, api := range []string{"private PR core", "public assessment seam"} {
				t.Run(api, func(t *testing.T) {
					t.Parallel()
					// No receiver database exists, so any SQL or transaction fallback
					// would fail before the exact nil-transaction result is returned.
					store := NewCodeReviewScheduleStore(nil)
					state := models.CodeReviewPRState{}
					var err error
					expectedError := "settle code review assessment in tx: nil transaction"
					if api == "private PR core" {
						expectedError = "lock code review PR in tx: nil transaction"
						state, err = store.withLockedPRInTx(context.Background(), tt.tx, uuid.New(), uuid.New(), uuid.New(), func(pgx.Tx, *models.CodeReviewPRState) error {
							t.Fatal("invalid transaction must not invoke the callback")
							return nil
						})
					} else {
						err = store.SettleAssessmentInTx(context.Background(), uuid.New(), tt.tx, uuid.New())
					}
					actualError := ""
					if err != nil {
						actualError = err.Error()
					}
					require.Equal(t, struct {
						state     models.CodeReviewPRState
						errorText string
					}{models.CodeReviewPRState{}, expectedError}, struct {
						state     models.CodeReviewPRState
						errorText string
					}{state, actualError}, "both seams should reject nil and typed nil transactions before touching SQL, callbacks or state")
				})
			}
		})
	}
}
