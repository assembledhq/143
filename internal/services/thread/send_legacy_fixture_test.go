package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// This CPU fixture executes the real resolver and scope guards against pinned
// SQL responses for a synthetic version-zero database. Business hooks and SQL
// forwarding prove call order and supplied-transaction use only; PostgreSQL
// integration tests own locking, atomic rollback and settlement evidence.
const sendFixturePolicySQL = `SELECT
		current_setting('transaction_isolation'),
		COALESCE(current_setting('lifecycle143.writer_version', true), ''),
		current_setting('lock_timeout'),
		current_setting('statement_timeout')`
const sendFixtureSharedSQL = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1::text, 0))`
const sendFixtureModeSQL = `SELECT s.org_id, s.id, s.lifecycle_version,
	c.org_id, c.session_id, c.version, c.state, c.legacy_input_cutoff_id,
	c.normalization, c.activated_at, c.minimum_writer_version, c.resumability_capability,
	s.agent_type, COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'protocol_version', t.lifecycle_version, 'provider', t.agent_type) ORDER BY t.id)
		FROM session_threads t WHERE t.org_id = s.org_id AND t.session_id = s.id
		AND t.archived_at IS NULL), '[]'::jsonb)
	FROM sessions s LEFT JOIN session_lifecycle_cutovers c
	ON c.org_id = s.org_id AND c.session_id = s.id
	WHERE s.org_id = $1 AND s.id = $2`
const sendFixtureParentSQL = `SELECT id, org_id, status, deleted_at, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM sessions WHERE org_id = $1 AND id = $2
		FOR NO KEY UPDATE NOWAIT`
const sendFixtureKeysSQL = `SELECT id FROM session_threads
	WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
	ORDER BY id LIMIT $4`
const sendFixtureLanesSQL = `SELECT id, org_id, session_id, status, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = ANY($3::uuid[])
		ORDER BY id FOR NO KEY UPDATE NOWAIT`

type legacyGuardFixtureTx struct {
	pgx.Tx
	t                  *testing.T
	guard              pgxmock.PgxPoolIface
	service            *Service
	stage              int
	org, session, lane uuid.UUID
	business           db.DBTX
	normalizing        bool
}

func (f *legacyGuardFixtureTx) requireScope() {
	f.t.Helper()
	require.Equal(f.t, 7, f.stage, "every atomic mutation and classification read must follow fresh policy, mode and canonical scope acquisition")
}
func (f *legacyGuardFixtureTx) businessReader() db.DBTX {
	f.requireScope()
	require.NotNil(f.t, f.business, "grouped CPU SQL must use the mock transaction already acquired by the outer fixture runner")
	return f.business
}

func (f *legacyGuardFixtureTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	var rows *pgxmock.Rows
	switch query {
	case sendFixturePolicySQL:
		require.Contains(f.t, []int{0, 3}, f.stage, "policy must precede interlock and canonical parent separately")
		f.stage++
		rows = pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms")
	case sendFixtureSharedSQL:
		require.Equal(f.t, 1, f.stage, "shared cutover interlock must precede fresh provenance lookup")
		f.stage++
		rows = pgxmock.NewRows([]string{"locked"}).AddRow(true)
	case sendFixtureModeSQL:
		require.Equal(f.t, 2, f.stage, "provenance must be read only after shared interlock")
		f.stage++
		f.org, f.session = args[0].(uuid.UUID), args[1].(uuid.UUID)
		rows = pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(f.org, f.session, int16(0), nil, nil, nil, nil, nil, nil, nil, nil, nil, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`))
		if f.normalizing {
			version, cutoff, minimum, state := int64(1), int64(1), 0, models.LifecycleCutoverStateNormalizing
			rows = pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(f.org, f.session, int16(0), &f.org, &f.session, &version, &state, &cutoff, json.RawMessage(`{"schema_version":1,"receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","floor_receipt_id":"ffffffff-1111-4222-8333-444444444444","cutoff_id":1,"complete":false}`), nil, &minimum, nil, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`))
		}
	case sendFixtureParentSQL:
		require.Equal(f.t, 4, f.stage, "parent lock must precede lane discovery")
		require.Equal(f.t, []any{f.org, f.session}, args, "parent lock must retain exact observed tenant and parent")
		f.stage++
		rows = pgxmock.NewRows([]string{"id", "org", "status", "deleted", "archived", "epoch", "end", "protocol"}).AddRow(f.session, f.org, models.SessionStatusIdle, nil, nil, int64(0), nil, int16(0))
	default:
		return f.businessReader().QueryRow(ctx, query, args...)
	}
	f.guard.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnRows(rows)
	return f.guard.QueryRow(ctx, query, args...)
}

func (f *legacyGuardFixtureTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	var rows *pgxmock.Rows
	switch query {
	case sendFixtureKeysSQL:
		require.Equal(f.t, 5, f.stage, "lane discovery must occur after parent lock in every unit")
		require.Equal(f.t, []any{f.org, f.session}, args[:2], "discovery must retain tenant/parent")
		f.stage++
		f.lane = args[2].(uuid.UUID)
		rows = pgxmock.NewRows([]string{"id"}).AddRow(f.lane)
	case sendFixtureLanesSQL:
		require.Equal(f.t, 6, f.stage, "ordered lane acquisition must finish before business writes")
		require.Equal(f.t, []any{f.org, f.session, []uuid.UUID{f.lane}}, args, "lane locks must use the exact discovered ordered set")
		f.stage++
		rows = pgxmock.NewRows([]string{"id", "org", "session", "status", "archived", "epoch", "end", "protocol"}).AddRow(f.lane, f.org, f.session, models.ThreadStatusIdle, nil, int64(0), nil, int16(0))
	default:
		return f.businessReader().Query(ctx, query, args...)
	}
	f.guard.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnRows(rows)
	return f.guard.Query(ctx, query, args...)
}
func (f *legacyGuardFixtureTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return f.businessReader().Exec(ctx, query, args...)
}

type legacyGuardFixtureRunner struct {
	t             *testing.T
	service       *Service
	units         int
	settled       bool
	events        []string
	failCommitAt  int
	normalizingAt int
	contentionAt  int
	retryAt       int
	rejectRetry   bool
	activeTx      *legacyGuardFixtureTx
	activeCtx     context.Context
	jobIDs        []uuid.UUID
}

func (r *legacyGuardFixtureRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.t.Helper()
	r.units++
	unit := r.units
	for attempt := 0; ; attempt++ {
		r.events = append(r.events, "begin")
		r.settled = false
		guard, err := pgxmock.NewPool()
		require.NoError(r.t, err, "create isolated per-attempt guard SQL fixture")
		tx := &legacyGuardFixtureTx{t: r.t, guard: guard, service: r.service, normalizing: r.normalizingAt == unit || (attempt > 0 && r.rejectRetry)}
		unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
		var business pgx.Tx
		if unit > 1 && r.service.txStarter != nil {
			business, err = r.service.txStarter.Begin(unitCtx)
			require.NoError(r.t, err, "the outer fixture runner must acquire the single grouped business transaction before the callback")
			tx.business = business
		}
		r.activeTx, r.activeCtx = tx, unitCtx
		err = work(unitCtx, tx)
		require.Contains(r.t, []int{3, 7}, tx.stage, "a completed fixture attempt must execute entry observation or full atomic scope guards")
		require.NoError(r.t, guard.ExpectationsWereMet(), "every real guard query must consume its pinned response")
		if business != nil {
			if err != nil {
				if rollbackErr := business.Rollback(unitCtx); rollbackErr != nil {
					err = errors.Join(err, rollbackErr)
				}
			} else {
				err = business.Commit(unitCtx)
			}
		}
		cancel()
		guard.Close()
		if attempt == 0 && r.retryAt == unit {
			// Model a known rollback from callback failure or retryable commit rejection.
			r.events = append(r.events, "rollback")
			continue
		}
		if err != nil && r.contentionAt == unit {
			r.settled = true
			r.events = append(r.events, "rollback")
			return &db.LifecycleContentionError{Attempts: 3, Cause: err}
		}
		if err == nil && r.failCommitAt == unit {
			r.events = append(r.events, "unknown")
			return errors.New("synthetic commit acknowledgement lost")
		}
		r.settled = true
		if err == nil {
			r.events = append(r.events, "commit")
		} else {
			r.events = append(r.events, "rollback")
		}
		return err
	}
}

type legacyHookPersistence struct {
	service                    *Service
	idleLimited, resumeLimited bool
	claimCap                   int
	targetStatus               models.ThreadStatus
	sqlEnqueue                 bool
	enqueues                   []db.EnqueueOpts
}

func (p *legacyHookPersistence) record(operation string) {
	p.service.send.Transactions.(*legacyGuardFixtureRunner).events = append(p.service.send.Transactions.(*legacyGuardFixtureRunner).events, operation)
}

func (p *legacyHookPersistence) scope(ctx context.Context, tx pgx.Tx) {
	runner := p.service.send.Transactions.(*legacyGuardFixtureRunner)
	runner.t.Helper()
	require.Same(runner.t, runner.activeTx, tx, "every legacy port must use the exact supplied atomic transaction")
	require.Equal(runner.t, runner.activeCtx, ctx, "every legacy port must use its supplied atomic callback context")
	require.False(runner.t, runner.settled, "business ports must not run after their transaction settles")
	tx.(*legacyGuardFixtureTx).requireScope()
}

func (p *legacyHookPersistence) RejectIfOwnedInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) error {
	p.scope(ctx, tx)
	if err := p.service.rejectIfCodeReviewOwned(ctx, org, session); err != nil {
		return err
	}
	return p.service.sessionStore.RejectIfAutomationOwned(ctx, org, session)
}

func (p *legacyHookPersistence) GetSessionInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) (models.Session, error) {
	p.scope(ctx, tx)
	return p.service.sessionStore.GetByID(ctx, org, session)
}

func (p *legacyHookPersistence) GetThreadInTx(ctx context.Context, tx pgx.Tx, org, thread uuid.UUID) (models.SessionThread, error) {
	p.scope(ctx, tx)
	row, err := p.service.threadStore.GetByID(ctx, org, thread)
	p.targetStatus = row.Status
	return row, err
}

func (p *legacyHookPersistence) CountActiveThreadsInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) (int, error) {
	p.scope(ctx, tx)
	if store, ok := p.service.threadStore.(*mockThreadStore); ok && store.activeCountFn != nil {
		return store.activeCountFn(ctx, org, session)
	}
	if p.idleLimited || p.resumeLimited {
		count := p.claimCap
		if p.targetStatus == models.ThreadStatusPending || p.targetStatus == models.ThreadStatusRunning || p.targetStatus == models.ThreadStatusAwaitingInput {
			count++
		}
		return count, nil
	}
	return 0, nil
}

func (p *legacyHookPersistence) GetInboxByClientMessageIDInTx(ctx context.Context, tx pgx.Tx, org, thread uuid.UUID, key string) (models.ThreadInboxEntry, error) {
	p.scope(ctx, tx)
	return p.service.inboxStore.GetByClientMessageID(ctx, org, thread, key)
}

func (p *legacyHookPersistence) GetMessageByIDInTx(ctx context.Context, tx pgx.Tx, org uuid.UUID, id int64) (models.SessionMessage, error) {
	p.scope(ctx, tx)
	return p.service.messageStore.GetByID(ctx, org, id)
}

func (p *legacyHookPersistence) CountPendingInboxByThreadInTx(ctx context.Context, tx pgx.Tx, org, thread uuid.UUID) (int, error) {
	p.scope(ctx, tx)
	return p.service.inboxStore.CountPendingByThread(ctx, org, thread)
}

func (p *legacyHookPersistence) CountPendingInboxBySessionInTx(ctx context.Context, tx pgx.Tx, org, session uuid.UUID) (int, error) {
	p.scope(ctx, tx)
	return p.service.inboxStore.CountPendingBySession(ctx, org, session)
}

func (p *legacyHookPersistence) GetActiveThreadRuntimeInTx(ctx context.Context, tx pgx.Tx, org, thread uuid.UUID) (models.ThreadRuntime, error) {
	p.scope(ctx, tx)
	return p.service.runtimeOwnerStore.GetActiveByThread(ctx, org, thread)
}

func (p *legacyHookPersistence) claim(ctx context.Context, tx pgx.Tx, org, session, lane uuid.UUID, max int, resume bool) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	p.scope(ctx, tx)
	p.claimCap = max
	p.record(fmt.Sprintf("claim-thread-%t", resume))
	var row models.SessionThread
	var err error
	if resume {
		row, err = p.service.threadStore.ClaimForResumeInSession(ctx, org, session, lane, max)
		p.resumeLimited = errors.Is(err, db.ErrThreadRunningLimitReached)
	} else {
		row, err = p.service.threadStore.ClaimIdleForSession(ctx, org, session, lane, max)
		p.idleLimited = errors.Is(err, db.ErrThreadRunningLimitReached)
	}
	if errors.Is(err, db.ErrThreadRunningLimitReached) {
		err = pgx.ErrNoRows
	}
	return row, db.ThreadClaimPersistenceEffects{}, err
}
func (p *legacyHookPersistence) ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, o, s, l uuid.UUID, m int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	return p.claim(ctx, tx, o, s, l, m, false)
}
func (p *legacyHookPersistence) ClaimResumeThreadInTx(ctx context.Context, tx pgx.Tx, o, s, l uuid.UUID, m int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	return p.claim(ctx, tx, o, s, l, m, true)
}
func (p *legacyHookPersistence) ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, o, s uuid.UUID) (models.Session, error) {
	p.scope(ctx, tx)
	p.record("claim-session-idle")
	return p.service.sessionStore.ClaimIdle(ctx, o, s)
}
func (p *legacyHookPersistence) ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, o, s uuid.UUID) (models.Session, error) {
	p.scope(ctx, tx)
	p.record("claim-session-resume")
	return p.service.sessionStore.ClaimForResume(ctx, o, s)
}
func (p *legacyHookPersistence) CreateMessageInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, msg *models.SessionMessage) error {
	p.scope(ctx, tx)
	p.record("message")
	if p.service.txStarter != nil {
		return newSQLSendPersistence(nil).CreateMessageInTx(ctx, tx, o, msg)
	}
	return p.service.createMessage(ctx, p.service.messageStore, msg)
}
func (p *legacyHookPersistence) AppendInboxInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	p.scope(ctx, tx)
	p.record("inbox")
	if p.service.txStarter != nil {
		return db.NewThreadInboxStore(tx).AppendForMessage(ctx, o, params)
	}
	return p.service.inboxStore.AppendForMessage(ctx, o, params)
}
func (p *legacyHookPersistence) IncrementPendingMessagesInTx(ctx context.Context, tx pgx.Tx, o, l uuid.UUID) (db.ThreadPendingMessagePersistenceEffects, error) {
	p.scope(ctx, tx)
	p.record("pending")
	if p.service.txStarter != nil && p.service.inboxStore != nil {
		return db.NewSessionThreadStore(tx).IncrementPendingMessagesInTx(ctx, o, tx, l)
	}
	return db.ThreadPendingMessagePersistenceEffects{}, p.service.threadStore.IncrementPendingMessages(ctx, o, l)
}
func (p *legacyHookPersistence) EnqueueInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	p.scope(ctx, tx)
	p.record("enqueue")
	p.enqueues = append(p.enqueues, opts)
	if p.sqlEnqueue {
		return newSQLSendPersistence(nil).EnqueueInTx(ctx, tx, o, opts)
	}
	return p.service.jobStore.EnqueueWithOpts(ctx, o, opts)
}

// Effects in hook tests intentionally retain the opaque zero tokens returned by
// the hook adapter. Real token delegation/publication is covered separately.
type legacyHookEffects struct{ runner *legacyGuardFixtureRunner }

func (e *legacyHookEffects) owned() {
	e.runner.t.Helper()
	require.True(e.runner.t, e.runner.settled, "effects must execute only after the whole atomic transaction owner confirms completion")
}
func (e *legacyHookEffects) PublishLifecycle(context.Context, uuid.UUID, []db.LifecycleEffect) {
	e.owned()
	e.runner.t.Fatal("legacy hook fixture cannot publish active effects")
}
func (e *legacyHookEffects) PublishThreadClaim(context.Context, uuid.UUID, db.ThreadClaimPersistenceEffects) {
	e.owned()
	e.runner.events = append(e.runner.events, "claim-effect")
}
func (e *legacyHookEffects) PublishThreadPending(context.Context, uuid.UUID, db.ThreadPendingMessagePersistenceEffects) {
	e.owned()
	e.runner.events = append(e.runner.events, "pending-effect")
}
func (e *legacyHookEffects) NotifyJob(_ context.Context, id uuid.UUID) {
	e.owned()
	e.runner.jobIDs = append(e.runner.jobIDs, id)
	e.runner.events = append(e.runner.events, "job-effect")
}

func installLegacyGuardFixture(t *testing.T, service *Service) {
	t.Helper()
	runner := &legacyGuardFixtureRunner{t: t, service: service}
	service.send = SendDependencies{Transactions: runner, Legacy: &legacyHookPersistence{service: service}, Effects: &legacyHookEffects{runner: runner}}
}
