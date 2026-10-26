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

// This CPU fixture executes the REAL resolver and scope guards against pinned
// SQL responses for a synthetic version-zero database. The lazy forwarding of
// grouped business SQL preserves older pgxmock assertions; it does NOT prove
// PostgreSQL same-connection locking, settlement or contention behavior.
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
	business           pgx.Tx
	normalizing        bool
}

func (f *legacyGuardFixtureTx) requireScope() {
	f.t.Helper()
	require.Equal(f.t, 7, f.stage, "every mutation must follow fresh policy, shared cutover, mode, parent and stable lane acquisition in its own unit")
}
func (f *legacyGuardFixtureTx) businessTx(ctx context.Context) pgx.Tx {
	f.requireScope()
	if f.business == nil {
		var err error
		f.business, err = f.service.txStarter.Begin(ctx)
		require.NoError(f.t, err, "fixture forwards the original grouped business transaction only after guard SQL")
	}
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
		return f.businessTx(ctx).QueryRow(ctx, query, args...)
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
		return f.businessTx(ctx).Query(ctx, query, args...)
	}
	f.guard.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnRows(rows)
	return f.guard.Query(ctx, query, args...)
}
func (f *legacyGuardFixtureTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return f.businessTx(ctx).Exec(ctx, query, args...)
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
}

func (r *legacyGuardFixtureRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.t.Helper()
	r.units++
	r.events = append(r.events, "begin")
	r.settled = false
	guard, err := pgxmock.NewPool()
	require.NoError(r.t, err, "create isolated per-unit guard SQL fixture")
	defer guard.Close()
	tx := &legacyGuardFixtureTx{t: r.t, guard: guard, service: r.service, normalizing: r.normalizingAt == r.units}
	unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	err = work(unitCtx, tx)
	require.Contains(r.t, []int{3, 7}, tx.stage, "a completed legacy fixture unit must execute entry observation or full mutation guards")
	require.NoError(r.t, guard.ExpectationsWereMet(), "every real guard query must consume its pinned response")
	if tx.business != nil {
		if err != nil {
			if rollbackErr := tx.business.Rollback(unitCtx); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
		} else {
			err = tx.business.Commit(unitCtx)
		}
	}
	if err != nil && r.contentionAt == r.units {
		r.settled = true
		r.events = append(r.events, "rollback")
		return &db.LifecycleContentionError{Attempts: 3, Cause: err}
	}
	if err == nil && r.failCommitAt == r.units {
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

type legacyHookPersistence struct {
	service                    *Service
	idleLimited, resumeLimited bool
}

func (p *legacyHookPersistence) record(operation string) {
	p.service.send.Transactions.(*legacyGuardFixtureRunner).events = append(p.service.send.Transactions.(*legacyGuardFixtureRunner).events, operation)
}

func (p *legacyHookPersistence) claim(ctx context.Context, tx pgx.Tx, org, session, lane uuid.UUID, max int, resume bool) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
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
func (p *legacyHookPersistence) IsAtIdleClaimLimit(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (bool, error) {
	runner := p.service.send.Transactions.(*legacyGuardFixtureRunner)
	require.True(runner.t, runner.settled, "claim-limit diagnostic must occur after known completion releases the original unit")
	p.record("idle-limit")
	return p.idleLimited, nil
}
func (p *legacyHookPersistence) IsAtResumeClaimLimit(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) (bool, error) {
	runner := p.service.send.Transactions.(*legacyGuardFixtureRunner)
	require.True(runner.t, runner.settled, "resume-limit diagnostic must occur after known completion")
	p.record("resume-limit")
	return p.resumeLimited, nil
}
func (p *legacyHookPersistence) ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, o, s uuid.UUID) (models.Session, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("claim-session-idle")
	return p.service.sessionStore.ClaimIdle(ctx, o, s)
}
func (p *legacyHookPersistence) ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, o, s uuid.UUID) (models.Session, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("claim-session-resume")
	return p.service.sessionStore.ClaimForResume(ctx, o, s)
}
func (p *legacyHookPersistence) UpdateThreadStatusInTx(ctx context.Context, tx pgx.Tx, o, l uuid.UUID, status models.ThreadStatus) (db.ThreadPersistenceEffects, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("thread-status")
	return db.ThreadPersistenceEffects{}, p.service.threadStore.UpdateStatus(ctx, o, l, status)
}
func (p *legacyHookPersistence) UpdateSessionStatusInTx(ctx context.Context, tx pgx.Tx, o, s uuid.UUID, status models.SessionStatus) (models.Session, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("session-status")
	return models.Session{}, p.service.sessionStore.UpdateStatus(ctx, o, s, status)
}
func (p *legacyHookPersistence) CreateMessageInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, msg *models.SessionMessage) error {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("message")
	if p.service.txStarter != nil {
		return newSQLSendPersistence(nil).CreateMessageInTx(ctx, tx, o, msg)
	}
	return p.service.createMessage(ctx, p.service.messageStore, msg)
}
func (p *legacyHookPersistence) AppendInboxInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("inbox")
	if p.service.txStarter != nil {
		return db.NewThreadInboxStore(tx).AppendForMessage(ctx, o, params)
	}
	return p.service.inboxStore.AppendForMessage(ctx, o, params)
}
func (p *legacyHookPersistence) IncrementPendingMessagesInTx(ctx context.Context, tx pgx.Tx, o, l uuid.UUID) (db.ThreadPendingMessagePersistenceEffects, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("pending")
	if tx.(*legacyGuardFixtureTx).business != nil {
		return db.NewSessionThreadStore(tx).IncrementPendingMessagesInTx(ctx, o, tx, l)
	}
	return db.ThreadPendingMessagePersistenceEffects{}, p.service.threadStore.IncrementPendingMessages(ctx, o, l)
}
func (p *legacyHookPersistence) EnqueueInTx(ctx context.Context, tx pgx.Tx, o uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	tx.(*legacyGuardFixtureTx).requireScope()
	p.record("enqueue")
	return p.service.jobStore.EnqueueWithOpts(ctx, o, opts)
}

// Effects in hook tests intentionally retain the opaque zero tokens returned by
// the hook adapter. Real token delegation/publication is covered separately.
type legacyHookEffects struct{ runner *legacyGuardFixtureRunner }

func (e *legacyHookEffects) owned() {
	e.runner.t.Helper()
	require.True(e.runner.t, e.runner.settled, "effects must execute only after their individual transaction owner confirms completion")
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
func (e *legacyHookEffects) PublishThreadStatus(context.Context, uuid.UUID, db.ThreadPersistenceEffects) {
	e.owned()
	e.runner.events = append(e.runner.events, "thread-effect")
}
func (e *legacyHookEffects) PublishSessionStatus(context.Context, uuid.UUID, models.Session) {
	e.owned()
	e.runner.events = append(e.runner.events, "session-effect")
}
func (e *legacyHookEffects) NotifyJob(context.Context, uuid.UUID) {
	e.owned()
	e.runner.events = append(e.runner.events, "job-effect")
}

func installLegacyGuardFixture(t *testing.T, service *Service) {
	t.Helper()
	runner := &legacyGuardFixtureRunner{t: t, service: service}
	service.send = SendDependencies{Transactions: runner, Legacy: &legacyHookPersistence{service: service}, Effects: &legacyHookEffects{runner: runner}}
}
