package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrReviewerDispatchInput = errors.New("invalid reviewer dispatch operation")
var ErrReviewerDispatchStorage = errors.New("reviewer dispatch storage failed")

type reviewerDispatchStorageError struct{ cause error }

func (*reviewerDispatchStorageError) Error() string   { return ErrReviewerDispatchStorage.Error() }
func (e *reviewerDispatchStorageError) Unwrap() error { return e.cause }
func (*reviewerDispatchStorageError) Is(target error) bool {
	return target == ErrReviewerDispatchStorage
}

type ReviewerDispatchState string

const (
	ReviewerDispatchPending    ReviewerDispatchState = "pending"
	ReviewerDispatchAttempting ReviewerDispatchState = "attempting"
	ReviewerDispatchUnknown    ReviewerDispatchState = "unknown"
	ReviewerDispatchAttention  ReviewerDispatchState = "attention"
	ReviewerDispatchAccepted   ReviewerDispatchState = "accepted"
	ReviewerDispatchRefused    ReviewerDispatchState = "refused"
	ReviewerDispatchCancelled  ReviewerDispatchState = "cancelled"
)

func (s ReviewerDispatchState) valid() bool {
	switch s {
	case ReviewerDispatchPending, ReviewerDispatchAttempting, ReviewerDispatchUnknown, ReviewerDispatchAttention, ReviewerDispatchAccepted, ReviewerDispatchRefused, ReviewerDispatchCancelled:
		return true
	}
	return false
}

// ReviewerDispatchExpectation is private controller correlation, not a public
// model or authority to send. Metadata identity is independently retained. The
// immutable provider/model remain DB-guarded; this expectation never reassigns
// them, renders input, chooses a lane or infers identity from current JSON.
type ReviewerDispatchExpectation struct {
	metadataID, orgID, sessionID, resultID, threadID uuid.UUID
	key, digest                                      string
	version                                          int16
	state                                            ReviewerDispatchState
	attemptID                                        uuid.UUID
	attemptSeq, writerSeq                            int32
}

func (e ReviewerDispatchExpectation) valid() bool {
	if e.metadataID == uuid.Nil || e.orgID == uuid.Nil || e.sessionID == uuid.Nil || e.resultID == uuid.Nil || e.threadID == uuid.Nil || e.version != 1 || !e.state.valid() || e.attemptSeq < 0 || e.writerSeq < 0 || (e.attemptSeq == 0) != (e.attemptID == uuid.Nil) {
		return false
	}
	if e.state != ReviewerDispatchPending && e.state != ReviewerDispatchCancelled && e.attemptSeq == 0 {
		return false
	}
	key, err := CallerSendStampKey(CallerSendStampCodeReviewDispatch, e.resultID)
	if err != nil || key != e.key {
		return false
	}
	_, err = ParseLifecycleDigest(e.digest)
	return err == nil
}
func ReviewerDispatchExpectationFromRetained(metadataID uuid.UUID, owner RetainedCallerSend) (ReviewerDispatchExpectation, error) {
	ref := owner.Ref()
	if owner.Shape() != CallerSendRetainedSupported || ref.Purpose != CallerSendStampCodeReviewDispatch {
		return ReviewerDispatchExpectation{}, ErrReviewerDispatchInput
	}
	e := ReviewerDispatchExpectation{metadataID: metadataID, orgID: owner.OrgID(), sessionID: ref.SessionID, resultID: ref.OwnerID, threadID: owner.ThreadID(), key: owner.Key(), digest: owner.Digest(), version: owner.Version(), state: ReviewerDispatchState(owner.State()), attemptID: owner.AttemptID(), attemptSeq: owner.AttemptSeq(), writerSeq: owner.WriterSeq()}
	if !e.valid() {
		return ReviewerDispatchExpectation{}, ErrReviewerDispatchInput
	}
	return e, nil
}
func (e ReviewerDispatchExpectation) MetadataID() uuid.UUID { return e.metadataID }
func (e ReviewerDispatchExpectation) OwnerRef() CallerSendOwnerRef {
	return CallerSendOwnerRef{Purpose: CallerSendStampCodeReviewDispatch, OwnerID: e.resultID, SessionID: e.sessionID}
}
func (e ReviewerDispatchExpectation) State() ReviewerDispatchState { return e.state }
func (e ReviewerDispatchExpectation) AttemptID() uuid.UUID         { return e.attemptID }
func (e ReviewerDispatchExpectation) AttemptSeq() int32            { return e.attemptSeq }
func (e ReviewerDispatchExpectation) WriterSeq() int32             { return e.writerSeq }

