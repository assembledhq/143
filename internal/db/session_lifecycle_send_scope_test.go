package db

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestThreadSendScopeDiscoveryQuery(t *testing.T) {
	t.Parallel()
	require.Contains(t, threadSendVisibleLaneLockQuery, "org_id = $1 AND session_id = $2", "scope must retain exact tenant and parent")
	require.Contains(t, threadSendVisibleLaneLockQuery, "(archived_at IS NULL OR id = $3)", "hidden explicit target is retained without admitting hidden siblings")
	require.Contains(t, threadSendVisibleLaneLockQuery, "ORDER BY id LIMIT $4 FOR NO KEY UPDATE NOWAIT", "bounded discovery must acquire ordered nonwaiting lane locks")
}

func TestLockThreadSendScopePreservesRawRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		hide bool
	}{{name: "visible"}, {name: "hidden target", hide: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f := newLifecycleScopeMockFixture()
			if tt.hide {
				at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				f.session.deletedAt = &at
				f.session.archivedAt = &at
				f.first.archivedAt = &at
			}
			expectThreadSendScopeParent(mock, f)
			expectThreadSendScopeKeys(mock, f, f.first.id, f.second.id)
			actual, err := lockThreadSendScopeInTx(ctx, mock, f.orgID, f.sessionID, f.first.id)
			require.NoError(t, err, "ordered acquisition must preserve the raw scope")
			require.Equal(t, lockedLifecycleScope{session: f.session, threads: []lifecycleLockedThread{f.first, f.second}}, actual, "locking must not infer eligibility or change retained rows")
			require.NoError(t, mock.ExpectationsWereMet(), "parent lock must precede the single bounded lane-lock statement")
		})
	}
}

func TestLockThreadSendScopeDiscardsFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{"parent query", io.ErrUnexpectedEOF}, {"lane query", io.ErrUnexpectedEOF}, {"lane scan", nil}, {"lane iteration", io.ErrUnexpectedEOF},
		{"empty", pgx.ErrNoRows}, {"missing target", pgx.ErrNoRows}, {"zero lane", pgx.ErrNoRows}, {"duplicate", pgx.ErrNoRows}, {"unordered", pgx.ErrNoRows},
		{"overflow", ErrLifecycleConsumerDiscoveryIncomplete}, {"tenant mismatch", pgx.ErrNoRows}, {"parent mismatch", pgx.ErrNoRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f := newLifecycleScopeMockFixture()
			if tt.name == "parent query" {
				expectLifecycleScopePolicy(mock)
				mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(pgx.QueryExecModeExec, f.orgID, f.sessionID).WillReturnError(io.ErrUnexpectedEOF)
			} else {
				expectThreadSendScopeParent(mock, f)
				query := mock.ExpectQuery(threadSendVisibleLaneLockQuery).WithArgs(pgx.QueryExecModeExec, f.orgID, f.sessionID, f.first.id, defaultLifecycleConsumerLimits().laneAnchors+1)
				lanes := []lifecycleLockedThread{f.first, f.second}
				switch tt.name {
				case "empty":
					lanes = nil
				case "missing target":
					lanes = []lifecycleLockedThread{f.second}
				case "zero lane":
					lanes[0].id = uuid.Nil
				case "duplicate":
					lanes[1] = lanes[0]
				case "unordered":
					lanes[0], lanes[1] = lanes[1], lanes[0]
				case "overflow":
					lanes = make([]lifecycleLockedThread, defaultLifecycleConsumerLimits().laneAnchors+1)
				case "tenant mismatch":
					lanes[0].orgID = uuid.New()
				case "parent mismatch":
					lanes[0].sessionID = uuid.New()
				}
				rows := lifecycleScopeThreadRows(lanes...)
				switch tt.name {
				case "lane query":
					query.WillReturnError(io.ErrUnexpectedEOF)
				case "lane scan":
					query.WillReturnRows(pgxmock.NewRows([]string{"bad"}).AddRow("invalid")).RowsWillBeClosed()
				case "lane iteration":
					query.WillReturnRows(rows.RowError(0, io.ErrUnexpectedEOF)).RowsWillBeClosed()
				default:
					query.WillReturnRows(rows).RowsWillBeClosed()
				}
			}
			actual, err := lockThreadSendScopeInTx(ctx, mock, f.orgID, f.sessionID, f.first.id)
			require.Error(t, err, "incomplete or contradictory scope must fail closed")
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "scope refusal must retain its cause")
			}
			require.Equal(t, lockedLifecycleScope{}, actual, "failed scope must not expose partial observations")
			require.NoError(t, mock.ExpectationsWereMet(), "scope failure must not mutate or settle the owner's transaction")
		})
	}
}

