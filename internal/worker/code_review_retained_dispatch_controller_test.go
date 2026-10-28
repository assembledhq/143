package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// These CPU fixtures run the actual controller and DB readers against SQL
// expectations. The ordered Run acknowledgements model the caller boundary;
// they do not prove PostgreSQL commits, lease ownership, isolation or receipts.
// Positive retained correlation is obtained through actual supplied-Tx readers,
// never by forging a sealed DB/observer result or widening its public surface.
type retainedDispatchCPUTransactions struct {
	t      *testing.T
	mock   pgxmock.PgxPoolIface
	inside bool
	calls  int
	events []string
	acks   map[int]error
}

func (r *retainedDispatchCPUTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.t.Helper()
	require.False(r.t, r.inside, "reviewer orchestration must not nest transaction runners")
	// Mirror the existing owner's callback deadline, preserving any earlier
	// caller deadline. This is test composition, not a new production budget
	// or evidence that the mocked transaction was configured or committed.
	unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	r.calls++
	unit := r.calls
	r.events = append(r.events, fmt.Sprintf("run:%d", unit))
	r.inside = true
	err := work(unitCtx, r.mock)
	if err == nil {
		err = unitCtx.Err()
	}
	r.inside = false
	if err != nil {
		r.events = append(r.events, fmt.Sprintf("work-error:%d", unit))
		return err
	}
	if err := r.acks[unit]; err != nil {
		r.events = append(r.events, fmt.Sprintf("ack-error:%d", unit))
		return err
	}
	r.events = append(r.events, fmt.Sprintf("ack:%d", unit))
	return nil
}

type retainedDispatchCPUSender struct {
	t      *testing.T
	runner *retainedDispatchCPUTransactions
	inputs []threadsvc.SendMessageInput
	err    error
	result *threadsvc.SendMessageResult
}

func (s *retainedDispatchCPUSender) SendMessage(_ context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	s.t.Helper()
	require.False(s.t, s.runner.inside, "the sole thread send must run after the claim unit has released transaction ownership")
	s.runner.events = append(s.runner.events, "send")
	s.inputs = append(s.inputs, input)
	return s.result, s.err
}

type retainedDispatchCPUFixture struct {
	job           runCodeReviewPayload
	resultID      uuid.UUID
	threadID      uuid.UUID
	command       threadsvc.CallerSendCommand
	raw           []byte
	attemptID     uuid.UUID
	mintedAttempt *uuid.UUID
	state         db.ReviewerDispatchState
	seq           int32
	writer        int32
	status        string
	eligible      bool
	due           bool
	now           time.Time
}

func newRetainedDispatchCPUFixture(t *testing.T) retainedDispatchCPUFixture {
	t.Helper()
	f := retainedDispatchCPUFixture{
		job: runCodeReviewPayload{
			OrgID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New(),
			RepositoryID: uuid.New(), PullRequestID: uuid.New(), PolicyID: uuid.New(),
			HeadSHA: "synthetic-original-head", OutputKey: "synthetic-original-review",
		},
		resultID: uuid.New(), threadID: uuid.New(), state: db.ReviewerDispatchPending,
		status: "queued", eligible: true, due: true,
		now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	changeset, user := uuid.New(), uuid.New()
	continuation := "retained-original-continuation"
	input := threadsvc.SendMessageInput{
		OrgID: f.job.OrgID, SessionID: f.job.SessionID, ThreadID: f.threadID,
		ChangesetID: &changeset, UserID: &user, Message: "retained original reviewer prompt",
		Images: []string{"synthetic/image-one.png"}, PlanMode: false,
		MessageSource:     models.SessionMessageSourceCodeReview,
		PostSuccessAction: "", PostSuccessAuthorMode: "",
		ContinuationDedupeKeyOverride: &continuation,
	}
	var err error
	f.command, err = threadsvc.NewCallerSendCommand(threadsvc.CallerSendCodeReviewReviewerDispatch, f.resultID, input)
	require.NoError(t, err, "CPU controller fixture must start with one valid complete frozen command")
	f.raw, err = f.command.Encode()
	require.NoError(t, err, "retain the exact canonical command bytes before running the controller")
	return f
}

func (f retainedDispatchCPUFixture) retainedRows() *pgxmock.Rows {
	version, key, digest, raw, state := int16(1), f.command.ClientMessageID(), f.command.Digest(), string(f.raw), string(f.state)
	var attempt any
	if f.attemptID != uuid.Nil {
		value := f.attemptID
		attempt = &value
	}
	if f.mintedAttempt != nil {
		attempt = f.mintedAttempt
	}
	return pgxmock.NewRows([]string{"id", "org", "session", "version", "key", "digest", "thread", "raw", "raw_bytes", "nonnull", "state", "attempt", "seq", "writer", "ordinary_thread", "pass", "reviewer", "valid"}).
		AddRow(f.resultID, f.job.OrgID, f.job.SessionID, &version, &key, &digest, &f.threadID, &raw, len(raw), 11, &state, attempt, &f.seq, &f.writer, nil, nil, true, true)
}

func (f retainedDispatchCPUFixture) intentRows(attemptOverride ...*uuid.UUID) *pgxmock.Rows {
	version, key, digest, state := int16(1), f.command.ClientMessageID(), f.command.Digest(), string(f.state)
	var attempt, attempted, due, deadline, attention, observed, message, detail, resolution any
	if f.attemptID != uuid.Nil {
		value := f.attemptID
		attempt, attempted = &value, &f.now
	}
	if f.mintedAttempt != nil {
		attempt, attempted = f.mintedAttempt, &f.now
	}
	if len(attemptOverride) == 1 {
		attempt, attempted = attemptOverride[0], &f.now
	}
	if f.state == db.ReviewerDispatchPending || f.state == db.ReviewerDispatchAttempting || f.state == db.ReviewerDispatchUnknown || f.state == db.ReviewerDispatchAttention {
		value := f.now.Add(time.Minute)
		due = &value
	}
	if f.state == db.ReviewerDispatchUnknown || f.state == db.ReviewerDispatchAttention {
		value, reason := f.now.Add(15*time.Minute), "unknown_ack"
		deadline, detail = &value, &reason
	}
	if f.state == db.ReviewerDispatchPending && f.seq > 0 {
		reason := "normalizing"
		detail = &reason
	}
	if f.state == db.ReviewerDispatchAttention {
		value := f.now.Add(16 * time.Minute)
		attention = &value
	}
	if f.state == db.ReviewerDispatchAccepted {
		id, reason := int64(73), "send_ack"
		message, resolution = &id, &reason
	}
	if f.state == db.ReviewerDispatchRefused {
		reason, code := "send_refusal", "stamped_active_unsupported"
		resolution, detail = &reason, &code
	}
	if f.state == db.ReviewerDispatchCancelled {
		reason := "owner_ineligible"
		resolution = &reason
	}
	return pgxmock.NewRows([]string{"version", "key", "digest", "thread", "state", "attempt", "seq", "writer", "attempted", "due", "deadline", "attention", "observed", "updated", "message", "detail", "resolution", "status", "eligible", "is_due", "valid"}).
		AddRow(&version, &key, &digest, &f.threadID, &state, attempt, &f.seq, &f.writer, attempted, due, deadline, attention, observed, &f.now, message, detail, resolution, f.status, f.eligible, f.due, true)
}

func expectRetainedDispatchCPUOwner(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(`(?s)^SELECT id,org_id,session_id,dispatch_version,.* FROM code_review_agent_results WHERE org_id=\$1 AND session_id=\$2 AND id=\$3$`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, 8<<20).WillReturnRows(f.retainedRows())
}

func expectRetainedDispatchCPUIntent(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(`(?s)^SELECT r\.dispatch_version,.* FROM code_review_agent_results r JOIN code_review_session_metadata m .* WHERE r\.org_id=\$1 AND r\.session_id=\$2 AND r\.id=\$3 AND m\.id=\$4 AND r\.role='reviewer'$`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, f.job.MetadataID).WillReturnRows(f.intentRows())
}

func expectRetainedDispatchCPUScope(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(`(?s)^SELECT true FROM code_review_agent_results r JOIN code_review_session_metadata m .* AND r\.dispatch_wake->'payload'->>'review_output_key'=\$5$`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, f.job.MetadataID, f.job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"matches"}).AddRow(true))
}

