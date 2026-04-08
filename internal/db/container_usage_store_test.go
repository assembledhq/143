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

func TestContainerUsageStore_RecordStart(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	event := &models.ContainerUsageEvent{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		SessionID:     uuid.New(),
		ContainerID:   "abc123",
		Provider:      "docker",
		CPULimit:      2,
		MemoryLimitMB: 4096,
		Image:         "143-sandbox:latest",
		StartedAt:     time.Now(),
	}

	mock.ExpectExec("INSERT INTO container_usage_events").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = store.RecordStart(context.Background(), event)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_RecordStop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	eventID := uuid.New()

	mock.ExpectExec("UPDATE container_usage_events").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.RecordStop(context.Background(), eventID, time.Now(), "completed")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_GetUsageSummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	orgID := uuid.New()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// Totals query
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_sessions"}).AddRow(125.5, 10))

	// Capacity breakdown query
	mock.ExpectQuery("SELECT cpu_limit, memory_limit_mb").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"cpu_limit", "memory_limit_mb", "minutes", "sessions"}).
				AddRow(2.0, 4096, 100.0, 8).
				AddRow(4.0, 8192, 25.5, 2),
		)

	// Peak concurrent query
	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(concurrent\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"peak"}).AddRow(3))

	summary, err := store.GetUsageSummary(context.Background(), orgID, start, end)
	require.NoError(t, err)
	require.Equal(t, 125.5, summary.TotalContainerMinutes)
	require.Equal(t, 10, summary.TotalSessions)
	require.Equal(t, 4, summary.PeakConcurrent) // 3 peers + 1 self
	require.Len(t, summary.ByCapacity, 2)
	require.Equal(t, 2.0, summary.ByCapacity[0].CPULimit)
	require.Equal(t, 4096, summary.ByCapacity[0].MemoryLimitMB)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	eventID := uuid.New()
	now := time.Now()
	dur := int64(120000)
	mins := 2.0
	reason := "completed"

	cols := []string{
		"id", "org_id", "session_id", "container_id", "provider",
		"cpu_limit", "memory_limit_mb", "image",
		"started_at", "stopped_at", "duration_ms", "container_minutes",
		"exit_reason", "created_at",
	}
	mock.ExpectQuery("SELECT .+ FROM container_usage_events WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).AddRow(
				eventID, orgID, sessionID, "ctr-1", "docker",
				2.0, 4096, "143-sandbox:latest",
				now, &now, &dur, &mins,
				&reason, now,
			),
		)

	events, err := store.ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, eventID, events[0].ID)
	require.Equal(t, "ctr-1", events[0].ContainerID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_CloseOrphans(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)

	mock.ExpectExec("UPDATE container_usage_events").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	closed, err := store.CloseOrphans(context.Background(), time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(3), closed)
	require.NoError(t, mock.ExpectationsWereMet())
}
