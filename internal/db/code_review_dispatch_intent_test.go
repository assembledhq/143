package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func reviewerDispatchCPUIntent() ReviewerDispatchIntent {
	stamp := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
	e := ReviewerDispatchExpectation{metadataID: uuid.MustParse("77777777-7777-4777-8777-777777777777"), orgID: stamp.OrgID, sessionID: stamp.Scope.SessionID, resultID: stamp.Scope.OwnerID, threadID: stamp.Scope.ThreadID, key: stamp.Scope.ClientMessageID, digest: stamp.Digest, version: 1, state: ReviewerDispatchPending}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return ReviewerDispatchIntent{expectation: e, dueAt: now, updatedAt: now, status: "queued", metadataEligible: true, due: true}
}

func reviewerDispatchCPURows(i ReviewerDispatchIntent) *pgxmock.Rows {
	e := i.expectation
	state := string(e.state)
	var attempt, attempted, due, deadline, attention, observed, message, detail, resolution any
	if e.attemptID != uuid.Nil {
		attempt = &e.attemptID
	}
	if !i.attemptedAt.IsZero() {
		attempted = &i.attemptedAt
	}
	if !i.dueAt.IsZero() {
		due = &i.dueAt
	}
	if !i.observeDeadline.IsZero() {
		deadline = &i.observeDeadline
	}
	if !i.attentionAt.IsZero() {
		attention = &i.attentionAt
	}
	if !i.observedAt.IsZero() {
		observed = &i.observedAt
	}
	if i.messageID != 0 {
		message = &i.messageID
	}
	if i.detail != "" {
		detail = &i.detail
	}
	if i.resolution != "" {
		resolution = &i.resolution
	}
	// pgxmock requires pointer values for real pgx nullable **T destinations.
	return pgxmock.NewRows([]string{"version", "key", "digest", "thread", "state", "attempt", "seq", "writer", "attempted", "due", "deadline", "attention", "observed", "updated", "message", "detail", "resolution", "status", "eligible", "is_due", "valid"}).AddRow(&e.version, &e.key, &e.digest, &e.threadID, &state, attempt, &e.attemptSeq, &e.writerSeq, attempted, due, deadline, attention, observed, &i.updatedAt, message, detail, resolution, i.status, i.metadataEligible, i.due, true)
}

func expectReviewerDispatchRead(mock pgxmock.PgxPoolIface, i ReviewerDispatchIntent) *pgxmock.ExpectedQuery {
	e := i.expectation
	return mock.ExpectQuery(reviewerDispatchReadQuery).WithArgs(e.orgID, e.sessionID, e.resultID, e.metadataID)
}
func expectReviewerDispatchLocks(mock pgxmock.PgxPoolIface, e ReviewerDispatchExpectation) {
	mock.ExpectQuery(reviewerDispatchMetadataLockQuery).WithArgs(e.orgID, e.sessionID, e.metadataID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(e.metadataID))
	mock.ExpectQuery(reviewerDispatchResultLockQuery).WithArgs(e.orgID, e.sessionID, e.resultID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(e.resultID))
}

func reviewerDispatchCPUCompletionRows(variant string) *pgxmock.Rows {
	id := uuid.MustParse("88888888-8888-4888-8888-888888888888")
	candidate := &id
	reviewID := int64(77)
	review := &reviewID
	version, digest, head := "719", "synthetic-original-digest", "synthetic-original-head"
	structural, fits := true, true
	receipt := []byte(`{"assessment_id":"` + id.String() + `","input_digest":"` + digest + `","submitted_commit_sha":"` + head + `","github_review_id":77}`)
	switch variant {
	case "canonical":
	case "alias":
		receipt = []byte(`{"ASSESSMENT_ID":"` + id.String() + `","INPUT_DIGEST":"` + digest + `","SUBMITTED_COMMIT_SHA":"` + head + `","GITHUB_REVIEW_ID":77}`)
	case "wrong alias type":
		receipt = []byte(`{"assessment_id":"` + id.String() + `","ASSESSMENT_ID":17,"input_digest":"` + digest + `","submitted_commit_sha":"` + head + `","github_review_id":77}`)
	case "unconfirmed":
		structural = false
	case "absent":
		candidate, review, receipt = nil, nil, nil
		version, digest, head, structural = "", "", "", false
	case "oversize":
		fits, receipt, digest, head = false, nil, "", ""
	}
	return pgxmock.NewRows([]string{"id", "version", "structural", "fits", "receipt", "digest", "head", "review_id"}).
		AddRow(candidate, version, structural, fits, receipt, digest, head, review)
}