func expectRetainedDispatchCPURead(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	expectRetainedDispatchCPUOwner(mock, f)
	expectRetainedDispatchCPUScope(mock, f)
	expectRetainedDispatchCPUIntent(mock, f)
}

func expectRetainedDispatchCPULocksOnly(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR SHARE`)).
		WithArgs(f.job.OrgID, f.job.SessionID, f.job.MetadataID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.job.MetadataID))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 AND role='reviewer' FOR UPDATE`)).
		WithArgs(f.job.OrgID, f.job.SessionID, f.resultID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.resultID))
}

func expectRetainedDispatchCPULocks(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	expectRetainedDispatchCPULocksOnly(mock, f)
	expectRetainedDispatchCPUIntent(mock, f)
}

func expectRetainedDispatchCPUCompletionAbsent(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(`(?s)^SELECT a\.id,COALESCE\(a\.xmin::text,''\),.* FROM code_review_session_metadata m LEFT JOIN code_review_revision_assessments a .* WHERE m\.org_id=\$1 AND m\.id=\$2$`).
		WithArgs(f.job.OrgID, f.job.MetadataID, 8<<20).
		WillReturnRows(pgxmock.NewRows([]string{"id", "version", "structural", "fits", "receipt", "digest", "head", "review_id"}).AddRow(nil, "", false, true, nil, "", "", nil))
}

func newRetainedDispatchCPUHarness(t *testing.T) (pgxmock.PgxPoolIface, *retainedDispatchCPUTransactions, *retainedDispatchCPUSender) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "each parallel controller case requires an isolated SQL expectation pool")
	t.Cleanup(mock.Close)
	runner := &retainedDispatchCPUTransactions{t: t, mock: mock, acks: make(map[int]error)}
	sender := &retainedDispatchCPUSender{t: t, runner: runner, result: &threadsvc.SendMessageResult{Message: &models.SessionMessage{ID: 73}}}
	return mock, runner, sender
}

type retainedDispatchCPUAttemptCapture struct{ value *uuid.UUID }

func (c retainedDispatchCPUAttemptCapture) Match(value any) bool {
	attempt, ok := value.(uuid.UUID)
	if !ok || attempt == uuid.Nil || attempt == *c.value {
		return false
	}
	*c.value = attempt
	return true
}

type retainedDispatchCPUExactAttempt struct{ value *uuid.UUID }

func (c retainedDispatchCPUExactAttempt) Match(value any) bool {
	attempt, ok := value.(uuid.UUID)
	return ok && *c.value != uuid.Nil && attempt == *c.value
}

func expectRetainedDispatchCPUUpdate(mock pgxmock.PgxPoolIface, old retainedDispatchCPUFixture, operation, detail string, messageID int64, attempt any) {
	var previous any
	if old.attemptID != uuid.Nil {
		previous = old.attemptID
	}
	if old.mintedAttempt != nil {
		previous = retainedDispatchCPUExactAttempt{value: old.mintedAttempt}
	}
	mock.ExpectQuery(`(?s)^UPDATE code_review_agent_results r SET dispatch_state=.* RETURNING r\.id$`).
		WithArgs(old.job.OrgID, old.job.SessionID, old.resultID, old.job.MetadataID, old.threadID,
			old.command.ClientMessageID(), int16(1), old.command.Digest(), string(old.state), previous,
			old.seq, old.writer, operation, detail, messageID, attempt).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(old.resultID))
}

