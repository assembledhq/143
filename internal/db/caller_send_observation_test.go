package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func callerObservationRow(stamp CallerSendStamp) []any {
	var ordinaryThread, pass any
	if stamp.Scope.Purpose == CallerSendStampReviewLoopStartup {
		ordinaryThread, pass = stamp.Scope.ThreadID, uuid.MustParse("55555555-5555-4555-8555-555555555555")
	}
	raw := `{"synthetic":"retained"}`
	return []any{stamp.Scope.OwnerID, stamp.OrgID, stamp.Scope.SessionID, int16(1),
		stamp.Scope.ClientMessageID, stamp.Digest, stamp.Scope.ThreadID, raw, len(raw), 11,
		"pending", nil, int32(0), int32(0), ordinaryThread, pass, true, true}
}

// pgxmock assigns Go values directly; unlike pgx it does not allocate *T
// destinations for nullable **T scans. Adapt only the mock representation,
// after case mutations, while keeping the original scalar expectations intact.
func callerObservationNullableMockValues(row []any) []any {
	values := append([]any(nil), row...)
	for _, index := range []int{3, 4, 5, 6, 7, 10, 11, 12, 13, 14, 15} {
		switch value := values[index].(type) {
		case nil:
			continue
		case int16:
			values[index] = &value
		case int32:
			values[index] = &value
		case string:
			values[index] = &value
		case uuid.UUID:
			values[index] = &value
		default:
			panic("unsupported nullable caller observation mock value")
		}
	}
	return values
}

func callerObservationRows(row ...any) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "session_id", "version", "key", "digest", "thread_id", "raw", "raw_bytes", "nonnull", "state", "attempt_id", "attempt_seq", "writer_seq", "ordinary_thread", "pass_id", "kind_matches", "state_valid"})
	if row != nil {
		rows.AddRow(callerObservationNullableMockValues(row)...)
	}
	return rows
}

func callerObservationQuery(purpose CallerSendStampPurpose) string {
	if purpose == CallerSendStampReviewLoopStartup {
		return callerSendStartupObservationQuery
	}
	return callerSendDispatchObservationQuery
}

func callerObservationRef(stamp CallerSendStamp) CallerSendOwnerRef {
	return CallerSendOwnerRef{Purpose: stamp.Scope.Purpose, OwnerID: stamp.Scope.OwnerID, SessionID: stamp.Scope.SessionID}
}

func TestReadRetainedCallerSendInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendStampPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendStampCodeReviewDispatch},
		{name: "loop startup", purpose: CallerSendStampReviewLoopStartup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			stamp := newCallerSendStampFixture(tt.purpose)
			ref, row := callerObservationRef(stamp), callerObservationRow(stamp)
			attempt := uuid.MustParse("66666666-6666-4666-8666-666666666666")
			row[10], row[11], row[12], row[13] = "attempting", attempt, int32(3), int32(7)
			mock.ExpectQuery(callerObservationQuery(tt.purpose)).WithArgs(stamp.OrgID, ref.SessionID, ref.OwnerID, callerSendObservationMaxBytes).WillReturnRows(callerObservationRows(row...))
			actual, found, err := ReadRetainedCallerSendInTx(context.Background(), stamp.OrgID, mock, ref)
			require.NoError(t, err, "a complete retained tuple should be read using only the supplied transaction")
			require.True(t, found, "the exact independently retained owner should be observed")
			expected := RetainedCallerSend{orgID: stamp.OrgID, ref: ref, shape: CallerSendRetainedSupported,
				threadID: stamp.Scope.ThreadID, key: stamp.Scope.ClientMessageID, digest: stamp.Digest, version: 1,
				state: "attempting", raw: []byte(row[7].(string)), attemptID: attempt, attemptSeq: 3, writerSeq: 7}
			if tt.purpose == CallerSendStampReviewLoopStartup {
				expected.passID = row[15].(uuid.UUID)
			}
			require.Equal(t, expected, actual, "retain the complete original typed identity and observed attempt correlation, not current admission authority")
			copyRaw := actual.Raw()
			copyRaw[0] = '!'
			require.Equal(t, expected.raw, actual.Raw(), "callers cannot mutate retained command bytes through an accessor")
			raw, err := json.Marshal(actual) //nolint:staticcheck // Intentionally verify sealed persistence capabilities serialize without private fields.
			require.NoError(t, err, "sealed observation storage must be safely marshalable")
			require.Equal(t, `{}`, string(raw), "private prompts, keys, digests and attempt identities must not become JSON response fields")
			require.NoError(t, mock.ExpectationsWereMet(), "only the exact scoped plain owner read may occur without transaction settlement or locking")
		})
	}
}

func TestReadRetainedCallerSendShapeClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  CallerSendStampPurpose
		change   func([]any)
		expected CallerSendRetainedShape
		version  int16
	}{
		{name: "dispatch all null is legacy", expected: CallerSendRetainedLegacy, change: func(row []any) {
			for i := 3; i <= 15; i++ {
				row[i] = nil
			}
			row[8], row[9], row[17] = 0, 0, false
		}},
		{name: "startup all intent null is legacy", purpose: CallerSendStampReviewLoopStartup, expected: CallerSendRetainedLegacy, change: func(row []any) {
			for i := 3; i <= 15; i++ {
				row[i] = nil
			}
			row[8], row[9], row[14], row[17] = 0, 0, uuid.New(), false
		}},
		{name: "null version plus retained key is corrupt", expected: CallerSendRetainedMalformed, change: func(row []any) { row[3], row[9] = nil, 1 }},
		{name: "null version plus attempt is corrupt", expected: CallerSendRetainedMalformed, change: func(row []any) { row[3], row[4], row[9], row[11] = nil, nil, 1, uuid.New() }},
		{name: "unknown version has no raw fetch", expected: CallerSendRetainedUnsupported, version: 2, change: func(row []any) { row[3], row[7], row[8], row[17] = int16(2), nil, 0, false }},
		{name: "supported raw bound exceeded", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[7], row[8] = nil, callerSendObservationMaxBytes+1 }},
		{name: "supported raw length disagreement", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[8] = 1 }},
		{name: "raw missing", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[7] = nil }},
		{name: "wrong owner returned", expected: CallerSendRetainedMalformed, change: func(row []any) { row[0] = uuid.New() }},
		{name: "wrong tenant returned", expected: CallerSendRetainedMalformed, change: func(row []any) { row[1] = uuid.New() }},
		{name: "wrong session returned", expected: CallerSendRetainedMalformed, change: func(row []any) { row[2] = uuid.New() }},
		{name: "non reviewer kind", expected: CallerSendRetainedMalformed, change: func(row []any) { row[16] = false }},
		{name: "wrong owner derived key", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[4] = "synthetic-wrong-key" }},
		{name: "invalid digest alphabet", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[5] = strings.Repeat("G", 64) }},
		{name: "zero digest", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[5] = strings.Repeat("0", 64) }},
		{name: "missing lane", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[6] = nil }},
		{name: "zero lane", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[6] = uuid.Nil }},
		{name: "partial invalid persisted state", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[17] = false }},
		{name: "negative attempt", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[12] = int32(-1) }},
		{name: "attempt sequence without identity", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[12] = int32(1) }},
		{name: "attempt identity without sequence", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[11] = uuid.New() }},
		{name: "zero attempt identity", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[11], row[12] = uuid.Nil, int32(1) }},
		{name: "negative writer sequence", expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[13] = int32(-1) }},
		{name: "loop ordinary lane mismatch", purpose: CallerSendStampReviewLoopStartup, expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[14] = uuid.New() }},
		{name: "loop missing retained pass", purpose: CallerSendStampReviewLoopStartup, expected: CallerSendRetainedMalformed, version: 1, change: func(row []any) { row[15] = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			purpose := tt.purpose
			if purpose == "" {
				purpose = CallerSendStampCodeReviewDispatch
			}
			stamp := newCallerSendStampFixture(purpose)
			ref, row, mock := callerObservationRef(stamp), callerObservationRow(stamp), newCallerSendStampMock(t)
			tt.change(row)
			mock.ExpectQuery(callerObservationQuery(purpose)).WithArgs(stamp.OrgID, ref.SessionID, ref.OwnerID, callerSendObservationMaxBytes).WillReturnRows(callerObservationRows(row...))
			actual, found, err := ReadRetainedCallerSendInTx(context.Background(), stamp.OrgID, mock, ref)
			require.NoError(t, err, "observed unsupported or malformed retained data is unresolved, not transport failure or resend permission")
			require.True(t, found, "the owner exists even when its retained command cannot be used")
			require.Equal(t, RetainedCallerSend{orgID: stamp.OrgID, ref: ref, shape: tt.expected, version: tt.version}, actual, "all NULL, partial corruption, and unsupported versions must remain distinct with no partial command exposure")
			require.NoError(t, mock.ExpectationsWereMet(), "shape classification may only use the one bounded tenant-scoped query")
		})
	}
}

