package db

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newCallerSendStampFixture(purpose CallerSendStampPurpose) CallerSendStamp {
	ownerID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	prefix := "cr-dispatch:v1:"
	if purpose == CallerSendStampReviewLoopStartup {
		prefix = "rl-startup:v1:"
	}
	return CallerSendStamp{
		OrgID: uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		CallerSendStampInput: CallerSendStampInput{
			Scope: CallerSendStampScope{
				SessionID:       uuid.MustParse("33333333-3333-4333-8333-333333333333"),
				ThreadID:        uuid.MustParse("44444444-4444-4444-8444-444444444444"),
				ClientMessageID: prefix + ownerID.String(), Purpose: purpose, OwnerID: ownerID,
			},
			Version: 1, Digest: strings.Repeat("a", 64), MessageID: 41,
		},
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC),
	}
}

func callerSendStampRows(stamps ...CallerSendStamp) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"org_id", "session_id", "thread_id", "client_message_id",
		"purpose", "owner_id", "version", "digest", "message_id", "created_at"})
	for _, stamp := range stamps {
		rows.AddRow(stamp.OrgID, stamp.Scope.SessionID, stamp.Scope.ThreadID,
			stamp.Scope.ClientMessageID, stamp.Scope.Purpose, stamp.Scope.OwnerID,
			stamp.Version, stamp.Digest, stamp.MessageID, stamp.CreatedAt)
	}
	return rows
}

func newCallerSendStampMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	require.NoError(t, err, "a fresh isolated transaction mock should be created")
	t.Cleanup(mock.Close)
	return mock
}

func expectCallerSendStampInsert(mock pgxmock.PgxPoolIface, stamp CallerSendStamp) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery(callerSendStampInsertQuery).WithArgs(stamp.OrgID,
		stamp.Scope.SessionID, stamp.Scope.ThreadID, stamp.Scope.ClientMessageID,
		stamp.Scope.Purpose, stamp.Scope.OwnerID, stamp.Version, stamp.Digest, stamp.MessageID)
}

func expectCallerSendStampRead(mock pgxmock.PgxPoolIface, stamp CallerSendStamp) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery(callerSendStampReadQuery).WithArgs(stamp.OrgID,
		stamp.Scope.SessionID, stamp.Scope.ThreadID, stamp.Scope.ClientMessageID,
		stamp.Scope.Purpose, stamp.Scope.OwnerID)
}

func expectCallerSendStampConflictRead(mock pgxmock.PgxPoolIface, stamp CallerSendStamp) *pgxmock.ExpectedQuery {
	return mock.ExpectQuery(callerSendStampConflictReadQuery).WithArgs(stamp.OrgID,
		stamp.Scope.SessionID, stamp.Scope.ThreadID, stamp.Scope.ClientMessageID,
		stamp.Scope.Purpose, stamp.Scope.OwnerID, stamp.MessageID)
}

func TestCallerSendStampPurposeAndKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  CallerSendStampPurpose
		ownerID  uuid.UUID
		expected string
	}{
		{name: "review dispatch", purpose: CallerSendStampCodeReviewDispatch, ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), expected: "cr-dispatch:v1:11111111-1111-4111-8111-111111111111"},
		{name: "loop startup", purpose: CallerSendStampReviewLoopStartup, ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), expected: "rl-startup:v1:11111111-1111-4111-8111-111111111111"},
		{name: "zero purpose", ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111")},
		{name: "short purpose alias", purpose: "cr-dispatch", ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111")},
		{name: "case alias", purpose: "Code-review-reviewer-dispatch", ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111")},
		{name: "whitespace alias", purpose: "review-loop-initial-send ", ownerID: uuid.MustParse("11111111-1111-4111-8111-111111111111")},
		{name: "zero owner", purpose: CallerSendStampCodeReviewDispatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := CallerSendStampKey(tt.purpose, tt.ownerID)
			if tt.expected == "" {
				require.ErrorIs(t, err, ErrCallerSendStampInvalid, "only the two exact purposes and a nonzero retained owner may derive a key")
			} else {
				require.NoError(t, err, "closed stored purposes should map to their explicit canonical key prefixes")
			}
			require.Equal(t, tt.expected, actual, "key derivation must neither mint nor normalize caller identity")
		})
	}
}

func TestCallerSendStampInputValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		edit  func(*CallerSendStampInput)
		valid bool
	}{
		{name: "canonical dispatch", valid: true},
		{name: "canonical loop startup", valid: true, edit: func(in *CallerSendStampInput) {
			in.Scope = newCallerSendStampFixture(CallerSendStampReviewLoopStartup).Scope
		}},
		{name: "zero session", edit: func(in *CallerSendStampInput) { in.Scope.SessionID = uuid.Nil }},
		{name: "zero thread", edit: func(in *CallerSendStampInput) { in.Scope.ThreadID = uuid.Nil }},
		{name: "zero owner", edit: func(in *CallerSendStampInput) { in.Scope.OwnerID = uuid.Nil }},
		{name: "empty key", edit: func(in *CallerSendStampInput) { in.Scope.ClientMessageID = "" }},
		{name: "unrelated key", edit: func(in *CallerSendStampInput) { in.Scope.ClientMessageID = "synthetic-other-key" }},
		{name: "different owner key", edit: func(in *CallerSendStampInput) { in.Scope.OwnerID = uuid.New() }},
		{name: "purpose prefix substituted for description", edit: func(in *CallerSendStampInput) { in.Scope.Purpose = "cr-dispatch" }},
		{name: "wrong namespace key", edit: func(in *CallerSendStampInput) {
			in.Scope.ClientMessageID = "rl-startup:v1:" + in.Scope.OwnerID.String()
		}},
		{name: "noncanonical UUID key", edit: func(in *CallerSendStampInput) {
			in.Scope.ClientMessageID = "cr-dispatch:v1:{" + in.Scope.OwnerID.String() + "}"
		}},
		{name: "zero version", edit: func(in *CallerSendStampInput) { in.Version = 0 }},
		{name: "unknown version", edit: func(in *CallerSendStampInput) { in.Version = 2 }},
		{name: "negative version", edit: func(in *CallerSendStampInput) { in.Version = -1 }},
		{name: "empty digest", edit: func(in *CallerSendStampInput) { in.Digest = "" }},
		{name: "all zero digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("0", 64) }},
		{name: "uppercase digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("A", 64) }},
		{name: "short digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("a", 63) }},
		{name: "long digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("a", 65) }},
		{name: "nonhex digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("g", 64) }},
		{name: "digest whitespace", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("a", 63) + " " }},
		{name: "binary digest", edit: func(in *CallerSendStampInput) { in.Digest = strings.Repeat("\x00", 64) }},
		{name: "zero message", edit: func(in *CallerSendStampInput) { in.MessageID = 0 }},
		{name: "negative message", edit: func(in *CallerSendStampInput) { in.MessageID = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			if tt.edit != nil {
				tt.edit(&fixture.CallerSendStampInput)
			}
			err := fixture.CallerSendStampInput.Validate()
			if tt.valid {
				require.NoError(t, err, "the canonical retained input should validate without transformation")
			} else {
				require.ErrorIs(t, err, ErrCallerSendStampInvalid, "invalid identity, digest or shape must be refused before any SQL")
				require.EqualError(t, err, "invalid caller send stamp input", "validation errors must not echo private field values")
				mock := newCallerSendStampMock(t)
				actual, recordErr := RecordCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.CallerSendStampInput)
				require.ErrorIs(t, recordErr, ErrCallerSendStampInvalid, "the public record helper must enforce the same complete pre-query validation")
				require.Equal(t, CallerSendStamp{}, actual, "invalid input must not expose a partial record")
				require.NoError(t, mock.ExpectationsWereMet(), "every invalid record shape must stop before issuing SQL or transaction control")
			}
		})
	}
}

func TestRecordCallerSendStampInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  CallerSendStampPurpose
		conflict bool
	}{
		{name: "new dispatch", purpose: CallerSendStampCodeReviewDispatch},
		{name: "new startup", purpose: CallerSendStampReviewLoopStartup},
		{name: "identical dispatch conflict", purpose: CallerSendStampCodeReviewDispatch, conflict: true},
		{name: "identical startup conflict", purpose: CallerSendStampReviewLoopStartup, conflict: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(tt.purpose)
			if tt.conflict {
				expectCallerSendStampInsert(mock, fixture).WillReturnRows(callerSendStampRows())
				expectCallerSendStampConflictRead(mock, fixture).WillReturnRows(callerSendStampRows(fixture))
			} else {
				expectCallerSendStampInsert(mock, fixture).WillReturnRows(callerSendStampRows(fixture))
			}
			actual, err := RecordCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.CallerSendStampInput)
			require.NoError(t, err, "new linkage or the same exact retained stamp should be correlated without a second effect")
			require.Equal(t, fixture, actual, "all persisted immutable fields and the database timestamp should be retained exactly")
			require.NoError(t, mock.ExpectationsWereMet(), "the helper must only insert and, for conflicts, read the exact existing scope with linkage")
		})
	}
}

