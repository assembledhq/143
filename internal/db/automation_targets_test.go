package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func newTestAutomationTarget(orgID uuid.UUID) models.AutomationTarget {
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	return models.AutomationTarget{
		ID:             uuid.New(),
		OrgID:          orgID,
		AutomationID:   uuid.New(),
		RepositoryID:   uuid.New(),
		TargetKind:     models.AutomationTargetKindGitHubPullRequest,
		TargetKey:      "1234",
		LifecycleState: models.AutomationTargetLifecycleOpen,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func newTestAutomationTargetSession(orgID, targetID uuid.UUID, generation int) models.AutomationTargetSession {
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	return models.AutomationTargetSession{
		ID:         uuid.New(),
		OrgID:      orgID,
		TargetID:   targetID,
		Generation: generation,
		SessionID:  uuid.New(),
		Status:     models.AutomationTargetSessionStatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestAutomationTargetStore_LockOrCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		kind      models.AutomationTargetKind
		setupMock func(mock pgxmock.PgxPoolIface, target models.AutomationTarget)
		wantErr   error
		wantRow   bool
	}{
		{
			name: "upserts then locks the row",
			key:  "1234",
			kind: models.AutomationTargetKindGitHubPullRequest,
			setupMock: func(mock pgxmock.PgxPoolIface, target models.AutomationTarget) {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").
					WithArgs(anyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectExec("INSERT INTO automation_targets").
					WithArgs(anyArgs(5)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectQuery("SELECT .+ FROM automation_targets .+ FOR UPDATE").
					WithArgs(anyArgs(5)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetColumnNames).AddRow(AutomationTargetRow(target)...))
			},
			wantRow: true,
		},
		{
			name: "cross-org parents produce no row",
			key:  "1234",
			kind: models.AutomationTargetKindGitHubPullRequest,
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationTarget) {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").
					WithArgs(anyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectExec("INSERT INTO automation_targets").
					WithArgs(anyArgs(5)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 0))
				mock.ExpectQuery("SELECT .+ FROM automation_targets .+ FOR UPDATE").
					WithArgs(anyArgs(5)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetColumnNames))
			},
			wantErr: ErrAutomationTargetNotFound,
		},
		{
			name:      "rejects an empty key before touching the database",
			key:       "",
			kind:      models.AutomationTargetKindGitHubPullRequest,
			setupMock: func(pgxmock.PgxPoolIface, models.AutomationTarget) {},
			wantErr:   errAny,
		},
		{
			name:      "rejects an unknown kind before touching the database",
			key:       "1234",
			kind:      models.AutomationTargetKind("jira_issue"),
			setupMock: func(pgxmock.PgxPoolIface, models.AutomationTarget) {},
			wantErr:   errAny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			target := newTestAutomationTarget(uuid.New())
			mock.ExpectBegin()
			tt.setupMock(mock, target)
			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "mock transaction should begin")

			store := NewAutomationTargetStore(mock)
			got, err := store.LockOrCreate(context.Background(), tx, target.OrgID, target.AutomationID, target.RepositoryID, tt.kind, tt.key)
			switch {
			case tt.wantErr == errAny:
				require.Error(t, err, "invalid input should be rejected")
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr, "missing row should map to the sentinel error")
			default:
				require.NoError(t, err, "lock-or-create should succeed")
			}
			if tt.wantRow {
				require.Equal(t, target, got, "locked row should round-trip through the scanner")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// errAny marks table cases that expect some error without a sentinel.
var errAny = &sentinelAnyError{}

type sentinelAnyError struct{}

func (*sentinelAnyError) Error() string { return "any error" }

func TestAutomationTargetStore_GetActiveGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession)
		wantErr   error
	}{
		{
			name: "returns the active generation",
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT .+ FROM automation_target_sessions .+ status = 'active'").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(generation)...))
			},
		},
		{
			name: "no active generation maps to the sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT .+ FROM automation_target_sessions .+ status = 'active'").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames))
			},
			wantErr: ErrAutomationTargetGenerationNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			generation := newTestAutomationTargetSession(orgID, uuid.New(), 1)
			tt.setupMock(mock, generation)

			store := NewAutomationTargetStore(mock)
			got, err := store.GetActiveGeneration(context.Background(), nil, orgID, generation.TargetID)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "missing generation should map to the sentinel error")
			} else {
				require.NoError(t, err, "lookup should succeed")
				require.Equal(t, generation, got, "generation row should round-trip through the scanner")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationTargetStore_InsertGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession)
		wantErr   error
	}{
		{
			name: "advances the target, inserts the row, and marks the session owned",
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("UPDATE automation_targets\\s+SET active_generation = active_generation \\+ 1").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"active_generation"}).AddRow(generation.Generation))
				mock.ExpectQuery("INSERT INTO automation_target_sessions").
					WithArgs(anyArgs(4)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(generation)...))
				mock.ExpectExec("UPDATE sessions\\s+SET automation_owner_generation_id = @generation_id").
					WithArgs(anyArgs(3)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "unknown target maps to the sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationTargetSession) {
				mock.ExpectQuery("UPDATE automation_targets\\s+SET active_generation = active_generation \\+ 1").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"active_generation"}))
			},
			wantErr: ErrAutomationTargetNotFound,
		},
		{
			name: "cross-org session maps to the mismatch sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("UPDATE automation_targets\\s+SET active_generation = active_generation \\+ 1").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"active_generation"}).AddRow(generation.Generation))
				mock.ExpectQuery("INSERT INTO automation_target_sessions").
					WithArgs(anyArgs(4)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames))
			},
			wantErr: ErrAutomationTargetSessionMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			generation := newTestAutomationTargetSession(orgID, uuid.New(), 2)
			mock.ExpectBegin()
			tt.setupMock(mock, generation)
			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "mock transaction should begin")

			store := NewAutomationTargetStore(mock)
			got, err := store.InsertGeneration(context.Background(), tx, orgID, generation.TargetID, generation.SessionID)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "failure should map to the sentinel error")
			} else {
				require.NoError(t, err, "insert should succeed")
				require.Equal(t, generation, got, "inserted generation should round-trip through the scanner")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationTargetStore_RetireGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		releasePending bool
		setupMock      func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession)
		wantErr        error
	}{
		{
			name: "idle generation releases ownership immediately and requests a wake",
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generation.TargetID))
				mock.ExpectQuery("UPDATE automation_target_sessions g\\s+SET status = 'retired'").
					WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(generation)...))
				mock.ExpectExec("UPDATE sessions\\s+SET automation_owner_generation_id = NULL").
					WithArgs(anyArgs(3)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}).AddRow(time.Now()))
			},
		},
		{
			name:           "executing generation defers the release to the turn's completion",
			releasePending: true,
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generation.TargetID))
				mock.ExpectQuery("UPDATE automation_target_sessions g\\s+SET status = 'retired'").
					WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(generation)...))
				mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}).AddRow(time.Now()))
			},
		},
		{
			name: "already retired generation maps to the not-active sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, generation models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generation.TargetID))
				mock.ExpectQuery("UPDATE automation_target_sessions g\\s+SET status = 'retired'").
					WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames))
			},
			wantErr: ErrAutomationTargetGenerationNotActive,
		},
		{
			name: "unknown generation maps to the not-found sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationTargetSession) {
				mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}))
			},
			wantErr: ErrAutomationTargetGenerationNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			generation := newTestAutomationTargetSession(orgID, uuid.New(), 1)
			reason := models.AutomationTargetRetiredManualReset
			retiredAt := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
			generation.Status = models.AutomationTargetSessionStatusRetired
			generation.RetiredReason = &reason
			generation.RetiredAt = &retiredAt
			generation.OwnershipReleasePending = tt.releasePending

			mock.ExpectBegin()
			tt.setupMock(mock, generation)
			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "mock transaction should begin")

			store := NewAutomationTargetStore(mock)
			got, err := store.RetireGeneration(context.Background(), tx, orgID, generation.ID, reason)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "failure should map to the sentinel error")
			} else {
				require.NoError(t, err, "retirement should succeed")
				require.Equal(t, generation, got, "retired generation should round-trip through the scanner")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationTargetStore_RetireGeneration_RejectsUnknownReason(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err, "mock transaction should begin")

	store := NewAutomationTargetStore(mock)
	_, err = store.RetireGeneration(context.Background(), tx, uuid.New(), uuid.New(), models.AutomationTargetRetiredReason("bored"))
	require.Error(t, err, "unknown retire reason should be rejected before any query")
	require.NoError(t, mock.ExpectationsWereMet(), "no query should run for an invalid reason")
}