// The dynamically minted attempt flows through the actual primitive's UPDATE
// argument into the returned SQL row, avoiding any test-only authority factory.
func expectRetainedDispatchCPUClaim(mock pgxmock.PgxPoolIface, pending retainedDispatchCPUFixture, attempt *uuid.UUID) retainedDispatchCPUFixture {
	expectRetainedDispatchCPURead(mock, pending)
	expectRetainedDispatchCPULocks(mock, pending)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT statement_timestamp()<$1::timestamptz`)).
		WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"before"}).AddRow(true))
	// The controller-specific deadline guard reuses the primitive, so its
	// second ordered read belongs to the same unit and same exact expectation.
	expectRetainedDispatchCPULocks(mock, pending)
	expectRetainedDispatchCPUCompletionAbsent(mock, pending)
	expectRetainedDispatchCPUUpdate(mock, pending, "claim", "", 0, retainedDispatchCPUAttemptCapture{value: attempt})
	claimed := pending
	claimed.state, claimed.seq, claimed.writer, claimed.due = db.ReviewerDispatchAttempting, pending.seq+1, pending.writer+1, false
	claimed.mintedAttempt = attempt
	mock.ExpectQuery(`(?s)^SELECT r\.dispatch_version,.* FROM code_review_agent_results r JOIN code_review_session_metadata m .* WHERE r\.org_id=\$1 AND r\.session_id=\$2 AND r\.id=\$3 AND m\.id=\$4 AND r\.role='reviewer'$`).
		WithArgs(pending.job.OrgID, pending.job.SessionID, pending.resultID, pending.job.MetadataID).
		WillReturnRows(claimed.intentRows(attempt))
	return claimed
}

func expectRetainedDispatchCPUSettlement(mock pgxmock.PgxPoolIface, claimed retainedDispatchCPUFixture, operation, detail string, next db.ReviewerDispatchState) retainedDispatchCPUFixture {
	expectRetainedDispatchCPURead(mock, claimed)
	expectRetainedDispatchCPULocks(mock, claimed)
	expectRetainedDispatchCPUUpdate(mock, claimed, operation, detail, 0, uuid.Nil)
	settled := claimed
	settled.state, settled.writer, settled.due = next, claimed.writer+1, false
	expectRetainedDispatchCPUIntent(mock, settled)
	return settled
}

func TestRetainedReviewerControllerClaimAcknowledgementIsSendGate(t *testing.T) {
	t.Parallel()
	ackLost := errors.New("synthetic claim acknowledgement unavailable")
	claimFailed := errors.New("synthetic claim storage refusal")
	tests := []struct {
		name       string
		claimError error
		claimACK   error
		sends      int
		events     []string
	}{
		{
			name: "known claim acknowledgement releases one send then separate unknown settlement", sends: 1,
			events: []string{"run:1", "ack:1", "run:2", "ack:2", "send", "run:3", "ack:3"},
		},
		{
			name: "unknown claim acknowledgement releases no send", claimACK: ackLost,
			events: []string{"run:1", "ack:1", "run:2", "ack-error:2"},
		},
		{
			name: "failed claim callback releases no send", claimError: claimFailed,
			events: []string{"run:1", "ack:1", "run:2", "work-error:2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			sender.err = &threadsvc.CommandOutcomeUnknownError{ClientMessageID: f.command.ClientMessageID(), Cause: errors.New("synthetic send acknowledgement unavailable")}
			expectRetainedDispatchCPURead(mock, f)
			var attempt uuid.UUID
			if tt.claimError != nil {
				expectRetainedDispatchCPUOwner(mock, f)
				mock.ExpectQuery(`(?s)^SELECT true FROM code_review_agent_results r JOIN code_review_session_metadata m .*`).
					WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, f.job.MetadataID, f.job.OutputKey).WillReturnError(tt.claimError)
			} else {
				claimed := expectRetainedDispatchCPUClaim(mock, f, &attempt)
				if tt.claimACK == nil {
					expectRetainedDispatchCPUSettlement(mock, claimed, "unknown", "", db.ReviewerDispatchUnknown)
				}
			}
			runner.acks[2] = tt.claimACK
			err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
			if tt.claimError != nil {
				require.ErrorIs(t, err, tt.claimError, "storage refusal during the exact claim unit cannot authorize a thread send")
			} else if tt.claimACK != nil {
				require.ErrorIs(t, err, tt.claimACK, "provisional claiming state must be discarded after a lost claim acknowledgement")
			} else {
				require.NoError(t, err, "one known claim may send once and retain an uncertain transport outcome separately")
			}
			require.Len(t, sender.inputs, tt.sends, "only the exact known claim acknowledgement may release the one original send")
			require.Equal(t, tt.events, runner.events, "send ordering must separate the read, known claim and settlement units")
			require.NoError(t, mock.ExpectationsWereMet(), "the controller must not borrow authority from extra reads, fallback creation or another transaction owner")
		})
	}
}

func TestRetainedReviewerControllerDoesNotSendFromNonPendingEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   db.ReviewerDispatchState
		expired bool
		legacy  bool
	}{
		{name: "legacy result remains with the original legacy controller", legacy: true},
		{name: "pending owner beyond contribution deadline", state: db.ReviewerDispatchPending, expired: true},
		{name: "attempting owner before stale due time", state: db.ReviewerDispatchAttempting},
		{name: "unknown send before observation due time", state: db.ReviewerDispatchUnknown},
		{name: "attention before observation due time", state: db.ReviewerDispatchAttention},
		{name: "accepted owner is never sent again", state: db.ReviewerDispatchAccepted},
		{name: "refused owner is not another send", state: db.ReviewerDispatchRefused},
		{name: "cancelled owner is not another send", state: db.ReviewerDispatchCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			f.state, f.due = tt.state, false
			if tt.state != db.ReviewerDispatchPending && tt.state != db.ReviewerDispatchCancelled && !tt.legacy {
				f.attemptID, f.seq, f.writer = uuid.New(), 1, 2
			}
			if tt.legacy {
				mock.ExpectQuery(`(?s)^SELECT id,org_id,session_id,dispatch_version,.* FROM code_review_agent_results WHERE org_id=\$1 AND session_id=\$2 AND id=\$3$`).
					WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, 8<<20).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org", "session", "version", "key", "digest", "thread", "raw", "raw_bytes", "nonnull", "state", "attempt", "seq", "writer", "ordinary_thread", "pass", "reviewer", "valid"}).
						AddRow(f.resultID, f.job.OrgID, f.job.SessionID, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil, nil, nil, nil, true, false))
			} else {
				expectRetainedDispatchCPURead(mock, f)
			}
			deadline := time.Now().Add(time.Hour)
			if tt.expired {
				deadline = time.Now().Add(-time.Hour)
			}
			err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, deadline)
			require.NoError(t, err, "nonpending or already closed contributions must remain read-only at this controller boundary")
			require.Empty(t, sender.inputs, "a retained uncertain, settled, cancelled or legacy owner cannot authorize another send")
			require.Equal(t, []string{"run:1", "ack:1"}, runner.events, "a safe no-op must not claim, settle, rerender or fall back")
			require.NoError(t, mock.ExpectationsWereMet(), "no-op handling must use only its original bounded retained owner read")
		})
	}
}

func TestRetainedReviewerControllerStaleOrCancelledClaimCannotSend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cancelled bool
	}{
		{name: "new writer sequence defeats original claim expectation"},
		{name: "cancellation wins before exact claim", cancelled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			expectRetainedDispatchCPURead(mock, f)
			current := f
			current.writer++
			if tt.cancelled {
				current.state, current.eligible = db.ReviewerDispatchCancelled, false
			}
			expectRetainedDispatchCPURead(mock, current)
			expectRetainedDispatchCPULocks(mock, current)
			err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
			require.NoError(t, err, "a superseded original claim expectation is a finite no-op, not fallback authority")
			require.Empty(t, sender.inputs, "cancelled metadata or changed retained owner sequence must release no send")
			require.Equal(t, []string{"run:1", "ack:1", "run:2", "ack:2"}, runner.events, "a failed exact owner match must stop before a claim write or thread call")
			require.NoError(t, mock.ExpectationsWereMet(), "stale expectations cannot borrow the new owner identity or perform an unfenced mutation")
		})
	}
}

func TestRetainedReviewerControllerLostSettlementAcknowledgementNeverResends(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		joined bool
	}{
		{name: "unknown settlement acknowledgement"},
		{name: "unknown send wrapping a normalizing known-abort cause", joined: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			cause := errors.New("synthetic unknown send cause")
			if tt.joined {
				cause = threadsvc.ErrSendNormalizing
			}
			sender.err = &threadsvc.CommandOutcomeUnknownError{ClientMessageID: f.command.ClientMessageID(), Cause: cause}
			ackLost := errors.New("synthetic settlement acknowledgement unavailable")
			runner.acks[3] = ackLost
			expectRetainedDispatchCPURead(mock, f)
			var attempt uuid.UUID
			claimed := expectRetainedDispatchCPUClaim(mock, f, &attempt)
			expectRetainedDispatchCPUSettlement(mock, claimed, "unknown", "", db.ReviewerDispatchUnknown)
			// A later invocation sees the original attempted identity, not a
			// fabricated negative receipt or a fresh command selected by policy.
			expectRetainedDispatchCPURead(mock, claimed)
			err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
			require.ErrorIs(t, err, ackLost, "uncertain settlement must be surfaced without granting another send")
			err = dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
			require.NoError(t, err, "resuming a not-yet-due original attempt is an observe-only no-op")
			require.Len(t, sender.inputs, 1, "unknown send or settlement acknowledgement must never resend the retained command")
			expectedInput := f.command.SendInput()
			proof, proofErr := f.command.Proof()
			require.NoError(t, proofErr, "the original command supplies only its existing opaque correlation proof")
			expectedInput.CallerCommand = proof
			require.Equal(t, []threadsvc.SendMessageInput{expectedInput}, sender.inputs, "no current prompt, lane, reviewer key or input option can replace the exact frozen send")
			require.Equal(t, []string{"run:1", "ack:1", "run:2", "ack:2", "send", "run:3", "ack-error:3", "run:4", "ack:4"}, runner.events, "re-entry after unknown settlement must stop before another claim or send")
			require.NoError(t, mock.ExpectationsWereMet(), "lost settlement acknowledgement must not cause compensation, a retry-pending write or a new owner")
		})
	}
}

func TestRetainedReviewerControllerKnownAbortRetriesOnlyFrozenCommand(t *testing.T) {
	t.Parallel()
	f := newRetainedDispatchCPUFixture(t)
	mock, runner, sender := newRetainedDispatchCPUHarness(t)
	sender.err = threadsvc.ErrSendNormalizing
	expectRetainedDispatchCPURead(mock, f)
	var firstAttempt, secondAttempt uuid.UUID
	firstClaim := expectRetainedDispatchCPUClaim(mock, f, &firstAttempt)
	pending := expectRetainedDispatchCPUSettlement(mock, firstClaim, "retry", "normalizing", db.ReviewerDispatchPending)
	// The database decides when the retained retry becomes due. A separate
	// controller invocation may claim that same frozen owner, not a new render.
	pending.due = true
	expectRetainedDispatchCPURead(mock, pending)
	secondClaim := expectRetainedDispatchCPUClaim(mock, pending, &secondAttempt)
	expectRetainedDispatchCPUSettlement(mock, secondClaim, "retry", "normalizing", db.ReviewerDispatchPending)
	for range 2 {
		err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
		require.NoError(t, err, "a positively known abort may retain one same-command retry for a later invocation")
	}
	expected := f.command.SendInput()
	proof, err := f.command.Proof()
	require.NoError(t, err, "the same original command must retain a valid opaque transport descriptor")
	expected.CallerCommand = proof
	require.Equal(t, []threadsvc.SendMessageInput{expected, expected}, sender.inputs, "known abort may change the attempt only, never command key, lane, digest, prompt or other frozen options")
	require.NotEqual(t, firstAttempt, secondAttempt, "each authorized claim must use a new attempt under the same retained command identity")
	require.Equal(t, []string{"run:1", "ack:1", "run:2", "ack:2", "send", "run:3", "ack:3", "run:4", "ack:4", "run:5", "ack:5", "send", "run:6", "ack:6"}, runner.events, "there must be no immediate resend loop inside any claim or settlement unit")
	require.NoError(t, mock.ExpectationsWereMet(), "both retries must use exact retained correlation and original metadata scope")
}

func expectRetainedDispatchCPUAwareOutcome(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture, result models.CodeReviewAgentResult, terminal bool) {
	expectRetainedDispatchCPULocksOnly(mock, f)
	expectRetainedDispatchCPURead(mock, f)
	query := mock.ExpectQuery(`(?s)^UPDATE code_review_agent_results SET status=\$4,raw_output=\$5,structured_result=COALESCE\(\$6::jsonb,structured_result\),.* AND \(\$4<>'running' OR status='queued'\) AND dispatch_writer_seq<2147483647 RETURNING .*`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, models.CodeReviewAgentResultStatusRunning, (*string)(nil), []byte(nil))
	if terminal {
		query.WillReturnRows(retainedDispatchCPUResultRows())
		mock.ExpectQuery(`(?s)^SELECT id, org_id, session_id, agent_provider, agent_model, role, status,.* FROM code_review_agent_results WHERE org_id=\$1 AND session_id=\$2 AND id=\$3 AND role='reviewer'$`).
			WithArgs(f.job.OrgID, f.job.SessionID, f.resultID).WillReturnRows(retainedDispatchCPUResultRows(result))
	} else {
		query.WillReturnRows(retainedDispatchCPUResultRows(result))
	}
}

func TestRetainedReviewerControllerAcknowledgedSendSettlesSeparately(t *testing.T) {
	t.Parallel()
	f := newRetainedDispatchCPUFixture(t)
	mock, runner, sender := newRetainedDispatchCPUHarness(t)
	expectRetainedDispatchCPURead(mock, f)
	var attempt uuid.UUID
	claimed := expectRetainedDispatchCPUClaim(mock, f, &attempt)
	expectRetainedDispatchCPURead(mock, claimed)
	expectRetainedDispatchCPULocks(mock, claimed)
	mock.ExpectQuery(`(?s)^SELECT c\.created_at FROM caller_send_command_stamps c .* WHERE c\.org_id=\$1 AND c\.session_id=\$2 AND c\.thread_id=\$3 AND c\.client_message_id=\$4.*`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.threadID, f.command.ClientMessageID(), f.resultID, int16(1), f.command.Digest(), int64(73)).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}).AddRow(f.now))
	expectRetainedDispatchCPUUpdate(mock, claimed, "ack", "", 73, uuid.Nil)
	accepted := claimed
	accepted.state, accepted.writer = db.ReviewerDispatchAccepted, claimed.writer+1
	expectRetainedDispatchCPUIntent(mock, accepted)
	running := retainedDispatchCPUResult(f)
	running.Status = models.CodeReviewAgentResultStatusRunning
	expectRetainedDispatchCPUAwareOutcome(mock, accepted, running, false)
	err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
	require.NoError(t, err, "a known original send acknowledgement must settle its exact stamp and contribution in a separate unit")
	expected := f.command.SendInput()
	proof, proofErr := f.command.Proof()
	require.NoError(t, proofErr, "the original command must supply its existing nonsemantic proof")
	expected.CallerCommand = proof
	require.Equal(t, []threadsvc.SendMessageInput{expected}, sender.inputs, "the known claim may release exactly the complete original command once")
	require.Equal(t, []string{"run:1", "ack:1", "run:2", "ack:2", "send", "run:3", "ack:3"}, runner.events, "thread service must be outside both claim and acceptance settlement ownership")
	require.NoError(t, mock.ExpectationsWereMet(), "acceptance settlement must revalidate exact typed stamp linkage before aware outcome promotion")
}

func TestRetainedReviewerAwareOutcomePreservesClosedContribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  models.CodeReviewAgentResultStatus
		ackLost bool
	}{
		{name: "late accepted pointer preserves timed out contribution", status: models.CodeReviewAgentResultStatusTimedOut},
		{name: "late accepted pointer preserves failed contribution", status: models.CodeReviewAgentResultStatusFailed},
		{name: "late accepted pointer preserves completed contribution", status: models.CodeReviewAgentResultStatusCompleted},
		{name: "unknown aware outcome acknowledgement exposes no result", status: models.CodeReviewAgentResultStatusTimedOut, ackLost: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			f.state, f.attemptID, f.seq, f.writer, f.status = db.ReviewerDispatchAccepted, uuid.New(), 1, 3, string(tt.status)
			original := retainedDispatchCPUResult(f)
			raw := "synthetic immutable closed contribution"
			original.RawOutput = &raw
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			expectRetainedDispatchCPUAwareOutcome(mock, f, original, true)
			ackLost := errors.New("synthetic aware outcome acknowledgement unavailable")
			if tt.ackLost {
				runner.acks[1] = ackLost
			}
			stores := &Stores{ThreadSendDependencies: threadsvc.SendDependencies{Transactions: runner}}
			actual, err := updateCodeReviewReviewerOutcome(context.Background(), stores, f.job, f.resultID, models.CodeReviewAgentResultStatusRunning, nil, nil, time.Time{})
			if tt.ackLost {
				require.ErrorIs(t, err, ackLost, "unknown acceptance metadata acknowledgement cannot expose a provisional contribution")
				require.Equal(t, models.CodeReviewAgentResult{}, actual, "failed aware outcome settlement must discard every staged result field")
			} else {
				require.NoError(t, err, "positive late admission linkage may be repaired without reopening a closed contribution")
				require.Equal(t, original, actual, "late acceptance cannot change closed status, output, reviewer identity, timestamps or prompt linkage")
			}
			require.Empty(t, sender.inputs, "aware late acceptance bookkeeping never calls the thread service again")
			require.NoError(t, mock.ExpectationsWereMet(), "the aware writer must retain terminal rows instead of issuing an unfenced status overwrite")
		})
	}
}

func expectRetainedDispatchCPUObserveUnresolved(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectExec(regexp.QuoteMeta(`SET TRANSACTION READ ONLY`)).WillReturnResult(pgxmock.NewResult("SET", 0))
	expectWorkerThreadSendSQLPolicy(mock)
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendSharedSQL)).
		WithArgs("session_lifecycle_cutover:" + f.job.OrgID.String() + ":" + f.job.SessionID.String()).
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(workerThreadSendModeSQL)).WithArgs(f.job.OrgID, f.job.SessionID).
		WillReturnRows(pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).
			AddRow(f.job.OrgID, f.job.SessionID, int16(0), nil, nil, nil, nil, nil, nil, nil, nil, nil, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`)))
	expectRetainedDispatchCPUOwner(mock, f)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT true FROM sessions s
 JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3
 AND s.deleted_at IS NULL AND s.archived_at IS NULL AND t.archived_at IS NULL`)).
		WithArgs(f.job.OrgID, f.job.SessionID, f.threadID).
		WillReturnRows(pgxmock.NewRows([]string{"visible"}).AddRow(true))
	mock.ExpectQuery(`(?s)^SELECT c\.message_id,c\.version,CASE WHEN octet_length\(c\.digest\)<=64 THEN c\.digest ELSE '' END,c\.created_at.* WHERE i\.org_id=\$1 AND i\.session_id=\$2 AND i\.thread_id=\$3 AND i\.client_message_id=\$4.*`).
		WithArgs(f.job.OrgID, f.job.SessionID, f.threadID, f.command.ClientMessageID(), db.CallerSendStampCodeReviewDispatch, f.resultID).
		WillReturnRows(pgxmock.NewRows([]string{"message", "version", "digest", "created_at"}))
}

func TestRetainedReviewerControllerUsesReadOnlyObserverWithoutResend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		state      db.ReviewerDispatchState
		observeACK bool
		changed    bool
	}{
		{name: "negative exact receipt remains unknown", state: db.ReviewerDispatchUnknown},
		{name: "negative attention receipt remains attention", state: db.ReviewerDispatchAttention},
		{name: "lost observer acknowledgement cannot settle", state: db.ReviewerDispatchUnknown, observeACK: true},
		{name: "newer observed owner defeats stale settlement expectation", state: db.ReviewerDispatchUnknown, changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			f.state, f.attemptID, f.seq, f.writer = tt.state, uuid.New(), 1, 2
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			expectRetainedDispatchCPURead(mock, f)
			observed := f
			if tt.changed {
				observed.writer++
			}
			expectRetainedDispatchCPUObserveUnresolved(mock, observed)
			ackLost := errors.New("synthetic observer acknowledgement unavailable")
			expectedEvents := []string{"run:1", "ack:1", "run:2", "ack:2"}
			if tt.observeACK {
				runner.acks[2] = ackLost
				expectedEvents[3] = "ack-error:2"
			} else if !tt.changed {
				expectRetainedDispatchCPUSettlement(mock, f, "negative", "", tt.state)
				expectedEvents = append(expectedEvents, "run:3", "ack:3")
			}
			err := dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour))
			if tt.observeACK {
				require.ErrorIs(t, err, threadsvc.ErrCallerSendObservation, "a failed observer boundary must discard staged evidence before any write")
				require.ErrorIs(t, err, ackLost, "observer acknowledgement loss must retain its internal diagnostic cause")
			} else {
				require.NoError(t, err, "negative or superseded exact observation must remain finite observe-only progress")
			}
			require.Empty(t, sender.inputs, "missing linkage, changed ownership or observation failure is never negative-send proof")
			require.Equal(t, expectedEvents, runner.events, "read-only observation must finish before an exact separate settlement CAS")
			require.NoError(t, mock.ExpectationsWereMet(), "observation must use the actual read-only visibility/linkage reader and never rerender or claim another command")
		})
	}
}

func retainedDispatchCPUResult(f retainedDispatchCPUFixture) models.CodeReviewAgentResult {
	model := models.DefaultCodexModel
	return models.CodeReviewAgentResult{
		ID: f.resultID, OrgID: f.job.OrgID, SessionID: f.job.SessionID,
		AgentProvider: string(models.AgentTypeCodex), AgentModel: &model,
		Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatus(f.status),
		StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ReviewerKey: codeReviewReviewerKey(0, models.AgentTypeCodex), ReviewerIndex: 0,
			ThreadID: f.threadID.String(), ReadOnly: true,
		}), CreatedAt: f.now,
	}
}

func retainedDispatchCPUResultRows(results ...models.CodeReviewAgentResult) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "agent_provider", "agent_model", "role", "status", "raw_output", "structured_result", "created_at"})
	for _, result := range results {
		rows.AddRow(result.ID, result.OrgID, result.SessionID, result.AgentProvider, result.AgentModel, result.Role, result.Status, result.RawOutput, result.StructuredResult, result.CreatedAt)
	}
	return rows
}

func expectRetainedDispatchCPUOwnerLookup(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture, results ...models.CodeReviewAgentResult) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3
 AND review_output_key=$4 AND status IN ('queued','running') AND NOT stale FOR UPDATE`)).
		WithArgs(f.job.OrgID, f.job.SessionID, f.job.MetadataID, f.job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.job.MetadataID))
	mock.ExpectQuery(`(?s)^SELECT id, org_id, session_id, agent_provider, agent_model, role, status,.* FROM code_review_agent_results\s+WHERE org_id=\$1 AND session_id=\$2 AND role='reviewer' AND structured_result->>'reviewer_key'=\$3 ORDER BY id LIMIT 2$`).
		WithArgs(f.job.OrgID, f.job.SessionID, codeReviewReviewerKey(0, models.AgentTypeCodex)).
		WillReturnRows(retainedDispatchCPUResultRows(results...))
}

