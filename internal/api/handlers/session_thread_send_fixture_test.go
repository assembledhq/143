package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These pinned synthetic version-zero rows exercise the REAL protocol resolver
// and per-unit scope guards in the five existing handler/service send cases.
// This is CPU emulation, not PostgreSQL locking, commit or contention evidence.
const handlerSendPolicySQL = `SELECT
		current_setting('transaction_isolation'),
		COALESCE(current_setting('lifecycle143.writer_version', true), ''),
		current_setting('lock_timeout'),
		current_setting('statement_timeout')`
const handlerSendSharedSQL = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1::text, 0))`
const handlerSendModeSQL = `SELECT s.org_id, s.id, s.lifecycle_version,
	c.org_id, c.session_id, c.version, c.state, c.legacy_input_cutoff_id,
	c.normalization, c.activated_at, c.minimum_writer_version, c.resumability_capability,
	s.agent_type, COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'protocol_version', t.lifecycle_version, 'provider', t.agent_type) ORDER BY t.id)
		FROM session_threads t WHERE t.org_id = s.org_id AND t.session_id = s.id
		AND t.archived_at IS NULL), '[]'::jsonb)
	FROM sessions s LEFT JOIN session_lifecycle_cutovers c
	ON c.org_id = s.org_id AND c.session_id = s.id
	WHERE s.org_id = $1 AND s.id = $2`
const handlerSendParentSQL = `SELECT id, org_id, status, deleted_at, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM sessions WHERE org_id = $1 AND id = $2
		FOR NO KEY UPDATE NOWAIT`
const handlerSendLaneKeysSQL = `SELECT id FROM session_threads
	WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
	ORDER BY id LIMIT $4`
const handlerSendLanesSQL = `SELECT id, org_id, session_id, status, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = ANY($3::uuid[])
		ORDER BY id FOR NO KEY UPDATE NOWAIT`

type handlerSendFixtureTx struct {
	pgx.Tx
	t                  *testing.T
	guard              pgxmock.PgxPoolIface
	stage              int
	org, session, lane uuid.UUID
}

func (f *handlerSendFixtureTx) guarded() {
	f.t.Helper()
	require.Equal(f.t, 7, f.stage, "every legacy mutation hook must follow real policy, interlock, fresh mode, parent and ordered lane guard SQL")
}

func (f *handlerSendFixtureTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	var rows *pgxmock.Rows
	switch query {
	case handlerSendPolicySQL:
		require.Contains(f.t, []int{0, 3}, f.stage, "policy must separately precede cutover and parent acquisition")
		f.stage++
		rows = pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms")
	case handlerSendSharedSQL:
		require.Equal(f.t, 1, f.stage, "interlock must precede mode observation")
		f.stage++
		rows = pgxmock.NewRows([]string{"locked"}).AddRow(true)
	case handlerSendModeSQL:
		require.Equal(f.t, 2, f.stage, "real resolver must read fresh pinned mode after interlock")
		f.stage++
		f.org, f.session = args[0].(uuid.UUID), args[1].(uuid.UUID)
		rows = pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(f.org, f.session, int16(0), nil, nil, nil, nil, nil, nil, nil, nil, nil, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`))
	case handlerSendParentSQL:
		require.Equal(f.t, 4, f.stage, "parent must lock before lane discovery")
		require.Equal(f.t, []any{f.org, f.session}, args, "parent guard must retain observed tenant and session")
		f.stage++
		rows = pgxmock.NewRows([]string{"id", "org", "status", "deleted", "archived", "epoch", "end", "protocol"}).AddRow(f.session, f.org, models.SessionStatusIdle, nil, nil, int64(0), nil, int16(0))
	default:
		f.t.Fatalf("unexpected SQL in CPU handler guard fixture: %s", query)
	}
	f.guard.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnRows(rows)
	return f.guard.QueryRow(ctx, query, args...)
}

func (f *handlerSendFixtureTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	var rows *pgxmock.Rows
	switch query {
	case handlerSendLaneKeysSQL:
		require.Equal(f.t, 5, f.stage, "lane discovery must follow the parent guard")
		require.Equal(f.t, []any{f.org, f.session}, args[:2], "lane discovery must retain exact tenant and session")
		f.stage++
		f.lane = args[2].(uuid.UUID)
		rows = pgxmock.NewRows([]string{"id"}).AddRow(f.lane)
	case handlerSendLanesSQL:
		require.Equal(f.t, 6, f.stage, "ordered exact lane locks must precede mutation hooks")
		require.Equal(f.t, []any{f.org, f.session, []uuid.UUID{f.lane}}, args, "lane guard must use the exact ordered discovered set")
		f.stage++
		rows = pgxmock.NewRows([]string{"id", "org", "session", "status", "archived", "epoch", "end", "protocol"}).AddRow(f.lane, f.org, f.session, models.ThreadStatusIdle, nil, int64(0), nil, int16(0))
	default:
		f.t.Fatalf("unexpected row query in CPU handler guard fixture: %s", query)
	}
	f.guard.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnRows(rows)
	return f.guard.Query(ctx, query, args...)
}

type handlerSendFixture struct {
	t       *testing.T
	deps    *threadTestDeps
	settled bool
}