func TestReviewerDispatchExpectationAndPrivateShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		edit  func(*RetainedCallerSend, *uuid.UUID)
		valid bool
	}{
		{name: "supported independent reviewer identity", valid: true},
		{name: "missing independent metadata", edit: func(_ *RetainedCallerSend, m *uuid.UUID) { *m = uuid.Nil }},
		{name: "legacy cannot upgrade", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.shape = CallerSendRetainedLegacy }},
		{name: "wrong closed purpose", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.ref.Purpose = CallerSendStampReviewLoopStartup }},
		{name: "unsupported persisted version", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.version = 2 }},
		{name: "wrong original key", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.key = "synthetic-other-key" }},
		{name: "noncanonical digest", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.digest = strings.Repeat("A", 64) }},
		{name: "missing lane", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.threadID = uuid.Nil }},
		{name: "unknown state", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.state = "synthetic-other-state" }},
		{name: "attempt without sequence", edit: func(o *RetainedCallerSend, _ *uuid.UUID) { o.attemptID = uuid.New() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			i := reviewerDispatchCPUIntent()
			e := i.expectation
			owner := RetainedCallerSend{orgID: e.orgID, ref: e.OwnerRef(), shape: CallerSendRetainedSupported, threadID: e.threadID, key: e.key, digest: e.digest, version: 1, state: "pending"}
			metadata := e.metadataID
			if tt.edit != nil {
				tt.edit(&owner, &metadata)
			}
			actual, err := ReviewerDispatchExpectationFromRetained(metadata, owner)
			if tt.valid {
				require.NoError(t, err, "only independently scoped supported retained reviewer identity may form a CAS expectation")
				require.Equal(t, e, actual, "the sealed expectation must preserve every original scope and generation")
			} else {
				require.ErrorIs(t, err, ErrReviewerDispatchInput, "legacy, unsupported and mismatched expectation input must fail before SQL")
				require.Equal(t, ReviewerDispatchExpectation{}, actual, "invalid expectation input must expose no partial authority")
			}
			for _, private := range []any{actual, i, ReviewerDispatchUnknownAcknowledgement()} {
				raw, err := json.Marshal(private)
				require.NoError(t, err, "private controller correlation should marshal safely")
				require.Equal(t, `{}`, string(raw), "no owner, frozen input, digest or attempt field may become public JSON")
			}
		})
	}
}