func TestRetainedReviewerControllerFreshModeAndExistingOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		control    string
		enabled    bool
		found      bool
		ackUnknown bool
	}{
		{name: "missing control preserves fresh legacy choice", control: "absent"},
		{name: "disabled control preserves fresh legacy choice", control: "present"},
		{name: "enabled control selects retained owner once", control: "present", enabled: true},
		{name: "existing reviewer key adopts retained original instead of skipping", found: true},
		{name: "unknown preparation read acknowledgement exposes no provisional owner", found: true, ackUnknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			original := retainedDispatchCPUResult(f)
			if tt.found {
				expectRetainedDispatchCPUOwnerLookup(mock, f, original)
				expectRetainedDispatchCPUOwner(mock, f)
				expectRetainedDispatchCPUScope(mock, f)
			} else {
				expectRetainedDispatchCPUOwnerLookup(mock, f)
				query := mock.ExpectQuery(regexp.QuoteMeta(`SELECT schema_ready AND dispatch_status_ready AND intents_enabled FROM caller_send_intent_control WHERE singleton FOR SHARE`))
				if tt.control == "absent" {
					query.WillReturnRows(pgxmock.NewRows([]string{"enabled"}))
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(tt.enabled))
				}
			}
			ackLost := errors.New("synthetic owner preparation acknowledgement unavailable")
			if tt.ackUnknown {
				runner.acks[1] = ackLost
			}
			if tt.found {
				candidate := original
				candidate.ID = uuid.New()
				candidate.AgentProvider, candidate.AgentModel = string(models.AgentTypeClaudeCode), nil
				candidate.StructuredResult = json.RawMessage(`{"reviewer_key":"changed-current-roster"}`)
				// A zero command is deliberate: adoption must happen before any
				// encoding/rendering of the caller's now-changed candidate.
				actual, retained, err := prepareCodeReviewReviewerOwner(context.Background(), runner, f.job,
					codeReviewReviewerKey(0, models.AgentTypeCodex), &candidate, threadsvc.CallerSendCommand{}, nil, true)
				if tt.ackUnknown {
					require.ErrorIs(t, err, ackLost, "unknown owner-read acknowledgement must not expose an adopted preparation result")
					require.Equal(t, models.CodeReviewAgentResult{}, actual, "a provisional owner cannot escape a failed outer preparation boundary")
					require.False(t, retained, "uncertain preparation cannot authorize dispatch of a retained command")
				} else {
					require.NoError(t, err, "the exact preexisting reviewer owner must resume without rendering a replacement candidate")
					require.True(t, retained, "an original intent-bearing result never falls back to legacy creation")
					require.Equal(t, original, actual, "current roster/provider/model mutations must not replace the retained original result")
				}
			} else {
				owner, err := readCodeReviewReviewerOwner(context.Background(), runner, f.job, codeReviewReviewerKey(0, models.AgentTypeCodex))
				require.NoError(t, err, "fresh mode selection must distinguish a genuinely absent or disabled control from an observation error")
				require.Equal(t, db.CodeReviewReviewerOwner{IntentsEnabled: tt.enabled}, owner, "only the fresh owner control observation may choose legacy versus retained creation")
			}
			require.Empty(t, sender.inputs, "mode selection and owner adoption must not send while their preparation transaction is owned")
			require.Equal(t, 1, runner.calls, "fresh selection or exact existing-owner adoption must use one bounded metadata-owned read unit")
			require.NoError(t, mock.ExpectationsWereMet(), "existing identity adoption must not insert another result or consult a new control mode")
		})
	}
}

