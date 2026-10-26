package thread

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const sendDispatchLookupSQL = `SELECT id,session_id,thread_id,session_epoch,thread_epoch,
 message_id,kind,payload_digest,CASE WHEN octet_length(validated_payload::text)<=8388608 THEN validated_payload ELSE NULL END,normalization_version
 FROM session_input_dispatches WHERE org_id=$1 AND ingress_kind=$2 AND ingress_key=$3`
const sendOwnersSQL = `SELECT s.code_review_owner_pr_id,s.automation_owner_generation_id,
 (SELECT g.target_id FROM automation_target_sessions g WHERE g.org_id=s.org_id AND g.id=s.automation_owner_generation_id),
 t.admission_claim_dispatch_id,t.admission_claim_token,
 (SELECT d.job_id FROM session_input_dispatches d WHERE d.org_id=t.org_id AND d.session_id=t.session_id AND d.thread_id=t.id AND d.id=t.admission_claim_dispatch_id)
 FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3`

func expectSendPolicy(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(regexp.QuoteMeta(sendFixturePolicySQL)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms"))
}

// This envelope is synthetic CPU data, never a provider conformance receipt or
// an activation assertion. The real resolver must still validate its shape.
func expectActiveSendMode(mock pgxmock.PgxPoolIface, input SendMessageInput) {
	expectSendPolicy(mock)
	mock.ExpectQuery(regexp.QuoteMeta(sendFixtureSharedSQL)).WithArgs("session_lifecycle_cutover:" + input.OrgID.String() + ":" + input.SessionID.String()).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	normalization := json.RawMessage(`{"schema_version":1,"receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","floor_receipt_id":"ffffffff-1111-4222-8333-444444444444","cutoff_id":1,"complete":true}`)
	capability := json.RawMessage(`{"schema_version":1,"release_receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","tuples":[{"provider":"codex","cli_build":"synthetic-cli-v1","image_build":"synthetic-image-v1","resume_identity":"native_conversation","verification_protocol":"synthetic-verify-v1","conformance_receipt_id":"ffffffff-1111-4222-8333-444444444444"}]}`)
	version, cutoff, minimum, state := int64(9), int64(1), 1, models.LifecycleCutoverStateActive
	mock.ExpectQuery(regexp.QuoteMeta(sendFixtureModeSQL)).WithArgs(input.OrgID, input.SessionID).WillReturnRows(pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(input.OrgID, input.SessionID, int16(1), &input.OrgID, &input.SessionID, &version, &state, &cutoff, normalization, &now, &minimum, capability, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":1,"provider":"codex"}]`)))
}

func expectActiveSendScope(mock pgxmock.PgxPoolIface, input SendMessageInput, epoch int64, parentStatus models.SessionStatus, laneStatus models.ThreadStatus, end *int64) {
	expectSendPolicy(mock)
	mock.ExpectQuery(regexp.QuoteMeta(sendFixtureParentSQL)).WithArgs(input.OrgID, input.SessionID).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "status", "deleted", "archived", "epoch", "end", "protocol"}).AddRow(input.SessionID, input.OrgID, parentStatus, nil, nil, epoch, end, int16(1)))
	mock.ExpectQuery(regexp.QuoteMeta(sendFixtureKeysSQL)).WithArgs(input.OrgID, input.SessionID, input.ThreadID, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(input.ThreadID))
	mock.ExpectQuery(regexp.QuoteMeta(sendFixtureLanesSQL)).WithArgs(input.OrgID, input.SessionID, []uuid.UUID{input.ThreadID}).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "session", "status", "archived", "epoch", "end", "protocol"}).AddRow(input.ThreadID, input.OrgID, input.SessionID, laneStatus, nil, int64(7), end, int16(1)))
}

func sendDispatchRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "session", "lane", "session_epoch", "thread_epoch", "message", "kind", "digest", "payload", "normalization"})
}
func sendDispatchFixtureRows(input SendMessageInput, id uuid.UUID, digest string, payload json.RawMessage) *pgxmock.Rows {
	parentEpoch, laneEpoch, messageID := int64(5), int64(7), int64(42)
	return sendDispatchRows().AddRow(id, input.SessionID, &input.ThreadID, &parentEpoch, &laneEpoch, &messageID, "message", digest, payload, 1)
}
func expectSendMiss(mock pgxmock.PgxPoolIface, input SendMessageInput, ingress db.LifecycleIngress) {
	mock.ExpectQuery(regexp.QuoteMeta(sendDispatchLookupSQL)).WithArgs(input.OrgID, ingress.Kind(), ingress.Key()).WillReturnRows(sendDispatchRows())
}
func expectSendOwners(mock pgxmock.PgxPoolIface, input SendMessageInput, owner *uuid.UUID) {
	mock.ExpectQuery(regexp.QuoteMeta(sendOwnersSQL)).WithArgs(input.OrgID, input.SessionID, input.ThreadID).WillReturnRows(pgxmock.NewRows([]string{"review", "generation", "target", "dispatch", "token", "job"}).AddRow(owner, nil, nil, nil, nil, nil))
	mock.ExpectQuery(`SELECT id,pull_request_id FROM pull_request_repair_runs WHERE \(CASE WHEN active THEN org_id END\)=\$1 AND \(CASE WHEN active THEN session_id END\)=\$2 ORDER BY id LIMIT \$3`).WithArgs(input.OrgID, input.SessionID, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id", "pr"}))
}

type activeSendEffects struct {
	t       *testing.T
	settled *bool
	calls   []string
}

func (e *activeSendEffects) record(kind string) {
	e.t.Helper()
	require.True(e.t, *e.settled, "postcommit effects require known outer ownership")
	e.calls = append(e.calls, kind)
}
func (e *activeSendEffects) PublishLifecycle(context.Context, uuid.UUID, []db.LifecycleEffect) {
	e.record("lifecycle")
}
func (e *activeSendEffects) NotifyJob(context.Context, uuid.UUID) { e.record("job") }
func (e *activeSendEffects) PublishThreadClaim(context.Context, uuid.UUID, db.ThreadClaimPersistenceEffects) {
	e.t.Fatal("active send cannot publish legacy claim effects")
}
func (e *activeSendEffects) PublishThreadPending(context.Context, uuid.UUID, db.ThreadPendingMessagePersistenceEffects) {
	e.t.Fatal("active send cannot publish legacy pending effects")
}
func (e *activeSendEffects) PublishThreadStatus(context.Context, uuid.UUID, db.ThreadPersistenceEffects) {
	e.t.Fatal("active send cannot publish legacy status effects")
}
func (e *activeSendEffects) PublishSessionStatus(context.Context, uuid.UUID, models.Session) {
	e.t.Fatal("active send cannot publish legacy parent effects")
}

func newActiveSendService(t *testing.T, mock pgxmock.PgxPoolIface) (*Service, *activeSendEffects) {
	t.Helper()
	service, deps := newTestService(t)
	deps.sessionStore.codeReviewErr = errors.New("legacy owner preflight must not run before active exact replay")
	settled := false
	service.send.Transactions = sendTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
		settled = false
		unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
		defer cancel()
		tx, err := mock.Begin(unitCtx)
		if err != nil {
			return err
		}
		if err = work(unitCtx, tx); err != nil {
			if rollbackErr := tx.Rollback(unitCtx); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			settled = true
			return err
		}
		if err = tx.Commit(unitCtx); err != nil {
			return err
		}
		settled = true
		return nil
	})
	effects := &activeSendEffects{t: t, settled: &settled}
	service.send.Effects = effects
	return service, effects
}