func TestRecordCallerSendStampInTxRejectsMismatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		edit    func(*CallerSendStamp)
		missing bool
	}{
		{name: "different organization", edit: func(s *CallerSendStamp) { s.OrgID = uuid.New() }},
		{name: "different session", edit: func(s *CallerSendStamp) { s.Scope.SessionID = uuid.New() }},
		{name: "different thread", edit: func(s *CallerSendStamp) { s.Scope.ThreadID = uuid.New() }},
		{name: "different client key", edit: func(s *CallerSendStamp) { s.Scope.ClientMessageID = "synthetic-replaced-key" }},
		{name: "different purpose", edit: func(s *CallerSendStamp) { s.Scope.Purpose = CallerSendStampReviewLoopStartup }},
		{name: "different owner", edit: func(s *CallerSendStamp) { s.Scope.OwnerID = uuid.New() }},
		{name: "different version", edit: func(s *CallerSendStamp) { s.Version = 2 }},
		{name: "different canonical digest", edit: func(s *CallerSendStamp) { s.Digest = strings.Repeat("b", 64) }},
		{name: "different message", edit: func(s *CallerSendStamp) { s.MessageID++ }},
		{name: "missing database timestamp", edit: func(s *CallerSendStamp) { s.CreatedAt = time.Time{} }},
		{name: "missing linked stamp", missing: true},
		{name: "original typed inbox linkage missing", missing: true},
		{name: "owner uniqueness conflict in another scope", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			observed := fixture
			if tt.edit != nil {
				tt.edit(&observed)
			}
			expectCallerSendStampInsert(mock, fixture).WillReturnRows(callerSendStampRows())
			rows := callerSendStampRows(observed)
			if tt.missing {
				rows = callerSendStampRows()
			}
			expectCallerSendStampConflictRead(mock, fixture).WillReturnRows(rows)
			actual, err := RecordCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.CallerSendStampInput)
			require.ErrorIs(t, err, ErrCallerSendStampMismatch, "a different immutable stamp or missing required typed linkage must refuse the owning unit")
			require.Equal(t, CallerSendStamp{}, actual, "a mismatch must return no partial record or permission to continue")
			require.EqualError(t, err, "caller send stamp does not match retained command", "mismatch errors must not reveal retained keys, digests, IDs or messages")
			require.NoError(t, mock.ExpectationsWereMet(), "a mismatch may only read and must never overwrite or settle the caller transaction")
		})
	}
}

func TestReadCallerSendStampInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		purpose    CallerSendStampPurpose
		missing    bool
		foreignOrg bool
	}{
		{name: "dispatch stamp", purpose: CallerSendStampCodeReviewDispatch},
		{name: "startup stamp", purpose: CallerSendStampReviewLoopStartup},
		{name: "not observed", purpose: CallerSendStampCodeReviewDispatch, missing: true},
		{name: "foreign tenant observes no matching stamp", purpose: CallerSendStampCodeReviewDispatch, missing: true, foreignOrg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(tt.purpose)
			if tt.foreignOrg {
				fixture.OrgID = uuid.New()
			}
			rows := callerSendStampRows(fixture)
			if tt.missing {
				rows = callerSendStampRows()
			}
			expectCallerSendStampRead(mock, fixture).WillReturnRows(rows)
			actual, found, err := ReadCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.Scope)
			require.NoError(t, err, "a plain scoped observation or absence should not settle or retry any caller work")
			expected := fixture
			if tt.missing {
				expected = CallerSendStamp{}
			}
			require.Equal(t, expected, actual, "read results must expose the complete exact scoped record or no record")
			require.Equal(t, !tt.missing, found, "missing evidence must remain a miss rather than an accepted or aborted outcome")
			require.NoError(t, mock.ExpectationsWereMet(), "read-only composition must use only one plain exact-scope SELECT")
		})
	}
}

