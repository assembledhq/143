package db

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const callerSendObservationMaxBytes = 8 << 20

var ErrCallerSendObservationInvalid = errors.New("invalid caller send observation scope")
var ErrCallerSendObservationStorage = errors.New("caller send observation storage failed")

type callerSendObservationStorageError struct{ cause error }

func (*callerSendObservationStorageError) Error() string {
	return ErrCallerSendObservationStorage.Error()
}
func (e *callerSendObservationStorageError) Unwrap() error { return e.cause }
func (*callerSendObservationStorageError) Is(target error) bool {
	return target == ErrCallerSendObservationStorage
}

// CallerSendOwnerRef is independently retained controller correlation, never
// an owner discovered from untrusted command JSON or the current primary lane.
type CallerSendOwnerRef struct {
	Purpose            CallerSendStampPurpose
	OwnerID, SessionID uuid.UUID
}

func (r CallerSendOwnerRef) Validate() error {
	if r.Purpose.Validate() != nil || r.OwnerID == uuid.Nil || r.SessionID == uuid.Nil {
		return ErrCallerSendObservationInvalid
	}
	return nil
}

type CallerSendRetainedShape string

const (
	CallerSendRetainedLegacy      CallerSendRetainedShape = "legacy"
	CallerSendRetainedSupported   CallerSendRetainedShape = "supported"
	CallerSendRetainedUnsupported CallerSendRetainedShape = "unsupported_version"
	CallerSendRetainedMalformed   CallerSendRetainedShape = "malformed"
)

// RetainedCallerSend has no public JSON fields. Raw returns a defensive copy.
// Attempt metadata is observed correlation for a later separate fenced CAS,
// never permission to send, settle, retry, execute, or mutate a business owner.
type RetainedCallerSend struct {
	orgID                 uuid.UUID
	ref                   CallerSendOwnerRef
	shape                 CallerSendRetainedShape
	threadID              uuid.UUID
	key, digest, state    string
	version               int16
	raw                   []byte
	attemptID             uuid.UUID
	attemptSeq, writerSeq int32
	passID                uuid.UUID
}

func (r RetainedCallerSend) OrgID() uuid.UUID               { return r.orgID }
func (r RetainedCallerSend) Ref() CallerSendOwnerRef        { return r.ref }
func (r RetainedCallerSend) Shape() CallerSendRetainedShape { return r.shape }
func (r RetainedCallerSend) ThreadID() uuid.UUID            { return r.threadID }
func (r RetainedCallerSend) Key() string                    { return r.key }
func (r RetainedCallerSend) Digest() string                 { return r.digest }
func (r RetainedCallerSend) Version() int16                 { return r.version }
func (r RetainedCallerSend) Raw() []byte                    { return bytes.Clone(r.raw) }
func (r RetainedCallerSend) State() string                  { return r.state }
func (r RetainedCallerSend) AttemptID() uuid.UUID           { return r.attemptID }
func (r RetainedCallerSend) AttemptSeq() int32              { return r.attemptSeq }
func (r RetainedCallerSend) WriterSeq() int32               { return r.writerSeq }
func (r RetainedCallerSend) PassID() uuid.UUID              { return r.passID }

const callerSendDispatchObservationQuery = `SELECT id,org_id,session_id,dispatch_version,
 CASE WHEN octet_length(dispatch_key)<=128 THEN dispatch_key END,
 CASE WHEN octet_length(dispatch_digest)<=64 THEN dispatch_digest END,dispatch_thread_id,
 CASE WHEN dispatch_version=1 THEN CASE WHEN octet_length(dispatch_input::text)<=$4 THEN dispatch_input::text END END,
 CASE WHEN dispatch_version=1 THEN COALESCE(octet_length(dispatch_input::text),0) ELSE 0 END,
 num_nonnulls(dispatch_version,dispatch_key,dispatch_input,dispatch_digest,dispatch_thread_id,dispatch_state,
 dispatch_attempt_id,dispatch_attempt_seq,dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,
 dispatch_attention_at,dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,
 dispatch_writer_seq,dispatch_updated_at,dispatch_wake),
 CASE WHEN octet_length(dispatch_state)<=16 THEN dispatch_state END,
 dispatch_attempt_id,dispatch_attempt_seq,dispatch_writer_seq,NULL::uuid,NULL::uuid,role='reviewer',
 CASE WHEN dispatch_version=1 THEN caller143_send_state_valid(dispatch_state,dispatch_attempt_id,dispatch_attempt_seq,
 dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,dispatch_attention_at,dispatch_observed_at,
 dispatch_message_id,dispatch_resolution,dispatch_detail,dispatch_writer_seq,dispatch_updated_at)
 AND dispatch_wake IS NOT NULL ELSE false END
 FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`