// ReviewerDispatchIntent is a bounded state-only projection. No raw command or
// wake is fetched here; the controller must separately restore the original
// complete command and release its transaction before any service call.
type ReviewerDispatchIntent struct {
	expectation                                                             ReviewerDispatchExpectation
	attemptedAt, dueAt, observeDeadline, attentionAt, observedAt, updatedAt time.Time
	messageID                                                               int64
	detail, resolution, status                                              string
	metadataEligible, due                                                   bool
}

func (i ReviewerDispatchIntent) Expectation() ReviewerDispatchExpectation { return i.expectation }
func (i ReviewerDispatchIntent) State() ReviewerDispatchState             { return i.expectation.state }
func (i ReviewerDispatchIntent) AttemptedAt() time.Time                   { return i.attemptedAt }
func (i ReviewerDispatchIntent) DueAt() time.Time                         { return i.dueAt }
func (i ReviewerDispatchIntent) ObserveDeadline() time.Time               { return i.observeDeadline }
func (i ReviewerDispatchIntent) AttentionAt() time.Time                   { return i.attentionAt }
func (i ReviewerDispatchIntent) ObservedAt() time.Time                    { return i.observedAt }
func (i ReviewerDispatchIntent) UpdatedAt() time.Time                     { return i.updatedAt }
func (i ReviewerDispatchIntent) MessageID() int64                         { return i.messageID }
func (i ReviewerDispatchIntent) Detail() string                           { return i.detail }
func (i ReviewerDispatchIntent) Resolution() string                       { return i.resolution }
func (i ReviewerDispatchIntent) Due() bool                                { return i.due }
func (i ReviewerDispatchIntent) MetadataEligible() bool                   { return i.metadataEligible }
func (i ReviewerDispatchIntent) ContributionStatus() string               { return i.status }

const reviewerDispatchReadQuery = `SELECT r.dispatch_version,CASE WHEN octet_length(r.dispatch_key)<=128 THEN r.dispatch_key END,
 CASE WHEN octet_length(r.dispatch_digest)<=64 THEN r.dispatch_digest END,r.dispatch_thread_id,CASE WHEN octet_length(r.dispatch_state)<=16 THEN r.dispatch_state END,
 r.dispatch_attempt_id,r.dispatch_attempt_seq,r.dispatch_writer_seq,r.dispatch_attempted_at,r.dispatch_due_at,
 r.dispatch_observe_deadline,r.dispatch_attention_at,r.dispatch_observed_at,r.dispatch_updated_at,r.dispatch_message_id,
 CASE WHEN octet_length(r.dispatch_detail)<=64 THEN r.dispatch_detail END,CASE WHEN octet_length(r.dispatch_resolution)<=32 THEN r.dispatch_resolution END,r.status,m.status IN ('queued','running') AND NOT m.stale,
 r.dispatch_due_at IS NOT NULL AND r.dispatch_due_at<=statement_timestamp(),
 caller143_send_state_valid(r.dispatch_state,r.dispatch_attempt_id,r.dispatch_attempt_seq,r.dispatch_attempted_at,
 r.dispatch_due_at,r.dispatch_observe_deadline,r.dispatch_attention_at,r.dispatch_observed_at,r.dispatch_message_id,
 r.dispatch_resolution,r.dispatch_detail,r.dispatch_writer_seq,r.dispatch_updated_at)
 AND (r.dispatch_detail IS NULL OR octet_length(r.dispatch_detail)<=64)
 AND (r.dispatch_resolution IS NULL OR octet_length(r.dispatch_resolution)<=32)
 FROM code_review_agent_results r JOIN code_review_session_metadata m ON m.org_id=r.org_id AND m.session_id=r.session_id
 WHERE r.org_id=$1 AND r.session_id=$2 AND r.id=$3 AND m.id=$4 AND r.role='reviewer'`

func ReadReviewerDispatchIntentInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, metadataID, sessionID, resultID uuid.UUID) (ReviewerDispatchIntent, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || metadataID == uuid.Nil || sessionID == uuid.Nil || resultID == uuid.Nil {
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	if err := ctx.Err(); err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	var version *int16
	var key, digest, state, detail, resolution *string
	var thread, attempt *uuid.UUID
	var attemptSeq, writerSeq *int32
	var attempted, due, deadline, attention, observed, updated *time.Time
	var message *int64
	var stateValid bool
	i := ReviewerDispatchIntent{}
	err := tx.QueryRow(ctx, reviewerDispatchReadQuery, orgID, sessionID, resultID, metadataID).Scan(&version, &key, &digest, &thread, &state, &attempt, &attemptSeq, &writerSeq, &attempted, &due, &deadline, &attention, &observed, &updated, &message, &detail, &resolution, &i.status, &i.metadataEligible, &i.due, &stateValid)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return ReviewerDispatchIntent{}, false, nil
	}
	if err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	// Legacy, unsupported and corrupt shapes are safe misses, never upgrade,
	// resend or fallback permission. Do not fetch an unsupported raw payload.
	if version == nil || key == nil || digest == nil || thread == nil || state == nil || attemptSeq == nil || writerSeq == nil || updated == nil || !stateValid {
		return ReviewerDispatchIntent{}, false, nil
	}
	i.expectation = ReviewerDispatchExpectation{metadataID: metadataID, orgID: orgID, sessionID: sessionID, resultID: resultID, threadID: *thread, key: *key, digest: *digest, version: *version, state: ReviewerDispatchState(*state), attemptSeq: *attemptSeq, writerSeq: *writerSeq}
	if attempt != nil {
		i.expectation.attemptID = *attempt
	}
	if !i.expectation.valid() {
		return ReviewerDispatchIntent{}, false, nil
	}
	if attempted != nil {
		i.attemptedAt = *attempted
	}
	if due != nil {
		i.dueAt = *due
	}
	if deadline != nil {
		i.observeDeadline = *deadline
	}
	if attention != nil {
		i.attentionAt = *attention
	}
	if observed != nil {
		i.observedAt = *observed
	}
	i.updatedAt = *updated
	if message != nil {
		i.messageID = *message
	}
	if detail != nil {
		i.detail = *detail
	}
	if resolution != nil {
		i.resolution = *resolution
	}
	return i, true, nil
}

const reviewerDispatchMetadataLockQuery = `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR SHARE`
const reviewerDispatchResultLockQuery = `SELECT id FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 AND role='reviewer' FOR UPDATE`