func TestReadCallerSendStampInTxRejectsUnexpectedRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*CallerSendStamp)
	}{
		{name: "organization", edit: func(s *CallerSendStamp) { s.OrgID = uuid.New() }},
		{name: "session", edit: func(s *CallerSendStamp) { s.Scope.SessionID = uuid.New() }},
		{name: "thread", edit: func(s *CallerSendStamp) { s.Scope.ThreadID = uuid.New() }},
		{name: "client key", edit: func(s *CallerSendStamp) { s.Scope.ClientMessageID += ":extra" }},
		{name: "purpose", edit: func(s *CallerSendStamp) { s.Scope.Purpose = "future-purpose" }},
		{name: "owner", edit: func(s *CallerSendStamp) { s.Scope.OwnerID = uuid.New() }},
		{name: "version", edit: func(s *CallerSendStamp) { s.Version = 2 }},
		{name: "noncanonical digest", edit: func(s *CallerSendStamp) { s.Digest = strings.Repeat("A", 64) }},
		{name: "zero digest", edit: func(s *CallerSendStamp) { s.Digest = strings.Repeat("0", 64) }},
		{name: "message shape", edit: func(s *CallerSendStamp) { s.MessageID = 0 }},
		{name: "timestamp shape", edit: func(s *CallerSendStamp) { s.CreatedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			observed := fixture
			tt.edit(&observed)
			expectCallerSendStampRead(mock, fixture).WillReturnRows(callerSendStampRows(observed))
			actual, found, err := ReadCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.Scope)
			require.ErrorIs(t, err, ErrCallerSendStampMismatch, "unexpected retained data or an unknown version must fail closed")
			require.Equal(t, CallerSendStamp{}, actual, "malformed stored rows must return no partial correlation")
			require.False(t, found, "invalid evidence must not become a positive lookup")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid stored evidence must not trigger rewriting, owner locking or transaction settlement")
		})
	}
}

func TestCallerSendStampInTxRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct{ operation, invalid string }{}
	for _, operation := range []string{"read", "record"} {
		for _, invalid := range []string{"nil context", "nil transaction", "typed nil transaction", "zero organization", "invalid scope", "cancelled context"} {
			tests = append(tests, struct{ operation, invalid string }{operation, invalid})
		}
	}
	for _, tt := range tests {
		t.Run(tt.operation+"/"+tt.invalid, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			var tx pgx.Tx = mock
			ctx := context.Background()
			switch tt.invalid {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var typedNil *pgxpool.Tx
				tx = typedNil
			case "zero organization":
				fixture.OrgID = uuid.Nil
			case "invalid scope":
				fixture.Scope.ThreadID = uuid.Nil
			case "cancelled context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var actual CallerSendStamp
			var found bool
			var err error
			if tt.operation == "record" {
				actual, err = RecordCallerSendStampInTx(ctx, fixture.OrgID, tx, fixture.CallerSendStampInput)
			} else {
				actual, found, err = ReadCallerSendStampInTx(ctx, fixture.OrgID, tx, fixture.Scope)
			}
			if tt.invalid == "cancelled context" {
				require.ErrorIs(t, err, context.Canceled, "cancellation must retain its original cause without attempting SQL")
			} else {
				require.ErrorIs(t, err, ErrCallerSendStampInvalid, "nil dependencies or scope cannot become caller authority")
			}
			require.Equal(t, CallerSendStamp{}, actual, "failed preparation must never expose a partial stamp")
			require.False(t, found, "invalid input must not produce a positive read")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid or cancelled input must issue no SQL or transaction control")
		})
	}
}

func TestCallerSendStampInTxPreservesSafeQueryErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{{"insert"}, {"conflict read"}, {"read"}, {"scan"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			cause := &pgconn.PgError{Code: "XX000", Message: "synthetic-private-query-value"}
			var actual CallerSendStamp
			var err error
			switch tt.name {
			case "insert":
				expectCallerSendStampInsert(mock, fixture).WillReturnError(cause)
				actual, err = RecordCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.CallerSendStampInput)
			case "conflict read":
				expectCallerSendStampInsert(mock, fixture).WillReturnRows(callerSendStampRows())
				expectCallerSendStampConflictRead(mock, fixture).WillReturnError(cause)
				actual, err = RecordCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.CallerSendStampInput)
			case "read":
				expectCallerSendStampRead(mock, fixture).WillReturnError(cause)
				actual, _, err = ReadCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.Scope)
			case "scan":
				expectCallerSendStampRead(mock, fixture).WillReturnRows(callerSendStampRows(fixture).RowError(0, cause))
				actual, _, err = ReadCallerSendStampInTx(context.Background(), fixture.OrgID, mock, fixture.Scope)
			}
			require.ErrorIs(t, err, ErrCallerSendStampStorage, "query and scan failures should have one safe typed storage category")
			require.ErrorIs(t, err, cause, "transaction owners must retain the exact database cause for outcome classification")
			var databaseCause *pgconn.PgError
			require.ErrorAs(t, err, &databaseCause, "typed database errors should remain available without exposing their private text")
			require.Equal(t, cause, databaseCause, "safe wrapping must not replace or reinterpret a database error")
			require.EqualError(t, err, "caller send stamp storage failed", "rendered errors must not include database values, queries, keys, IDs or digests")
			require.Equal(t, CallerSendStamp{}, actual, "failed storage must yield no usable partial record")
			require.NoError(t, mock.ExpectationsWereMet(), "storage errors must not retry, compensate, fall back or settle caller work")
		})
	}
}

