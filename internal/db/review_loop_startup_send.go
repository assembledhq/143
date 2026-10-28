package db

import (
	"context"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrReviewLoopStartupPointerConflict = errors.New("review startup pass pointer conflict")

// ReviewLoopStartupIntent is an observed attempt, never permission to send.
// Only a known successful outer claim commit releases the original command.
type ReviewLoopStartupIntent struct {
	owner         RetainedCallerSend
	due, eligible bool
}

func (i ReviewLoopStartupIntent) Owner() RetainedCallerSend { return i.owner }
func (i ReviewLoopStartupIntent) Due() bool                 { return i.due }
func (i ReviewLoopStartupIntent) Eligible() bool            { return i.eligible }

func ReadReviewLoopStartupInTx(ctx context.Context, orgID, sessionID, loopID uuid.UUID, tx pgx.Tx) (ReviewLoopStartupIntent, bool, error) {
	owner, found, err := ReadRetainedCallerSendInTx(ctx, orgID, tx, CallerSendOwnerRef{Purpose: CallerSendStampReviewLoopStartup, OwnerID: loopID, SessionID: sessionID})
	if err != nil || !found {
		return ReviewLoopStartupIntent{}, false, err
	}
	if owner.Shape() == CallerSendRetainedLegacy {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if owner.Shape() != CallerSendRetainedSupported {
		return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
	}
	i := ReviewLoopStartupIntent{owner: owner}
	err = tx.QueryRow(ctx, `SELECT l.startup_send_due_at IS NOT NULL AND l.startup_send_due_at<=statement_timestamp(),
 l.status='running' AND EXISTS(SELECT 1 FROM session_review_loop_passes p WHERE p.org_id=l.org_id AND p.session_id=l.session_id
 AND p.loop_id=l.id AND p.id=l.startup_send_pass_id AND p.status='reviewing' AND p.review_message_id IS NULL)
 FROM session_review_loops l WHERE l.org_id=$1 AND l.session_id=$2 AND l.id=$3`, orgID, sessionID, loopID).Scan(&i.due, &i.eligible)
	return i, err == nil, err
}

func sameReviewLoopStartup(a, b RetainedCallerSend) bool {
	return a.orgID == b.orgID && a.ref == b.ref && a.shape == b.shape && a.threadID == b.threadID && a.key == b.key &&
		a.digest == b.digest && a.version == b.version && a.state == b.state && a.attemptID == b.attemptID &&
		a.attemptSeq == b.attemptSeq && a.writerSeq == b.writerSeq && a.passID == b.passID
}

func lockReviewLoopStartup(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartupIntent) (ReviewLoopStartupIntent, bool, error) {
	e := expected.owner
	if ctx == nil || nilLifecycleDependency(tx) || e.orgID != orgID || e.shape != CallerSendRetainedSupported || e.ref.Purpose != CallerSendStampReviewLoopStartup {
		return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`, orgID, e.ref.SessionID, e.ref.OwnerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if err != nil {
		return ReviewLoopStartupIntent{}, false, err
	}
	i, found, err := ReadReviewLoopStartupInTx(ctx, orgID, e.ref.SessionID, e.ref.OwnerID, tx)
	if err != nil || !found {
		return ReviewLoopStartupIntent{}, false, err
	}
	return i, sameReviewLoopStartup(i.owner, e), nil
}

type ReviewLoopStartupSettlement struct {
	operation, detail string
	messageID         int64
	stampAt           time.Time
	observed          RetainedCallerSend
}

func ReviewLoopStartupAcknowledged(messageID int64) (ReviewLoopStartupSettlement, error) {
	if messageID <= 0 {
		return ReviewLoopStartupSettlement{}, ErrReviewLoopStartIdentity
	}
	return ReviewLoopStartupSettlement{operation: "ack", messageID: messageID}, nil
}
func ReviewLoopStartupUnknown() ReviewLoopStartupSettlement {
	return ReviewLoopStartupSettlement{operation: "unknown"}
}
func ReviewLoopStartupStale() ReviewLoopStartupSettlement {
	return ReviewLoopStartupSettlement{operation: "stale"}
}
func ReviewLoopStartupNegativeObservation() ReviewLoopStartupSettlement {
	return ReviewLoopStartupSettlement{operation: "negative"}
}
func ReviewLoopStartupKnownAbort(detail string) (ReviewLoopStartupSettlement, error) {
	switch detail {
	case "contention", "normalizing", "mode_changed", "replay_observation_needed":
		return ReviewLoopStartupSettlement{operation: "retry", detail: detail}, nil
	}
	return ReviewLoopStartupSettlement{}, ErrReviewLoopStartIdentity
}
func ReviewLoopStartupRefused(unsupportedActive bool) ReviewLoopStartupSettlement {
	detail := "send_refused"
	if unsupportedActive {
		detail = "stamped_active_unsupported"
	}
	return ReviewLoopStartupSettlement{operation: "refuse", detail: detail}
}
func ReviewLoopStartupObserved(owner RetainedCallerSend, messageID int64, stampAt time.Time) (ReviewLoopStartupSettlement, error) {
	if owner.shape != CallerSendRetainedSupported || owner.ref.Purpose != CallerSendStampReviewLoopStartup ||
		(owner.state != "unknown" && owner.state != "attention") || messageID <= 0 || stampAt.IsZero() {
		return ReviewLoopStartupSettlement{}, ErrReviewLoopStartIdentity
	}
	return ReviewLoopStartupSettlement{operation: "observe_auto", observed: owner, messageID: messageID, stampAt: stampAt}, nil
}

const reviewLoopStartupUpdateQuery = `UPDATE session_review_loops l SET
 startup_send_state=CASE $13::text WHEN 'claim' THEN 'attempting' WHEN 'retry' THEN 'pending' WHEN 'refuse' THEN 'refused'
 WHEN 'unknown' THEN 'unknown' WHEN 'stale' THEN 'unknown' WHEN 'cancel' THEN 'cancelled' WHEN 'pointer_conflict' THEN 'attention'
 WHEN 'negative' THEN CASE WHEN l.startup_send_state='attention' OR l.startup_send_observe_deadline<=statement_timestamp() THEN 'attention' ELSE 'unknown' END ELSE 'accepted' END,
 startup_send_attempt_id=CASE WHEN $13='claim' THEN $16::uuid ELSE l.startup_send_attempt_id END,
 startup_send_attempt_seq=l.startup_send_attempt_seq+CASE WHEN $13='claim' THEN 1 ELSE 0 END,
 startup_send_attempted_at=CASE WHEN $13='claim' THEN GREATEST(l.startup_send_attempted_at,statement_timestamp()) ELSE l.startup_send_attempted_at END,
 startup_send_due_at=CASE $13 WHEN 'claim' THEN statement_timestamp()+interval '60 seconds'
 WHEN 'retry' THEN statement_timestamp()+LEAST(300,5*power(2,LEAST(l.startup_send_attempt_seq-1,6)))*interval '1 second'
 WHEN 'unknown' THEN statement_timestamp()+interval '5 seconds' WHEN 'stale' THEN statement_timestamp()+interval '5 seconds'
 WHEN 'pointer_conflict' THEN GREATEST(l.startup_send_due_at,statement_timestamp()+interval '1 hour')
 WHEN 'negative' THEN statement_timestamp()+CASE WHEN l.startup_send_state='attention' OR l.startup_send_observe_deadline<=statement_timestamp() THEN interval '1 hour'
 ELSE LEAST(60,GREATEST(5,CASE WHEN l.startup_send_observed_at IS NULL THEN 5 ELSE 2*extract(epoch FROM (l.startup_send_due_at-l.startup_send_observed_at)) END))*interval '1 second' END ELSE NULL END,
 startup_send_observe_deadline=CASE WHEN $13 IN ('unknown','stale','pointer_conflict') THEN COALESCE(l.startup_send_observe_deadline,l.startup_send_attempted_at+interval '15 minutes') ELSE l.startup_send_observe_deadline END,
 startup_send_attention_at=CASE WHEN $13='pointer_conflict' THEN COALESCE(l.startup_send_attention_at,statement_timestamp())
 WHEN $13='negative' AND l.startup_send_state='unknown' AND l.startup_send_observe_deadline<=statement_timestamp() THEN statement_timestamp() ELSE l.startup_send_attention_at END,
 startup_send_observed_at=CASE WHEN $13 IN ('negative','observe_auto') THEN GREATEST(l.startup_send_observed_at,statement_timestamp()) ELSE l.startup_send_observed_at END,
 startup_send_message_id=CASE WHEN $13 IN ('ack','observe_auto') THEN $15::bigint ELSE l.startup_send_message_id END,
 startup_send_resolution=CASE $13 WHEN 'ack' THEN 'send_ack' WHEN 'observe_auto' THEN 'observed_auto' WHEN 'refuse' THEN 'send_refusal' WHEN 'cancel' THEN 'owner_ineligible' ELSE l.startup_send_resolution END,
 startup_send_detail=CASE $13 WHEN 'claim' THEN NULL WHEN 'retry' THEN $14::text WHEN 'refuse' THEN $14::text
 WHEN 'unknown' THEN 'unknown_ack' WHEN 'stale' THEN 'stale_attempt' WHEN 'pointer_conflict' THEN 'pass_pointer_conflict' ELSE l.startup_send_detail END,
 startup_send_writer_seq=l.startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp()
 WHERE l.org_id=$1 AND l.session_id=$2 AND l.id=$3 AND l.startup_send_pass_id=$4
 AND l.startup_send_thread_id=$5 AND l.startup_send_key=$6 AND l.startup_send_version=$7 AND l.startup_send_digest=$8
 AND l.startup_send_state=$9 AND l.startup_send_attempt_id IS NOT DISTINCT FROM $10::uuid
 AND l.startup_send_attempt_seq=$11 AND l.startup_send_writer_seq=$12
 AND ($13<>'claim' OR l.status='running' AND l.startup_send_due_at<=statement_timestamp() AND EXISTS(
 SELECT 1 FROM session_review_loop_passes p WHERE p.org_id=l.org_id AND p.session_id=l.session_id AND p.loop_id=l.id
 AND p.id=l.startup_send_pass_id AND p.status='reviewing' AND p.review_message_id IS NULL))
 AND ($13 NOT IN ('stale','negative') OR l.startup_send_due_at<=statement_timestamp()) RETURNING l.id`

func updateReviewLoopStartup(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartupIntent, s ReviewLoopStartupSettlement, attempt uuid.UUID) (ReviewLoopStartupIntent, bool, error) {
	e := expected.owner
	if e.writerSeq == 1<<31-1 {
		return ReviewLoopStartupIntent{}, false, nil
	}
	var prior any
	if e.attemptID != uuid.Nil {
		prior = e.attemptID
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, reviewLoopStartupUpdateQuery, orgID, e.ref.SessionID, e.ref.OwnerID, e.passID, e.threadID, e.key, e.version, e.digest,
		e.state, prior, e.attemptSeq, e.writerSeq, s.operation, s.detail, s.messageID, attempt).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if err != nil {
		return ReviewLoopStartupIntent{}, false, err
	}
	if id != e.ref.OwnerID {
		return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
	}
	return ReadReviewLoopStartupInTx(ctx, orgID, e.ref.SessionID, e.ref.OwnerID, tx)
}

func ClaimReviewLoopStartupInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartupIntent, attempt uuid.UUID) (ReviewLoopStartupIntent, bool, error) {
	if attempt == uuid.Nil || attempt == expected.owner.attemptID {
		return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
	}
	i, found, err := lockReviewLoopStartup(ctx, orgID, tx, expected)
	if err != nil || !found {
		return ReviewLoopStartupIntent{}, false, err
	}
	if i.owner.state != "pending" || i.owner.attemptSeq == 1<<31-1 {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if !i.eligible {
		return updateReviewLoopStartup(ctx, orgID, tx, i, ReviewLoopStartupSettlement{operation: "cancel"}, uuid.Nil)
	}
	if !i.due {
		return ReviewLoopStartupIntent{}, false, nil
	}
	return updateReviewLoopStartup(ctx, orgID, tx, i, ReviewLoopStartupSettlement{operation: "claim"}, attempt)
}

// SettleReviewLoopStartupInTx commits acceptance and the NULL-or-same bound
// first-pass pointer atomically. A pointer conflict aborts this entire unit;
// the controller may then mark attention in a separately committed CAS.
// A refusal retires its running loop in the same unit, so clearing the send's
// due time cannot leave a running owner without a recovery wake.
func SettleReviewLoopStartupInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartupIntent, s ReviewLoopStartupSettlement) (ReviewLoopStartupIntent, bool, error) {
	e := expected.owner
	switch s.operation {
	case "ack", "retry", "refuse", "unknown", "stale":
		if e.state != "attempting" {
			return ReviewLoopStartupIntent{}, false, nil
		}
	case "negative", "observe_auto":
		if e.state != "unknown" && e.state != "attention" {
			return ReviewLoopStartupIntent{}, false, nil
		}
	default:
		return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
	}
	if s.operation == "observe_auto" && !sameReviewLoopStartup(e, s.observed) {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if s.operation == "refuse" {
		// The terminal writer can update the caller. Acquire its established
		// caller -> start -> loop order before the ordinary startup loop lock.
		if ctx == nil || nilLifecycleDependency(tx) || e.orgID != orgID || e.shape != CallerSendRetainedSupported || e.ref.Purpose != CallerSendStampReviewLoopStartup {
			return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartIdentity
		}
		if err := prelockReviewLoopWriter(ctx, orgID, e.ref.OwnerID, tx); err != nil {
			return ReviewLoopStartupIntent{}, false, err
		}
	}
	i, found, err := lockReviewLoopStartup(ctx, orgID, tx, expected)
	if err != nil || !found {
		return ReviewLoopStartupIntent{}, false, err
	}
	if (s.operation == "negative" || s.operation == "stale") && !i.due {
		return ReviewLoopStartupIntent{}, false, nil
	}
	if s.operation == "ack" || s.operation == "observe_auto" {
		var stampAt time.Time
		err := tx.QueryRow(ctx, `SELECT c.created_at FROM caller_send_command_stamps c
 JOIN thread_inbox_entries i ON i.org_id=c.org_id AND i.session_id=c.session_id AND i.thread_id=c.thread_id
 AND i.client_message_id=c.client_message_id AND i.message_id=c.message_id AND i.entry_type='user_message'
 JOIN session_messages m ON m.org_id=c.org_id AND m.session_id=c.session_id AND m.thread_id=c.thread_id AND m.id=c.message_id AND m.role='user'
 WHERE c.org_id=$1 AND c.session_id=$2 AND c.thread_id=$3 AND c.client_message_id=$4
 AND c.purpose='review-loop-initial-send' AND c.owner_id=$5 AND c.version=$6 AND c.digest=$7 AND c.message_id=$8`,
			orgID, e.ref.SessionID, e.threadID, e.key, e.ref.OwnerID, e.version, e.digest, s.messageID).Scan(&stampAt)
		if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			return ReviewLoopStartupIntent{}, false, nil
		}
		if err != nil {
			return ReviewLoopStartupIntent{}, false, err
		}
		if stampAt.IsZero() || s.operation == "observe_auto" && !stampAt.Equal(s.stampAt) {
			return ReviewLoopStartupIntent{}, false, nil
		}
		var pointer *int64
		err = tx.QueryRow(ctx, `SELECT review_message_id FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4 FOR UPDATE`, orgID, e.ref.SessionID, e.ref.OwnerID, e.passID).Scan(&pointer)
		if err != nil {
			return ReviewLoopStartupIntent{}, false, err
		}
		if pointer != nil && *pointer != s.messageID {
			return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartupPointerConflict
		}
	}
	updated, changed, err := updateReviewLoopStartup(ctx, orgID, tx, i, s, uuid.Nil)
	if err != nil || !changed {
		return ReviewLoopStartupIntent{}, false, err
	}
	if s.operation == "ack" || s.operation == "observe_auto" {
		tag, err := tx.Exec(ctx, `UPDATE session_review_loop_passes SET review_message_id=$5
 WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4 AND (review_message_id IS NULL OR review_message_id=$5)`, orgID, e.ref.SessionID, e.ref.OwnerID, e.passID, s.messageID)
		if err != nil {
			return ReviewLoopStartupIntent{}, false, err
		}
		if tag.RowsAffected() != 1 {
			return ReviewLoopStartupIntent{}, false, ErrReviewLoopStartupPointerConflict
		}
	}
	if s.operation == "refuse" {
		var running bool
		if err := tx.QueryRow(ctx, `SELECT status='running' FROM session_review_loops
 WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, e.ref.SessionID, e.ref.OwnerID).Scan(&running); err != nil {
			return ReviewLoopStartupIntent{}, false, err
		}
		if running {
			if err := markLoopTerminalOn(ctx, tx, orgID, e.ref.OwnerID, models.ReviewLoopStatusFailed, "Review startup was refused."); err != nil {
				return ReviewLoopStartupIntent{}, false, err
			}
			// The terminal writer advances writer_seq while retaining the full
			// startup receipt. Return that committed generation to the caller.
			return ReadReviewLoopStartupInTx(ctx, orgID, e.ref.SessionID, e.ref.OwnerID, tx)
		}
	}
	return updated, true, nil
}

func MarkReviewLoopStartupPointerConflictInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartupIntent) error {
	i, found, err := lockReviewLoopStartup(ctx, orgID, tx, expected)
	if err != nil || !found {
		return err
	}
	e := i.owner
	if e.state != "attempting" && e.state != "unknown" && e.state != "attention" {
		return nil
	}
	var pointer *int64
	err = tx.QueryRow(ctx, `SELECT review_message_id FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3 AND id=$4 FOR UPDATE`, orgID, e.ref.SessionID, e.ref.OwnerID, e.passID).Scan(&pointer)
	if err != nil {
		return err
	}
	if pointer == nil {
		return nil
	}
	_, _, err = updateReviewLoopStartup(ctx, orgID, tx, i, ReviewLoopStartupSettlement{operation: "pointer_conflict"}, uuid.Nil)
	return err
}