func TestRetainedReviewerControllerPreparationIsAtomicAndNeverFallsBack(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		disabled   bool
		ackUnknown bool
	}{
		{name: "known preparation then known claim sends original once"},
		{name: "unknown preparation acknowledgement exposes no sendable owner", ackUnknown: true},
		{name: "selected retained mode cannot fall back when control changes", disabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			result := retainedDispatchCPUResult(f)
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "freeze the original actual controller payload before any owner creation")
			wake, err := freezeCodeReviewReviewerDispatchWake(f.job, payload)
			require.NoError(t, err, "retain the original wake rather than reconstructing it from future policy")
			expectRetainedDispatchCPUOwnerLookup(mock, f)
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT schema_ready AND dispatch_status_ready AND intents_enabled FROM caller_send_intent_control WHERE singleton FOR SHARE`)).
				WillReturnRows(pgxmock.NewRows([]string{"enabled"}).AddRow(!tt.disabled))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT m.id FROM code_review_session_metadata m
 WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$3
 AND m.status IN ('queued','running') AND NOT m.stale AND m.review_output_key=$4
 FOR SHARE OF m`)).
				WithArgs(f.job.OrgID, f.job.SessionID, f.job.MetadataID, f.job.OutputKey).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.job.MetadataID))
			expectRetainedDispatchCPUCompletionAbsent(mock, f)
			persisted, marshalErr := json.Marshal(result)
			require.NoError(t, marshalErr, "the mock atomic insert returns the complete exact original reviewer row")
			var inserted any = persisted
			if tt.disabled {
				inserted = nil
			}
			mock.ExpectQuery(`(?s)^WITH document AS MATERIALIZED \(.*inserted_wake AS \(.*SELECT EXISTS\(SELECT 1 FROM owner\).*`).
				WithArgs(f.job.OrgID, f.job.SessionID, f.job.MetadataID, f.resultID, f.threadID,
					result.AgentProvider, result.AgentModel, f.command.ClientMessageID(), f.command.Digest(), f.raw,
					[]byte(wake), f.job.OutputKey, 8<<20, result.RawOutput, []byte(result.StructuredResult), true,
					false, uuid.Nil, "", false).
				WillReturnRows(pgxmock.NewRows([]string{"owner", "enabled", "fits", "result", "wake_id"}).
					AddRow(true, !tt.disabled, true, inserted, uuid.New()))
			ackLost := errors.New("synthetic preparation acknowledgement unavailable")
			if tt.ackUnknown {
				runner.acks[1] = ackLost
			}
			if !tt.ackUnknown && !tt.disabled {
				expectRetainedDispatchCPURead(mock, f)
				var attempt uuid.UUID
				claimed := expectRetainedDispatchCPUClaim(mock, f, &attempt)
				expectRetainedDispatchCPUSettlement(mock, claimed, "unknown", "", db.ReviewerDispatchUnknown)
			}
			prepared, retained, err := prepareCodeReviewReviewerOwner(context.Background(), runner, f.job,
				codeReviewReviewerKey(0, models.AgentTypeCodex), &result, f.command, wake, true)
			if tt.ackUnknown || tt.disabled {
				if tt.ackUnknown {
					require.ErrorIs(t, err, ackLost, "a provisional preparation must be discarded after unknown commit acknowledgement")
				} else {
					require.ErrorIs(t, err, db.ErrCodeReviewDispatchPreparationDisabled, "a selected retained path must refuse control loss rather than downgrade to legacy")
				}
				require.Equal(t, models.CodeReviewAgentResult{}, prepared, "failed preparation exposes no provisional result or wake identity")
				require.False(t, retained, "failed preparation cannot release retained dispatch authority")
				require.Empty(t, sender.inputs, "preparation failure or uncertainty must not send, retry with a fresh identity or select legacy")
				require.Equal(t, 1, runner.calls, "failed preparation must stop before a claim or a second creator")
			} else {
				require.NoError(t, err, "atomic result, frozen intent and wake preparation must complete before claiming")
				require.True(t, retained, "known preparation retains the originally selected S1 mode")
				require.Equal(t, result, prepared, "the committed preparation must preserve the complete original reviewer identity")
				sender.err = &threadsvc.CommandOutcomeUnknownError{ClientMessageID: f.command.ClientMessageID()}
				err = dispatchRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, prepared.ID, time.Now().Add(time.Hour))
				require.NoError(t, err, "one known preparation and one known claim may make the sole original send")
				require.Equal(t, []string{"run:1", "ack:1", "run:2", "ack:2", "run:3", "ack:3", "send", "run:4", "ack:4"}, runner.events, "preparation, claim, external send and settlement must stay separate ordered boundaries")
				require.Len(t, sender.inputs, 1, "the actual preparation-to-dispatch path releases exactly one send after both known acknowledgements")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the actual atomic prepare statement must install result, intent and original wake without a legacy insert")
		})
	}
}

