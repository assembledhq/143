package db

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestThreadSendModeValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, expectedError string
		mode                ThreadSendMode
	}{
		{name: "legacy", mode: ThreadSendModeLegacy},
		{name: "normalizing", mode: ThreadSendModeNormalizing},
		{name: "active", mode: ThreadSendModeActive},
		{name: "zero", expectedError: `invalid thread send mode: ""`},
		{name: "unknown", mode: "future", expectedError: `invalid thread send mode: "future"`},
		{name: "case alias", mode: "ACTIVE", expectedError: `invalid thread send mode: "ACTIVE"`},
		{name: "whitespace alias", mode: " legacy", expectedError: `invalid thread send mode: " legacy"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.mode.Validate()
			if tt.expectedError == "" {
				require.NoError(t, err, "only the exact three public mode shapes may validate, without conferring lifecycle authority")
			} else {
				require.EqualError(t, err, tt.expectedError, "zero, unknown and aliased modes must remain invalid rather than normalize into a branch")
			}
		})
	}
}

type threadSendProtocolResult struct {
	mode  ThreadSendMode
	cause error
}

func callThreadSendProtocol(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, operation string) (ThreadSendMode, error) {
	switch operation {
	case "observe":
		return ObserveThreadSendModeInTx(ctx, tx, orgID, sessionID)
	case "legacy":
		return "", RequireLegacyThreadSendModeInTx(ctx, tx, orgID, sessionID)
	default:
		return "", RequireActiveThreadSendModeInTx(ctx, tx, orgID, sessionID)
	}
}

func TestThreadSendModeInTxFiniteBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, operation string
		observed        ThreadSendMode
		expected        threadSendProtocolResult
	}{
		{name: "observe legacy", operation: "observe", observed: ThreadSendModeLegacy, expected: threadSendProtocolResult{mode: ThreadSendModeLegacy}},
		{name: "observe normalizing", operation: "observe", observed: ThreadSendModeNormalizing, expected: threadSendProtocolResult{mode: ThreadSendModeNormalizing}},
		{name: "observe active", operation: "observe", observed: ThreadSendModeActive, expected: threadSendProtocolResult{mode: ThreadSendModeActive}},
		{name: "require legacy matches", operation: "legacy", observed: ThreadSendModeLegacy},
		{name: "require legacy refuses normalizing", operation: "legacy", observed: ThreadSendModeNormalizing, expected: threadSendProtocolResult{cause: ErrThreadSendModeChanged}},
		{name: "require legacy refuses active", operation: "legacy", observed: ThreadSendModeActive, expected: threadSendProtocolResult{cause: ErrThreadSendModeChanged}},
		{name: "require active refuses legacy", operation: "active", observed: ThreadSendModeLegacy, expected: threadSendProtocolResult{cause: ErrThreadSendModeChanged}},
		{name: "require active refuses normalizing", operation: "active", observed: ThreadSendModeNormalizing, expected: threadSendProtocolResult{cause: ErrThreadSendModeChanged}},
		{name: "require active matches", operation: "active", observed: ThreadSendModeActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			if tt.observed == ThreadSendModeLegacy {
				snapshot = legacyLifecycleModeSnapshot()
			} else if tt.observed == ThreadSendModeNormalizing {
				makeInactiveLifecycleModeSnapshot(&snapshot, models.LifecycleCutoverStateNormalizing)
				snapshot.normalization = incompleteLifecycleNormalization()
			}
			// Existing G.1 fixtures pin the independent exact policy, shared
			// interlock and fresh tenant/parent observation SQL in that order.
			expectLifecycleModeRead(mock, snapshot)
			mode, err := callThreadSendProtocol(ctx, mock, snapshot.orgID, snapshot.sessionID, tt.operation)
			require.Equal(t, tt.expected, threadSendProtocolResult{mode: mode, cause: err}, "the public boundary must map only the three resolved modes and report a fixed-branch mismatch without fallback")
			require.NoError(t, mock.ExpectationsWereMet(), "each mode API may invoke only one G.1 resolution without settings, row locks, mutations, retries, effects or transaction settlement")
		})
	}
}

func TestThreadSendModeInTxPreservesResolverFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		operation, failure string
		cause              error
	}{}
	for _, operation := range []string{"observe", "legacy", "active"} {
		for _, failure := range []struct {
			name  string
			cause error
		}{
			{name: "policy query", cause: io.ErrUnexpectedEOF},
			{name: "busy interlock"},
			{name: "interlock query", cause: io.ErrUnexpectedEOF},
			{name: "missing parent", cause: pgx.ErrNoRows},
			{name: "parent query", cause: io.ErrUnexpectedEOF},
			{name: "foreign tenant", cause: pgx.ErrNoRows},
			{name: "foreign session", cause: pgx.ErrNoRows},
			{name: "retained stamp without cutover", cause: ErrLifecycleGuardProvenanceRequired},
			{name: "unknown cutover state", cause: ErrLifecycleGuardProvenanceRequired},
			{name: "parent stamp scan"},
			{name: "cancellation after scan", cause: context.Canceled},
		} {
			tests = append(tests, struct {
				operation, failure string
				cause              error
			}{operation, failure.name, failure.cause})
		}
	}
	for _, tt := range tests {
		t.Run(tt.operation+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			mock, budget := newLifecycleScopeMock(t)
			ctx, cancel := context.WithCancel(budget)
			defer cancel()
			snapshot := activeLifecycleModeSnapshot()
			orgID, sessionID := snapshot.orgID, snapshot.sessionID
			var tx pgx.Tx = mock
			if tt.failure == "policy query" {
				mock.ExpectQuery(lifecycleTransactionPolicyQuery).WillReturnError(io.ErrUnexpectedEOF)
			} else {
				expectLifecycleScopePolicy(mock)
				lock := mock.ExpectQuery(cutoverSharedSQL).WithArgs(newCutoverUnitFixture().key)
				switch tt.failure {
				case "busy interlock":
					lock.WillReturnRows(cutoverLockRows().AddRow(false))
				case "interlock query":
					lock.WillReturnError(io.ErrUnexpectedEOF)
				default:
					lock.WillReturnRows(cutoverLockRows().AddRow(true))
					query := mock.ExpectQuery(lifecycleModeSQL).WithArgs(orgID, sessionID)
					switch tt.failure {
					case "missing parent":
						query.WillReturnRows(lifecycleModeSnapshotRows())
					case "parent query":
						query.WillReturnError(io.ErrUnexpectedEOF)
					default:
						if tt.failure == "retained stamp without cutover" {
							snapshot = legacyLifecycleModeSnapshot()
							snapshot.protocolVersion = 1
						} else if tt.failure == "unknown cutover state" {
							snapshot.state = requestLifecycleScopePointer(models.LifecycleCutoverState("future-sensitive-state"))
						}
						values := lifecycleModeSnapshotValues(snapshot)
						switch tt.failure {
						case "foreign tenant":
							values[0] = uuid.New()
						case "foreign session":
							values[1] = uuid.New()
						case "parent stamp scan":
							values[2] = "synthetic nonnumeric stamp"
						case "cancellation after scan":
							tx = &lifecycleScopeCancellingTx{Tx: mock, query: lifecycleModeObservationQuery, cancel: cancel}
						}
						query.WillReturnRows(lifecycleModeSnapshotRows().AddRow(values...))
					}
				}
			}
			mode, err := callThreadSendProtocol(ctx, tx, orgID, sessionID, tt.operation)
			require.Error(t, err, "every failed provenance, acquisition or scoped observation must remain a failed mode check")
			if tt.failure == "busy interlock" {
				requireCutoverPrivateBusy(t, err)
			} else if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "the public mode boundary must preserve the G.1 resolver's exact error identity without remapping it to branch change")
			}
			if tt.cause == ErrLifecycleGuardProvenanceRequired {
				requireLifecycleProvenanceError(t, err, snapshot)
			}
			require.NotErrorIs(t, err, ErrThreadSendModeChanged, "an invalid or failed observation is not a different valid mode and cannot authorize a branch fallback")
			require.Equal(t, ThreadSendMode(""), mode, "all errors must return no public mode rather than silently defaulting to legacy")
			require.NoError(t, mock.ExpectationsWereMet(), "failed mode APIs must retain the exact resolver query boundary without retries, settings, mutations or settlement")
		})
	}
}

func TestThreadSendModeInTxRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		operation, invalid, expectedText string
		cause                            error
	}{}
	for _, operation := range []string{"observe", "legacy", "active"} {
		for _, invalid := range []struct {
			name, text string
			cause      error
		}{
			{name: "nil context", text: "nil context"},
			{name: "nil transaction", text: "nil transaction"},
			{name: "typed nil transaction", text: "nil transaction"},
			{name: "zero organization", text: "organization"},
			{name: "zero session", text: "session"},
			{name: "missing deadline", text: "deadline"},
			{name: "overlong deadline", text: "deadline"},
			{name: "canceled context", cause: context.Canceled},
			{name: "expired context", cause: context.DeadlineExceeded},
		} {
			tests = append(tests, struct {
				operation, invalid, expectedText string
				cause                            error
			}{operation, invalid.name, invalid.text, invalid.cause})
		}
	}
	for _, tt := range tests {
		t.Run(tt.operation+"/"+tt.invalid, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			var tx pgx.Tx = mock
			switch tt.invalid {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var typedNil *pgxpool.Tx
				tx = typedNil
			case "zero organization":
				snapshot.orgID = uuid.Nil
			case "zero session":
				snapshot.sessionID = uuid.Nil
			case "missing deadline":
				ctx = context.Background()
			case "overlong deadline":
				overlong, cancel := context.WithTimeout(context.Background(), 10*LifecycleTransactionTimeout)
				defer cancel()
				ctx = overlong
			case "canceled context":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "expired context":
				expired, cancel := context.WithDeadline(context.Background(), time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC))
				defer cancel()
				ctx = expired
			}
			mode, err := callThreadSendProtocol(ctx, tx, snapshot.orgID, snapshot.sessionID, tt.operation)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "invalid unit contexts must preserve their cancellation or expiry identity")
			} else {
				require.ErrorContains(t, err, tt.expectedText, "mode APIs must reject missing exact caller prerequisites before issuing SQL")
			}
			require.Equal(t, ThreadSendMode(""), mode, "nil or invalid prerequisites must expose no default legacy observation")
			require.NotErrorIs(t, err, ErrThreadSendModeChanged, "missing prerequisites cannot become a valid changed-mode observation")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid inputs must cause no query, settings write, row lock or transaction settlement")
		})
	}
}