func TestCallerSendStampInTxLeavesTransactionWithCaller(t *testing.T) {
	t.Parallel()
	tests := []struct{ operation string }{{"record"}, {"read"}}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			t.Parallel()
			mock := newCallerSendStampMock(t)
			fixture := newCallerSendStampFixture(CallerSendStampCodeReviewDispatch)
			ctx := context.Background()
			mock.ExpectBegin()
			tx, err := mock.Begin(ctx)
			require.NoError(t, err, "the caller should begin its own outer transaction")
			if tt.operation == "record" {
				expectCallerSendStampInsert(mock, fixture).WillReturnRows(callerSendStampRows(fixture))
				_, err = RecordCallerSendStampInTx(ctx, fixture.OrgID, tx, fixture.CallerSendStampInput)
			} else {
				expectCallerSendStampRead(mock, fixture).WillReturnRows(callerSendStampRows(fixture))
				_, _, err = ReadCallerSendStampInTx(ctx, fixture.OrgID, tx, fixture.Scope)
			}
			require.NoError(t, err, "stamp helpers should compose within the caller's supplied transaction")
			mock.ExpectExec("SELECT 42").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			_, err = tx.Exec(ctx, "SELECT 42")
			require.NoError(t, err, "the caller must retain an open transaction after stamp work")
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback(ctx), "only the caller may settle its outer transaction")
			require.NoError(t, mock.ExpectationsWereMet(), "helpers must not begin nested units, alter settings, lock owners or commit/rollback")
		})
	}
}

func TestCallerSendStampSQLScopeContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
		required    []string
	}{
		{name: "insert typed linkage", query: callerSendStampInsertQuery, required: []string{
			"INSERT INTO caller_send_command_stamps (", "statement_timestamp()", "FROM session_messages m", "JOIN thread_inbox_entries i",
			"i.org_id = m.org_id AND i.session_id = m.session_id", "i.thread_id = m.thread_id AND i.message_id = m.id",
			"m.org_id = $1 AND m.session_id = $2 AND m.thread_id = $3 AND m.id = $9",
			"i.org_id = $1 AND i.session_id = $2 AND i.thread_id = $3", "i.client_message_id = $4 AND i.message_id = $9", "ON CONFLICT DO NOTHING", "RETURNING " + callerSendStampColumns,
		}},
		{name: "read all independent scopes", query: callerSendStampReadQuery, required: []string{
			"SELECT " + callerSendStampColumns, "FROM caller_send_command_stamps", "org_id = $1 AND session_id = $2 AND thread_id = $3",
			"client_message_id = $4 AND purpose = $5 AND owner_id = $6",
		}},
		{name: "conflict requires original typed linkage", query: callerSendStampConflictReadQuery, required: []string{
			callerSendStampReadQuery, "AND EXISTS (", "FROM session_messages m", "JOIN thread_inbox_entries i",
			"i.org_id = m.org_id AND i.session_id = m.session_id", "i.thread_id = m.thread_id AND i.message_id = m.id",
			"m.org_id = $1 AND m.session_id = $2 AND m.thread_id = $3 AND m.id = $7",
			"i.org_id = $1 AND i.session_id = $2 AND i.thread_id = $3", "i.client_message_id = $4 AND i.message_id = $7",
		}},
	}
	space := regexp.MustCompile(`\s+`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			query := space.ReplaceAllString(tt.query, " ")
			for _, required := range tt.required {
				require.Contains(t, query, space.ReplaceAllString(required, " "), "SQL must retain every exact scope predicate, typed link and immutable conflict rule")
			}
			for _, forbidden := range []string{"FOR UPDATE", "FOR SHARE", "SET LOCAL", "BEGIN", "COMMIT", "ROLLBACK", "DO UPDATE", "code_review_agent_results", "session_review_loops", "payload"} {
				require.NotContains(t, query, forbidden, "stamp correlation must not add owner locks, settlement, settings, payload authority or a second admission gate")
			}
		})
	}
}

func TestCallerSendStampStorageErrorDoesNotExposeCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("synthetic-private-value")
	err := &callerSendStampError{cause: cause}
	require.EqualError(t, err, ErrCallerSendStampStorage.Error(), "fixed public error text must not render a private database cause")
	require.ErrorIs(t, err, cause, "safe text must preserve the original cause for trusted classification")
	require.ErrorIs(t, err, ErrCallerSendStampStorage, "safe typed wrappers should preserve their fixed category")
}
