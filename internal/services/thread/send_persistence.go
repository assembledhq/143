package thread

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SendTransactions owns one bounded, top-level DB-only unit. A callback may run
// more than once; its owner must discard aborted results and publish only after
// Run succeeds. Production construction accepts only an owned pgx pool.
type SendTransactions interface {
	Run(context.Context, func(context.Context, pgx.Tx) error) error
}

// LegacySendPersistence supplies the original send mutations, not lifecycle
// authority. The service must acquire fresh mode and canonical scope guards in
// the supplied transaction before each mutation. No method owns transaction
// settlement, retries, settings or effects. Claim-limit diagnostics use the
// pool-backed reader only after the claim unit has completed successfully.
type LegacySendPersistence interface {
	ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error)
	ClaimResumeThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error)
	IsAtIdleClaimLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (bool, error)
	IsAtResumeClaimLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (bool, error)
	ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateThreadStatusInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID, status models.ThreadStatus) (db.ThreadPersistenceEffects, error)
	UpdateSessionStatusInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, status models.SessionStatus) (models.Session, error)
	IncrementPendingMessagesInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (db.ThreadPendingMessagePersistenceEffects, error)
	CreateMessageInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, message *models.SessionMessage) error
	AppendInboxInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error)
	EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
}

// SendEffects is explicitly postcommit. Tokens are opaque notification data,
// never proof of commit or admission. The owner must not call this interface
// after an aborted or uncertain unit. Existing publishers retain their own
// best-effort logging and snapshot-versus-current-row behavior.
type SendEffects interface {
	PublishThreadClaim(context.Context, uuid.UUID, db.ThreadClaimPersistenceEffects)
	PublishThreadPending(context.Context, uuid.UUID, db.ThreadPendingMessagePersistenceEffects)
	PublishThreadStatus(context.Context, uuid.UUID, db.ThreadPersistenceEffects)
	PublishSessionStatus(context.Context, uuid.UUID, models.Session)
	NotifyJob(context.Context, uuid.UUID)
}

// SendDependencies is mandatory send plumbing, independent of the legacy
// optional message/inbox transaction configuration. Its presence selects no
// lifecycle version and grants no mutation authority.
type SendDependencies struct {
	Transactions SendTransactions
	Legacy       LegacySendPersistence
	Effects      SendEffects
}

// Validate rejects missing dependencies, including typed nils, before SQL. The
// service constructor must call it; validation is not an admission check.
func (d SendDependencies) Validate() error {
	for _, dependency := range []struct {
		name  string
		value any
	}{
		{"transactions", d.Transactions}, {"legacy persistence", d.Legacy}, {"effects", d.Effects},
	} {
		if nilSendDependency(dependency.value) {
			return fmt.Errorf("thread send: %s is required", dependency.name)
		}
	}
	return nil
}

// NewSendDependencies wires only concrete production stores and an owned pool.
// It never acquires a connection or installs a legacy/active fallback.
func NewSendDependencies(pool *pgxpool.Pool, sessions *db.SessionStore, threads *db.SessionThreadStore, jobs *db.JobStore) (SendDependencies, error) {
	transactions, err := db.NewThreadSendTransactions(pool)
	if err != nil {
		return SendDependencies{}, err
	}
	if sessions == nil || threads == nil || jobs == nil {
		return SendDependencies{}, errors.New("thread send: session, thread and job stores are required")
	}
	return SendDependencies{
		Transactions: transactions,
		Legacy:       &sqlLegacySendPersistence{sessions: sessions, threads: threads, jobs: jobs},
		Effects:      &sqlSendEffects{sessions: sessions, threads: threads, jobs: jobs},
	}, nil
}

type sqlLegacySendPersistence struct {
	sessions *db.SessionStore
	threads  *db.SessionThreadStore
	jobs     *db.JobStore
}

var _ LegacySendPersistence = (*sqlLegacySendPersistence)(nil)

func (p *sqlLegacySendPersistence) validate(ctx context.Context, orgID uuid.UUID, ids ...uuid.UUID) error {
	if p == nil || p.sessions == nil || p.threads == nil || p.jobs == nil {
		return errors.New("thread send: concrete persistence dependencies are required")
	}
	if ctx == nil {
		return errors.New("thread send: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if orgID == uuid.Nil {
		return errors.New("thread send: organization is required")
	}
	for _, id := range ids {
		if id == uuid.Nil {
			return errors.New("thread send: explicit nonzero scope IDs are required")
		}
	}
	return nil
}

func (p *sqlLegacySendPersistence) validateTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, ids ...uuid.UUID) error {
	if err := p.validate(ctx, orgID, ids...); err != nil {
		return err
	}
	if nilSendDependency(tx) {
		return errors.New("thread send: supplied transaction is required")
	}
	return nil
}

func (p *sqlLegacySendPersistence) ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID, threadID); err != nil {
		return models.SessionThread{}, db.ThreadClaimPersistenceEffects{}, err
	}
	return p.threads.ClaimIdleForSessionInTx(ctx, orgID, tx, sessionID, threadID, maxRunning)
}

