package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CallerSendStampPurpose identifies one closed caller-owned command namespace.
// A stamp correlates immutable input with a message; it grants no execution,
// admission, settlement, retry, or negative-observation authority.
type CallerSendStampPurpose string

const (
	CallerSendStampCodeReviewDispatch CallerSendStampPurpose = "code-review-reviewer-dispatch"
	CallerSendStampReviewLoopStartup  CallerSendStampPurpose = "review-loop-initial-send"
	CallerSendStampVersion            int16                  = 1
)

var (
	ErrCallerSendStampInvalid  = errors.New("invalid caller send stamp input")
	ErrCallerSendStampMismatch = errors.New("caller send stamp does not match retained command")
	ErrCallerSendStampStorage  = errors.New("caller send stamp storage failed")
)

// callerSendStampError deliberately does not render its database cause: query
// errors may contain private keys or values. Unwrap retains the cause for
// errors.Is/errors.As and the transaction owner's outcome classification.
type callerSendStampError struct{ cause error }

func (*callerSendStampError) Error() string        { return ErrCallerSendStampStorage.Error() }
func (e *callerSendStampError) Unwrap() error      { return e.cause }
func (*callerSendStampError) Is(target error) bool { return target == ErrCallerSendStampStorage }

func (p CallerSendStampPurpose) Validate() error {
	switch p {
	case CallerSendStampCodeReviewDispatch, CallerSendStampReviewLoopStartup:
		return nil
	default:
		return ErrCallerSendStampInvalid
	}
}

// CallerSendStampKey is deterministic; callers supply the original owner ID.
// The descriptive stored purpose is intentionally not the key's short prefix.
func CallerSendStampKey(purpose CallerSendStampPurpose, ownerID uuid.UUID) (string, error) {
	if purpose.Validate() != nil || ownerID == uuid.Nil {
		return "", ErrCallerSendStampInvalid
	}
	prefix := "cr-dispatch:v1:"
	if purpose == CallerSendStampReviewLoopStartup {
		prefix = "rl-startup:v1:"
	}
	return prefix + ownerID.String(), nil
}

// CallerSendStampScope carries independently retained caller and lane identity.
// No field is discovered from a current or primary lane.
type CallerSendStampScope struct {
	SessionID       uuid.UUID
	ThreadID        uuid.UUID
	ClientMessageID string
	Purpose         CallerSendStampPurpose
	OwnerID         uuid.UUID
}

func (s CallerSendStampScope) Validate() error {
	if s.SessionID == uuid.Nil || s.ThreadID == uuid.Nil {
		return ErrCallerSendStampInvalid
	}
	key, err := CallerSendStampKey(s.Purpose, s.OwnerID)
	if err != nil || s.ClientMessageID != key {
		return ErrCallerSendStampInvalid
	}
	return nil
}

// CallerSendStampInput has no caller-selected timestamp or alternate digest
// encoding. Digest uses the lifecycle contract's canonical lowercase text and
// nonzero identity rule, including rejection of an all-zero digest.
type CallerSendStampInput struct {
	Scope     CallerSendStampScope
	Version   int16
	Digest    string
	MessageID int64
}

func (in CallerSendStampInput) Validate() error {
	if in.Scope.Validate() != nil || in.Version != CallerSendStampVersion || in.MessageID <= 0 {
		return ErrCallerSendStampInvalid
	}
	if _, err := ParseLifecycleDigest(in.Digest); err != nil {
		return ErrCallerSendStampInvalid
	}
	return nil
}

// CallerSendStamp remains internal database correlation, not a public model,
// inbox payload, or acceptance receipt. CreatedAt is supplied by PostgreSQL.
type CallerSendStamp struct {
	OrgID uuid.UUID
	CallerSendStampInput
	CreatedAt time.Time
}

const callerSendStampColumns = `org_id, session_id, thread_id, client_message_id,
 purpose, owner_id, version, digest, message_id, created_at`

// The inbox owns the client key; partitioned session_messages has no such
// column or globally unique message-ID FK. Both typed rows must agree on all
// scope and message columns. No JSON payload is treated as linkage proof.
const callerSendStampInsertQuery = `INSERT INTO caller_send_command_stamps (
 org_id, session_id, thread_id, client_message_id, purpose, owner_id,
 version, digest, message_id, created_at
)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, statement_timestamp()
FROM session_messages m
JOIN thread_inbox_entries i
 ON i.org_id = m.org_id AND i.session_id = m.session_id
 AND i.thread_id = m.thread_id AND i.message_id = m.id
WHERE m.org_id = $1 AND m.session_id = $2 AND m.thread_id = $3 AND m.id = $9
 AND i.org_id = $1 AND i.session_id = $2 AND i.thread_id = $3
 AND i.client_message_id = $4 AND i.message_id = $9
ON CONFLICT DO NOTHING
RETURNING ` + callerSendStampColumns

const callerSendStampReadQuery = `SELECT ` + callerSendStampColumns + `
FROM caller_send_command_stamps
WHERE org_id = $1 AND session_id = $2 AND thread_id = $3
 AND client_message_id = $4 AND purpose = $5 AND owner_id = $6`