func TestReadRetainedCallerSendMissAndStorageError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
		found bool
	}{
		{name: "foreign tenant exact lookup misses", cause: pgx.ErrNoRows},
		{name: "storage cause retained privately", cause: errors.New("synthetic private storage failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stamp := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			ref, mock := callerObservationRef(stamp), newCallerSendStampMock(t)
			mock.ExpectQuery(callerSendDispatchObservationQuery).WithArgs(stamp.OrgID, ref.SessionID, ref.OwnerID, callerSendObservationMaxBytes).WillReturnError(tt.cause)
			actual, found, err := ReadRetainedCallerSendInTx(context.Background(), stamp.OrgID, mock, ref)
			if errors.Is(tt.cause, pgx.ErrNoRows) {
				require.NoError(t, err, "missing scoped evidence is an unresolved miss, never an abort receipt")
			} else {
				require.ErrorIs(t, err, ErrCallerSendObservationStorage, "the fixed storage boundary must remain identifiable")
				require.ErrorIs(t, err, tt.cause, "the private storage cause must remain available to internal diagnosis")
				require.EqualError(t, err, "caller send observation storage failed", "public error text must not disclose raw owner or storage content")
			}
			require.Equal(t, RetainedCallerSend{}, actual, "a miss or storage error exposes no provisional identity")
			require.Equal(t, tt.found, found, "neither scoped absence nor error proves positive correlation")
			require.NoError(t, mock.ExpectationsWereMet(), "misses and errors must not trigger owner fallback queries")
		})
	}
}

func TestCallerSendObservationInvalidScopeNoSQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		change                      func(*CallerSendStamp)
		nilTx, nilContext, canceled bool
	}{
		{name: "nil transaction", nilTx: true},
		{name: "nil context", nilContext: true},
		{name: "zero tenant", change: func(s *CallerSendStamp) { s.OrgID = uuid.Nil }},
		{name: "zero session", change: func(s *CallerSendStamp) { s.Scope.SessionID = uuid.Nil }},
		{name: "zero owner", change: func(s *CallerSendStamp) { s.Scope.OwnerID = uuid.Nil }},
		{name: "unknown purpose", change: func(s *CallerSendStamp) { s.Scope.Purpose = "synthetic-other-purpose" }},
		{name: "canceled before read", canceled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stamp, mock := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch), newCallerSendStampMock(t)
			if tt.change != nil {
				tt.change(&stamp)
			}
			var tx pgx.Tx = mock
			if tt.nilTx {
				tx = nil
			}
			ctx := context.Background()
			if tt.nilContext {
				ctx = nil
			}
			if tt.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			actual, found, err := ReadRetainedCallerSendInTx(ctx, stamp.OrgID, tx, callerObservationRef(stamp))
			expected := ErrCallerSendObservationInvalid
			if tt.canceled {
				expected = context.Canceled
			}
			require.ErrorIs(t, err, expected, "invalid or already canceled scope must stop before accessing owner data")
			require.Equal(t, RetainedCallerSend{}, actual, "rejected observation input cannot reveal retained bytes")
			require.False(t, found, "no query means no observed positive owner")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid input must issue no SQL, nested transaction or settlement operation")
		})
	}
}

