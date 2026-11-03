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
	"github.com/rs/zerolog"
)

// SendTransactions owns one bounded, top-level DB-only unit. A callback may run
// more than once; its owner must discard aborted results and publish only after
// Run succeeds. Production construction accepts only an owned pgx pool.
type SendTransactions interface {
	Run(context.Context, func(context.Context, pgx.Tx) error) error
}

// SendChangesetReader is a scoped read-only compatibility dependency. It cannot
// acquire a changeset lease or attest runtime availability for active admission.
type SendChangesetReader interface {
	GetByID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionChangeset, error)
}

// LegacySendPersistence composes legacy send reads and writes in the owner's
// supplied transaction. The owner first acquires fresh mode, parent and ordered
// lane guards for the entire send. No method owns transaction settlement,
// retries, settings or effects. Atomic send classification uses the explicit
// supplied-transaction row and active-count readers.
type LegacySendPersistence interface {
	RejectIfOwnedInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error
	GetSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error)
	GetThreadInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (models.SessionThread, error)
	CountActiveThreadsInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (int, error)
	GetInboxByClientMessageIDInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error)
	GetMessageByIDInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, id int64) (models.SessionMessage, error)
	CountPendingInboxByThreadInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (int, error)
	CountPendingInboxBySessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (int, error)
	GetActiveThreadRuntimeInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (models.ThreadRuntime, error)
	ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error)
	ClaimResumeThreadInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error)
	ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error)
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
	PublishLifecycle(context.Context, uuid.UUID, []db.LifecycleEffect)
	PublishThreadClaim(context.Context, uuid.UUID, db.ThreadClaimPersistenceEffects)
	PublishThreadPending(context.Context, uuid.UUID, db.ThreadPendingMessagePersistenceEffects)
	NotifyJob(context.Context, uuid.UUID)
}

// SendDependencies is mandatory send plumbing, independent of the legacy
// optional message/inbox transaction configuration. Its presence selects no
// lifecycle version and grants no mutation authority.
type SendDependencies struct {
	Transactions SendTransactions
	Legacy       LegacySendPersistence
	Effects      SendEffects
	// Optional for unchanged direct callers; requested legacy HTTP policy
	// validates this dependency only after the sole router selects legacy.
	Changesets SendChangesetReader
}

// Validate rejects missing mandatory dependencies, including typed nils, before
// SQL. Optional legacy compatibility reads are checked at their chosen branch.
// The service constructor must call it; validation is not an admission check.
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
	if validator, ok := d.Transactions.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("thread send: invalid transactions: %w", err)
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
		Changesets:   db.NewSessionChangesetStore(pool),
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

func (p *sqlLegacySendPersistence) RejectIfOwnedInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return err
	}
	store := db.NewSessionStore(tx)
	if err := store.RejectIfCodeReviewOwned(ctx, orgID, sessionID); err != nil {
		return err
	}
	return store.RejectIfAutomationOwned(ctx, orgID, sessionID)
}

func (p *sqlLegacySendPersistence) GetSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return models.Session{}, err
	}
	return p.sessions.GetByIDInTx(ctx, tx, orgID, sessionID)
}

func (p *sqlLegacySendPersistence) GetThreadInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return models.SessionThread{}, err
	}
	return db.NewSessionThreadStore(tx).GetByID(ctx, orgID, threadID)
}

func (p *sqlLegacySendPersistence) CountActiveThreadsInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (int, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return 0, err
	}
	return db.NewSessionThreadStore(tx).CountActive(ctx, orgID, sessionID)
}

func (p *sqlLegacySendPersistence) GetInboxByClientMessageIDInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return models.ThreadInboxEntry{}, err
	}
	return db.NewThreadInboxStore(tx).GetByClientMessageID(ctx, orgID, threadID, clientMessageID)
}

func (p *sqlLegacySendPersistence) GetMessageByIDInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, id int64) (models.SessionMessage, error) {
	if err := p.validateTx(ctx, tx, orgID); err != nil {
		return models.SessionMessage{}, err
	}
	return db.NewSessionMessageStore(tx).GetByID(ctx, orgID, id)
}

func (p *sqlLegacySendPersistence) CountPendingInboxByThreadInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (int, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return 0, err
	}
	return db.NewThreadInboxStore(tx).CountPendingByThread(ctx, orgID, threadID)
}

func (p *sqlLegacySendPersistence) CountPendingInboxBySessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (int, error) {
	if err := p.validateTx(ctx, tx, orgID, sessionID); err != nil {
		return 0, err
	}
	return db.NewThreadInboxStore(tx).CountPendingBySession(ctx, orgID, sessionID)
}

func (p *sqlLegacySendPersistence) GetActiveThreadRuntimeInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID) (models.ThreadRuntime, error) {
	if err := p.validateTx(ctx, tx, orgID, threadID); err != nil {
		return models.ThreadRuntime{}, err
	}
	return db.NewThreadRuntimeStore(tx).GetActiveByThread(ctx, orgID, threadID)
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

func (e *sqlSendEffects) PublishLifecycle(ctx context.Context, orgID uuid.UUID, effects []db.LifecycleEffect) {
	for _, effect := range effects {
		if err := effect.Validate(); err != nil || effect.OrgID() != orgID {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("discarded invalid postcommit lifecycle effect")
			continue
		}
		switch effect.Kind() {
		case db.LifecycleEffectWakeJobs:
			// The owner notifies the exact committed receipt's job separately.
		case db.LifecycleEffectSessionChanged:
			row, err := e.sessions.GetByID(ctx, orgID, effect.SessionID())
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("could not reload committed lifecycle session")
				continue
			}
			e.sessions.PublishCommittedSessionStatus(ctx, orgID, row)
		case db.LifecycleEffectThreadChanged, db.LifecycleEffectInboxChanged:
			if err := e.threads.PublishCommittedLifecycleThreadEffect(ctx, orgID, effect); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("could not publish committed lifecycle lane effect")
			}
		}
	}
}

func (e *sqlSendEffects) PublishThreadClaim(ctx context.Context, orgID uuid.UUID, effects db.ThreadClaimPersistenceEffects) {
	e.threads.PublishCommittedThreadClaimEffects(ctx, orgID, effects)
}

func (e *sqlSendEffects) PublishThreadPending(ctx context.Context, orgID uuid.UUID, effects db.ThreadPendingMessagePersistenceEffects) {
	e.threads.PublishCommittedThreadPendingMessageEffects(ctx, orgID, effects)
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