func (p *sqlLegacySendPersistence) ClaimResumeThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID, threadID); err != nil {
		return models.SessionThread{}, db.ThreadClaimPersistenceEffects{}, err
	}
	return p.threads.ClaimForResumeInSessionInTx(ctx, orgID, tx, sessionID, threadID, maxRunning)
}

func (p *sqlLegacySendPersistence) IsAtIdleClaimLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (bool, error) {
	if err := p.validate(ctx, orgID, sessionID, threadID); err != nil {
		return false, err
	}
	return p.threads.IsAtRunningLimitForIdleClaim(ctx, orgID, sessionID, threadID, maxRunning)
}

func (p *sqlLegacySendPersistence) IsAtResumeClaimLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (bool, error) {
	if err := p.validate(ctx, orgID, sessionID, threadID); err != nil {
		return false, err
	}
	return p.threads.IsAtRunningLimitForResumeClaim(ctx, orgID, sessionID, threadID, maxRunning)
}

func (p *sqlLegacySendPersistence) ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return models.Session{}, err
	}
	return p.sessions.ClaimIdleInTx(ctx, orgID, tx, sessionID)
}

func (p *sqlLegacySendPersistence) ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return models.Session{}, err
	}
	return p.sessions.ClaimForResumeInTx(ctx, orgID, tx, sessionID)
}

func (p *sqlLegacySendPersistence) UpdateThreadStatusInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID, status models.ThreadStatus) (db.ThreadPersistenceEffects, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return db.ThreadPersistenceEffects{}, err
	}
	return p.threads.UpdateStatusInTx(ctx, orgID, tx, threadID, status)
}

func (p *sqlLegacySendPersistence) UpdateSessionStatusInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, status models.SessionStatus) (models.Session, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return models.Session{}, err
	}
	return p.sessions.UpdateStatusInTx(ctx, orgID, tx, sessionID, status)
}

func (p *sqlLegacySendPersistence) IncrementPendingMessagesInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (db.ThreadPendingMessagePersistenceEffects, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return db.ThreadPendingMessagePersistenceEffects{}, err
	}
	return p.threads.IncrementPendingMessagesInTx(ctx, orgID, tx, threadID)
}

func (p *sqlLegacySendPersistence) CreateMessageInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, message *models.SessionMessage) error {
	if err := p.validateTx(ctx, tx, orgID); err != nil {
		return err
	}
	if message == nil || message.OrgID != orgID || message.SessionID == uuid.Nil || (message.ThreadID != nil && *message.ThreadID == uuid.Nil) {
		return errors.New("thread send: message must have the exact organization and nonzero scope")
	}
	store := db.NewSessionMessageStore(tx)
	if message.Source != "" {
		return store.CreateWithSource(ctx, message)
	}
	// The legacy store fills only the inserted ID and creation time on the
	// caller's message. Preserve that existing return-value convention.
	return store.Create(ctx, message)
}

func (p *sqlLegacySendPersistence) AppendInboxInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	if err := p.validateTx(ctx, tx, orgID, params.SessionID, params.ThreadID); err != nil {
		return models.ThreadInboxEntry{}, err
	}
	return db.NewThreadInboxStore(tx).AppendForMessage(ctx, orgID, params)
}

func (p *sqlLegacySendPersistence) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	if err := p.validateTx(ctx, tx, orgID); err != nil {
		return uuid.Nil, err
	}
	return p.jobs.EnqueueInTxWithOpts(ctx, tx, orgID, opts)
}

type sqlSendEffects struct {
	sessions *db.SessionStore
	threads  *db.SessionThreadStore
	jobs     *db.JobStore
}

var _ SendEffects = (*sqlSendEffects)(nil)

func (e *sqlSendEffects) PublishThreadClaim(ctx context.Context, orgID uuid.UUID, effects db.ThreadClaimPersistenceEffects) {
	e.threads.PublishCommittedThreadClaimEffects(ctx, orgID, effects)
}

func (e *sqlSendEffects) PublishThreadPending(ctx context.Context, orgID uuid.UUID, effects db.ThreadPendingMessagePersistenceEffects) {
	e.threads.PublishCommittedThreadPendingMessageEffects(ctx, orgID, effects)
}

func (e *sqlSendEffects) PublishThreadStatus(ctx context.Context, orgID uuid.UUID, effects db.ThreadPersistenceEffects) {
	e.threads.PublishCommittedThreadPersistenceEffects(ctx, orgID, effects)
}

func (e *sqlSendEffects) PublishSessionStatus(ctx context.Context, orgID uuid.UUID, session models.Session) {
	e.sessions.PublishCommittedSessionStatus(ctx, orgID, session)
}

func (e *sqlSendEffects) NotifyJob(ctx context.Context, id uuid.UUID) {
	e.jobs.Notify(ctx, id)
}

func nilSendDependency(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