func TestCallerSendObservationVisibilityAndLinkage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		missing, storage bool
	}{
		{name: "complete exact typed linkage"},
		{name: "inner join miss is only unresolved", missing: true},
		{name: "source storage error", storage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, stamp := newCallerSendStampMock(t), newCallerSendStampFixture(CallerSendStampReviewLoopStartup)
			cause := errors.New("synthetic linkage read failure")
			visibility := mock.ExpectQuery(callerSendVisibilityQuery).WithArgs(stamp.OrgID, stamp.Scope.SessionID, stamp.Scope.ThreadID)
			link := mock.ExpectQuery(callerSendLinkageQuery).WithArgs(stamp.OrgID, stamp.Scope.SessionID, stamp.Scope.ThreadID, stamp.Scope.ClientMessageID, stamp.Scope.Purpose, stamp.Scope.OwnerID)
			if tt.storage {
				visibility.WillReturnError(cause)
				link.WillReturnError(cause)
			} else if tt.missing {
				visibility.WillReturnError(pgx.ErrNoRows)
				link.WillReturnError(pgx.ErrNoRows)
			} else {
				visibility.WillReturnRows(pgxmock.NewRows([]string{"visible"}).AddRow(true))
				link.WillReturnRows(pgxmock.NewRows([]string{"message_id", "version", "digest", "created_at"}).AddRow(stamp.MessageID, stamp.Version, stamp.Digest, stamp.CreatedAt))
			}
			visible, visibilityErr := ReadCallerSendVisibilityInTx(context.Background(), stamp.OrgID, mock, stamp.Scope.SessionID, stamp.Scope.ThreadID)
			actual, found, err := ReadCallerSendLinkageInTx(context.Background(), stamp.OrgID, mock, stamp.Scope)
			if tt.storage {
				require.ErrorIs(t, visibilityErr, cause, "visibility errors cannot be treated as positive availability")
				require.ErrorIs(t, err, cause, "linkage errors must retain their private cause")
				require.ErrorIs(t, err, ErrCallerSendObservationStorage, "linkage transport errors retain a fixed safe boundary")
			} else {
				require.NoError(t, visibilityErr, "scoped availability can be observed without owner mutation")
				require.NoError(t, err, "a joined miss is not storage failure or permission to retry")
			}
			want := CallerSendLinkage{}
			if !tt.missing && !tt.storage {
				want = CallerSendLinkage{messageID: stamp.MessageID, version: stamp.Version, digest: stamp.Digest, createdAt: stamp.CreatedAt}
			}
			require.Equal(t, want, actual, "only the complete same-statement typed tuple yields immutable correlation")
			require.Equal(t, !tt.missing && !tt.storage, found, "join absence does not identify a missing component or prove nonacceptance")
			require.Equal(t, !tt.missing && !tt.storage, visible, "visibility requires explicit positive source evidence")
			require.NoError(t, mock.ExpectationsWereMet(), "both observations must use only supplied transaction and exact tenant/session/lane/key/owner scopes")
		})
	}
}

func TestCallerSendObservationSQLBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, query, prefix, ownerTable string }{
		{name: "dispatch", query: callerSendDispatchObservationQuery, prefix: "dispatch_", ownerTable: "code_review_agent_results"},
		{name: "startup", query: callerSendStartupObservationQuery, prefix: "startup_send_", ownerTable: "session_review_loops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, tt.query, "FROM "+tt.ownerTable+" WHERE org_id=$1 AND session_id=$2 AND id=$3", "owner purpose selects a closed table and all independent scopes")
			require.Contains(t, tt.query, "CASE WHEN "+tt.prefix+"version=1 THEN CASE WHEN octet_length("+tt.prefix+"input::text)<=$4 THEN "+tt.prefix+"input::text END END", "an outer version CASE must avoid relying on SQL AND evaluation order before raw JSONB detoast and scan bounds")
			require.Contains(t, tt.query, "CASE WHEN "+tt.prefix+"version=1 THEN COALESCE(octet_length("+tt.prefix+"input::text),0) ELSE 0 END", "unknown versions must avoid even unconditional raw JSONB rendering merely to count bytes")
			require.Contains(t, tt.query, "num_nonnulls("+tt.prefix+"version", "legacy detection must inspect the complete intent column set rather than NULL version")
			for _, field := range []string{"key", "input", "digest", "thread_id", "state", "attempt_id", "attempt_seq", "attempted_at", "due_at", "observe_deadline", "attention_at", "observed_at", "message_id", "resolution", "detail", "writer_seq", "updated_at"} {
				require.Contains(t, tt.query, tt.prefix+field, "the bounded typed read must account for every retained intent field")
			}
			for _, forbidden := range []string{"FOR UPDATE", "FOR SHARE", "SKIP LOCKED", "status='queued'", "status='running'", "intents_enabled"} {
				require.NotContains(t, tt.query, forbidden, "immutable late evidence must not depend on owner locks, insertion enablement, or business eligibility")
			}
		})
	}
	require.Contains(t, callerSendLinkageQuery, "END,c.created_at", "the original immutable stamp timestamp must come from the same final positive statement")
	for _, predicate := range []string{"m.org_id=i.org_id", "m.session_id=i.session_id", "m.thread_id=i.thread_id", "m.id=i.message_id", "c.org_id=i.org_id", "c.session_id=i.session_id", "c.thread_id=i.thread_id", "c.client_message_id=i.client_message_id", "c.message_id=i.message_id", "c.purpose=$5 AND c.owner_id=$6", "i.entry_type='user_message' AND m.role='user'", "s.deleted_at IS NULL AND s.archived_at IS NULL AND t.archived_at IS NULL"} {
		require.Contains(t, callerSendLinkageQuery, predicate, "final positive evidence must repeat exact typed linkage and visibility in the same source statement")
	}
}