func TestSendMessageActiveCallThroughRejectsWithoutLegacyFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		parent       models.SessionStatus
		lane         models.ThreadStatus
		epoch        int64
		comments     bool
		capacity     bool
		unknown      bool
		target       bool
		targetPolicy LegacyChangesetPolicy
		want         error
	}{
		{name: "exact key changed payload conflicts before capture", want: db.ErrMessageAdmissionConflict},
		{name: "captured epoch is not replaced", parent: models.SessionStatusIdle, lane: models.ThreadStatusIdle, epoch: 6, want: db.ErrMessageAdmissionConflict},
		{name: "awaiting input remains A3 unsupported", parent: models.SessionStatusAwaitingInput, lane: models.ThreadStatusAwaitingInput, epoch: 5, want: db.ErrMessageAdmissionAwaitingInput},
		{name: "comments on busy lane preserve not-sent fallback", parent: models.SessionStatusRunning, lane: models.ThreadStatusRunning, epoch: 5, comments: true, want: ErrThreadNotIdle},
		{name: "current inbox capacity rejects before writes", parent: models.SessionStatusIdle, lane: models.ThreadStatusIdle, epoch: 5, capacity: true, want: ErrThreadInboxBackpressure},
		{name: "ambiguous active rollback retains ingress after negative observation", parent: models.SessionStatusIdle, lane: models.ThreadStatusIdle, epoch: 5, capacity: true, unknown: true, want: ErrCommandOutcomeUnknown},
		{name: "active configured HTTP target never reads current materialization", parent: models.SessionStatusIdle, lane: models.ThreadStatusIdle, epoch: 5, capacity: true, target: true, targetPolicy: LegacyChangesetRequireMaterialized, want: ErrThreadInboxBackpressure},
		{name: "active unconfigured HTTP target never takes legacy refusal", parent: models.SessionStatusIdle, lane: models.ThreadStatusIdle, epoch: 5, capacity: true, target: true, targetPolicy: LegacyChangesetUnconfigured, want: ErrThreadInboxBackpressure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated active send SQL fixture")
			defer mock.Close()
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ClientMessageID: "synthetic-key", Message: "continue"}
			input.LegacyChangesetPolicy = tt.targetPolicy
			if tt.target {
				targetID := uuid.New()
				input.ChangesetID = &targetID
			}
			if tt.comments {
				input.ResolveReviewCommentIDs = []uuid.UUID{uuid.New()}
			}
			prepared, _, err := prepareSendMessage(input)
			require.NoError(t, err, "prepare fixed identity once for exact SQL expectations")
			mock.ExpectBegin()
			expectActiveSendMode(mock, input)
			expectSendPolicy(mock)
			if tt.epoch == 0 {
				digest := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
				mock.ExpectQuery(regexp.QuoteMeta(sendDispatchLookupSQL)).WithArgs(input.OrgID, prepared.Ingress.Kind(), prepared.Ingress.Key()).WillReturnRows(sendDispatchFixtureRows(input, uuid.New(), digest, json.RawMessage(`{}`)))
				mock.ExpectRollback()
			} else {
				expectSendMiss(mock, input, prepared.Ingress)
				expectActiveSendMode(mock, input)
				expectSendOwners(mock, input, nil)
				expectActiveSendMode(mock, input)
				expectActiveSendScope(mock, input, 5, models.SessionStatusIdle, models.ThreadStatusIdle, nil)
				expectSendOwners(mock, input, nil)
				mock.ExpectCommit()
				mock.ExpectBegin()
				expectActiveSendMode(mock, input)
				expectSendPolicy(mock)
				expectSendPolicy(mock)
				expectSendMiss(mock, input, prepared.Ingress)
				expectActiveSendMode(mock, input)
				expectSendOwners(mock, input, nil)
				expectActiveSendMode(mock, input)
				expectActiveSendScope(mock, input, tt.epoch, tt.parent, tt.lane, nil)
				expectSendMiss(mock, input, prepared.Ingress)
				if tt.epoch == 5 {
					expectSendOwners(mock, input, nil)
					mock.ExpectQuery(`SELECT t.status,t.current_turn,t.admission_claim_dispatch_id,t.admission_claim_token,s.container_id,s.worker_node_id,s.current_turn`).WithArgs(input.OrgID, input.SessionID, input.ThreadID).WillReturnRows(pgxmock.NewRows([]string{"status", "turn", "dispatch", "token", "container", "worker", "parent_turn"}).AddRow(tt.lane, int64(2), nil, nil, nil, nil, int64(2)))
					mock.ExpectQuery(`SELECT\s+EXISTS\(SELECT 1 FROM session_questions`).WithArgs(input.OrgID, input.SessionID, input.ThreadID).WillReturnRows(pgxmock.NewRows([]string{"pending_answer"}).AddRow(false))
					if tt.capacity {
						mock.ExpectQuery(`SELECT count\(\*\) FILTER\(WHERE thread_id=\$3\),count\(\*\) FROM thread_inbox_entries`).WithArgs(input.OrgID, input.SessionID, input.ThreadID).WillReturnRows(pgxmock.NewRows([]string{"lane", "session"}).AddRow(int64(200), int64(201)))
					}
				}
				rollback := mock.ExpectRollback()
				if tt.unknown {
					rollback.WillReturnError(errors.New("synthetic rollback acknowledgement failure"))
					mock.ExpectBegin()
					expectActiveSendMode(mock, input)
					expectSendPolicy(mock)
					expectSendMiss(mock, input, prepared.Ingress)
					mock.ExpectCommit()
				}
			}
			service, effects := newActiveSendService(t, mock)
			targetReads := 0
			service.send.Changesets = sendChangesetReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionChangeset, error) {
				targetReads++
				return models.SessionChangeset{}, errors.New("active routing must not read legacy physical materialization")
			})
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, tt.want, "active call-through must retain the precise rejection without recapture, AnswerLatest, or legacy owner hooks")
			require.Nil(t, result, "a rejected active command must expose no accepted message/receipt")
			if tt.unknown {
				var unknown *CommandOutcomeUnknownError
				require.ErrorAs(t, err, &unknown, "negative exact read cannot prove the prior active unit aborted")
				require.Equal(t, prepared.Ingress, unknown.Ingress, "active uncertainty must retain the full original ingress unchanged")
				require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "active uncertainty also retains correlation without replacing its canonical ingress")
			}
			require.Empty(t, effects.calls, "rollback must discard all effects")
			require.Zero(t, targetReads, "fresh active routing and canonical error handling must never invoke the legacy target reader")
			require.NoError(t, mock.ExpectationsWereMet(), "entry/capture and mutating units must preserve exact ordered read and rollback boundaries")
		})
	}
}