func TestReviewerDispatchClosedSettlementFactories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		make  func() (ReviewerDispatchSettlement, error)
		valid bool
	}{
		{name: "known ACK", valid: true, make: func() (ReviewerDispatchSettlement, error) { return ReviewerDispatchSendAcknowledged(41) }},
		{name: "zero ACK", make: func() (ReviewerDispatchSettlement, error) { return ReviewerDispatchSendAcknowledged(0) }},
		{name: "known contention abort", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchRetryAfterKnownAbort(ReviewerDispatchAbortContention)
		}},
		{name: "known normalizing abort", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchRetryAfterKnownAbort(ReviewerDispatchAbortNormalizing)
		}},
		{name: "known mode change abort", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchRetryAfterKnownAbort(ReviewerDispatchAbortModeChanged)
		}},
		{name: "known replay observation abort", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchRetryAfterKnownAbort(ReviewerDispatchAbortReplayObservation)
		}},
		{name: "arbitrary abort text", make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchRetryAfterKnownAbort("synthetic private transport text")
		}},
		{name: "known prewrite refusal", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchKnownRefusal(ReviewerDispatchSendRefused)
		}},
		{name: "stamped active refusal", valid: true, make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchKnownRefusal(ReviewerDispatchStampedActiveUnsupported)
		}},
		{name: "arbitrary refusal text", make: func() (ReviewerDispatchSettlement, error) {
			return ReviewerDispatchKnownRefusal("synthetic private transport text")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := tt.make()
			if tt.valid {
				require.NoError(t, err, "the closed known-outcome vocabulary must retain its explicit classification")
				require.True(t, s.valid(), "a successful factory must produce a fully validated private settlement")
			} else {
				require.ErrorIs(t, err, ErrReviewerDispatchInput, "arbitrary transport text and invalid receipt shape must not reach SQL")
				require.Equal(t, ReviewerDispatchSettlement{}, s, "rejected factories must expose no partial operation")
			}
		})
	}
}

func TestClaimReviewerDispatchSuppliedTransaction(t *testing.T) {
	t.Parallel()
	mock := newCallerSendStampMock(t)
	before := reviewerDispatchCPUIntent()
	e := before.expectation
	attempt := uuid.New()
	after := before
	after.expectation.state = ReviewerDispatchAttempting
	after.expectation.attemptID = attempt
	after.expectation.attemptSeq = 1
	after.expectation.writerSeq = 1
	after.attemptedAt = before.updatedAt
	after.dueAt = after.attemptedAt.Add(60 * time.Second)
	after.due = false
	expectReviewerDispatchLocks(mock, e)
	expectReviewerDispatchRead(mock, before).WillReturnRows(reviewerDispatchCPURows(before))
	mock.ExpectQuery(codeReviewConfirmedCompletionObservationQuery).WithArgs(e.orgID, e.metadataID, codeReviewConfirmedCompletionObservationMaxBytes).
		WillReturnRows(reviewerDispatchCPUCompletionRows("absent"))
	mock.ExpectQuery(reviewerDispatchUpdateQuery).WithArgs(e.orgID, e.sessionID, e.resultID, e.metadataID, e.threadID, e.key, e.version, e.digest, string(e.state), nil, e.attemptSeq, e.writerSeq, "claim", "", int64(0), attempt).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(e.resultID))
	expectReviewerDispatchRead(mock, after).WillReturnRows(reviewerDispatchCPURows(after))
	actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), e.orgID, mock, e, attempt)
	require.NoError(t, err, "the helper may only stage one exact claim within the supplied owner transaction")
	require.True(t, changed, "the exact due pending owner should produce a provisional claim, not a known-commit permission")
	require.Equal(t, after, actual, "a provisional claim must retain its exact new attempt and database-owned schedule")
	require.NoError(t, mock.ExpectationsWereMet(), "metadata lock, result lock, fresh read, exact bounded completion observation, fenced update and readback must occur in order without nested begin/commit or effects")
}

