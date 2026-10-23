package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCutoverInterlockModesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		holderExclusive, exclusive bool
		commit, wantBusy           bool
	}{
		{name: "shared shares then commit releases", commit: true},
		{name: "shared shares then rollback releases"},
		{name: "shared excludes exclusive until commit", exclusive: true, commit: true, wantBusy: true},
		{name: "shared excludes exclusive until rollback", exclusive: true, wantBusy: true},
		{name: "exclusive excludes shared until commit", holderExclusive: true, commit: true, wantBusy: true},
		{name: "exclusive excludes shared until rollback", holderExclusive: true, wantBusy: true},
		{name: "exclusive excludes exclusive until commit", holderExclusive: true, exclusive: true, commit: true, wantBusy: true},
		{name: "exclusive excludes exclusive until rollback", holderExclusive: true, exclusive: true, wantBusy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			holder := beginSessionLifecycleProbe(t, ctx, pool)
			contender := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "configure the holder's existing bounded transaction")
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, contender), "configure the contender without nesting or replacing its transaction")
			require.NoError(t, tryLockLifecycleCutoverInTx(ctx, holder, orgID, sessionID, tt.holderExclusive), "the first owner must acquire the real transaction-scoped interlock")
			before := lifecycleCutoverPostgresRows(t, ctx, contender, orgID, sessionID)
			policy := lifecycleCutoverPostgresPolicy(t, ctx, contender)
			// An independent literal key and SQL probe prove the protocol key,
			// not merely agreement between two helpers sharing the same bug.
			key := "session_lifecycle_cutover:" + orgID.String() + ":" + sessionID.String()
			var acquired bool
			require.NoError(t, contender.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`, key).Scan(&acquired), "probe the independently specified exclusive key without waiting")
			require.False(t, acquired, "the exact tenant/session protocol key must already be held by the other transaction")
			wrapper := &lifecycleScopeConcurrencyTx{Tx: contender}
			err := tryLockLifecycleCutoverInTx(ctx, wrapper, orgID, sessionID, tt.exclusive)
			if tt.wantBusy {
				requireLifecycleCutoverPostgresBusy(t, err)
			} else {
				require.NoError(t, err, "two shared owners must coexist on the same real advisory key")
			}
			require.Equal(t, byte('T'), contender.Conn().PgConn().TxStatus(), "a false try-lock must not manufacture a SQLSTATE abort or settle the caller")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, policy, lifecycleCutoverPostgresPolicy(t, ctx, contender), "interlock acquisition and refusal must not change the owner's settings")
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, contender, orgID, sessionID), "lock helpers must not create or alter cutover metadata")
			if tt.commit {
				require.NoError(t, holder.Commit(ctx), "only the holder may commit and release its transaction-scoped lock")
			} else {
				require.NoError(t, holder.Rollback(ctx), "only the holder may roll back and release its transaction-scoped lock")
			}
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, wrapper, orgID, sessionID), "the same live contender must acquire exclusive after holder settlement, including shared-to-exclusive upgrade")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.NoError(t, contender.Rollback(ctx), "the contender retains responsibility for releasing its acquired lock")
			probe := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, probe), "configure a final owner after both prior transactions have settled")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, probe, orgID, sessionID), "no transaction-scoped lock may survive its owner's commit or rollback")
		})
	}
}

func TestLifecycleCutoverIndependentKeysPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		otherOrg bool
		sameID   bool
	}{
		{name: "same tenant different session"},
		{name: "different tenant same session argument", otherOrg: true, sameID: true},
		{name: "different tenant different session", otherOrg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			otherOrgID := orgID
			if tt.otherOrg {
				otherOrgID, _, _ = seedRuntimeSeedThread(t, pool)
			}
			otherSessionID := sessionID
			if !tt.sameID {
				other := &models.Session{OrgID: otherOrgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
				require.NoError(t, NewSessionStore(pool).Create(context.Background(), other), "create a real independent parent before beginning the protocol budget")
				otherSessionID = other.ID
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			holder := beginSessionLifecycleProbe(t, ctx, pool)
			contender := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "configure the original key owner")
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, contender), "configure the independent key owner")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, holder, orgID, sessionID), "hold the exact original key exclusively")
			// The mismatched tenant/session argument case proves key separation
			// only. Advisory acquisition is deliberately not parent authority.
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, contender, otherOrgID, otherSessionID), "changing either key component must not contend with the original key")
			requireLifecycleCutoverPostgresBusy(t, tryLockLifecycleCutoverSharedInTx(ctx, contender, orgID, sessionID))
			require.NoError(t, holder.Rollback(ctx), "release the original key without settling the independent owner")
			require.NoError(t, tryLockLifecycleCutoverSharedInTx(ctx, contender, orgID, sessionID), "the positive-control key must become available only after the original holder releases it")
		})
	}
}

func TestLifecycleCutoverObservationMetadataPostgres(t *testing.T) {
	t.Parallel()
	cutoff := int64(1<<62 + 7)
	minimum := 0
	activated := time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	tests := []struct {
		name string
		row  lifecycleCutoverObservation
	}{
		{name: "explicit shadow zero with SQL NULL metadata", row: lifecycleCutoverObservation{state: models.LifecycleCutoverStateShadow}},
		{name: "large version and JSON null stay distinct from SQL NULL", row: lifecycleCutoverObservation{version: cutoff, state: models.LifecycleCutoverStateNormalizing, legacyInputCutoffID: &cutoff, normalization: json.RawMessage(`null`), minimumWriterVersion: &minimum, resumabilityCapability: json.RawMessage(`null`)}},
		{name: "opaque populated metadata is returned without interpretation", row: lifecycleCutoverObservation{version: 17, state: models.LifecycleCutoverStateActive, legacyInputCutoffID: &cutoff, normalization: json.RawMessage(`{"opaque": [1, "future"]}`), activatedAt: &activated, minimumWriterVersion: &minimum, resumabilityCapability: json.RawMessage(`{"unproven": true}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			want := tt.row
			want.orgID, want.sessionID = orgID, sessionID
			insertLifecycleCutoverPostgresRow(t, context.Background(), pool, want)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			tx := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "prepare the caller-owned observation transaction")
			before := lifecycleCutoverPostgresRows(t, ctx, tx, orgID, sessionID)
			policy := lifecycleCutoverPostgresPolicy(t, ctx, tx)
			wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
			actual, err := observeLifecycleCutoverInTx(ctx, wrapper, orgID, sessionID)
			require.NoError(t, err, "observe only the exact tenant/session row after acquiring shared interlock")
			if actual.activatedAt != nil {
				// Compare the exact stored instant independently of pgx's display location.
				instant := actual.activatedAt.UTC()
				actual.activatedAt = &instant
			}
			require.Equal(t, want, actual, "the complete typed observation must preserve bigint precision, SQL NULL, JSON null and opaque metadata exactly")
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, tx, orgID, sessionID), "observation must not stamp, normalize or infer any durable metadata")
			require.Equal(t, policy, lifecycleCutoverPostgresPolicy(t, ctx, tx), "observation must leave every effective protocol setting unchanged")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, byte('T'), tx.Conn().PgConn().TxStatus(), "a successful observation remains inside the original unsettled transaction")
		})
	}
}

func TestLifecycleCutoverObservationMissingScopePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		missing      bool
		wrongOrg     bool
		wrongSession bool
	}{
		{name: "absent metadata is not inferred version zero", missing: true},
		{name: "different tenant cannot read the stored session", wrongOrg: true},
		{name: "different session cannot read the tenant's stored row", wrongSession: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			if !tt.missing {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
			}
			queryOrg, querySession := orgID, sessionID
			if tt.wrongOrg {
				queryOrg = uuid.New()
			}
			if tt.wrongSession {
				querySession = uuid.New()
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			tx := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "configure an exact scoped metadata reader")
			before := lifecycleCutoverPostgresRows(t, ctx, tx, orgID, sessionID)
			wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
			actual, err := observeLifecycleCutoverInTx(ctx, wrapper, queryOrg, querySession)
			require.ErrorIs(t, err, pgx.ErrNoRows, "missing exact membership must remain absent rather than become an implicit V0 cutover")
			require.Equal(t, lifecycleCutoverObservation{}, actual, "any missing or wrong-scope lookup must return a completely zero observation")
			require.Equal(t, byte('T'), tx.Conn().PgConn().TxStatus(), "an application-level missing row must not abort or settle the caller transaction")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, tx, orgID, sessionID), "missing-scope reads must not create defaults or modify an existing different-scope row")
		})
	}
}

func TestLifecycleCutoverFreshStatementObservationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		missing bool
	}{
		{name: "earlier shadow statement does not hide a later committed version"},
		{name: "earlier absent statement does not infer V0 after insertion", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			if !tt.missing {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "begin one configured READ COMMITTED actor before the synthetic activator commits")
			identity := lifecycleCutoverPostgresTransactionIdentity(t, ctx, actor)
			var oldVersion int64
			var oldState models.LifecycleCutoverState
			err := actor.QueryRow(ctx, `SELECT version,state FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, orgID, sessionID).Scan(&oldVersion, &oldState)
			if tt.missing {
				require.ErrorIs(t, err, pgx.ErrNoRows, "the actor's earlier plain statement must truly observe absence")
			} else {
				require.NoError(t, err, "the actor's earlier plain statement must truly observe shadow metadata")
				require.Equal(t, int64(0), oldVersion, "the earlier snapshot must precede the new stored version")
				require.Equal(t, models.LifecycleCutoverStateShadow, oldState, "the earlier statement must see shadow rather than the later synthetic active label")
			}
			// Statements are intentionally ordered on the two held transactions.
			// The earlier actor read does NOT take the interlock. This synthetic
			// writer is not a production activation implementation or certificate.
			activator := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, activator), "configure the independent synthetic metadata writer")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, activator, orgID, sessionID), "the earlier plain read must not block the synthetic writer's exclusive key")
			_, err = activator.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,normalization) VALUES($1,$2,42,'active','{"synthetic": true}') ON CONFLICT(org_id,session_id) DO UPDATE SET version=42,state='active',normalization=EXCLUDED.normalization`, orgID, sessionID)
			require.NoError(t, err, "stage the new raw metadata only inside the test's exclusive transaction")
			before := lifecycleCutoverPostgresRows(t, ctx, activator, orgID, sessionID)
			require.NoError(t, activator.Commit(ctx), "commit and release the synthetic exclusive owner before the actor's late acquisition")
			policy := lifecycleCutoverPostgresPolicy(t, ctx, actor)
			wrapper := &lifecycleScopeConcurrencyTx{Tx: actor}
			actual, err := observeLifecycleCutoverInTx(ctx, wrapper, orgID, sessionID)
			require.NoError(t, err, "successful late shared acquisition must continue to the separate fresh statement without a manufactured restart")
			require.Equal(t, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, version: 42, state: models.LifecycleCutoverStateActive, normalization: json.RawMessage(`{"synthetic": true}`)}, actual, "the fresh observation must see the complete metadata committed after the earlier actor statement")
			require.Equal(t, identity, lifecycleCutoverPostgresTransactionIdentity(t, ctx, actor), "fresh visibility must occur in the exact same backend and transaction, not a hidden replacement")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, actor, orgID, sessionID), "late observation must not alter the synthetic committed row")
			require.Equal(t, policy, lifecycleCutoverPostgresPolicy(t, ctx, actor), "fresh observation must not reset settings or expand the existing budget")
			// This proves separate Go/READ COMMITTED statement visibility only;
			// it says nothing about a future VOLATILE trigger's within-statement
			// snapshot behavior or the safety of any future active write guards.
		})
	}
}

func TestLifecycleCutoverBusyRollsBackWithoutRunnerReplayPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
	setupCtx, stopSetup := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopSetup()
	insertLifecycleCutoverPostgresRow(t, setupCtx, pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
	_, err := pool.Exec(setupCtx, `CREATE TABLE lifecycle_cutover_effect_probe(org_id uuid NOT NULL, attempt integer NOT NULL, PRIMARY KEY(org_id,attempt))`)
	require.NoError(t, err, "create a synthetic effect table only in this test's owned disposable database")
	ctx, cancel := context.WithTimeout(setupCtx, LifecycleTransactionTimeout)
	defer cancel()
	holder := beginSessionLifecycleProbe(t, ctx, pool)
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "configure the real exclusive blocker before running the independent unit")
	require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, holder, orgID, sessionID), "hold the blocker for the complete runner call without sleeps or release races")
	before := lifecycleCutoverPostgresRows(t, ctx, holder, orgID, sessionID)
	attempts := 0
	var busyCause error
	var attemptedTx pgx.Tx
	err = RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
		attempts++
		attemptedTx = tx
		_, stageErr := tx.Exec(workCtx, `INSERT INTO lifecycle_cutover_effect_probe(org_id,attempt) VALUES($1,$2)`, orgID, attempts)
		if stageErr != nil {
			return fmt.Errorf("stage synthetic outer effect: %w", stageErr)
		}
		observation, observeErr := observeLifecycleCutoverInTx(workCtx, tx, orgID, sessionID)
		require.Equal(t, lifecycleCutoverObservation{}, observation, "busy interlock must discard all captured metadata for the attempted unit")
		requireLifecycleCutoverPostgresBusy(t, observeErr)
		require.Equal(t, byte('T'), tx.Conn().PgConn().TxStatus(), "the private busy error must not pretend PostgreSQL has already aborted this transaction")
		busyCause = observeErr
		return fmt.Errorf("synthetic outer command rejected: %w", observeErr)
	})
	require.Equal(t, 1, attempts, "the SQLSTATE-only public runner must never automatically replay an advisory false application outcome")
	require.NotNil(t, busyCause, "the tested failure must come from the real private busy interlock rather than an unrelated setup error")
	require.ErrorIs(t, err, busyCause, "outer wrapping must retain the exact private busy cause")
	requireLifecycleCutoverPostgresBusy(t, err)
	// A settled pgx transaction rejects subsequent operations without borrowing
	// another observer connection; durable readback uses the held blocker.
	_, closedErr := attemptedTx.Exec(setupCtx, "SELECT 1")
	require.ErrorIs(t, closedErr, pgx.ErrTxClosed, "the runner must settle its rejected whole unit before returning")
	var effects []int
	require.NoError(t, holder.QueryRow(setupCtx, `SELECT COALESCE(array_agg(attempt ORDER BY attempt),ARRAY[]::integer[]) FROM lifecycle_cutover_effect_probe WHERE org_id=$1`, orgID).Scan(&effects), "read committed effects through the held transaction without an extra pool observer")
	require.Equal(t, []int{}, effects, "the complete staged unit must roll back rather than leave a durable effect or replay artifact")
	require.Equal(t, before, lifecycleCutoverPostgresRows(t, setupCtx, holder, orgID, sessionID), "runner refusal must leave all preexisting cutover data unchanged")
	require.Equal(t, byte('T'), holder.Conn().PgConn().TxStatus(), "the helper and runner must not settle the independent exclusive holder")
}

func newLifecycleCutoverPostgresFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := newFeedbackLifecyclePool(t)
	orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
	return pool, orgID, sessionID
}

func insertLifecycleCutoverPostgresRow(t *testing.T, ctx context.Context, db DBTX, row lifecycleCutoverObservation) {
	t.Helper()
	_, err := db.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,legacy_input_cutoff_id,normalization,activated_at,minimum_writer_version,resumability_capability) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, row.orgID, row.sessionID, row.version, row.state, row.legacyInputCutoffID, row.normalization, row.activatedAt, row.minimumWriterVersion, row.resumabilityCapability)
	require.NoError(t, err, "seed raw synthetic metadata explicitly without invoking any production cutover writer")
}

func lifecycleCutoverPostgresRows(t *testing.T, ctx context.Context, db DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := db.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c)), '[]'::jsonb) FROM session_lifecycle_cutovers c WHERE org_id=$1 AND session_id=$2`, orgID, sessionID).Scan(&rows)
	require.NoError(t, err, "read every durable field of only the exact scoped synthetic cutover row")
	return rows
}

func lifecycleCutoverPostgresPolicy(t *testing.T, ctx context.Context, tx pgx.Tx) [4]string {
	t.Helper()
	var actual [4]string
	err := tx.QueryRow(ctx, `SELECT current_setting('transaction_isolation'), COALESCE(current_setting('lifecycle143.writer_version',true),''), current_setting('lock_timeout'), current_setting('statement_timeout')`).Scan(&actual[0], &actual[1], &actual[2], &actual[3])
	require.NoError(t, err, "observe the existing effective settings through the already-held transaction")
	require.Equal(t, [4]string{"read committed", "1", "250ms", "1500ms"}, actual, "the test owner must already have the independent exact protocol policy")
	return actual
}

func lifecycleCutoverPostgresTransactionIdentity(t *testing.T, ctx context.Context, tx pgx.Tx) [2]string {
	t.Helper()
	var actual [2]string
	err := tx.QueryRow(ctx, `SELECT pg_backend_pid()::text, txid_current()::text`).Scan(&actual[0], &actual[1])
	require.NoError(t, err, "identify the current transaction using only its already-held connection")
	return actual
}

func requireLifecycleCutoverPostgresBusy(t *testing.T, err error) {
	t.Helper()
	var busy *lifecycleCutoverInterlockBusyError
	require.ErrorAs(t, err, &busy, "an actual false advisory try-lock must preserve the private interlock busy cause")
	var pgErr *pgconn.PgError
	require.False(t, errors.As(err, &pgErr), "private busy must not manufacture a PostgreSQL SQLSTATE contention failure")
	require.NotErrorIs(t, err, ErrLifecycleContention, "a nonwaiting advisory refusal is not exhausted confirmed-aborted SQL contention")
	require.False(t, retryableLifecycleTransaction(err), "the unchanged public runner must not classify private advisory busy as SQLSTATE-retryable")
}