const callerSendStartupObservationQuery = `SELECT id,org_id,session_id,startup_send_version,
 CASE WHEN octet_length(startup_send_key)<=128 THEN startup_send_key END,
 CASE WHEN octet_length(startup_send_digest)<=64 THEN startup_send_digest END,startup_send_thread_id,
 CASE WHEN startup_send_version=1 THEN CASE WHEN octet_length(startup_send_input::text)<=$4 THEN startup_send_input::text END END,
 CASE WHEN startup_send_version=1 THEN COALESCE(octet_length(startup_send_input::text),0) ELSE 0 END,
 num_nonnulls(startup_send_version,startup_send_key,startup_send_input,startup_send_digest,startup_send_thread_id,startup_send_state,
 startup_send_attempt_id,startup_send_attempt_seq,startup_send_attempted_at,startup_send_due_at,startup_send_observe_deadline,
 startup_send_attention_at,startup_send_observed_at,startup_send_message_id,startup_send_resolution,startup_send_detail,
 startup_send_writer_seq,startup_send_updated_at,startup_send_pass_id),
 CASE WHEN octet_length(startup_send_state)<=16 THEN startup_send_state END,
 startup_send_attempt_id,startup_send_attempt_seq,startup_send_writer_seq,thread_id,startup_send_pass_id,true,
 CASE WHEN startup_send_version=1 THEN caller143_send_state_valid(startup_send_state,startup_send_attempt_id,startup_send_attempt_seq,
 startup_send_attempted_at,startup_send_due_at,startup_send_observe_deadline,startup_send_attention_at,startup_send_observed_at,
 startup_send_message_id,startup_send_resolution,startup_send_detail,startup_send_writer_seq,startup_send_updated_at) ELSE false END
 FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`

// ReadRetainedCallerSendInTx only reads typed immutable owner identity and its
// observed attempt. Unknown versions never fetch raw JSONB. Complete all-NULL
// intent shape, not NULL version alone, identifies an old no-intent owner.
func ReadRetainedCallerSendInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, ref CallerSendOwnerRef) (RetainedCallerSend, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || ref.Validate() != nil {
		return RetainedCallerSend{}, false, ErrCallerSendObservationInvalid
	}
	if err := ctx.Err(); err != nil {
		return RetainedCallerSend{}, false, &callerSendObservationStorageError{cause: err}
	}
	query := callerSendDispatchObservationQuery
	if ref.Purpose == CallerSendStampReviewLoopStartup {
		query = callerSendStartupObservationQuery
	}
	var id, org, session uuid.UUID
	var version *int16
	var key, digest, raw, state *string
	var thread, attempt, ordinaryThread, pass *uuid.UUID
	var attemptSeq, writerSeq *int32
	var rawBytes, nonnull int
	var kindMatches, stateValid bool
	err := tx.QueryRow(ctx, query, orgID, ref.SessionID, ref.OwnerID, callerSendObservationMaxBytes).Scan(
		&id, &org, &session, &version, &key, &digest, &thread, &raw, &rawBytes, &nonnull, &state, &attempt, &attemptSeq, &writerSeq, &ordinaryThread, &pass, &kindMatches, &stateValid)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return RetainedCallerSend{}, false, nil
	}
	if err != nil {
		return RetainedCallerSend{}, false, &callerSendObservationStorageError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return RetainedCallerSend{}, false, &callerSendObservationStorageError{cause: err}
	}
	r := RetainedCallerSend{orgID: orgID, ref: ref, shape: CallerSendRetainedMalformed}
	if id != ref.OwnerID || org != orgID || session != ref.SessionID || !kindMatches {
		return r, true, nil
	}
	if nonnull == 0 {
		r.shape = CallerSendRetainedLegacy
		return r, true, nil
	}
	if version == nil {
		return r, true, nil
	}
	r.version = *version
	if *version != 1 {
		r.shape = CallerSendRetainedUnsupported
		return r, true, nil
	}
	if key == nil || digest == nil || thread == nil || *thread == uuid.Nil || raw == nil || rawBytes <= 0 || rawBytes > callerSendObservationMaxBytes || len(*raw) != rawBytes || state == nil || attemptSeq == nil || writerSeq == nil || !stateValid {
		return r, true, nil
	}
	expectedKey, keyErr := CallerSendStampKey(ref.Purpose, ref.OwnerID)
	if keyErr != nil || *key != expectedKey {
		return r, true, nil
	}
	if _, digestErr := ParseLifecycleDigest(*digest); digestErr != nil {
		return r, true, nil
	}
	if *attemptSeq < 0 || *writerSeq < 0 || (*attemptSeq == 0) != (attempt == nil) || (attempt != nil && *attempt == uuid.Nil) {
		return r, true, nil
	}
	if ref.Purpose == CallerSendStampReviewLoopStartup && (ordinaryThread == nil || *ordinaryThread != *thread || pass == nil || *pass == uuid.Nil) {
		return r, true, nil
	}
	r.threadID, r.key, r.digest, r.state, r.raw = *thread, *key, *digest, *state, []byte(*raw)
	r.attemptSeq, r.writerSeq = *attemptSeq, *writerSeq
	if attempt != nil {
		r.attemptID = *attempt
	}
	if pass != nil {
		r.passID = *pass
	}
	r.shape = CallerSendRetainedSupported
	return r, true, nil
}

