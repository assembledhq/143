package db

import (
	"context"
	"errors"
	"io"
	"strings"
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
	require.Equal(t, strings.Join(strings.Fields(`SELECT id FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
		ORDER BY id LIMIT $4`), " "), strings.Join(strings.Fields(threadSendVisibleLaneKeysQuery), " "),
		"discovery must retain exact tenant/parent, visible siblings and the explicit target without inferring a primary lane")
}

func TestLockThreadSendScopePreservesRawRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		hide bool
	}{
		{name: "visible lanes retain terminal and awaiting statuses"},
		{name: "hidden target and parent are locked without admitting them", hide: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f := newLifecycleScopeMockFixture()
			if tt.hide {
				at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				f.session.deletedAt, f.session.archivedAt, f.first.archivedAt = &at, &at, &at
			}
			expectThreadSendScopeParent(mock, f)
			expectThreadSendScopeKeys(mock, f, f.first.id, f.second.id)
			mock.ExpectQuery(lifecycleThreadScopeLockQuery).
				WithArgs(f.orgID, f.sessionID, []uuid.UUID{f.first.id, f.second.id}).
				WillReturnRows(lifecycleScopeThreadRows(f.first, f.second)).RowsWillBeClosed()
			actual, err := lockThreadSendScopeInTx(ctx, mock, f.orgID, f.sessionID, f.first.id)
			require.NoError(t, err, "raw scope acquisition must preserve the exact requested target and siblings")
			require.Equal(t, lockedLifecycleScope{session: f.session, threads: []lifecycleLockedThread{f.first, f.second}}, actual,
				"scope acquisition must not substitute eligibility, epochs, visibility or terminal-state policy")
			require.NoError(t, mock.ExpectationsWereMet(), "scope work must acquire parent before discovery and one ordered lane batch without mutation or settlement")
		})
	}
}

func TestLockThreadSendScopeDiscardsFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{name: "parent query", cause: io.ErrUnexpectedEOF},
		{name: "discovery query", cause: io.ErrUnexpectedEOF},
		{name: "discovery scan"},
		{name: "discovery iteration", cause: io.ErrUnexpectedEOF},
		{name: "empty discovery", cause: pgx.ErrNoRows},
		{name: "missing exact target", cause: pgx.ErrNoRows},
		{name: "zero discovered lane"},
		{name: "duplicate discovered lane"},
		{name: "unordered discovery"},
		{name: "discovery overflow", cause: ErrLifecycleConsumerDiscoveryIncomplete},
		{name: "lane query", cause: io.ErrUnexpectedEOF},
		{name: "lane missing", cause: pgx.ErrNoRows},
		{name: "lane scan"},
		{name: "lane iteration", cause: io.ErrUnexpectedEOF},
		{name: "lane tenant mismatch", cause: pgx.ErrNoRows},
		{name: "lane parent mismatch", cause: pgx.ErrNoRows},
		{name: "lane identity mismatch", cause: pgx.ErrNoRows},
		{name: "lane ordering mismatch", cause: pgx.ErrNoRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			f := newLifecycleScopeMockFixture()
			if tt.name == "parent query" {
				expectLifecycleScopePolicy(mock)
				mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(f.orgID, f.sessionID).WillReturnError(io.ErrUnexpectedEOF)
			} else {
				expectThreadSendScopeParent(mock, f)
				keys := pgxmock.NewRows([]string{"id"})
				discovery := mock.ExpectQuery(threadSendVisibleLaneKeysQuery).
					WithArgs(f.orgID, f.sessionID, f.first.id, defaultLifecycleConsumerLimits().laneAnchors+1)
				switch tt.name {
				case "discovery query":
					discovery.WillReturnError(io.ErrUnexpectedEOF)
				case "discovery scan":
					discovery.WillReturnRows(keys.AddRow("synthetic invalid UUID")).RowsWillBeClosed()
				case "discovery iteration":
					discovery.WillReturnRows(keys.AddRow(f.first.id).RowError(0, io.ErrUnexpectedEOF)).RowsWillBeClosed()
				case "empty discovery":
					discovery.WillReturnRows(keys).RowsWillBeClosed()
				case "missing exact target":
					discovery.WillReturnRows(keys.AddRow(f.second.id)).RowsWillBeClosed()
				case "zero discovered lane":
					discovery.WillReturnRows(keys.AddRow(uuid.Nil).AddRow(f.first.id)).RowsWillBeClosed()
				case "duplicate discovered lane":
					discovery.WillReturnRows(keys.AddRow(f.first.id).AddRow(f.first.id)).RowsWillBeClosed()
				case "unordered discovery":
					discovery.WillReturnRows(keys.AddRow(f.second.id).AddRow(f.first.id)).RowsWillBeClosed()
				case "discovery overflow":
					for i := 0; i <= defaultLifecycleConsumerLimits().laneAnchors; i++ {
						keys.AddRow(f.first.id)
					}
					discovery.WillReturnRows(keys).RowsWillBeClosed()
				default:
					discovery.WillReturnRows(keys.AddRow(f.first.id).AddRow(f.second.id)).RowsWillBeClosed()
					lane := mock.ExpectQuery(lifecycleThreadScopeLockQuery).
						WithArgs(f.orgID, f.sessionID, []uuid.UUID{f.first.id, f.second.id})
					lanes := lifecycleScopeThreadRows(f.first, f.second)
					switch tt.name {
					case "lane query":
						lane.WillReturnError(io.ErrUnexpectedEOF)
					case "lane missing":
						lane.WillReturnRows(lifecycleScopeThreadRows(f.first)).RowsWillBeClosed()
					case "lane scan":
						lane.WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "status", "archived_at", "admission_epoch", "end_epoch", "lifecycle_version"}).AddRow(f.first.id, f.orgID, f.sessionID, "idle", nil, "synthetic invalid epoch", nil, 0)).RowsWillBeClosed()
					case "lane iteration":
						lane.WillReturnRows(lanes.RowError(1, io.ErrUnexpectedEOF)).RowsWillBeClosed()
					default:
						switch tt.name {
						case "lane tenant mismatch":
							f.first.orgID = uuid.New()
						case "lane parent mismatch":
							f.first.sessionID = uuid.New()
						case "lane identity mismatch":
							f.first.id = uuid.New()
						case "lane ordering mismatch":
							f.first, f.second = f.second, f.first
						}
						lane.WillReturnRows(lifecycleScopeThreadRows(f.first, f.second)).RowsWillBeClosed()
					}
				}
			}
			actual, err := lockThreadSendScopeInTx(ctx, mock, f.orgID, f.sessionID, uuid.UUID{15: 1})
			require.Error(t, err, "incomplete scope acquisition must abort the whole unit without exposing earlier locks as a usable snapshot")
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "scope acquisition must preserve diagnostic and retry classification causes")
			}
			require.Equal(t, lockedLifecycleScope{}, actual, "failed discovery or lane acquisition must discard all partially observed rows")
			require.NoError(t, mock.ExpectationsWereMet(), "scope failure must stop before any later SQL or transaction settlement")
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
				mock.ExpectQuery(lifecycleThreadScopeLockQuery).
					WithArgs(f.orgID, f.sessionID, []uuid.UUID{f.first.id, f.second.id}).
					WillReturnRows(lifecycleScopeThreadRows(f.first, f.second)).RowsWillBeClosed()
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
	mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(f.orgID, f.sessionID).
		WillReturnRows(lifecycleScopeSessionRows(f.session))
}

func expectThreadSendScopeKeys(mock pgxmock.PgxPoolIface, f lifecycleScopeMockFixture, ids ...uuid.UUID) {
	rows := pgxmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	mock.ExpectQuery(threadSendVisibleLaneKeysQuery).
		WithArgs(f.orgID, f.sessionID, f.first.id, defaultLifecycleConsumerLimits().laneAnchors+1).
		WillReturnRows(rows).RowsWillBeClosed()
}