func TestClaimReviewerDispatchConfirmedCompletionExclusion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, receipt        string
		notDue, cancel, miss bool
	}{
		{name: "confirmed original cancels a known-abort pending retry", receipt: "canonical", cancel: true},
		{name: "confirmed folded aliases share the same receipt decoder", receipt: "alias", cancel: true},
		{name: "unconfirmed active owner keeps its next original attempt", receipt: "unconfirmed"},
		{name: "recognized wrong-type alias does not confer confirmed authority", receipt: "wrong alias type"},
		{name: "no original assessment preserves existing claim behavior", receipt: "absent"},
		{name: "confirmed owner cancels even when its retry is not yet due", receipt: "canonical", notDue: true, cancel: true},
		{name: "unconfirmed retry not yet due is still an unchanged miss", receipt: "unconfirmed", notDue: true, miss: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			before := reviewerDispatchCPUIntent()
			before.expectation.attemptID = uuid.New()
			before.expectation.attemptSeq, before.expectation.writerSeq = 1, 2
			before.attemptedAt = before.updatedAt.Add(-time.Minute)
			before.detail = string(ReviewerDispatchAbortContention)
			before.due = !tt.notDue
			e := before.expectation
			attempt := uuid.New()
			expectReviewerDispatchLocks(mock, e)
			expectReviewerDispatchRead(mock, before).WillReturnRows(reviewerDispatchCPURows(before))
			mock.ExpectQuery(codeReviewConfirmedCompletionObservationQuery).WithArgs(e.orgID, e.metadataID, codeReviewConfirmedCompletionObservationMaxBytes).
				WillReturnRows(reviewerDispatchCPUCompletionRows(tt.receipt))
			after := before
			if !tt.miss {
				op, detail, newAttempt := "claim", "", attempt
				after.expectation.writerSeq++
				if tt.cancel {
					op, detail, newAttempt = "cancel", string(ReviewerDispatchOwnerIneligible), uuid.Nil
					after.expectation.state = ReviewerDispatchCancelled
					after.resolution, after.dueAt, after.due = detail, time.Time{}, false
				} else {
					after.expectation.state, after.expectation.attemptID = ReviewerDispatchAttempting, attempt
					after.expectation.attemptSeq++
					after.attemptedAt, after.dueAt, after.due, after.detail = before.updatedAt, before.updatedAt.Add(time.Minute), false, ""
				}
				mock.ExpectQuery(reviewerDispatchUpdateQuery).WithArgs(e.orgID, e.sessionID, e.resultID, e.metadataID, e.threadID, e.key, e.version, e.digest,
					string(e.state), e.attemptID, e.attemptSeq, e.writerSeq, op, detail, int64(0), newAttempt).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(e.resultID))
				expectReviewerDispatchRead(mock, after).WillReturnRows(reviewerDispatchCPURows(after))
			} else {
				after = ReviewerDispatchIntent{}
			}
			actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), e.orgID, mock, e, attempt)
			require.NoError(t, err, "a bounded exact-owner observation may cancel confirmed completion or preserve the existing unconfirmed claim policy")
			require.Equal(t, !tt.miss, changed, "only an actual cancellation or eligible due claim may return a provisional changed intent")
			require.Equal(t, after, actual, "confirmed cancellation must preserve the original known-abort attempt/history while ordinary retries alone acquire the new attempt")
			if tt.cancel {
				require.Equal(t, e.attemptID, actual.Expectation().AttemptID(), "confirmed owner exclusion must never install the requested new attempt")
				require.Equal(t, e.attemptSeq, actual.Expectation().AttemptSeq(), "confirmed owner exclusion must not increment its durable attempt budget")
				require.Equal(t, string(ReviewerDispatchAbortContention), actual.Detail(), "truthful known-abort evidence survives later owner-ineligible cancellation")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the exact tenant/metadata receipt observation must follow existing locks and fresh read and precede any single fenced update, without an alternate claim or sender")
		})
	}
}