func TestThreadSendScopeInputValidation(t *testing.T) {
	t.Parallel()
	tests := []string{"nil context", "nil transaction", "typed nil transaction", "cancelled", "zero org", "zero session", "zero lane"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f := newLifecycleScopeMockFixture()
			var tx pgx.Tx = mock
			var cause error
			switch name {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var nilTx *pgxpool.Tx
				tx = nilTx
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, cause = cancelled, context.Canceled
			case "zero org":
				f.orgID = uuid.Nil
			case "zero session":
				f.sessionID = uuid.Nil
			case "zero lane":
				f.first.id = uuid.Nil
			}
			err := RequireLegacyThreadSendScopeInTx(ctx, tx, f.orgID, f.sessionID, f.first.id)
			require.Error(t, err, "invalid scope inputs must fail before mode observation, row acquisition or any mutation")
			if cause != nil {
				require.ErrorIs(t, err, cause, "scope guard must preserve context cancellation")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "invalid scope inputs must not issue SQL")
		})
	}
}

func TestRequireLegacyThreadSendScopeRechecksMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode ThreadSendMode
	}{
		{name: "fresh legacy locks parent and sorted lanes", mode: ThreadSendModeLegacy},
		{name: "active cannot fall back to legacy", mode: ThreadSendModeActive},
		{name: "normalization cannot mutate through legacy", mode: ThreadSendModeNormalizing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			if tt.mode == ThreadSendModeLegacy {
				snapshot = legacyLifecycleModeSnapshot()
			} else if tt.mode == ThreadSendModeNormalizing {
				makeInactiveLifecycleModeSnapshot(&snapshot, models.LifecycleCutoverStateNormalizing)
				snapshot.normalization = incompleteLifecycleNormalization()
			}
			expectLifecycleModeRead(mock, snapshot)
			f := newLifecycleScopeMockFixture()
			f.orgID, f.sessionID = snapshot.orgID, snapshot.sessionID
			f.session.orgID, f.session.id = f.orgID, f.sessionID
			f.first.orgID, f.first.sessionID = f.orgID, f.sessionID
			f.second.orgID, f.second.sessionID = f.orgID, f.sessionID
			if tt.mode == ThreadSendModeLegacy {
				expectThreadSendScopeParent(mock, f)
				expectThreadSendScopeKeys(mock, f, f.first.id, f.second.id)
			}
			err := RequireLegacyThreadSendScopeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID, f.first.id)
			if tt.mode == ThreadSendModeLegacy {
				require.NoError(t, err, "fresh legacy observation may proceed to canonical scope acquisition")
			} else {
				require.ErrorIs(t, err, ErrThreadSendModeChanged, "different valid mode must refuse legacy without a second router or fallback")
			}
			require.False(t, errors.Is(err, ErrLifecycleConsumerDiscoveryIncomplete), "finite mode mismatches must not be reclassified as partial scope discovery")
			require.NoError(t, mock.ExpectationsWereMet(), "every legacy mutation guard must freshly resolve provenance before taking any parent or lane lock")
		})
	}
}

func expectThreadSendScopeParent(mock pgxmock.PgxPoolIface, f lifecycleScopeMockFixture) {
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(pgx.QueryExecModeExec, f.orgID, f.sessionID).
		WillReturnRows(lifecycleScopeSessionRows(f.session))
}

func expectThreadSendScopeKeys(mock pgxmock.PgxPoolIface, f lifecycleScopeMockFixture, ids ...uuid.UUID) {
	lanes := make([]lifecycleLockedThread, 0, len(ids))
	for _, id := range ids {
		if id == f.first.id {
			lanes = append(lanes, f.first)
		} else {
			lanes = append(lanes, f.second)
		}
	}
	mock.ExpectQuery(threadSendVisibleLaneLockQuery).WithArgs(pgx.QueryExecModeExec, f.orgID, f.sessionID, f.first.id, defaultLifecycleConsumerLimits().laneAnchors+1).WillReturnRows(lifecycleScopeThreadRows(lanes...)).RowsWillBeClosed()
}