// A conflict read must prove that the requested typed linkage still exists.
// An old matching stamp cannot mask an absent/different message or inbox row.
const callerSendStampConflictReadQuery = callerSendStampReadQuery + `
 AND EXISTS (
  SELECT 1 FROM session_messages m
  JOIN thread_inbox_entries i
   ON i.org_id = m.org_id AND i.session_id = m.session_id
   AND i.thread_id = m.thread_id AND i.message_id = m.id
  WHERE m.org_id = $1 AND m.session_id = $2 AND m.thread_id = $3 AND m.id = $7
   AND i.org_id = $1 AND i.session_id = $2 AND i.thread_id = $3
   AND i.client_message_id = $4 AND i.message_id = $7
 )`

// RecordCallerSendStampInTx writes only immutable correlation in the supplied
// transaction, after its owner has guarded and written the legacy send. The
// message and inbox entry must both have been created in this same transaction.
// An existing-message replay must only read and exactly compare its original
// stamp; calling Record there would launder unstamped historical linkage. It
// neither acquires the canonical scope/owner locks nor begins, settles, retries,
// configures, or authorizes a transaction. Any error requires the transaction
// owner to roll back its whole unit; this helper cannot establish abort proof.
func RecordCallerSendStampInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, input CallerSendStampInput) (CallerSendStamp, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || input.Validate() != nil {
		return CallerSendStamp{}, ErrCallerSendStampInvalid
	}
	if err := ctx.Err(); err != nil {
		return CallerSendStamp{}, &callerSendStampError{cause: err}
	}
	scope := input.Scope
	stamp, err := scanCallerSendStamp(tx.QueryRow(ctx, callerSendStampInsertQuery,
		orgID, scope.SessionID, scope.ThreadID, scope.ClientMessageID,
		scope.Purpose, scope.OwnerID, input.Version, input.Digest, input.MessageID))
	if errors.Is(err, pgx.ErrNoRows) {
		if contextErr := ctx.Err(); contextErr != nil {
			return CallerSendStamp{}, &callerSendStampError{cause: contextErr}
		}
		// This also handles an owner-identity uniqueness conflict: if the
		// original scope/key is different, no exact row is returned.
		stamp, err = scanCallerSendStamp(tx.QueryRow(ctx, callerSendStampConflictReadQuery,
			orgID, scope.SessionID, scope.ThreadID, scope.ClientMessageID,
			scope.Purpose, scope.OwnerID, input.MessageID))
		if errors.Is(err, pgx.ErrNoRows) {
			if contextErr := ctx.Err(); contextErr != nil {
				return CallerSendStamp{}, &callerSendStampError{cause: contextErr}
			}
			return CallerSendStamp{}, ErrCallerSendStampMismatch
		}
	}
	if err != nil {
		return CallerSendStamp{}, &callerSendStampError{cause: err}
	}
	if !validCallerSendStamp(stamp) || stamp.OrgID != orgID || stamp.CallerSendStampInput != input {
		return CallerSendStamp{}, ErrCallerSendStampMismatch
	}
	if err := ctx.Err(); err != nil {
		return CallerSendStamp{}, &callerSendStampError{cause: err}
	}
	return stamp, nil
}

// ReadCallerSendStampInTx performs a plain exact-scope SELECT in the caller's
// transaction, including read-only composition. A miss means only no matching
// stamp was observed; it is never abort proof or permission to resend.
func ReadCallerSendStampInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope CallerSendStampScope) (CallerSendStamp, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || scope.Validate() != nil {
		return CallerSendStamp{}, false, ErrCallerSendStampInvalid
	}
	if err := ctx.Err(); err != nil {
		return CallerSendStamp{}, false, &callerSendStampError{cause: err}
	}
	stamp, err := scanCallerSendStamp(tx.QueryRow(ctx, callerSendStampReadQuery,
		orgID, scope.SessionID, scope.ThreadID, scope.ClientMessageID, scope.Purpose, scope.OwnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		if contextErr := ctx.Err(); contextErr != nil {
			return CallerSendStamp{}, false, &callerSendStampError{cause: contextErr}
		}
		return CallerSendStamp{}, false, nil
	}
	if err != nil {
		return CallerSendStamp{}, false, &callerSendStampError{cause: err}
	}
	if !validCallerSendStamp(stamp) || stamp.OrgID != orgID || stamp.Scope != scope {
		return CallerSendStamp{}, false, ErrCallerSendStampMismatch
	}
	if err := ctx.Err(); err != nil {
		return CallerSendStamp{}, false, &callerSendStampError{cause: err}
	}
	return stamp, true, nil
}

func validCallerSendStamp(stamp CallerSendStamp) bool {
	return stamp.OrgID != uuid.Nil && stamp.CallerSendStampInput.Validate() == nil && !stamp.CreatedAt.IsZero()
}

func scanCallerSendStamp(row pgx.Row) (CallerSendStamp, error) {
	var stamp CallerSendStamp
	err := row.Scan(&stamp.OrgID, &stamp.Scope.SessionID, &stamp.Scope.ThreadID,
		&stamp.Scope.ClientMessageID, &stamp.Scope.Purpose, &stamp.Scope.OwnerID,
		&stamp.Version, &stamp.Digest, &stamp.MessageID, &stamp.CreatedAt)
	if err != nil {
		return CallerSendStamp{}, err
	}
	return stamp, nil
}
