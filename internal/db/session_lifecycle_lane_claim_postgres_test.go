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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleLaneClaimPostgresObservations(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name, scenario string
		comparison     lifecycleLaneClaimComparison
	}{
		{name: "exact explicit receipt", comparison: lifecycleLaneClaimExact},
		{name: "explicit zero epochs and turn", scenario: "zero", comparison: lifecycleLaneClaimExact},
		{name: "hidden terminal rows remain raw identity only", scenario: "hidden", comparison: lifecycleLaneClaimExact},
		{name: "absent claim preserves complete receipt", scenario: "absent", comparison: lifecycleLaneClaimAbsent},
		{name: "NULL token remains representable without claim", scenario: "null-token", comparison: lifecycleLaneClaimAbsent},
		{name: "NULL turn is not inferred", scenario: "null-turn", comparison: lifecycleLaneClaimIncomplete},
		{name: "NULL session epoch is not zero", scenario: "null-session-epoch", comparison: lifecycleLaneClaimIncomplete},
		{name: "NULL lane epoch is not zero", scenario: "null-thread-epoch", comparison: lifecycleLaneClaimIncomplete},
		{name: "negative recorded turn is incomplete", scenario: "negative-turn", comparison: lifecycleLaneClaimIncomplete},
		{name: "recorded session epoch is stale", scenario: "recorded-session-epoch", comparison: lifecycleLaneClaimStale},
		{name: "recorded lane epoch is stale", scenario: "recorded-thread-epoch", comparison: lifecycleLaneClaimStale},
		{name: "recorded turn is stale", scenario: "recorded-turn", comparison: lifecycleLaneClaimStale},
		{name: "current session epoch is stale", scenario: "current-session-epoch", comparison: lifecycleLaneClaimStale},
		{name: "current lane epoch is stale", scenario: "current-thread-epoch", comparison: lifecycleLaneClaimStale},
		{name: "running claim follows last completed turn", scenario: "running", comparison: lifecycleLaneClaimExact},
		{name: "completed claim keeps receipt identity", scenario: "completed", comparison: lifecycleLaneClaimExact},
		{name: "later lane turn remains raw evidence", scenario: "current-turn", comparison: lifecycleLaneClaimExact},
		{name: "wrong explicit token is not adopted", scenario: "expected-token", comparison: lifecycleLaneClaimMismatch},
		{name: "successor dispatch and token are not adopted", scenario: "successor", comparison: lifecycleLaneClaimMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedLifecycleLaneClaimFixture(t, ctx, pool, tt.scenario == "hidden")
			switch tt.scenario {
			case "zero":
				f.expected.sessionEpoch, f.expected.threadEpoch, f.expected.turn = 0, 0, 0
				f.want.scope.session.epochs.AdmissionEpoch, f.want.scope.threads[0].epochs.AdmissionEpoch, f.want.currentTurn = 0, 0, 0
				f.want.receipt.sessionEpoch, f.want.receipt.threadEpoch, f.want.receipt.turn = requestLifecycleScopePointer(int64(0)), requestLifecycleScopePointer(int64(0)), requestLifecycleScopePointer(int32(0))
			case "absent", "null-token":
				f.want.claim = LifecycleAdmissionClaimState{}
				if tt.scenario == "null-token" {
					f.want.receipt.token = nil
				}
			case "null-turn":
				f.want.receipt.turn = nil
			case "null-session-epoch":
				f.want.receipt.sessionEpoch = nil
			case "null-thread-epoch":
				f.want.receipt.threadEpoch = nil
			case "negative-turn":
				f.want.receipt.turn = requestLifecycleScopePointer(int32(-1))
			case "recorded-session-epoch":
				f.want.receipt.sessionEpoch = requestLifecycleScopePointer(int64(8))
			case "recorded-thread-epoch":
				f.want.receipt.threadEpoch = requestLifecycleScopePointer(int64(12))
			case "recorded-turn":
				f.want.receipt.turn = requestLifecycleScopePointer(int32(4))
			case "current-session-epoch":
				f.want.scope.session.epochs.AdmissionEpoch++
			case "current-thread-epoch":
				f.want.scope.threads[0].epochs.AdmissionEpoch++
			case "current-turn":
				f.want.currentTurn++
			case "running", "completed":
				f.want.currentTurn = f.expected.turn - 1
			case "expected-token":
				f.expected.token = uuid.New()
			case "successor":
				successor := f.expected
				successor.dispatchID, successor.token = uuid.New(), uuid.New()
				insertLifecycleLaneClaimReceipt(t, ctx, pool, f.orgID, successor)
				f.want.claim = LifecycleAdmissionClaimState{DispatchID: &successor.dispatchID, Token: &successor.token}
			}
			writeLifecycleLaneClaimFixture(t, ctx, pool, f)
			if tt.scenario == "running" || tt.scenario == "completed" {
				store := NewSessionThreadStore(pool)
				require.NoError(t, store.UpdateStatus(ctx, f.orgID, f.expected.threadID, models.ThreadStatusRunning), "prepare a running lane whose receipt names the next turn after current_turn")
				f.want.scope.threads[0].status = models.ThreadStatusRunning
				if tt.scenario == "completed" {
					require.NoError(t, store.CompleteTurn(ctx, f.orgID, f.expected.threadID, int(f.expected.turn), ""), "the existing completion owner advances current_turn without replacing the receipt identity")
					f.want.scope.threads[0].status = models.ThreadStatusIdle
					f.want.currentTurn = f.expected.turn
				}
			}
			f.want.comparison = tt.comparison
			before := lifecycleLaneClaimGraph(t, ctx, pool, f)
			workCtx, workCancel, tx := beginLifecycleLaneClaimOwner(t, ctx, pool)
			defer workCancel()
			actual, err := verifyLifecycleLaneClaimInTx(workCtx, tx, f.orgID, f.expected)
			require.NoError(t, err, "raw present receipt evidence must be observable without inventing eligibility")
			require.Equal(t, f.want, actual, "the complete exact scoped observation must preserve every raw identity NULL epoch turn status and visibility field")
			require.NoError(t, tx.Rollback(ctx), "only the caller settles its successful comparison transaction")
			require.Equal(t, before, lifecycleLaneClaimGraph(t, ctx, pool, f), "comparison must preserve every organization session lane and dispatch field")
		})
	}
}