const callerSendVisibilityQuery = `SELECT true FROM sessions s
 JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3
 AND s.deleted_at IS NULL AND s.archived_at IS NULL AND t.archived_at IS NULL`

func ReadCallerSendVisibilityInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID, threadID uuid.UUID) (bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || sessionID == uuid.Nil || threadID == uuid.Nil {
		return false, ErrCallerSendObservationInvalid
	}
	var visible bool
	err := tx.QueryRow(ctx, callerSendVisibilityQuery, orgID, sessionID, threadID).Scan(&visible)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return false, nil
	}
	if err != nil {
		return false, &callerSendObservationStorageError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return false, &callerSendObservationStorageError{cause: err}
	}
	return visible, nil
}

// CallerSendLinkage is sealed same-statement correlation, not a public model or
// active receipt. The final query repeats visibility and every typed scope.
type CallerSendLinkage struct {
	messageID int64
	version   int16
	digest    string
	createdAt time.Time
}

func (l CallerSendLinkage) MessageID() int64     { return l.messageID }
func (l CallerSendLinkage) Version() int16       { return l.version }
func (l CallerSendLinkage) Digest() string       { return l.digest }
func (l CallerSendLinkage) CreatedAt() time.Time { return l.createdAt }

const callerSendLinkageQuery = `SELECT c.message_id,c.version,CASE WHEN octet_length(c.digest)<=64 THEN c.digest ELSE '' END,c.created_at
 FROM thread_inbox_entries i
 JOIN session_messages m ON m.org_id=i.org_id AND m.session_id=i.session_id AND m.thread_id=i.thread_id AND m.id=i.message_id
 JOIN caller_send_command_stamps c ON c.org_id=i.org_id AND c.session_id=i.session_id AND c.thread_id=i.thread_id
 AND c.client_message_id=i.client_message_id AND c.message_id=i.message_id
 JOIN sessions s ON s.org_id=i.org_id AND s.id=i.session_id
 JOIN session_threads t ON t.org_id=i.org_id AND t.session_id=i.session_id AND t.id=i.thread_id
 WHERE i.org_id=$1 AND i.session_id=$2 AND i.thread_id=$3 AND i.client_message_id=$4
 AND c.purpose=$5 AND c.owner_id=$6 AND i.entry_type='user_message' AND m.role='user'
 AND s.deleted_at IS NULL AND s.archived_at IS NULL AND t.archived_at IS NULL`

// ReadCallerSendLinkageInTx deliberately does not name a missing constituent: the inner join
// only establishes that the complete exact tuple was not positively observed.
func ReadCallerSendLinkageInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope CallerSendStampScope) (CallerSendLinkage, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || scope.Validate() != nil {
		return CallerSendLinkage{}, false, ErrCallerSendObservationInvalid
	}
	var link CallerSendLinkage
	err := tx.QueryRow(ctx, callerSendLinkageQuery, orgID, scope.SessionID, scope.ThreadID, scope.ClientMessageID, scope.Purpose, scope.OwnerID).Scan(&link.messageID, &link.version, &link.digest, &link.createdAt)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return CallerSendLinkage{}, false, nil
	}
	if err != nil {
		return CallerSendLinkage{}, false, &callerSendObservationStorageError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return CallerSendLinkage{}, false, &callerSendObservationStorageError{cause: err}
	}
	return link, true, nil
}