func TestAutomationTargetStore_RetireActiveGenerationsForAutomation(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	automationID := uuid.New()
	reason := models.AutomationTargetRetiredContinuityDisabled
	retiredAt := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	first := newTestAutomationTargetSession(orgID, uuid.New(), 1)
	second := newTestAutomationTargetSession(orgID, uuid.New(), 3)
	for _, g := range []*models.AutomationTargetSession{&first, &second} {
		g.Status = models.AutomationTargetSessionStatusRetired
		g.RetiredReason = &reason
		g.RetiredAt = &retiredAt
	}
	second.OwnershipReleasePending = true
	activeFirst := newTestAutomationTargetSession(orgID, first.TargetID, first.Generation)
	activeFirst.ID = first.ID
	activeSecond := newTestAutomationTargetSession(orgID, second.TargetID, second.Generation)
	activeSecond.ID = second.ID

	mock.ExpectBegin()
	// The automation-scoped advisory lock comes first, then targets are
	// locked in id order, and each active generation is read only after its
	// lock is held.
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(anyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT id\\s+FROM automation_targets\\s+WHERE org_id = @org_id AND automation_id = @automation_id\\s+ORDER BY id\\s+FOR UPDATE").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(first.TargetID).AddRow(second.TargetID))
	// first: idle, releases immediately.
	mock.ExpectQuery("SELECT .+ FROM automation_target_sessions .+ status = 'active'").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(activeFirst)...))
	mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(first.TargetID))
	mock.ExpectQuery("UPDATE automation_target_sessions g\\s+SET status = 'retired'").
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(first)...))
	mock.ExpectExec("UPDATE sessions\\s+SET automation_owner_generation_id = NULL").
		WithArgs(anyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}).AddRow(time.Now()))
	// second: executing, release deferred.
	mock.ExpectQuery("SELECT .+ FROM automation_target_sessions .+ status = 'active'").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(activeSecond)...))
	mock.ExpectQuery("SELECT t.id\\s+FROM automation_targets t").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(second.TargetID))
	mock.ExpectQuery("UPDATE automation_target_sessions g\\s+SET status = 'retired'").
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(second)...))
	mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}).AddRow(time.Now()))
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err, "mock transaction should begin")

	store := NewAutomationTargetStore(mock)
	retired, err := store.RetireActiveGenerationsForAutomation(context.Background(), tx, orgID, automationID, reason)
	require.NoError(t, err, "retiring every active generation should succeed")
	require.Equal(t, []models.AutomationTargetSession{first, second}, retired, "every active generation should be retired in target order")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAutomationTargetStore_RetireActiveGenerationsForAutomation_SkipsTargetsWithoutActiveGeneration(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(anyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT id\\s+FROM automation_targets\\s+WHERE org_id = @org_id AND automation_id = @automation_id\\s+ORDER BY id\\s+FOR UPDATE").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM automation_target_sessions .+ status = 'active'").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames))
	tx, err := mock.Begin(context.Background())
	require.NoError(t, err, "mock transaction should begin")

	store := NewAutomationTargetStore(mock)
	retired, err := store.RetireActiveGenerationsForAutomation(context.Background(), tx, orgID, uuid.New(), models.AutomationTargetRetiredContinuityDisabled)
	require.NoError(t, err, "a target whose generation was retired concurrently is skipped")
	require.Empty(t, retired, "nothing is retired when no generation is active")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAutomationTargetStore_SetLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    models.AutomationTargetLifecycleState
		affected int64
		wantErr  error
	}{
		{name: "updates an existing target", state: models.AutomationTargetLifecycleMerged, affected: 1},
		{name: "unknown target maps to the sentinel", state: models.AutomationTargetLifecycleClosed, affected: 0, wantErr: ErrAutomationTargetNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			mock.ExpectExec("UPDATE automation_targets\\s+SET lifecycle_state = @state").
				WithArgs(anyArgs(3)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.affected))

			store := NewAutomationTargetStore(mock)
			err = store.SetLifecycle(context.Background(), nil, uuid.New(), uuid.New(), tt.state)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "missing target should map to the sentinel error")
			} else {
				require.NoError(t, err, "lifecycle update should succeed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationTargetStore_SetLifecycle_RejectsUnknownState(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	store := NewAutomationTargetStore(mock)
	require.Error(t, store.SetLifecycle(context.Background(), nil, uuid.New(), uuid.New(), models.AutomationTargetLifecycleState("draft")), "unknown lifecycle state should be rejected before any query")
	require.NoError(t, mock.ExpectationsWereMet(), "no query should run for an invalid state")
}

func TestAutomationTargetStore_Wake(t *testing.T) {
	t.Parallel()

	t.Run("request returns the recorded time", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock should initialize")
		defer mock.Close()

		requested := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
		mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}).AddRow(requested))

		store := NewAutomationTargetStore(mock)
		got, err := store.RequestWake(context.Background(), nil, uuid.New(), uuid.New())
		require.NoError(t, err, "wake request should succeed")
		require.Equal(t, requested, got, "wake request should return the outbox timestamp")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("request on an unknown target maps to the sentinel", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock should initialize")
		defer mock.Close()

		mock.ExpectQuery("UPDATE automation_targets\\s+SET wake_requested_at = now\\(\\)").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"wake_requested_at"}))

		store := NewAutomationTargetStore(mock)
		_, err = store.RequestWake(context.Background(), nil, uuid.New(), uuid.New())
		require.ErrorIs(t, err, ErrAutomationTargetNotFound, "missing target should map to the sentinel error")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	tests := []struct {
		name     string
		affected int64
		want     bool
	}{
		{name: "clear removes a marker not newer than the request", affected: 1, want: true},
		{name: "clear leaves a newer marker in place", affected: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			mock.ExpectExec("UPDATE automation_targets\\s+SET wake_requested_at = NULL").
				WithArgs(anyArgs(3)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.affected))

			store := NewAutomationTargetStore(mock)
			cleared, err := store.ClearWake(context.Background(), nil, uuid.New(), uuid.New(), time.Now())
			require.NoError(t, err, "clear should succeed")
			require.Equal(t, tt.want, cleared, "clear should report whether the marker was removed")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationTargetStore_Getters(t *testing.T) {
	t.Parallel()

	t.Run("GetByID round-trips the target", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock should initialize")
		defer mock.Close()

		target := newTestAutomationTarget(uuid.New())
		mock.ExpectQuery("SELECT .+ FROM automation_targets\\s+WHERE id = @id AND org_id = @org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(AutomationTargetColumnNames).AddRow(AutomationTargetRow(target)...))

		store := NewAutomationTargetStore(mock)
		got, err := store.GetByID(context.Background(), target.OrgID, target.ID)
		require.NoError(t, err, "lookup should succeed")
		require.Equal(t, target, got, "target should round-trip through the scanner")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("GetGenerationByID maps a missing row to the sentinel", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock should initialize")
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM automation_target_sessions\\s+WHERE id = @id AND org_id = @org_id").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames))

		store := NewAutomationTargetStore(mock)
		_, err = store.GetGenerationByID(context.Background(), uuid.New(), uuid.New())
		require.ErrorIs(t, err, ErrAutomationTargetGenerationNotFound, "missing generation should map to the sentinel error")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("GetActiveGenerationBySession round-trips the generation", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock should initialize")
		defer mock.Close()

		generation := newTestAutomationTargetSession(uuid.New(), uuid.New(), 4)
		mock.ExpectQuery("SELECT .+ FROM automation_target_sessions\\s+WHERE org_id = @org_id AND session_id = @session_id AND status = 'active'").
			WithArgs(anyArgs(2)...).
			WillReturnRows(pgxmock.NewRows(AutomationTargetSessionColumnNames).AddRow(AutomationTargetSessionRow(generation)...))

		store := NewAutomationTargetStore(mock)
		got, err := store.GetActiveGenerationBySession(context.Background(), generation.OrgID, generation.SessionID)
		require.NoError(t, err, "lookup should succeed")
		require.Equal(t, generation, got, "generation should round-trip through the scanner")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}
