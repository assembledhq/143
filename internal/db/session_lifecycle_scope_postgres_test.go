package db

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleScopePostgresSnapshots(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name       string
		hidden     bool
		lanes      string
		suppliedTx bool
	}{
		{name: "nil lanes mean session only", lanes: "nil"},
		{name: "empty lanes do not infer primary", lanes: "empty"},
		{name: "explicit sorted raw snapshot", lanes: "all"},
		{name: "unsorted duplicates never mutate input backing array", lanes: "duplicates"},
		{name: "deleted archived terminal rows remain raw observations", lanes: "all", hidden: true},
		{name: "caller configured transaction retains ownership", lanes: "duplicates", suppliedTx: true},
		{name: "caller configured hidden session only", lanes: "nil", hidden: true, suppliedTx: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fixture := seedLifecycleScopeSnapshotFixture(t, ctx, pool, uuid.Nil, tt.hidden)
			before := lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, fixture.want.session.id)
			scope := lifecycleScope{sessionID: fixture.want.session.id}
			want := fixture.want
			var buffer []uuid.UUID
			switch tt.lanes {
			case "nil":
				want.threads = []lifecycleLockedThread{}
			case "empty":
				scope.threadIDs = []uuid.UUID{}
				want.threads = []lifecycleLockedThread{}
			case "all":
				scope.threadIDs = []uuid.UUID{want.threads[0].id, want.threads[1].id}
			case "duplicates":
				buffer = []uuid.UUID{want.threads[1].id, want.threads[0].id, want.threads[1].id, uuid.New()}
				scope.threadIDs = buffer[:3]
			}
			beforeScope := lifecycleScope{sessionID: scope.sessionID, threadIDs: slices.Clone(scope.threadIDs)}
			beforeBuffer := slices.Clone(buffer)
			var actual lockedLifecycleScope
			if tt.suppliedTx {
				workCtx, workCancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
				defer workCancel()
				tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
				require.NoError(t, err, "the caller must own its explicitly selected transaction")
				defer func() { _ = tx.Rollback(context.Background()) }()
				require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "the owner must configure policy within its existing whole-unit budget before invoking the read-only scope kernel")
				actual, err = lockLifecycleScopeInTx(workCtx, tx, fixture.orgID, scope)
				require.NoError(t, err, "the private kernel must compose with a caller-configured bounded transaction")
				var open bool
				require.NoError(t, tx.QueryRow(workCtx, `SELECT true`).Scan(&open), "the kernel must leave transaction settlement with its caller")
				require.True(t, open, "the supplied transaction must remain open after successful scope acquisition")
				require.NoError(t, tx.Rollback(ctx), "the caller alone decides to release the supplied transaction")
			} else {
				err := RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
					actual = lockedLifecycleScope{}
					var err error
					actual, err = lockLifecycleScopeInTx(workCtx, tx, fixture.orgID, scope)
					return err
				})
				require.NoError(t, err, "ordinary scope acquisition must use the real bounded lifecycle transaction runner")
			}
			require.Equal(t, want, actual, "the kernel must return every exact raw identity status visibility epoch and version field in stable lane order")
			require.Equal(t, beforeScope, scope, "normalization must not mutate caller-owned lane order duplicates or nilness")
			require.Equal(t, beforeBuffer, buffer, "cloning must also preserve unused capacity in the caller's backing array")
			require.Equal(t, before, lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, scope.sessionID), "acquisition must preserve all parent and lane data bytes including bigint precision")
		})
	}
}

