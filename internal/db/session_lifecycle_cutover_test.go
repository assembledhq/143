package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// These independent SQL goldens keep sharing production constants from hiding
// a changed lock mode, a settings write or a missing tenant/session predicate.
const (
	cutoverSharedSQL      = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1::text, 0))`
	cutoverExclusiveSQL   = `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`
	cutoverObservationSQL = `SELECT org_id, session_id, version, state, legacy_input_cutoff_id,
		normalization, activated_at, minimum_writer_version, resumability_capability
		FROM session_lifecycle_cutovers WHERE org_id = $1 AND session_id = $2`
)

func TestLifecycleCutoverInterlockRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, invalid, wantText string
		wantErr                 error
	}{
		{name: "nil context", invalid: "nil context", wantText: "nil context"},
		{name: "nil transaction", invalid: "nil transaction", wantText: "nil transaction"},
		{name: "typed nil transaction", invalid: "typed nil transaction", wantText: "nil transaction"},
		{name: "cancelled context", invalid: "cancelled context", wantErr: context.Canceled},
		{name: "expired context", invalid: "expired context", wantErr: context.DeadlineExceeded},
		{name: "missing deadline", invalid: "missing deadline", wantText: "deadline"},
		{name: "overlong deadline", invalid: "overlong deadline", wantText: "deadline"},
		{name: "zero organization", invalid: "zero organization", wantText: "org"},
		{name: "zero session", invalid: "zero session", wantText: "session"},
	}
	for _, target := range []string{"shared", "exclusive", "observation"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					mock, ctx := newLifecycleScopeMock(t)
					fixture := newCutoverUnitFixture()
					orgID, sessionID := fixture.orgID, fixture.sessionID
					var tx pgx.Tx = mock
					switch tt.invalid {
					case "nil context":
						ctx = nil
					case "nil transaction":
						tx = nil
					case "typed nil transaction":
						var typedNil *pgxpool.Tx
						tx = typedNil
					case "cancelled context":
						cancelled, cancel := context.WithCancel(ctx)
						cancel()
						ctx = cancelled
					case "expired context":
						expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
						t.Cleanup(cancel)
						ctx = expired
					case "missing deadline":
						ctx = context.Background()
					case "overlong deadline":
						overlong, cancel := context.WithTimeout(context.Background(), 10*LifecycleTransactionTimeout)
						t.Cleanup(cancel)
						ctx = overlong
					case "zero organization":
						orgID = uuid.Nil
					case "zero session":
						sessionID = uuid.Nil
					}
					got, err := callCutoverUnitTarget(ctx, tx, orgID, sessionID, target)
					if tt.wantErr != nil {
						require.ErrorIs(t, err, tt.wantErr, "cutover guard should preserve the caller's cancellation cause")
					} else {
						require.ErrorContains(t, err, tt.wantText, "invalid cutover inputs should fail before any SQL")
					}
					require.Equal(t, lifecycleCutoverObservation{}, got, "invalid inputs must not produce an observation")
					require.NoError(t, mock.ExpectationsWereMet(), "input validation must not acquire locks, reset policy or settle the transaction")
				})
			}
		})
	}
}

func TestLifecycleCutoverInterlockRequiresExistingPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy []any
		err    error
	}{
		{name: "wrong isolation", policy: []any{"repeatable read", "1", "250ms", "1500ms"}},
		{name: "missing writer marker", policy: []any{"read committed", "", "250ms", "1500ms"}},
		{name: "older writer marker", policy: []any{"read committed", "0", "250ms", "1500ms"}},
		{name: "wrong lock timeout", policy: []any{"read committed", "1", "0", "1500ms"}},
		{name: "wrong statement timeout", policy: []any{"read committed", "1", "250ms", "0"}},
		{name: "late policy scan failure", policy: []any{"read committed", "1", "250ms", struct{}{}}},
		{name: "policy lookup failure", err: io.ErrUnexpectedEOF},
	}
	for _, target := range []string{"shared", "exclusive", "observation"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					mock, ctx := newLifecycleScopeMock(t)
					fixture := newCutoverUnitFixture()
					query := mock.ExpectQuery(lifecycleTransactionPolicyQuery)
					if tt.err != nil {
						query.WillReturnError(tt.err)
					} else {
						query.WillReturnRows(lifecycleScopePolicyRows().AddRow(tt.policy...))
					}
					got, err := callCutoverUnitTarget(ctx, mock, fixture.orgID, fixture.sessionID, target)
					require.Error(t, err, "cutover acquisition must fail unless the transaction already has the exact lifecycle policy")
					if tt.err != nil {
						require.ErrorIs(t, err, tt.err, "policy query failure identity must survive wrapping")
					}
					require.Equal(t, lifecycleCutoverObservation{}, got, "unverified policy must not yield raw cutover state")
					require.NoError(t, mock.ExpectationsWereMet(), "policy rejection must precede advisory acquisition and must not install settings")
				})
			}
		})
	}
}

func TestLifecycleCutoverInterlockAcquisition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
	}{
		{name: "actual successful acquisition", kind: "success"},
		{name: "actual false is a private busy result", kind: "busy"},
		{name: "SQL error preserves its cause", kind: "query error"},
		{name: "missing lock result preserves no rows", kind: "missing"},
		{name: "malformed lock result is a scan failure", kind: "scan error"},
	}
	for _, target := range []string{"shared", "exclusive", "observation"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					mock, ctx := newLifecycleScopeMock(t)
					fixture := newCutoverUnitFixture()
					expectLifecycleScopePolicy(mock)
					query := mock.ExpectQuery(cutoverTargetSQL(target)).WithArgs(fixture.key)
					queryErr := errors.New("cutover advisory lookup unavailable")
					want := lifecycleCutoverObservation{}
					switch tt.kind {
					case "success":
						query.WillReturnRows(cutoverLockRows().AddRow(true))
						if target == "observation" {
							want = fixture.observation
							mock.ExpectQuery(cutoverObservationSQL).WithArgs(fixture.orgID, fixture.sessionID).
								WillReturnRows(cutoverObservationRows(want))
						}
					case "busy":
						query.WillReturnRows(cutoverLockRows().AddRow(false))
					case "query error":
						query.WillReturnError(queryErr)
					case "missing":
						query.WillReturnRows(cutoverLockRows())
					case "scan error":
						query.WillReturnRows(cutoverLockRows().AddRow("not-a-boolean"))
					}
					deadline, _ := ctx.Deadline()
					got, err := callCutoverUnitTarget(ctx, mock, fixture.orgID, fixture.sessionID, target)
					if tt.kind == "success" {
						require.NoError(t, err, "successful advisory acquisition should allow only the selected raw operation")
					} else {
						require.Error(t, err, "failed acquisition must not read cutover metadata or report success")
					}
					switch tt.kind {
					case "busy":
						requireCutoverPrivateBusy(t, err)
					case "query error":
						require.ErrorIs(t, err, queryErr, "acquisition SQL cause must remain discoverable")
					case "missing":
						require.ErrorIs(t, err, pgx.ErrNoRows, "missing acquisition row must not be treated as a busy or successful lock")
					}
					require.Equal(t, want, got, "only successful observation may return the complete raw row")
					after, _ := ctx.Deadline()
					require.Equal(t, deadline, after, "advisory helpers must not create or extend the unit budget")
					require.NoError(t, mock.ExpectationsWereMet(), "acquisition must issue one nonblocking try-lock without settings writes or transaction settlement")
				})
			}
		})
	}
}

func TestObserveLifecycleCutoverInTxPreservesRawMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*cutoverUnitFixture)
	}{
		{name: "SQL NULL metadata remains unknown", prepare: func(_ *cutoverUnitFixture) {}},
		{name: "normalizing state is returned without an eligibility decision", prepare: func(f *cutoverUnitFixture) {
			f.observation.state = models.LifecycleCutoverStateNormalizing
		}},
		{name: "active label alone is still raw metadata", prepare: func(f *cutoverUnitFixture) {
			f.observation.state = models.LifecycleCutoverStateActive
		}},
		{name: "explicit JSON null differs from SQL NULL", prepare: func(f *cutoverUnitFixture) {
			f.observation.normalization = json.RawMessage(`null`)
			f.observation.resumabilityCapability = json.RawMessage(`null`)
		}},
		{name: "future JSON shapes remain opaque", prepare: func(f *cutoverUnitFixture) {
			f.observation.normalization = json.RawMessage(`["future-cursor", 3, null]`)
			f.observation.resumabilityCapability = json.RawMessage(`"future-capability-format"`)
		}},
		{name: "unknown state and large independent values are not interpreted", prepare: func(f *cutoverUnitFixture) {
			cutoff, minimum := int64(math.MaxInt64), int(math.MaxInt32)
			at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			f.observation.version = math.MaxInt64
			f.observation.state = models.LifecycleCutoverState("future-state")
			f.observation.legacyInputCutoffID = &cutoff
			f.observation.minimumWriterVersion = &minimum
			f.observation.activatedAt = &at
			f.observation.normalization = json.RawMessage(`{"future":true,"cursor":9007199254740993}`)
			f.observation.resumabilityCapability = json.RawMessage(`{"provider":"synthetic","build":"fixture","receipt":{"v":3}}`)
		}},
		{name: "explicit zero minimum differs from absent minimum", prepare: func(f *cutoverUnitFixture) {
			minimum := 0
			f.observation.minimumWriterVersion = &minimum
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			tt.prepare(&fixture)
			expectCutoverSharedAcquisition(mock, fixture)
			mock.ExpectQuery(cutoverObservationSQL).WithArgs(fixture.orgID, fixture.sessionID).
				WillReturnRows(cutoverObservationRows(fixture.observation))
			got, err := observeLifecycleCutoverInTx(ctx, mock, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "raw observation must not validate or reinterpret persisted state and proof-shaped data")
			require.Equal(t, fixture.observation, got, "all cutover fields must round trip without defaulting, normalization or inference")
			require.NoError(t, mock.ExpectationsWereMet(), "metadata must be read in a separate scoped statement after successful shared acquisition")
		})
	}
}

func TestObserveLifecycleCutoverInTxDiscardsFailedSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
	}{
		{name: "absent cutover is not inferred version zero", kind: "missing"},
		{name: "observation SQL error preserves cause", kind: "query error"},
		{name: "version scan failure", kind: "version scan"},
		{name: "late nullable integer scan failure", kind: "minimum scan"},
		{name: "last JSON column scan failure", kind: "capability scan"},
		{name: "foreign tenant observation rejected", kind: "wrong org"},
		{name: "foreign session observation rejected", kind: "wrong session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			expectCutoverSharedAcquisition(mock, fixture)
			query := mock.ExpectQuery(cutoverObservationSQL).WithArgs(fixture.orgID, fixture.sessionID)
			queryErr := errors.New("cutover metadata unavailable")
			values := cutoverObservationValues(fixture.observation)
			switch tt.kind {
			case "missing":
				query.WillReturnRows(cutoverObservationRows())
			case "query error":
				query.WillReturnError(queryErr)
			default:
				switch tt.kind {
				case "version scan":
					values[2] = "not-a-version"
				case "minimum scan":
					values[7] = "not-an-integer"
				case "capability scan":
					values[8] = struct{}{}
				case "wrong org":
					values[0] = uuid.New()
				case "wrong session":
					values[1] = uuid.New()
				}
				query.WillReturnRows(cutoverObservationRows().AddRow(values...))
			}
			got, err := observeLifecycleCutoverInTx(ctx, mock, fixture.orgID, fixture.sessionID)
			require.Error(t, err, "missing, malformed or mis-scoped cutover metadata must fail observation")
			if tt.kind == "query error" {
				require.ErrorIs(t, err, queryErr, "observation SQL failure should retain its original cause")
			}
			if tt.kind == "missing" || tt.kind == "wrong org" || tt.kind == "wrong session" {
				require.ErrorIs(t, err, pgx.ErrNoRows, "absent exact membership must remain not-found rather than a compatible version-zero result")
			}
			require.Equal(t, lifecycleCutoverObservation{}, got, "even a last-column error must discard every decoded observation field")
			require.NoError(t, mock.ExpectationsWereMet(), "observation failure must not settle, retry or mutate the caller's transaction")
		})
	}
}

func TestLifecycleCutoverInterlockCancellationWinsAfterReads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, target, cancelQuery string
		acquired                  bool
	}{
		{name: "shared policy cancellation", target: "shared", cancelQuery: lifecycleTransactionPolicyQuery},
		{name: "exclusive policy cancellation", target: "exclusive", cancelQuery: lifecycleTransactionPolicyQuery},
		{name: "shared successful scan cancellation", target: "shared", cancelQuery: cutoverSharedSQL, acquired: true},
		{name: "exclusive successful scan cancellation", target: "exclusive", cancelQuery: cutoverExclusiveSQL, acquired: true},
		{name: "shared false scan cancellation is not busy", target: "shared", cancelQuery: cutoverSharedSQL},
		{name: "exclusive false scan cancellation is not busy", target: "exclusive", cancelQuery: cutoverExclusiveSQL},
		{name: "observation shared scan cancellation prevents metadata read", target: "observation", cancelQuery: cutoverSharedSQL, acquired: true},
		{name: "observation completed metadata scan cancellation", target: "observation", cancelQuery: cutoverObservationSQL, acquired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, budgetCtx := newLifecycleScopeMock(t)
			ctx, cancel := context.WithCancel(budgetCtx)
			t.Cleanup(cancel)
			fixture := newCutoverUnitFixture()
			expectLifecycleScopePolicy(mock)
			if tt.cancelQuery != lifecycleTransactionPolicyQuery {
				mock.ExpectQuery(cutoverTargetSQL(tt.target)).WithArgs(fixture.key).
					WillReturnRows(cutoverLockRows().AddRow(tt.acquired))
			}
			if tt.cancelQuery == cutoverObservationSQL {
				mock.ExpectQuery(cutoverObservationSQL).WithArgs(fixture.orgID, fixture.sessionID).
					WillReturnRows(cutoverObservationRows(fixture.observation))
			}
			cancelQuery := tt.cancelQuery
			if cancelQuery == cutoverObservationSQL {
				// The mock pins the independent SQL golden with normalized
				// whitespace; the cancellation hook matches the actual query.
				cancelQuery = lifecycleCutoverObservationQuery
			}
			tx := &lifecycleScopeCancellingTx{Tx: mock, query: cancelQuery, cancel: cancel}
			got, err := callCutoverUnitTarget(ctx, tx, fixture.orgID, fixture.sessionID, tt.target)
			require.ErrorIs(t, err, context.Canceled, "cancellation after a completed scan must win over success or advisory busy")
			var busy *lifecycleCutoverInterlockBusyError
			require.False(t, errors.As(err, &busy), "a cancelled try-lock result must not be mislabeled as contention")
			require.Equal(t, lifecycleCutoverObservation{}, got, "cancelled observations must discard even fully decoded raw metadata")
			require.NoError(t, mock.ExpectationsWereMet(), "cancellation must stop subsequent SQL without retries or transaction settlement")
		})
	}
}

func TestLifecycleCutoverInterlockLeavesCallerTransactionOpen(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"shared", "exclusive", "observation"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			mock.ExpectBegin()
			tx, err := mock.Begin(ctx)
			require.NoError(t, err, "caller should begin its transaction before requesting a private interlock")
			expectLifecycleScopePolicy(mock)
			mock.ExpectQuery(cutoverTargetSQL(target)).WithArgs(fixture.key).WillReturnRows(cutoverLockRows().AddRow(true))
			want := lifecycleCutoverObservation{}
			if target == "observation" {
				want = fixture.observation
				mock.ExpectQuery(cutoverObservationSQL).WithArgs(fixture.orgID, fixture.sessionID).
					WillReturnRows(cutoverObservationRows(want))
			}
			mock.ExpectExec("SELECT 42").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectRollback()
			got, err := callCutoverUnitTarget(ctx, tx, fixture.orgID, fixture.sessionID, target)
			require.NoError(t, err, "private interlock should compose in the caller's existing transaction")
			require.Equal(t, want, got, "only the observation helper should return a raw row")
			_, err = tx.Exec(ctx, "SELECT 42")
			require.NoError(t, err, "caller must retain control of later transactional work")
			require.NoError(t, tx.Rollback(ctx), "caller must settle the unit after its later work")
			require.NoError(t, mock.ExpectationsWereMet(), "interlock helper must not begin, commit or roll back the caller's transaction")
		})
	}
}

func TestLifecycleCutoverBusyRetriesPreserveFailureExclusions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		wrap        func(error) error
		cancel      bool
		rollbackErr error
		wantErr     error
	}{
		{name: "joined network uncertainty", wrap: func(err error) error { return errors.Join(err, io.EOF) }, wantErr: io.EOF},
		{name: "wrapped joined network uncertainty", wrap: func(err error) error {
			return fmt.Errorf("outer admission: %w", errors.Join(err, io.EOF))
		}, wantErr: io.EOF},
		{name: "joined real SQLSTATE still cannot authorize replay", wrap: func(err error) error {
			return errors.Join(err, &pgconn.PgError{Code: "55P03", Message: "independent synthetic cause"})
		}},
		{name: "joined deadline remains nonretryable", wrap: func(err error) error {
			return errors.Join(err, context.DeadlineExceeded)
		}, wantErr: context.DeadlineExceeded},
		{name: "cancellation after actual busy", wrap: func(err error) error { return err }, cancel: true, wantErr: context.Canceled},
		{name: "failed rollback is not abort proof", wrap: func(err error) error { return err }, rollbackErr: io.ErrClosedPipe, wantErr: io.ErrClosedPipe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, budgetCtx := newLifecycleScopeMock(t)
			ctx, cancel := context.WithCancel(budgetCtx)
			t.Cleanup(cancel)
			fixture := newCutoverUnitFixture()
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			mock.ExpectQuery("SHOW transaction_isolation").WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow("read committed"))
			mock.ExpectExec(expectedLifecycleSettings).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			expectLifecycleScopePolicy(mock)
			mock.ExpectQuery(cutoverSharedSQL).WithArgs(fixture.key).WillReturnRows(cutoverLockRows().AddRow(false))
			mock.ExpectRollback().WillReturnError(tt.rollbackErr)
			calls := 0
			var original error
			err := runLifecycleTransaction(ctx, mock, func(workCtx context.Context, tx pgx.Tx) error {
				calls++
				original = tryLockLifecycleCutoverSharedInTx(workCtx, tx, fixture.orgID, fixture.sessionID)
				requireCutoverPrivateBusy(t, original)
				if tt.cancel {
					cancel()
				}
				return tt.wrap(original)
			})
			require.ErrorIs(t, err, original, "runner should retain the actual advisory-conflict cause after whole rollback")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "additional cancellation, uncertainty or rollback failures must remain visible")
			}
			require.Equal(t, 1, calls, "eligible advisory conflict must not bypass cancellation, uncertainty or failed-abort exclusions")
			require.NotErrorIs(t, err, ErrLifecycleContention, "an excluded advisory conflict must not be labeled exhausted safe contention")
			require.NoError(t, mock.ExpectationsWereMet(), "runner must perform exactly one whole rollback and no retry or commit")
		})
	}
}

type cutoverUnitFixture struct {
	orgID, sessionID uuid.UUID
	key              string
	observation      lifecycleCutoverObservation
}

func newCutoverUnitFixture() cutoverUnitFixture {
	orgID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	sessionID := uuid.MustParse("ffffffff-1111-4222-8333-444444444444")
	return cutoverUnitFixture{
		orgID: orgID, sessionID: sessionID,
		key: "session_lifecycle_cutover:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee:ffffffff-1111-4222-8333-444444444444",
		observation: lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID,
			state: models.LifecycleCutoverStateShadow},
	}
}

func callCutoverUnitTarget(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, target string) (lifecycleCutoverObservation, error) {
	switch target {
	case "shared":
		return lifecycleCutoverObservation{}, tryLockLifecycleCutoverSharedInTx(ctx, tx, orgID, sessionID)
	case "exclusive":
		return lifecycleCutoverObservation{}, tryLockLifecycleCutoverExclusiveInTx(ctx, tx, orgID, sessionID)
	default:
		return observeLifecycleCutoverInTx(ctx, tx, orgID, sessionID)
	}
}

func cutoverTargetSQL(target string) string {
	if target == "exclusive" {
		return cutoverExclusiveSQL
	}
	return cutoverSharedSQL
}

func cutoverLockRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"acquired"})
}

func expectCutoverSharedAcquisition(mock pgxmock.PgxPoolIface, fixture cutoverUnitFixture) {
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(cutoverSharedSQL).WithArgs(fixture.key).WillReturnRows(cutoverLockRows().AddRow(true))
}

func cutoverObservationRows(observations ...lifecycleCutoverObservation) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"org_id", "session_id", "version", "state", "legacy_input_cutoff_id", "normalization", "activated_at", "minimum_writer_version", "resumability_capability"})
	for _, observation := range observations {
		rows.AddRow(cutoverObservationValues(observation)...)
	}
	return rows
}

func cutoverObservationValues(observation lifecycleCutoverObservation) []any {
	return []any{observation.orgID, observation.sessionID, observation.version, observation.state,
		observation.legacyInputCutoffID, observation.normalization, observation.activatedAt,
		observation.minimumWriterVersion, observation.resumabilityCapability}
}

func requireCutoverPrivateBusy(t *testing.T, err error) {
	t.Helper()
	var busy *lifecycleCutoverInterlockBusyError
	require.ErrorAs(t, err, &busy, "an actual advisory false result must retain its private concrete conflict type")
	require.NotNil(t, busy, "an eligible advisory conflict must contain a real nonnil private cause")
	var pgErr *pgconn.PgError
	require.False(t, errors.As(err, &pgErr), "advisory false must not fabricate a PostgreSQL error or SQLSTATE")
	require.NotErrorIs(t, err, ErrLifecycleContention, "an acquisition conflict must not masquerade as exhausted confirmed-aborted retries")
	require.True(t, retryableLifecycleTransaction(err), "actual advisory conflict is eligible for owner retry only after a successful whole rollback")
}