func TestLifecycleLaneClaimPostgresMissingScopedEvidence(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct{ name string }{
		{name: "missing session"}, {name: "wrong organization"}, {name: "wrong session"},
		{name: "missing lane"}, {name: "sibling lane"}, {name: "missing expected dispatch"}, {name: "sibling expected dispatch"},
		{name: "absent claim and missing expected dispatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedLifecycleLaneClaimFixture(t, ctx, pool, false)
			orgID, expected := f.orgID, f.expected
			switch tt.name {
			case "missing session":
				expected.sessionID = uuid.New()
			case "wrong organization":
				orgID = uuid.New()
			case "wrong session":
				expected.sessionID = seedLifecycleScopeSnapshotFixture(t, ctx, pool, f.orgID, false).want.session.id
			case "missing lane":
				expected.threadID = uuid.New()
			case "sibling lane":
				expected.threadID = f.siblingID
			case "missing expected dispatch":
				expected.dispatchID = uuid.New()
			case "absent claim and missing expected dispatch":
				_, err := pool.Exec(ctx, `UPDATE session_threads SET admission_claim_dispatch_id=NULL,admission_claim_token=NULL WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.expected.sessionID, f.expected.threadID)
				require.NoError(t, err, "an absent claim must not conceal a missing explicit expected receipt")
				expected.dispatchID = uuid.New()
			case "sibling expected dispatch":
				sibling := expected
				sibling.threadID, sibling.dispatchID = f.siblingID, uuid.New()
				insertLifecycleLaneClaimReceipt(t, ctx, pool, orgID, sibling)
				expected.dispatchID = sibling.dispatchID
			}
			before := lifecycleLaneClaimGraph(t, ctx, pool, f)
			workCtx, workCancel, tx := beginLifecycleLaneClaimOwner(t, ctx, pool)
			defer workCancel()
			actual, err := verifyLifecycleLaneClaimInTx(workCtx, tx, orgID, expected)
			require.ErrorIs(t, err, pgx.ErrNoRows, "every missing exact tenant parent lane or expected receipt identity must preserve no-row evidence")
			require.Equal(t, lifecycleLaneClaimObservation{}, actual, "scope or receipt failure must discard already observed parent lane and claim metadata")
			require.NoError(t, tx.Rollback(ctx), "the owner must release all partial read locks after nonmatching scope")
			require.Equal(t, before, lifecycleLaneClaimGraph(t, ctx, pool, f), "missing scoped evidence cannot write or normalize any lifecycle graph row")
		})
	}
}

func TestLifecycleLaneClaimPostgresPhysicalLockOrder(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := seedLifecycleLaneClaimFixture(t, ctx, pool, false)
	before := lifecycleLaneClaimGraph(t, ctx, pool, f)
	workCtx, workCancel, owner := beginLifecycleLaneClaimOwner(t, ctx, pool)
	defer workCancel()
	intercepted := 0
	tx := &lifecycleLaneClaimProbeTx{Tx: owner, beforeReceipt: func() {
		intercepted++
		// This is a synchronous barrier before the actual receipt SQL, not an
		// ordering guess based on sleeps or a successful final observation.
		probeLifecycleLaneClaimInterlock(t, ctx, pool, f, false)
		probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, nil, true)
		probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, &f.expected.threadID, true)
		probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, &f.siblingID, false)
		probeLifecycleLaneClaimReceiptLock(t, ctx, pool, f, false)
	}}
	actual, err := verifyLifecycleLaneClaimInTx(workCtx, tx, f.orgID, f.expected)
	require.NoError(t, err, "the verifier must compose lock prerequisites with an already configured caller transaction")
	require.Equal(t, f.want, actual, "the physically locked comparison must still return the entire raw receipt observation")
	require.Equal(t, 1, intercepted, "the barrier must run exactly before the real explicit receipt acquisition")
	probeLifecycleLaneClaimReceiptLock(t, ctx, pool, f, true)
	probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, &f.siblingID, false)
	require.Equal(t, [3]int{}, tx.settlement, "verification cannot begin commit or roll back its owner's transaction")
	require.NoError(t, owner.Rollback(ctx), "only the real transaction owner releases all retained receipt and scope locks")
	probeLifecycleLaneClaimInterlock(t, ctx, pool, f, true)
	probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, nil, false)
	probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.expected.sessionID, &f.expected.threadID, false)
	probeLifecycleLaneClaimReceiptLock(t, ctx, pool, f, false)
	require.Equal(t, before, lifecycleLaneClaimGraph(t, ctx, pool, f), "lock acquisition and owner rollback cannot change durable lifecycle data")
}

func TestLifecycleLaneClaimPostgresFreshSuccessorStatement(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := seedLifecycleLaneClaimFixture(t, ctx, pool, false)
	successor := f.expected
	successor.dispatchID, successor.token = uuid.New(), uuid.New()
	insertLifecycleLaneClaimReceipt(t, ctx, pool, f.orgID, successor)
	workCtx, workCancel, tx := beginLifecycleLaneClaimOwner(t, ctx, pool)
	defer workCancel()
	var oldPointer uuid.UUID
	require.NoError(t, tx.QueryRow(workCtx, `SELECT admission_claim_dispatch_id FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.expected.sessionID, f.expected.threadID).Scan(&oldPointer), "establish the old receipt observation before another connection commits its successor")
	require.Equal(t, f.expected.dispatchID, oldPointer, "the old snapshot must actually contain the first receipt")
	_, err := pool.Exec(ctx, `UPDATE session_threads SET admission_claim_dispatch_id=$4,admission_claim_token=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, successor.sessionID, successor.threadID, successor.dispatchID, successor.token)
	require.NoError(t, err, "commit the successor claim on a separate connection before the verifier's next statement")
	before := lifecycleLaneClaimGraph(t, ctx, pool, f)
	old, err := verifyLifecycleLaneClaimInTx(workCtx, tx, f.orgID, f.expected)
	require.NoError(t, err, "READ COMMITTED must allow a fresh claim observation within the same caller-owned transaction")
	f.want.claim = LifecycleAdmissionClaimState{DispatchID: &successor.dispatchID, Token: &successor.token}
	f.want.comparison = lifecycleLaneClaimMismatch
	require.Equal(t, f.want, old, "the old explicit receipt must remain old while the raw claim observes the committed successor without adoption")
	f.want.receipt.id, f.want.receipt.token = successor.dispatchID, &successor.token
	f.want.comparison = lifecycleLaneClaimExact
	current, err := verifyLifecycleLaneClaimInTx(workCtx, tx, f.orgID, successor)
	require.NoError(t, err, "a separately explicit successor comparison may compose within the still owned transaction")
	require.Equal(t, f.want, current, "only the independently supplied successor identity may match its exact receipt")
	require.NoError(t, tx.Rollback(ctx), "the original caller retains settlement even across fresh statement observations")
	require.Equal(t, before, lifecycleLaneClaimGraph(t, ctx, pool, f), "verification must not modify either old or successor dispatch data")
}

func TestLifecycleLaneClaimPostgresReceiptContentionOwnerRollback(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f := seedLifecycleLaneClaimFixture(t, ctx, pool, false)
	before := lifecycleLaneClaimGraph(t, ctx, pool, f)
	blocker := beginSessionLifecycleProbe(t, ctx, pool)
	var id uuid.UUID
	require.NoError(t, blocker.QueryRow(ctx, `SELECT id FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4 FOR NO KEY UPDATE NOWAIT`, f.orgID, f.expected.sessionID, f.expected.threadID, f.expected.dispatchID).Scan(&id), "an independent transaction must positively hold the exact expected receipt before verification")
	require.Equal(t, f.expected.dispatchID, id, "the contention blocker must be the intended receipt rather than an empty lookup")
	workCtx, workCancel, owner := beginLifecycleLaneClaimOwner(t, ctx, pool)
	defer workCancel()
	_, err := owner.Exec(workCtx, `UPDATE session_threads SET label='Synthetic caller-owned uncommitted effect' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.expected.sessionID, f.expected.threadID)
	require.NoError(t, err, "record an outer-unit write before the private verifier so rollback ownership has an observable effect")
	tx := &lifecycleLaneClaimProbeTx{Tx: owner}
	actual, err := verifyLifecycleLaneClaimInTx(workCtx, tx, f.orgID, f.expected)
	requireSessionLifecycleSQLState(t, err, "55P03", "expected receipt contention must fail NOWAIT rather than expose a matching partial result")
	require.Equal(t, lifecycleLaneClaimObservation{}, actual, "a receipt-lock failure must discard even the locked parent lane and claim observations")
	require.Equal(t, [3]int{}, tx.settlement, "the verifier cannot retry or settle the failed outer unit on its owner's behalf")
	require.NoError(t, owner.Rollback(ctx), "the caller must roll back its entire failed unit including effects preceding verification")
	require.NoError(t, blocker.Rollback(ctx), "release only the independent contention probe's own receipt lock")
	require.Equal(t, before, lifecycleLaneClaimGraph(t, ctx, pool, f), "caller rollback must restore the complete graph and leave no partial outer-unit effect")
}

type lifecycleLaneClaimFixture struct {
	orgID, siblingID uuid.UUID
	expected         lifecycleLaneClaimExpected
	want             lifecycleLaneClaimObservation
}

func seedLifecycleLaneClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, hidden bool) lifecycleLaneClaimFixture {
	t.Helper()
	base := seedLifecycleScopeSnapshotFixture(t, ctx, pool, uuid.Nil, hidden)
	e := lifecycleLaneClaimExpected{sessionID: base.want.session.id, threadID: base.want.threads[0].id, dispatchID: uuid.New(), token: uuid.New(), sessionEpoch: 7, threadEpoch: 11, turn: 3}
	f := lifecycleLaneClaimFixture{orgID: base.orgID, siblingID: base.want.threads[1].id, expected: e, want: lifecycleLaneClaimObservation{scope: lockedLifecycleScope{session: base.want.session, threads: []lifecycleLockedThread{base.want.threads[0]}}, claim: LifecycleAdmissionClaimState{DispatchID: &e.dispatchID, Token: &e.token}, currentTurn: e.turn, receipt: lifecycleLaneClaimReceipt{id: e.dispatchID, orgID: base.orgID, sessionID: e.sessionID, threadID: e.threadID, sessionEpoch: &e.sessionEpoch, threadEpoch: &e.threadEpoch, token: &e.token, turn: &e.turn}, comparison: lifecycleLaneClaimExact}}
	f.want.scope.session.epochs.AdmissionEpoch, f.want.scope.threads[0].epochs.AdmissionEpoch = e.sessionEpoch, e.threadEpoch
	insertLifecycleLaneClaimReceipt(t, ctx, pool, f.orgID, e)
	writeLifecycleLaneClaimFixture(t, ctx, pool, f)
	return f
}

func insertLifecycleLaneClaimReceipt(t *testing.T, ctx context.Context, database DBTX, orgID uuid.UUID, e lifecycleLaneClaimExpected) {
	t.Helper()
	_, err := database.Exec(ctx, `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_claim_token,admission_claim_turn) VALUES($1,$2,$3,$4,$5,$6,'message','synthetic-lane-claim',$1::uuid::text,'synthetic-digest','{}',0,$7,$8)`, e.dispatchID, orgID, e.sessionID, e.threadID, e.sessionEpoch, e.threadEpoch, e.token, e.turn)
	require.NoError(t, err, "insert a complete synthetic dispatch before installing its exact token-bound lane pointer")
}

func writeLifecycleLaneClaimFixture(t *testing.T, ctx context.Context, database DBTX, f lifecycleLaneClaimFixture) {
	t.Helper()
	_, err := database.Exec(ctx, `UPDATE sessions SET admission_epoch=$3,current_turn=99 WHERE org_id=$1 AND id=$2`, f.orgID, f.expected.sessionID, f.want.scope.session.epochs.AdmissionEpoch)
	require.NoError(t, err, "install parent epoch while an unrelated parent turn remains independent of the selected lane receipt")
	_, err = database.Exec(ctx, `UPDATE session_threads SET current_turn=$4,admission_epoch=$5,admission_claim_dispatch_id=$6,admission_claim_token=$7 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.expected.sessionID, f.expected.threadID, f.want.currentTurn, f.want.scope.threads[0].epochs.AdmissionEpoch, f.want.claim.DispatchID, f.want.claim.Token)
	require.NoError(t, err, "install the structurally representable pair before changing independently nullable receipt observations")
	r := f.want.receipt
	_, err = database.Exec(ctx, `UPDATE session_input_dispatches SET session_epoch=$4,thread_epoch=$5,admission_claim_token=$6,admission_claim_turn=$7 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.expected.sessionID, r.id, r.sessionEpoch, r.threadEpoch, r.token, r.turn)
	require.NoError(t, err, "retain raw independently nullable receipt evidence without disabling full-schema constraints")
}

func lifecycleLaneClaimGraph(t *testing.T, ctx context.Context, database DBTX, f lifecycleLaneClaimFixture) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, database.QueryRow(ctx, `SELECT jsonb_build_object('scope',$3::jsonb,'dispatches',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM session_input_dispatches d WHERE d.org_id=$1 AND d.session_id=$2))`, f.orgID, f.expected.sessionID, lifecycleScopeSnapshotRows(t, ctx, database, f.orgID, f.expected.sessionID)).Scan(&raw), "capture the complete scoped graph including every dispatch receipt without float64 conversion")
	return raw
}

func beginLifecycleLaneClaimOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (context.Context, context.CancelFunc, pgx.Tx) {
	t.Helper()
	workCtx, cancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
	t.Cleanup(cancel)
	tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "the caller must begin its own concrete bounded READ COMMITTED transaction")
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := tx.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			require.NoError(t, err, "cleanup must roll back only this owner's unfinished transaction")
		}
	})
	require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "the owner must install policy before invoking the private read-only verifier")
	return workCtx, cancel, tx
}

func probeLifecycleLaneClaimReceiptLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f lifecycleLaneClaimFixture, blocked bool) {
	t.Helper()
	tx := beginSessionLifecycleProbe(t, ctx, pool)
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4 FOR NO KEY UPDATE NOWAIT`, f.orgID, f.expected.sessionID, f.expected.threadID, f.expected.dispatchID).Scan(&id)
	if blocked {
		requireSessionLifecycleSQLState(t, err, "55P03", "an independent connection must see the selected receipt as physically locked")
	} else {
		require.NoError(t, err, "a receipt not yet acquired or released by owner rollback must remain physically lockable")
		require.Equal(t, f.expected.dispatchID, id, "the successful independent probe must lock exactly the expected receipt")
	}
	require.NoError(t, tx.Rollback(ctx), "the probe must release its own lock or aborted transaction before verifier progress")
}

func probeLifecycleLaneClaimInterlock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f lifecycleLaneClaimFixture, wantAcquired bool) {
	t.Helper()
	tx := beginSessionLifecycleProbe(t, ctx, pool)
	var acquired bool
	require.NoError(t, tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('session_lifecycle_cutover:' || $1::text || ':' || $2::text,0))`, f.orgID, f.expected.sessionID).Scan(&acquired), "independently probe the literal namespaced organization/session interlock key")
	require.Equal(t, wantAcquired, acquired, "the selected shared cutover lock must exclude activation until only its owning transaction settles")
	require.NoError(t, tx.Rollback(ctx), "release only the independent cutover probe's transaction-scoped lock")
}

type lifecycleLaneClaimProbeTx struct {
	pgx.Tx
	beforeReceipt func()
	settlement    [3]int
}

func (tx *lifecycleLaneClaimProbeTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if query == lifecycleLaneClaimReceiptQuery && tx.beforeReceipt != nil {
		tx.beforeReceipt()
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

func (tx *lifecycleLaneClaimProbeTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlement[0]++
	return nil, errors.New("private receipt comparison must not begin a nested transaction")
}

func (tx *lifecycleLaneClaimProbeTx) Commit(context.Context) error {
	tx.settlement[1]++
	return errors.New("private receipt comparison must not commit its owner")
}

func (tx *lifecycleLaneClaimProbeTx) Rollback(context.Context) error {
	tx.settlement[2]++
	return errors.New("private receipt comparison must not roll back its owner")
}