func TestLifecycleScopePostgresRejectsMismatchedScope(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name       string
		selection  string
		wantNoRows bool
	}{
		{name: "zero organization", selection: "zero_org"},
		{name: "zero session", selection: "zero_session"},
		{name: "zero lane", selection: "zero_lane"},
		{name: "missing session", selection: "missing_session", wantNoRows: true},
		{name: "foreign session organization", selection: "foreign_org", wantNoRows: true},
		{name: "missing lane after parent and existing lane", selection: "missing_lane", wantNoRows: true},
		{name: "foreign organization lane", selection: "foreign_lane", wantNoRows: true},
		{name: "same tenant wrong parent lane", selection: "wrong_parent_lane", wantNoRows: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fixture := seedLifecycleScopeSnapshotFixture(t, ctx, pool, uuid.Nil, false)
			other := seedLifecycleScopeSnapshotFixture(t, ctx, pool, fixture.orgID, false)
			foreign := seedLifecycleScopeSnapshotFixture(t, ctx, pool, uuid.Nil, false)
			before := lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, fixture.want.session.id)
			orgID := fixture.orgID
			scope := lifecycleScope{sessionID: fixture.want.session.id, threadIDs: []uuid.UUID{fixture.want.threads[1].id, fixture.want.threads[0].id}}
			switch tt.selection {
			case "zero_org":
				orgID = uuid.Nil
			case "zero_session":
				scope.sessionID = uuid.Nil
			case "zero_lane":
				scope.threadIDs[0] = uuid.Nil
			case "missing_session":
				scope.sessionID = uuid.New()
			case "foreign_org":
				orgID = foreign.orgID
			case "missing_lane":
				scope.threadIDs[0] = uuid.New()
			case "foreign_lane":
				scope.threadIDs[0] = foreign.want.threads[0].id
			case "wrong_parent_lane":
				scope.threadIDs[0] = other.want.threads[0].id
			}
			beforeScope := lifecycleScope{sessionID: scope.sessionID, threadIDs: slices.Clone(scope.threadIDs)}
			var actual lockedLifecycleScope
			err := RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
				actual = lockedLifecycleScope{}
				var err error
				actual, err = lockLifecycleScopeInTx(workCtx, tx, orgID, scope)
				return err
			})
			require.Error(t, err, "invalid or mismatched explicit scope must not become a successful observation")
			if tt.wantNoRows {
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing tenant parent or lane identity must preserve the PostgreSQL no-row cause")
			}
			require.Equal(t, lockedLifecycleScope{}, actual, "a failed scope must expose neither the already-read parent nor a partial lane set")
			require.Equal(t, beforeScope, scope, "failed normalization and lookup must leave the supplied scope unchanged")
			require.Equal(t, before, lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, fixture.want.session.id), "rejected scope acquisition must not mutate any parent or lane field")
		})
	}
}