func TestSendMessageActiveReplayPrecedesEligibilityAndFenceCapture(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated exact replay SQL fixture")
	defer mock.Close()
	input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ClientMessageID: "synthetic-key", Message: "continue"}
	changeset := uuid.New()
	input.ChangesetID = &changeset
	input.LegacyChangesetPolicy = LegacyChangesetRequireMaterialized
	prepared, _, err := prepareSendMessage(input)
	require.NoError(t, err, "prepare exact replay identity without current state")
	epoch := int64(7)
	prepared.Fence, err = db.NewAdmissionFence(input.SessionID, &input.ThreadID, 5, &epoch)
	require.NoError(t, err, "retain original fence")
	command, err := db.NewMessageAdmission(prepared)
	require.NoError(t, err, "retain original semantic command")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dispatch, inboxID, owner := uuid.New(), uuid.New(), uuid.New()
	envelope, err := json.Marshal(map[string]any{"version": 1, "semantic_payload": string(command.SemanticPayload()), "run_at": prepared.Dispatch.RunAt, "max_attempts": 3, "session_epoch": 5, "thread_epoch": 7, "message_id": 42, "message_turn": 3, "lease_holder_id": prepared.LeaseHolderID, "disposition": "waiting", "thread_status": "idle", "inbox_id": inboxID, "job_id": uuid.Nil, "claim_token": uuid.Nil, "claim_turn": 0, "resolved_comments": []models.SessionReviewComment{}, "reason": "consumer_blocked"})
	require.NoError(t, err, "encode synthetic immutable original outcome")
	digest, err := prepared.Ingress.Digest().Encode()
	require.NoError(t, err, "encode exact semantic digest")
	expectReceipt := func() {
		mock.ExpectQuery(regexp.QuoteMeta(sendDispatchLookupSQL)).WithArgs(input.OrgID, prepared.Ingress.Kind(), prepared.Ingress.Key()).WillReturnRows(sendDispatchFixtureRows(input, dispatch, digest, envelope))
	}
	mock.ExpectBegin()
	expectActiveSendMode(mock, input)
	expectSendPolicy(mock)
	expectReceipt()
	expectActiveSendMode(mock, input)
	expectSendOwners(mock, input, &owner)
	mock.ExpectQuery(`SELECT bool_and\(pg_try_advisory_xact_lock`).WithArgs([]string{"code_review_pr:" + input.OrgID.String() + ":" + owner.String()}).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(`SELECT id FROM session_input_dispatches WHERE org_id=\$1 AND id=ANY`).WithArgs(input.OrgID, []uuid.UUID{dispatch}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(dispatch))
	end := int64(999)
	expectActiveSendScope(mock, input, 999, models.SessionStatusAwaitingInput, models.ThreadStatusAwaitingInput, &end)
	expectSendOwners(mock, input, &owner)
	expectReceipt()
	mock.ExpectQuery(`SELECT .* FROM session_messages WHERE org_id`).WithArgs(input.OrgID, int64(42)).WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "turn_number", "role", "content", "source", "created_at"}).AddRow(int64(42), input.SessionID, input.OrgID, &input.ThreadID, 3, models.MessageRoleUser, input.Message, models.SessionMessageSource(""), now))
	key := input.ClientMessageID
	mock.ExpectQuery(`SELECT .* FROM thread_inbox_entries WHERE org_id=\$1 AND session_id=\$2 AND thread_id=\$3 AND id=\$4 AND message_id=\$5`).WithArgs(input.OrgID, input.SessionID, input.ThreadID, inboxID, int64(42)).WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "thread_id", "sequence_no", "message_id", "client_message_id", "entry_type", "payload", "delivery_state", "delivery_attempts", "last_error", "owner_node_id", "runtime_id", "accepted_at", "delivered_at", "acked_at", "applied_at", "created_at", "updated_at"}).AddRow(inboxID, input.OrgID, input.SessionID, input.ThreadID, int64(1), int64(42), key, models.ThreadInboxEntryTypeUserMessage, json.RawMessage(`{"message_id":42,"turn_number":3,"content":"continue","plan_mode":false}`), models.ThreadInboxDeliveryStatePending, 0, nil, nil, nil, now, nil, nil, nil, now, now))
	mock.ExpectCommit()
	service, effects := newActiveSendService(t, mock)
	targetReads := 0
	service.send.Changesets = sendChangesetReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionChangeset, error) {
		targetReads++
		return models.SessionChangeset{ID: changeset, OrgID: input.OrgID, SessionID: input.SessionID, WorktreePath: nil}, nil
	})
	result, err := service.SendMessage(context.Background(), input)
	require.NoError(t, err, "exact accepted receipt must precede new current owner/status/end/capacity eligibility and fence capture; fixture cause: %v", errors.Unwrap(err))
	require.NotNil(t, result.Admission, "active replay must retain a canonical receipt rather than legacy inbox fallback")
	require.Equal(t, db.MessageAdmissionReplayed, result.Admission.Disposition(), "replay must remain distinct from a newly enqueued admission")
	require.Equal(t, prepared.Fence, result.Admission.Fence(), "replay must return original epochs instead of current 999")
	require.Equal(t, prepared.Dispatch, result.Admission.DispatchOptions(), "replay must preserve original server options")
	holder, hasHolder := result.Admission.LeaseHolderID()
	require.True(t, hasHolder, "original changeset receipt must retain its holder")
	require.Equal(t, *prepared.LeaseHolderID, holder, "replay must not adopt the newly minted per-call holder")
	require.Empty(t, effects.calls, "receipt observation cannot publish or notify")
	require.Zero(t, targetReads, "same-key accepted replay must not be vetoed by the target's current missing materialization")
	require.NoError(t, mock.ExpectationsWereMet(), "positive replay must run only one nonmutating locked observation unit, with higher locks before parent")
}