func TestRetainedReviewerPartialEvidencePreservesQuorumAndFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     models.CodeReviewAgentResultStatus
		unresolved bool
		filled     int
		selections []codeReviewReviewerSelection
		quorum     int
		failures   int
		coverage   bool
	}{
		{name: "unresolved timed out contribution occupies its original slot", status: models.CodeReviewAgentResultStatusTimedOut, unresolved: true, filled: 2, selections: []codeReviewReviewerSelection{}, quorum: 1, failures: 1},
		{name: "known failed contribution retains ordinary fallback", status: models.CodeReviewAgentResultStatusFailed, filled: 1, selections: []codeReviewReviewerSelection{{Index: 2, AgentType: models.AgentTypeOpenCode, Available: true}}, quorum: 1, failures: 1},
		{name: "completed panel remains reusable control", status: models.CodeReviewAgentResultStatusCompleted, filled: 2, selections: []codeReviewReviewerSelection{}, quorum: 2, coverage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := models.CodeReviewPolicyConfig{AgentRoster: models.CodeReviewAgentRoster{
				Reviewers: []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeOpenCode}, ReviewerCount: 2, RequireReviewerQuorum: 2,
			}}
			first := models.CodeReviewAgentResult{ID: uuid.New(), AgentProvider: string(models.AgentTypeCodex), Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted,
				StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(0, models.AgentTypeCodex), ReadOnly: true})}
			fields := map[string]any{"reviewer_key": codeReviewReviewerKey(1, models.AgentTypeClaudeCode), "reviewer_index": 1, "thread_id": uuid.NewString(), "read_only": true}
			if tt.unresolved {
				fields["error"], fields["dispatch_unresolved"] = "dispatch_unresolved", true
			}
			structured, err := json.Marshal(fields)
			require.NoError(t, err, "each case creates its own exact reviewer contribution presentation")
			second := models.CodeReviewAgentResult{ID: uuid.New(), AgentProvider: string(models.AgentTypeClaudeCode), Role: models.CodeReviewAgentRoleReviewer, Status: tt.status, StructuredResult: structured}
			results := []models.CodeReviewAgentResult{first, second}
			require.Equal(t, tt.filled, codeReviewReviewerSlotsFilled(cfg, results), "retained uncertainty cannot be interpreted as free capacity for a replacement command")
			selections, err := resolveCodeReviewReviewerAvailability(context.Background(), nil, uuid.New(), cfg, results, false)
			require.NoError(t, err, "retained occupancy must compose with the existing ranked availability resolver")
			require.Equal(t, tt.selections, selections, "only a positively known failure may expose the next unused fallback reviewer")
			quorum, failures := codeReviewReviewerEvidence(results)
			require.Equal(t, [3]int{2, tt.quorum, tt.failures}, [3]int{codeReviewRequiredReviewerQuorum(cfg, results), quorum, failures}, "dispatch uncertainty must not lower the configured quorum or counterfeit completed evidence")
			require.True(t, codeReviewReviewerRosterTerminal(cfg, results, true), "closed deadline contributions must permit progress without perpetual reviewer polling")
			require.False(t, codeReviewReviewerExecutionFailed(cfg, results, true), "existing usable partial evidence must continue to the synthesis path without selecting either zero-output policy")
			synthesis := codeReviewOrchestratorSynthesis{Summary: "synthetic summary", ReviewSummary: "synthetic review", DescriptionAssessments: []codeReviewDescriptionAssessment{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}
			orchestrator := models.CodeReviewAgentResult{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString(), ReadOnly: true, SynthesisValidated: true, Synthesis: synthesis})}
			withSynthesis := append(append([]models.CodeReviewAgentResult(nil), results...), orchestrator)
			require.Equal(t, tt.coverage, fullAssessmentCoverage(cfg, withSynthesis, codereviewsvc.ReviewInputManifest{ReuseEligible: true}), "a valid synthesis cannot turn an incomplete reviewer panel into reusable full coverage")
		})
	}
}