func lockReviewerDispatch(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation) (ReviewerDispatchIntent, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !e.valid() {
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	if e.orgID != orgID {
		return ReviewerDispatchIntent{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	var id uuid.UUID
	for _, lock := range []struct {
		query string
		id    uuid.UUID
	}{{reviewerDispatchMetadataLockQuery, e.metadataID}, {reviewerDispatchResultLockQuery, e.resultID}} {
		err := tx.QueryRow(ctx, lock.query, orgID, e.sessionID, lock.id).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			return ReviewerDispatchIntent{}, false, nil
		}
		if err != nil {
			return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
		}
		if id != lock.id {
			return ReviewerDispatchIntent{}, false, nil
		}
	}
	// Fresh source statement after BOTH lock waits; the metadata owner remains
	// locked before the result, matching cancellation/completion ordering.
	i, found, err := ReadReviewerDispatchIntentInTx(ctx, orgID, tx, e.metadataID, e.sessionID, e.resultID)
	if err != nil || !found {
		return ReviewerDispatchIntent{}, false, err
	}
	if i.expectation != e {
		return ReviewerDispatchIntent{}, false, nil
	}
	return i, true, nil
}

type ReviewerDispatchAbortCode string

const (
	ReviewerDispatchAbortContention        ReviewerDispatchAbortCode = "contention"
	ReviewerDispatchAbortNormalizing       ReviewerDispatchAbortCode = "normalizing"
	ReviewerDispatchAbortModeChanged       ReviewerDispatchAbortCode = "mode_changed"
	ReviewerDispatchAbortReplayObservation ReviewerDispatchAbortCode = "replay_observation_needed"
)

type ReviewerDispatchRefusalCode string

const (
	ReviewerDispatchSendRefused              ReviewerDispatchRefusalCode = "send_refused"
	ReviewerDispatchStampedActiveUnsupported ReviewerDispatchRefusalCode = "stamped_active_unsupported"
)

type ReviewerDispatchCancelReason string

const (
	ReviewerDispatchOwnerCancelled        ReviewerDispatchCancelReason = "owner_cancelled"
	ReviewerDispatchOwnerIneligible       ReviewerDispatchCancelReason = "owner_ineligible"
	ReviewerDispatchDeadlineBeforeAttempt ReviewerDispatchCancelReason = "deadline_before_attempt"
)

type ReviewerDispatchSettlement struct {
	operation, detail string
	messageID         int64
	stampAt           time.Time
	observed          ReviewerDispatchExpectation
}

func (s ReviewerDispatchSettlement) valid() bool {
	switch s.operation {
	case "ack":
		return s.messageID > 0 && s.detail == "" && s.stampAt.IsZero() && s.observed == (ReviewerDispatchExpectation{})
	case "retry":
		switch ReviewerDispatchAbortCode(s.detail) {
		case ReviewerDispatchAbortContention, ReviewerDispatchAbortNormalizing, ReviewerDispatchAbortModeChanged, ReviewerDispatchAbortReplayObservation:
			return s.messageID == 0 && s.stampAt.IsZero() && s.observed == (ReviewerDispatchExpectation{})
		}
	case "refuse":
		switch ReviewerDispatchRefusalCode(s.detail) {
		case ReviewerDispatchSendRefused, ReviewerDispatchStampedActiveUnsupported:
			return s.messageID == 0 && s.stampAt.IsZero() && s.observed == (ReviewerDispatchExpectation{})
		}
	case "unknown", "stale", "negative":
		return s.detail == "" && s.messageID == 0 && s.stampAt.IsZero() && s.observed == (ReviewerDispatchExpectation{})
	case "observe_auto", "observe_manual":
		return s.detail == "" && s.messageID > 0 && !s.stampAt.IsZero() && s.observed.valid() && (s.observed.state == ReviewerDispatchUnknown || s.observed.state == ReviewerDispatchAttention)
	}
	return false
}
func ReviewerDispatchSendAcknowledged(messageID int64) (ReviewerDispatchSettlement, error) {
	if messageID <= 0 {
		return ReviewerDispatchSettlement{}, ErrReviewerDispatchInput
	}
	return ReviewerDispatchSettlement{operation: "ack", messageID: messageID}, nil
}
func ReviewerDispatchRetryAfterKnownAbort(code ReviewerDispatchAbortCode) (ReviewerDispatchSettlement, error) {
	switch code {
	case ReviewerDispatchAbortContention, ReviewerDispatchAbortNormalizing, ReviewerDispatchAbortModeChanged, ReviewerDispatchAbortReplayObservation:
		return ReviewerDispatchSettlement{operation: "retry", detail: string(code)}, nil
	}
	return ReviewerDispatchSettlement{}, ErrReviewerDispatchInput
}
func ReviewerDispatchKnownRefusal(code ReviewerDispatchRefusalCode) (ReviewerDispatchSettlement, error) {
	switch code {
	case ReviewerDispatchSendRefused, ReviewerDispatchStampedActiveUnsupported:
		return ReviewerDispatchSettlement{operation: "refuse", detail: string(code)}, nil
	}
	return ReviewerDispatchSettlement{}, ErrReviewerDispatchInput
}
func ReviewerDispatchUnknownAcknowledgement() ReviewerDispatchSettlement {
	return ReviewerDispatchSettlement{operation: "unknown"}
}
func ReviewerDispatchStaleAttempt() ReviewerDispatchSettlement {
	return ReviewerDispatchSettlement{operation: "stale"}
}
func ReviewerDispatchNegativeObservation() ReviewerDispatchSettlement {
	return ReviewerDispatchSettlement{operation: "negative"}
}

// ReviewerDispatchObservedAccepted binds the full settled I5 candidate, never a bare message mutation
// receipt. The writable helper additionally rechecks actual exact typed linkage.
func ReviewerDispatchObservedAccepted(e ReviewerDispatchExpectation, messageID int64, stampAt time.Time, manual bool) (ReviewerDispatchSettlement, error) {
	if !e.valid() || (e.state != ReviewerDispatchUnknown && e.state != ReviewerDispatchAttention) || messageID <= 0 || stampAt.IsZero() {
		return ReviewerDispatchSettlement{}, ErrReviewerDispatchInput
	}
	op := "observe_auto"
	if manual {
		op = "observe_manual"
	}
	return ReviewerDispatchSettlement{operation: op, messageID: messageID, stampAt: stampAt, observed: e}, nil
}

const reviewerDispatchUpdateQuery = `UPDATE code_review_agent_results r SET
 dispatch_state=CASE $13::text WHEN 'claim' THEN 'attempting' WHEN 'retry' THEN 'pending' WHEN 'refuse' THEN 'refused'
 WHEN 'unknown' THEN 'unknown' WHEN 'stale' THEN 'unknown' WHEN 'cancel' THEN 'cancelled'
 WHEN 'negative' THEN CASE WHEN r.dispatch_state='attention' OR r.dispatch_observe_deadline<=statement_timestamp() THEN 'attention' ELSE 'unknown' END ELSE 'accepted' END,
 dispatch_attempt_id=CASE WHEN $13='claim' THEN $16::uuid ELSE r.dispatch_attempt_id END,
 dispatch_attempt_seq=r.dispatch_attempt_seq+CASE WHEN $13='claim' THEN 1 ELSE 0 END,
 dispatch_attempted_at=CASE WHEN $13='claim' THEN GREATEST(r.dispatch_attempted_at,statement_timestamp()) ELSE r.dispatch_attempted_at END,
 dispatch_due_at=CASE $13 WHEN 'claim' THEN statement_timestamp()+interval '60 seconds'
 WHEN 'retry' THEN statement_timestamp()+LEAST(300,5*power(2,LEAST(r.dispatch_attempt_seq-1,6))) * interval '1 second'
 WHEN 'unknown' THEN statement_timestamp()+interval '5 seconds' WHEN 'stale' THEN statement_timestamp()+interval '5 seconds'
 WHEN 'negative' THEN statement_timestamp()+CASE WHEN r.dispatch_state='attention' OR r.dispatch_observe_deadline<=statement_timestamp() THEN interval '1 hour'
 ELSE LEAST(60,GREATEST(5,CASE WHEN r.dispatch_observed_at IS NULL THEN 5 ELSE 2*extract(epoch FROM (r.dispatch_due_at-r.dispatch_observed_at)) END))*interval '1 second' END ELSE NULL END,
 dispatch_observe_deadline=CASE WHEN $13 IN ('unknown','stale') THEN r.dispatch_attempted_at+interval '15 minutes' ELSE r.dispatch_observe_deadline END,
 dispatch_attention_at=CASE WHEN $13='negative' AND r.dispatch_state='unknown' AND r.dispatch_observe_deadline<=statement_timestamp() THEN statement_timestamp() ELSE r.dispatch_attention_at END,
 dispatch_observed_at=CASE WHEN $13 IN ('negative','observe_auto','observe_manual') THEN GREATEST(r.dispatch_observed_at,statement_timestamp()) ELSE r.dispatch_observed_at END,
 dispatch_message_id=CASE WHEN $13 IN ('ack','observe_auto','observe_manual') THEN $15::bigint ELSE r.dispatch_message_id END,
 dispatch_resolution=CASE $13 WHEN 'ack' THEN 'send_ack' WHEN 'observe_auto' THEN 'observed_auto' WHEN 'observe_manual' THEN 'observed_manual' WHEN 'refuse' THEN 'send_refusal' WHEN 'cancel' THEN $14::text ELSE r.dispatch_resolution END,
 dispatch_detail=CASE $13 WHEN 'claim' THEN NULL WHEN 'retry' THEN $14::text WHEN 'refuse' THEN $14::text WHEN 'unknown' THEN 'unknown_ack' WHEN 'stale' THEN 'stale_attempt' ELSE r.dispatch_detail END,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 WHERE r.org_id=$1 AND r.session_id=$2 AND r.id=$3 AND r.role='reviewer'
 AND r.dispatch_thread_id=$5 AND r.dispatch_key=$6 AND r.dispatch_version=$7 AND r.dispatch_digest=$8
 AND r.dispatch_state=$9 AND r.dispatch_attempt_id IS NOT DISTINCT FROM $10::uuid AND r.dispatch_attempt_seq=$11 AND r.dispatch_writer_seq=$12
 AND EXISTS(SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$4)
 AND ($13<>'claim' OR r.status='queued' AND r.dispatch_due_at<=statement_timestamp())
 AND ($13 NOT IN ('stale','negative') OR r.dispatch_due_at<=statement_timestamp()) RETURNING r.id`

func updateReviewerDispatch(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, s ReviewerDispatchSettlement, newAttempt uuid.UUID) (ReviewerDispatchIntent, bool, error) {
	var oldAttempt any
	if e.attemptID != uuid.Nil {
		oldAttempt = e.attemptID
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, reviewerDispatchUpdateQuery, orgID, e.sessionID, e.resultID, e.metadataID, e.threadID, e.key, e.version, e.digest, string(e.state), oldAttempt, e.attemptSeq, e.writerSeq, s.operation, s.detail, s.messageID, newAttempt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return ReviewerDispatchIntent{}, false, nil
	}
	if err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	if id != e.resultID {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: ErrReviewerDispatchInput}
	}
	i, found, err := ReadReviewerDispatchIntentInTx(ctx, orgID, tx, e.metadataID, e.sessionID, e.resultID)
	if err != nil {
		return ReviewerDispatchIntent{}, false, err
	}
	if !found {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: pgx.ErrNoRows}
	}
	return i, true, nil
}