func TestClaimReviewerDispatchCompletionObservationFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		queryFailure bool
	}{
		{name: "query failure is not permission to send", queryFailure: true},
		{name: "oversize receipt observation is not a semantic negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			before := reviewerDispatchCPUIntent()
			e := before.expectation
			cause := errors.New("synthetic private confirmed-completion observation failure")
			expectReviewerDispatchLocks(mock, e)
			expectReviewerDispatchRead(mock, before).WillReturnRows(reviewerDispatchCPURows(before))
			query := mock.ExpectQuery(codeReviewConfirmedCompletionObservationQuery).WithArgs(e.orgID, e.metadataID, codeReviewConfirmedCompletionObservationMaxBytes)
			if tt.queryFailure {
				query.WillReturnError(cause)
			} else {
				query.WillReturnRows(reviewerDispatchCPUCompletionRows("oversize"))
			}
			actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), e.orgID, mock, e, uuid.New())
			require.ErrorIs(t, err, ErrReviewerDispatchStorage, "every failed receipt observation must cross the existing safe reviewer storage boundary")
			require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservation, "the private cause must retain the explicit observation failure classification")
			if tt.queryFailure {
				require.ErrorIs(t, err, cause, "internal diagnosis must retain the original private driver failure")
			} else {
				require.ErrorIs(t, err, ErrCodeReviewConfirmedCompletionObservationTooLarge, "the bounded observation refusal must not silently become a negative proof")
			}
			require.EqualError(t, err, "reviewer dispatch storage failed", "safe storage text must not render the raw driver cause, receipt, scope or command")
			require.False(t, changed, "failed observation must release neither a cancellation nor a new provisional send claim")
			require.Equal(t, ReviewerDispatchIntent{}, actual, "failed observation must erase all partial intent authority")
			require.NoError(t, mock.ExpectationsWereMet(), "observation failure must issue no claim/cancel update, fallback query, transaction settlement or sender work")
		})
	}
}

func TestClaimReviewerDispatchExistingIneligibilityPrecedesCompletionObservation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*ReviewerDispatchIntent)
	}{
		{name: "terminal or stale metadata retains existing owner cancellation", edit: func(i *ReviewerDispatchIntent) { i.metadataEligible = false }},
		{name: "nonqueued contribution retains existing owner cancellation", edit: func(i *ReviewerDispatchIntent) { i.status = "completed" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			before := reviewerDispatchCPUIntent()
			tt.edit(&before)
			e := before.expectation
			after := before
			after.expectation.state, after.expectation.writerSeq = ReviewerDispatchCancelled, e.writerSeq+1
			after.resolution, after.dueAt, after.due = string(ReviewerDispatchOwnerIneligible), time.Time{}, false
			expectReviewerDispatchLocks(mock, e)
			expectReviewerDispatchRead(mock, before).WillReturnRows(reviewerDispatchCPURows(before))
			mock.ExpectQuery(reviewerDispatchUpdateQuery).WithArgs(e.orgID, e.sessionID, e.resultID, e.metadataID, e.threadID, e.key, e.version, e.digest,
				string(e.state), nil, e.attemptSeq, e.writerSeq, "cancel", string(ReviewerDispatchOwnerIneligible), int64(0), uuid.Nil).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(e.resultID))
			expectReviewerDispatchRead(mock, after).WillReturnRows(reviewerDispatchCPURows(after))
			actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), e.orgID, mock, e, uuid.New())
			require.NoError(t, err, "existing independently ineligible metadata or contribution must retain its original cancellation precedence")
			require.True(t, changed, "the existing ineligible owner may stage exactly its original pending cancellation")
			require.Equal(t, after, actual, "completion observation must not alter the previous owner-ineligible result or add an attempt")
			require.NoError(t, mock.ExpectationsWereMet(), "an already ineligible owner must not issue a receipt query or attempt a fresh send")
		})
	}
}

