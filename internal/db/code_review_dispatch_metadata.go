package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCodeReviewMetadataTerminalCapability = errors.New("code review terminal transaction capability is required")
var ErrCodeReviewMetadataTerminalIsolation = errors.New("code review terminalization requires READ COMMITTED")
var ErrCodeReviewMetadataTerminalStorage = errors.New("code review terminalization storage failed")

type codeReviewMetadataTerminalError struct{ cause error }

func (*codeReviewMetadataTerminalError) Error() string {
	return ErrCodeReviewMetadataTerminalStorage.Error()
}
func (e *codeReviewMetadataTerminalError) Unwrap() error { return e.cause }
func (*codeReviewMetadataTerminalError) Is(target error) bool {
	return target == ErrCodeReviewMetadataTerminalStorage
}

// CodeReviewMetadataTransactions is explicitly injected at pool-owned
// composition. The existing ThreadSendTransactions implements this capability;
// no new runner, retry policy, connection acquisition or budget lives here.
// Run must own the complete top-level bounded READ COMMITTED unit and return
// nil only after known commit. Its callback is DB-only and may be replayed.
type CodeReviewMetadataTransactions interface {
	Run(context.Context, func(context.Context, pgx.Tx) error) error
}

// NewCodeReviewStoreWithTerminalTransactions selects pool-owned terminal
// wrappers explicitly. It never derives a runner from DBTX or probes for old
// no-intent data. A caller must supply the existing concrete owned-pool Run
// capability at composition; a nil/typed-nil dependency is a refusal.
func NewCodeReviewStoreWithTerminalTransactions(database DBTX, transactions CodeReviewMetadataTransactions) (*CodeReviewStore, error) {
	if nilLifecycleDependency(database) || nilLifecycleDependency(transactions) {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	store := NewCodeReviewStore(database)
	store.terminalTransactions = transactions
	return store, nil
}

// NewCodeReviewStoreInTx selects supplied-tx terminal composition explicitly.
// Terminal methods never begin, commit, retry, configure, borrow a checkout or
// publish, even if a stream is later attached. The outer owner must establish
// the whole-unit bounded READ COMMITTED transaction and carry effects past its
// own known commit. The isolation read below does not prove budget ownership.
func NewCodeReviewStoreInTx(tx pgx.Tx) (*CodeReviewStore, error) {
	return newCodeReviewMetadataStoreInTx(tx)
}

// Both explicit owners use the same terminal primitives without inferring
// authority from DBTX. The named constructors retain their distinct contracts.
func newCodeReviewMetadataStoreInTx(tx pgx.Tx) (*CodeReviewStore, error) {
	if nilLifecycleDependency(tx) {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	store := NewCodeReviewStore(tx)
	store.terminalInTx = true
	store.terminalTx = tx
	return store, nil
}

// PublishCodeReviewTerminalOutcome emits only descriptors released by the
// pool retirement wrapper's known successful commit. It has no SQL, runner,
// acquisition or transaction operation. Provisional supplied-tx outcomes and
// job-only retirements cannot authorize a target event. The worker independently
// checks exact job/lease/type/owner correlation and owns at-most-once invocation.
func (s *CodeReviewStore) PublishCodeReviewTerminalOutcome(ctx context.Context, orgID uuid.UUID, outcome CodeReviewTerminalOutcome) {
	if s == nil || ctx == nil || orgID == uuid.Nil || outcome.scope.OrgID != orgID || !outcome.committed || !outcome.owned || !outcome.hasOwner {
		return
	}
	if metadata, ok := outcome.Metadata(); ok {
		s.publishUpdated(ctx, metadata)
	}
	if assessment, ok := outcome.Assessment(); ok {
		s.PublishAssessmentUpdated(ctx, assessment)
	}
}

type codeReviewMetadataTerminalWork func(context.Context, pgx.Tx) ([]models.CodeReviewSessionMetadata, error)

func (s *CodeReviewStore) terminalMetadata(ctx context.Context, orgID uuid.UUID, batch bool, work codeReviewMetadataTerminalWork) ([]models.CodeReviewSessionMetadata, error) {
	if s == nil || ctx == nil || work == nil {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	if s.terminalInTx {
		if nilLifecycleDependency(s.terminalTx) || !nilLifecycleDependency(s.terminalTransactions) {
			return nil, ErrCodeReviewMetadataTerminalCapability
		}
		// Explicit tx-bound mode has no postcommit effects of its own.
		result, err := work(ctx, s.terminalTx)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	if nilLifecycleDependency(s.terminalTransactions) || !nilLifecycleDependency(s.terminalTx) {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	var staged []models.CodeReviewSessionMetadata
	called := false
	err := s.terminalTransactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		// Every replay discards prior provisional rows and effect descriptors.
		staged = nil
		called = true
		changed, err := work(unitCtx, tx)
		if err != nil {
			return err
		}
		staged = changed
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !called {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	// Run's known nil settlement is the only pool-wrapper effect boundary.
	if len(staged) > 0 {
		if batch {
			s.publishUpdated(ctx, models.CodeReviewSessionMetadata{OrgID: orgID, Status: models.CodeReviewSessionStatusStale})
		} else {
			s.publishUpdated(ctx, staged[0])
		}
	}
	return staged, nil
}

func (s *CodeReviewStore) terminalMetadataOne(ctx context.Context, orgID uuid.UUID, work func(context.Context, pgx.Tx) (models.CodeReviewSessionMetadata, error)) (models.CodeReviewSessionMetadata, error) {
	metadata, err := s.terminalMetadata(ctx, orgID, false, func(unitCtx context.Context, tx pgx.Tx) ([]models.CodeReviewSessionMetadata, error) {
		changed, err := work(unitCtx, tx)
		if err != nil {
			return nil, err
		}
		return []models.CodeReviewSessionMetadata{changed}, nil
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	if len(metadata) != 1 {
		return models.CodeReviewSessionMetadata{}, ErrCodeReviewMetadataTerminalStorage
	}
	return metadata[0], nil
}

// This check only observes isolation. It installs no settings and establishes
// neither acquisition age nor the outer owner's total transaction budget.
func requireCodeReviewMetadataTerminalTx(ctx context.Context, tx pgx.Tx) error {
	if ctx == nil || nilLifecycleDependency(tx) {
		return ErrCodeReviewMetadataTerminalCapability
	}
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if isolation != string(pgx.ReadCommitted) {
		return ErrCodeReviewMetadataTerminalIsolation
	}
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	return nil
}

const codeReviewMetadataTerminalSessionLocks = `SELECT id FROM code_review_session_metadata
 WHERE org_id=@org_id AND session_id=@session_id
 AND (NOT @active_only OR status IN ('queued','running')) ORDER BY id FOR UPDATE`

const codeReviewMetadataTerminalBatchLocks = `SELECT id FROM code_review_session_metadata
 WHERE org_id=@org_id AND pull_request_id=@pull_request_id AND head_sha<>@current_head_sha
 AND status IN ('queued','running') ORDER BY id FOR UPDATE`

// Only the two static queries above enter here. IDs are needed by the following
// UPDATE to keep its lock membership exactly the ordered prelocked set. Rows
// are streamed and closed, not retained as unused parent projections. There is
// no new pagination/cap/refusal policy. Canonical owners use their existing Run
// bound; the named legacy PR exception retains its explicitly separate policy.
func lockCodeReviewMetadataTerminal(ctx context.Context, tx pgx.Tx, query string, args pgx.NamedArgs) ([]uuid.UUID, error) {
	if err := requireCodeReviewMetadataTerminalTx(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, query, args)
	if err != nil {
		return nil, &codeReviewMetadataTerminalError{cause: err}
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, &codeReviewMetadataTerminalError{cause: err}
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, &codeReviewMetadataTerminalError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, &codeReviewMetadataTerminalError{cause: err}
	}
	return ids, nil
}

// This is a LATER statement after the metadata UPDATE/RETURNING is completely
// consumed. Its READ COMMITTED snapshot sees children committed while the
// earlier parent lock waited. The child-only CTE orders child row locks; it is
// not a claim that dependent parent/child CTEs refresh a statement snapshot.
const codeReviewMetadataCancelPending = `WITH pending AS MATERIALIZED (
 SELECT r.id FROM code_review_agent_results r
 WHERE r.org_id=$1 AND r.role='reviewer' AND r.dispatch_version=1 AND r.dispatch_state='pending'
 AND EXISTS (
 SELECT 1 FROM unnest($2::uuid[],$3::uuid[]) AS changed(metadata_id,session_id)
 JOIN code_review_session_metadata m ON m.org_id=$1 AND m.id=changed.metadata_id AND m.session_id=changed.session_id
 WHERE r.session_id=changed.session_id
 ) ORDER BY r.id FOR UPDATE OF r
)
UPDATE code_review_agent_results r
 SET dispatch_state='cancelled',dispatch_resolution='owner_cancelled',dispatch_due_at=NULL,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 FROM pending p WHERE r.org_id=$1 AND r.id=p.id AND r.role='reviewer'
 AND r.dispatch_version=1 AND r.dispatch_state='pending'`

func cancelCodeReviewMetadataPendingInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, changed []models.CodeReviewSessionMetadata) error {
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if len(changed) == 0 {
		return nil
	}
	metadataIDs, sessionIDs := make([]uuid.UUID, len(changed)), make([]uuid.UUID, len(changed))
	for i, metadata := range changed {
		if metadata.OrgID != orgID {
			return ErrCodeReviewMetadataTerminalStorage
		}
		metadataIDs[i], sessionIDs[i] = metadata.ID, metadata.SessionID
	}
	if _, err := tx.Exec(ctx, codeReviewMetadataCancelPending, orgID, metadataIDs, sessionIDs); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	return nil
}

// Complete and close every RETURNING row before taking a fresh child snapshot.
// Existing single-owner methods still return their first row (or ErrNoRows),
// while all actually changed metadata IDs scope the one set-based child write.
func collectCodeReviewTerminalMetadata(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, rows pgx.Rows) ([]models.CodeReviewSessionMetadata, error) {
	changed, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewSessionMetadata])
	if err != nil {
		return nil, err
	}
	if err := cancelCodeReviewMetadataPendingInTx(ctx, orgID, tx, changed); err != nil {
		return nil, err
	}
	return changed, nil
}

func firstCodeReviewTerminalMetadata(changed []models.CodeReviewSessionMetadata, err error) (models.CodeReviewSessionMetadata, error) {
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	if len(changed) == 0 {
		return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
	}
	return changed[0], nil
}