// Settlement is immutable correlation, not a new user visibility decision.
// I5 already checked visibility in its successful readonly unit; an original
// known ACK also survives later archival/terminality. Recheck the full actual
// typed message/inbox/stamp tuple, never a bare ID or payload-only pointer.
const reviewerDispatchImmutableLinkQuery = `SELECT c.created_at FROM caller_send_command_stamps c
 JOIN thread_inbox_entries i ON i.org_id=c.org_id AND i.session_id=c.session_id AND i.thread_id=c.thread_id
 AND i.client_message_id=c.client_message_id AND i.message_id=c.message_id AND i.entry_type='user_message'
 JOIN session_messages m ON m.org_id=c.org_id AND m.session_id=c.session_id AND m.thread_id=c.thread_id AND m.id=c.message_id AND m.role='user'
 WHERE c.org_id=$1 AND c.session_id=$2 AND c.thread_id=$3 AND c.client_message_id=$4
 AND c.purpose='code-review-reviewer-dispatch' AND c.owner_id=$5 AND c.version=$6 AND c.digest=$7 AND c.message_id=$8`

func reviewerDispatchImmutableLink(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, s ReviewerDispatchSettlement) (bool, error) {
	var stampAt time.Time
	err := tx.QueryRow(ctx, reviewerDispatchImmutableLinkQuery, orgID, e.sessionID, e.threadID, e.key, e.resultID, e.version, e.digest, s.messageID).Scan(&stampAt)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return false, nil
	}
	if err != nil {
		return false, &reviewerDispatchStorageError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return false, &reviewerDispatchStorageError{cause: err}
	}
	return !stampAt.IsZero() && (s.operation == "ack" || stampAt.Equal(s.stampAt)), nil
}