func TestReviewerDispatchStaleAndForeignMisses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		edit    func(*ReviewerDispatchIntent)
		foreign bool
	}{
		{name: "newer writer", edit: func(i *ReviewerDispatchIntent) { i.expectation.writerSeq++ }},
		{name: "different original digest", edit: func(i *ReviewerDispatchIntent) { i.expectation.digest = strings.Repeat("b", 64) }},
		{name: "different original lane", edit: func(i *ReviewerDispatchIntent) { i.expectation.threadID = uuid.New() }},
		{name: "different state", edit: func(i *ReviewerDispatchIntent) {
			i.expectation.state = ReviewerDispatchCancelled
			i.dueAt = time.Time{}
			i.resolution = "owner_cancelled"
		}},
		{name: "foreign calling tenant", foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			before := reviewerDispatchCPUIntent()
			e := before.expectation
			current := before
			org := e.orgID
			if tt.foreign {
				org = uuid.New()
			} else {
				tt.edit(&current)
				expectReviewerDispatchLocks(mock, e)
				expectReviewerDispatchRead(mock, current).WillReturnRows(reviewerDispatchCPURows(current))
			}
			actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), org, mock, e, uuid.New())
			require.NoError(t, err, "foreign or stale correlation must be a safe miss, never a fallback or another identity")
			require.False(t, changed, "a stale expected tuple cannot mutate the current owner")
			require.Equal(t, ReviewerDispatchIntent{}, actual, "a miss cannot leak a current foreign or newer owner projection")
			require.NoError(t, mock.ExpectationsWereMet(), "safe misses must not issue an UPDATE, transaction settlement or resend")
		})
	}
}

func TestReviewerDispatchStorageCauseAndSQLContract(t *testing.T) {
	t.Parallel()
	mock := newCallerSendStampMock(t)
	i := reviewerDispatchCPUIntent()
	e := i.expectation
	cause := errors.New("synthetic private database failure")
	mock.ExpectQuery(reviewerDispatchMetadataLockQuery).WithArgs(e.orgID, e.sessionID, e.metadataID).WillReturnError(cause)
	actual, changed, err := ClaimReviewerDispatchInTx(context.Background(), e.orgID, mock, e, uuid.New())
	require.ErrorIs(t, err, cause, "internal storage diagnosis must retain the actual cause")
	require.ErrorIs(t, err, ErrReviewerDispatchStorage, "the fixed safe storage boundary must remain recognizable")
	require.EqualError(t, err, "reviewer dispatch storage failed", "safe errors must not render frozen input, scope values or raw driver text")
	require.False(t, changed, "storage failure exposes no provisional permission")
	require.Equal(t, ReviewerDispatchIntent{}, actual, "storage failure exposes no partial result")
	require.NoError(t, mock.ExpectationsWereMet(), "failed first owner lock cannot perform later reads or mutations")
	for _, scope := range []string{"r.org_id=$1", "r.session_id=$2", "r.id=$3", "m.id=$4", "r.dispatch_thread_id=$5", "r.dispatch_key=$6", "r.dispatch_version=$7", "r.dispatch_digest=$8", "r.dispatch_state=$9", "r.dispatch_attempt_id IS NOT DISTINCT FROM $10::uuid", "r.dispatch_attempt_seq=$11", "r.dispatch_writer_seq=$12"} {
		require.Contains(t, reviewerDispatchUpdateQuery, scope, "every mutation must bind the exact immutable owner and expected generation")
	}
	setClause := strings.SplitN(reviewerDispatchUpdateQuery, " WHERE ", 2)[0]
	for _, forbidden := range []string{"dispatch_input=", "dispatch_wake=", "status=", "raw_output=", "structured_result="} {
		require.NotContains(t, setClause, forbidden, "settlement must not rewrite retained payloads or business contribution fields")
	}
	for _, forbidden := range []string{"archived_at", "deleted_at", "FOR UPDATE", "FOR SHARE"} {
		require.NotContains(t, reviewerDispatchImmutableLinkQuery, forbidden, "immutable late settlement must not add current visibility permission or message-owner row locking")
	}
	for _, scope := range []string{"i.message_id=c.message_id", "m.id=c.message_id", "c.org_id=$1", "c.session_id=$2", "c.thread_id=$3", "c.client_message_id=$4", "c.owner_id=$5", "c.version=$6", "c.digest=$7", "c.message_id=$8"} {
		require.Contains(t, reviewerDispatchImmutableLinkQuery, scope, "settlement must verify actual exact typed immutable linkage, never a bare message id")
	}
}