func TestSendMessageActiveZeroTargetRefusesBeforeReceiptLookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy LegacyChangesetPolicy
	}{
		{name: "configured HTTP", policy: LegacyChangesetRequireMaterialized},
		{name: "unconfigured HTTP", policy: LegacyChangesetUnconfigured},
		{name: "unchanged direct caller", policy: LegacyChangesetUnchanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "construct isolated active target identity fixture")
			defer mock.Close()
			zeroID := uuid.Nil
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ChangesetID: &zeroID, LegacyChangesetPolicy: tt.policy, ClientMessageID: "synthetic-key", Message: "continue"}
			_, holder, err := prepareSendMessage(input)
			require.ErrorIs(t, err, ErrSendChangesetNotFound, "zero target identity must retain a typed deferred active refusal")
			require.NotNil(t, holder, "deferred validation must preserve legacy direct callers' original holder allocation shape")
			mock.ExpectBegin()
			expectActiveSendMode(mock, input)
			mock.ExpectRollback()
			service, effects := newActiveSendService(t, mock)
			reads := 0
			service.send.Changesets = sendChangesetReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionChangeset, error) {
				reads++
				return models.SessionChangeset{}, errors.New("legacy target reader must not validate active identity")
			})
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, ErrSendChangesetNotFound, "active zero identity should render the safe target-not-found category rather than generic server failure")
			require.Nil(t, result, "zero target identity must not create accepted input")
			require.Zero(t, reads, "active identity refusal must not invoke legacy physical materialization reads")
			require.Empty(t, effects.calls, "identity refusal must not emit any postcommit mutation effects")
			require.NoError(t, mock.ExpectationsWereMet(), "only active mode observation and known rollback may occur before zero target refusal")
		})
	}
}