func TestLifecycleScopePostgresRejectsUnconfiguredPolicyBeforeParentLocks(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name        string
		isolation   pgx.TxIsoLevel
		setting     string
		value       string
		contextMode string
	}{
		{name: "unset effective marker", setting: "lifecycle143.writer_version", value: ""},
		{name: "old marker", setting: "lifecycle143.writer_version", value: "0"},
		{name: "unknown future marker", setting: "lifecycle143.writer_version", value: "2"},
		{name: "malformed marker", setting: "lifecycle143.writer_version", value: "not-a-version"},
		{name: "unbounded lock timeout", setting: "lock_timeout", value: "0"},
		{name: "different lock timeout", setting: "lock_timeout", value: "251ms"},
		{name: "unbounded statement timeout", setting: "statement_timeout", value: "0"},
		{name: "different statement timeout", setting: "statement_timeout", value: "1501ms"},
		{name: "repeatable read", isolation: pgx.RepeatableRead},
		{name: "serializable", isolation: pgx.Serializable},
		{name: "missing caller deadline", contextMode: "none"},
		{name: "excess remaining caller deadline", contextMode: "long"},
		{name: "expired caller deadline", contextMode: "expired"},
		{name: "canceled caller context", contextMode: "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fixture := seedLifecycleScopeSnapshotFixture(t, ctx, pool, uuid.Nil, false)
			before := lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, fixture.want.session.id)
			isolation := tt.isolation
			if isolation == "" {
				isolation = pgx.ReadCommitted
			}
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
			require.NoError(t, err, "the caller must own the intentionally selected transaction policy")
			defer func() { _ = tx.Rollback(context.Background()) }()
			_, err = tx.Exec(ctx, lifecycleTransactionSettings)
			require.NoError(t, err, "install known effective settings without changing the selected transaction isolation")
			if tt.setting != "" {
				_, err = tx.Exec(ctx, `SELECT set_config($1,$2,true)`, tt.setting, tt.value)
				require.NoError(t, err, "replace only the one effective setting that this case deliberately rejects")
			}
			var settingsBefore [4]string
			require.NoError(t, tx.QueryRow(ctx, lifecycleTransactionPolicyQuery).Scan(&settingsBefore[0], &settingsBefore[1], &settingsBefore[2], &settingsBefore[3]), "record every effective policy field before invoking the read-only verifier")
			workCtx, workCancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
			defer workCancel()
			switch tt.contextMode {
			case "none":
				workCtx = context.WithoutCancel(ctx)
			case "long":
				workCtx, workCancel = context.WithTimeout(ctx, 3*LifecycleTransactionTimeout)
				defer workCancel()
			case "expired":
				workCtx, workCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer workCancel()
			case "canceled":
				workCtx, workCancel = context.WithCancel(workCtx)
				workCancel()
			}
			actual, err := lockLifecycleScopeInTx(workCtx, tx, fixture.orgID, lifecycleScope{sessionID: fixture.want.session.id, threadIDs: []uuid.UUID{fixture.want.threads[0].id}})
			require.Error(t, err, "scope acquisition must reject a missing or nonconforming effective transaction policy")
			if tt.contextMode == "expired" {
				require.ErrorIs(t, err, context.DeadlineExceeded, "deadline rejection must preserve the caller's original context cause")
			}
			if tt.contextMode == "canceled" {
				require.ErrorIs(t, err, context.Canceled, "cancellation rejection must preserve the caller's original context cause")
			}
			require.Equal(t, lockedLifecycleScope{}, actual, "policy rejection must return no parent or lane observation")
			rows, err := tx.Query(ctx, `SELECT relation::regclass::text || ':' || mode FROM pg_locks WHERE pid=pg_backend_pid() AND relation IN ('public.sessions'::regclass,'public.session_threads'::regclass) ORDER BY relation,mode`)
			require.NoError(t, err, "inspect this backend's parent-table locks without consuming an observer connection")
			locks, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect every held parent relation lock after policy rejection")
			require.Equal(t, []string{}, locks, "invalid policy must be rejected before any parent query acquires even a relation lock")
			var settingsAfter [4]string
			require.NoError(t, tx.QueryRow(ctx, lifecycleTransactionPolicyQuery).Scan(&settingsAfter[0], &settingsAfter[1], &settingsAfter[2], &settingsAfter[3]), "the verifier must leave the caller transaction open and queryable after rejection")
			require.Equal(t, settingsBefore, settingsAfter, "policy verification must not repair configure reset or downgrade any effective setting")
			require.NoError(t, tx.Rollback(ctx), "the caller alone releases the rejected supplied transaction")
			require.Equal(t, before, lifecycleScopeSnapshotRows(t, ctx, pool, fixture.orgID, fixture.want.session.id), "policy rejection must leave every parent and lane data byte unchanged")
		})
	}
}

func newLifecycleScopeSnapshotPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema lifecycle scope proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to the explicitly supplied disposable PostgreSQL server for prerequisite verification")
	var version int
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version), "read the server version before applying the complete migration chain")
	require.NoError(t, conn.Close(ctx), "close the read-only prerequisite connection before creating an isolated full-schema fixture")
	require.GreaterOrEqual(t, version, 150000, "the complete lifecycle schema requires PostgreSQL 15 or newer")
	return newFeedbackLifecyclePool(t)
}

type lifecycleScopeSnapshotFixture struct {
	orgID uuid.UUID
	want  lockedLifecycleScope
}

func seedLifecycleScopeSnapshotFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, hidden bool) lifecycleScopeSnapshotFixture {
	t.Helper()
	if orgID == uuid.Nil {
		orgID = uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic lifecycle scope tenant')`, orgID)
		require.NoError(t, err, "create an independent synthetic tenant for exact lifecycle membership tests")
	}
	session := models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, &session), "create a real session and its durable primary thread through the legacy store")
	var stamp time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT '2026-01-02 03:04:05.123456+00'::timestamptz`).Scan(&stamp), "decode a fixed visibility marker with the same PostgreSQL driver used by scope snapshots")
	fixture := lifecycleScopeSnapshotFixture{orgID: orgID, want: lockedLifecycleScope{session: lifecycleLockedSession{id: session.ID, orgID: orgID, status: models.SessionStatusRunning, epochs: LifecycleEpochState{AdmissionEpoch: math.MaxInt64 - 1, EndEpoch: requestLifecycleScopePointer(int64(math.MaxInt64)), LifecycleVersion: math.MaxInt16}}, threads: []lifecycleLockedThread{}}}
	if hidden {
		fixture.want.session.status = models.SessionStatusCancelled
		fixture.want.session.deletedAt, fixture.want.session.archivedAt = &stamp, &stamp
		fixture.want.session.epochs = LifecycleEpochState{AdmissionEpoch: math.MinInt64, EndEpoch: requestLifecycleScopePointer(int64(0)), LifecycleVersion: math.MinInt16}
	}
	_, err := pool.Exec(ctx, `UPDATE sessions SET status=$3,deleted_at=$4,archived_at=$5,admission_epoch=$6,end_epoch=$7,lifecycle_version=$8 WHERE org_id=$1 AND id=$2`, orgID, session.ID, fixture.want.session.status, fixture.want.session.deletedAt, fixture.want.session.archivedAt, fixture.want.session.epochs.AdmissionEpoch, fixture.want.session.epochs.EndEpoch, fixture.want.session.epochs.LifecycleVersion)
	require.NoError(t, err, "install exact raw session visibility and epoch observations without inferring eligibility")
	lanes := []struct {
		status models.ThreadStatus
		epochs LifecycleEpochState
	}{
		{status: models.ThreadStatusIdle, epochs: LifecycleEpochState{}},
		{status: models.ThreadStatusFailed, epochs: LifecycleEpochState{AdmissionEpoch: math.MaxInt64, EndEpoch: requestLifecycleScopePointer(int64(math.MinInt64)), LifecycleVersion: math.MaxInt16}},
	}
	for _, lane := range lanes {
		thread := models.SessionThread{OrgID: orgID, SessionID: session.ID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic explicit scope lane", Status: lane.status}
		require.NoError(t, NewSessionThreadStore(pool).Create(ctx, &thread, 10), "create every explicit lane through the real legacy store rather than discovering a primary lane")
		want := lifecycleLockedThread{id: thread.ID, orgID: orgID, sessionID: session.ID, status: lane.status, epochs: lane.epochs}
		if hidden {
			want.archivedAt = &stamp
		}
		_, err := pool.Exec(ctx, `UPDATE session_threads SET archived_at=$4,admission_epoch=$5,end_epoch=$6,lifecycle_version=$7 WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, session.ID, thread.ID, want.archivedAt, lane.epochs.AdmissionEpoch, lane.epochs.EndEpoch, lane.epochs.LifecycleVersion)
		require.NoError(t, err, "install the exact lane visibility and epoch extremes that the raw locked snapshot must preserve")
		fixture.want.threads = append(fixture.want.threads, want)
	}
	slices.SortFunc(fixture.want.threads, func(a, b lifecycleLockedThread) int { return bytes.Compare(a.id[:], b.id[:]) })
	return fixture
}

func requestLifecycleScopePointer[T any](value T) *T { return &value }

func lifecycleScopeSnapshotRows(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
		'organization',(SELECT to_jsonb(o) FROM organizations o WHERE o.id=$1),
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2))`, orgID, sessionID).Scan(&value)
	require.NoError(t, err, "capture all raw organization parent and lane fields without float64 conversion or visibility filtering")
	return value
}