func (f *handlerSendFixture) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	f.t.Helper()
	f.settled = false
	guard, err := pgxmock.NewPool()
	require.NoError(f.t, err, "each original unit owns an independent synthetic guard fixture")
	defer guard.Close()
	tx := &handlerSendFixtureTx{t: f.t, guard: guard}
	unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	err = work(unitCtx, tx)
	require.Contains(f.t, []int{3, 7}, tx.stage, "completed CPU units must execute entry observation or full fixed-branch mutation guards")
	require.NoError(f.t, guard.ExpectationsWereMet(), "real resolver/scope queries must consume all pinned responses")
	f.settled = true
	return err
}

func (f *handlerSendFixture) ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, org, session, lane uuid.UUID, max int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	tx.(*handlerSendFixtureTx).guarded()
	row, err := f.deps.threadStore.ClaimIdleForSession(ctx, org, session, lane, max)
	return row, db.ThreadClaimPersistenceEffects{}, err
}
func (f *handlerSendFixture) ClaimResumeThreadInTx(ctx context.Context, tx pgx.Tx, org, session, lane uuid.UUID, max int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	tx.(*handlerSendFixtureTx).guarded()
	row, err := f.deps.threadStore.ClaimForResumeInSession(ctx, org, session, lane, max)
	return row, db.ThreadClaimPersistenceEffects{}, err
}
func (f *handlerSendFixture) IsAtIdleClaimLimit(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (bool, error) {
	require.True(f.t, f.settled, "claim diagnostics must be outside the guarded unit")
	return false, nil
}
func (f *handlerSendFixture) IsAtResumeClaimLimit(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (bool, error) {
	require.True(f.t, f.settled, "resume diagnostics must be outside the guarded unit")
	return false, nil
}
func (f *handlerSendFixture) ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) (models.Session, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return f.deps.sessionStore.ClaimIdle(ctx, org, session)
}
func (f *handlerSendFixture) ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) (models.Session, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return f.deps.sessionStore.ClaimForResume(ctx, org, session)
}
func (f *handlerSendFixture) UpdateThreadStatusInTx(ctx context.Context, tx pgx.Tx, org, lane uuid.UUID, status models.ThreadStatus) (db.ThreadPersistenceEffects, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return db.ThreadPersistenceEffects{}, f.deps.threadStore.UpdateStatus(ctx, org, lane, status)
}
func (f *handlerSendFixture) UpdateSessionStatusInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID, status models.SessionStatus) (models.Session, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return models.Session{}, f.deps.sessionStore.UpdateStatus(ctx, org, session, status)
}
func (f *handlerSendFixture) IncrementPendingMessagesInTx(ctx context.Context, tx pgx.Tx, org, lane uuid.UUID) (db.ThreadPendingMessagePersistenceEffects, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return db.ThreadPendingMessagePersistenceEffects{}, f.deps.threadStore.IncrementPendingMessages(ctx, org, lane)
}
func (f *handlerSendFixture) CreateMessageInTx(ctx context.Context, tx pgx.Tx, _ uuid.UUID, message *models.SessionMessage) error {
	tx.(*handlerSendFixtureTx).guarded()
	return f.deps.messageStore.Create(ctx, message)
}
func (f *handlerSendFixture) AppendInboxInTx(context.Context, pgx.Tx, uuid.UUID, db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	f.t.Fatal("legacy handler cases have no inbox configured")
	return models.ThreadInboxEntry{}, fmt.Errorf("unexpected inbox mutation")
}
func (f *handlerSendFixture) EnqueueInTx(ctx context.Context, tx pgx.Tx, org uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	tx.(*handlerSendFixtureTx).guarded()
	return f.deps.jobStore.EnqueueWithOpts(ctx, org, opts)
}

func (f *handlerSendFixture) PublishLifecycle(context.Context, uuid.UUID, []db.LifecycleEffect) {
	f.t.Fatal("synthetic v0 handler cannot publish canonical v1 effects")
}
func (f *handlerSendFixture) PublishThreadClaim(context.Context, uuid.UUID, db.ThreadClaimPersistenceEffects) {
	require.True(f.t, f.settled, "claim effects must follow their completed original unit")
}
func (f *handlerSendFixture) PublishThreadPending(context.Context, uuid.UUID, db.ThreadPendingMessagePersistenceEffects) {
	require.True(f.t, f.settled, "pending effects must follow their completed original unit")
}
func (f *handlerSendFixture) PublishThreadStatus(context.Context, uuid.UUID, db.ThreadPersistenceEffects) {
	require.True(f.t, f.settled, "status effects must follow their completed original unit")
}
func (f *handlerSendFixture) PublishSessionStatus(context.Context, uuid.UUID, models.Session) {
	require.True(f.t, f.settled, "parent effects must follow their completed original unit")
}
func (f *handlerSendFixture) NotifyJob(context.Context, uuid.UUID) {
	require.True(f.t, f.settled, "job notifications must follow their completed original unit")
}

func newThreadSendHandler(t *testing.T) (*SessionThreadHandler, *threadTestDeps) {
	t.Helper()
	deps := &threadTestDeps{threadStore: &mockThreadStore{}, sessionStore: &mockSessionStoreForThread{}, messageStore: &mockMessageStore{}, logStore: &mockLogStore{}, jobStore: &mockJobStore{}}
	fixture := &handlerSendFixture{t: t, deps: deps}
	svc, err := thread.NewService(deps.threadStore, deps.sessionStore, deps.messageStore, deps.logStore, deps.jobStore, thread.SendDependencies{Transactions: fixture, Legacy: fixture, Effects: fixture}, zerolog.Nop())
	require.NoError(t, err, "construct the narrow CPU handler/service send fixture with mandatory guarded dependencies")
	return NewSessionThreadHandler(svc), deps
}