// ClaimReviewerDispatchInTx is DB-only and provisional. Only the caller's KNOWN successful outer
// claim commit may release its one send; failed/unknown settlement permits none.
func ClaimReviewerDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, newAttemptID uuid.UUID) (ReviewerDispatchIntent, bool, error) {
	if newAttemptID == uuid.Nil || newAttemptID == e.attemptID {
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	i, found, err := lockReviewerDispatch(ctx, orgID, tx, e)
	if err != nil || !found {
		return ReviewerDispatchIntent{}, false, err
	}
	if e.state != ReviewerDispatchPending || e.attemptSeq == 1<<31-1 || e.writerSeq == 1<<31-1 {
		return ReviewerDispatchIntent{}, false, nil
	}
	if !i.metadataEligible || i.status != "queued" {
		return updateReviewerDispatch(ctx, orgID, tx, e, ReviewerDispatchSettlement{operation: "cancel", detail: string(ReviewerDispatchOwnerIneligible)}, uuid.Nil)
	}
	// A truthful known-abort settlement can return an earlier attempt to
	// pending after confirmed publication has retired its controller. Retain
	// that history, but never authorize another send for the exact confirmed
	// completion owner. Observe only after the existing metadata/result locks;
	// observation failure is storage refusal, not negative send authority.
	completion, err := observeConfirmedFullAssessmentCompletionPending(ctx, tx, orgID, e.metadataID)
	if err != nil {
		return ReviewerDispatchIntent{}, false, &reviewerDispatchStorageError{cause: err}
	}
	if completion.pending {
		return updateReviewerDispatch(ctx, orgID, tx, e, ReviewerDispatchSettlement{operation: "cancel", detail: string(ReviewerDispatchOwnerIneligible)}, uuid.Nil)
	}
	if !i.due {
		return ReviewerDispatchIntent{}, false, nil
	}
	return updateReviewerDispatch(ctx, orgID, tx, e, ReviewerDispatchSettlement{operation: "claim"}, newAttemptID)
}

// SettleReviewerDispatchInTx changes only dispatch columns. Contribution status/output are always
// preserved, including late evidence after timed_out/failed/completed; aware
// contribution writers are a separate controller responsibility.
func SettleReviewerDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, s ReviewerDispatchSettlement) (ReviewerDispatchIntent, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !e.valid() || !s.valid() {
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	if e.orgID != orgID {
		return ReviewerDispatchIntent{}, false, nil
	}
	switch s.operation {
	case "ack", "retry", "refuse", "unknown", "stale":
		if e.state != ReviewerDispatchAttempting {
			return ReviewerDispatchIntent{}, false, nil
		}
	case "negative", "observe_auto", "observe_manual":
		if e.state != ReviewerDispatchUnknown && e.state != ReviewerDispatchAttention {
			return ReviewerDispatchIntent{}, false, nil
		}
	default:
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	if (s.operation == "observe_auto" || s.operation == "observe_manual") && s.observed != e {
		return ReviewerDispatchIntent{}, false, nil
	}
	i, found, err := lockReviewerDispatch(ctx, orgID, tx, e)
	if err != nil || !found {
		return ReviewerDispatchIntent{}, false, err
	}
	if e.writerSeq == 1<<31-1 {
		return ReviewerDispatchIntent{}, false, nil
	}
	if (s.operation == "negative" || s.operation == "stale") && !i.due {
		return ReviewerDispatchIntent{}, false, nil
	}
	if s.operation == "ack" || s.operation == "observe_auto" || s.operation == "observe_manual" {
		linked, err := reviewerDispatchImmutableLink(ctx, orgID, tx, e, s)
		if err != nil {
			return ReviewerDispatchIntent{}, false, err
		}
		if !linked {
			return ReviewerDispatchIntent{}, false, nil
		}
	}
	return updateReviewerDispatch(ctx, orgID, tx, e, s, uuid.Nil)
}

// First negative schedules five seconds. Later negatives double the persisted
// due_at-observed_at interval, clamped to [5s,60s] in PostgreSQL numeric arithmetic.
// Ancillary writer increments are NOT observation counts. No negative result
// ever returns unknown/attention to pending or changes original attempt history.

// CancelPendingReviewerDispatchInTx takes a reason from independently authorized metadata/deadline caller
// logic; it is not authorization itself. Whole-controller integration must
// prove the actual metadata-owner change and pending cancellation share the
// same outer unit, with metadata first. This primitive cannot cancel an attempt.
func CancelPendingReviewerDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, reason ReviewerDispatchCancelReason) (ReviewerDispatchIntent, bool, error) {
	switch reason {
	case ReviewerDispatchOwnerCancelled, ReviewerDispatchOwnerIneligible, ReviewerDispatchDeadlineBeforeAttempt:
	default:
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	_, found, err := lockReviewerDispatch(ctx, orgID, tx, e)
	if err != nil || !found {
		return ReviewerDispatchIntent{}, false, err
	}
	if e.state != ReviewerDispatchPending || e.writerSeq == 1<<31-1 {
		return ReviewerDispatchIntent{}, false, nil
	}
	return updateReviewerDispatch(ctx, orgID, tx, e, ReviewerDispatchSettlement{operation: "cancel", detail: string(reason)}, uuid.Nil)
}
