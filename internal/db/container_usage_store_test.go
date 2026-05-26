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
	eventID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	startedAt := time.Now()
	event := &models.ContainerUsageEvent{
		ID:            eventID,
		OrgID:         orgID,
		SessionID:     sessionID,
		ContainerID:   "abc123",
		Provider:      "docker",
		CPULimit:      2,
		MemoryLimitMB: 4096,
		DiskLimitMB:   10240,
		Image:         "143-sandbox:latest",
		StartedAt:     startedAt,
	}

	// Match actual named args so parameter ordering bugs are caught.
	mock.ExpectExec("INSERT INTO container_usage_events").
		WithArgs(
			eventID, orgID, sessionID, "abc123",
			"docker", 2.0, 4096, 10240, "143-sandbox:latest",
			startedAt,
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
	stoppedAt := time.Now()

	// Args ordered by first appearance in SQL: @stopped_at, @exit_reason, @id.
	mock.ExpectExec("UPDATE container_usage_events").
		WithArgs(stoppedAt, "completed", eventID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.RecordStop(context.Background(), eventID, stoppedAt, "completed")
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

	// Totals query — match orgID and time range.
	mock.ExpectQuery("SELECT COALESCE\\(SUM").
		WithArgs(orgID, start, end).
		WillReturnRows(pgxmock.NewRows([]string{"total_minutes", "total_sessions"}).AddRow(125.5, 10))

	// Capacity breakdown query
	mock.ExpectQuery("SELECT cpu_limit, memory_limit_mb, disk_limit_mb").
		WithArgs(orgID, start, end).
		WillReturnRows(
			pgxmock.NewRows([]string{"cpu_limit", "memory_limit_mb", "disk_limit_mb", "minutes", "sessions"}).
				AddRow(2.0, 4096, 10240, 100.0, 8).
				AddRow(4.0, 8192, 20480, 25.5, 2),
		)

	// Peak concurrent intervals. This expectation intentionally matches the
	// bounded interval fetch, not the old self-join aggregate.
	mock.ExpectQuery("SELECT started_at, COALESCE\\(stopped_at, now\\(\\)\\) AS stopped_at").
		WithArgs(orgID, start, end).
		WillReturnRows(pgxmock.NewRows([]string{"started_at", "stopped_at"}).
			AddRow(start.Add(1*time.Hour), start.Add(3*time.Hour)).
			AddRow(start.Add(2*time.Hour), start.Add(4*time.Hour)).
			AddRow(start.Add(2*time.Hour), start.Add(5*time.Hour)).
			AddRow(start.Add(3*time.Hour), start.Add(6*time.Hour)))

	summary, err := store.GetUsageSummary(context.Background(), orgID, start, end)
	require.NoError(t, err, "GetUsageSummary should not return an error")
	require.Equal(t, 125.5, summary.TotalContainerMinutes, "GetUsageSummary should preserve total minutes")
	require.Equal(t, 10, summary.TotalSessions, "GetUsageSummary should preserve total session count")
	require.Equal(t, 4, summary.PeakConcurrent, "GetUsageSummary should compute peak concurrency from fetched intervals")
	require.Len(t, summary.ByCapacity, 2, "GetUsageSummary should return all capacity buckets")
	require.Equal(t, 2.0, summary.ByCapacity[0].CPULimit, "GetUsageSummary should preserve capacity CPU limit")
	require.Equal(t, 4096, summary.ByCapacity[0].MemoryLimitMB, "GetUsageSummary should preserve capacity memory")
	require.Equal(t, 10240, summary.ByCapacity[0].DiskLimitMB, "GetUsageSummary should preserve capacity disk")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestContainerUsageStore_GetUsageSummaryPeakConcurrent(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		intervals []timeInterval
		expected  int
	}{
		{
			name:      "empty",
			intervals: nil,
			expected:  0,
		},
		{
			name: "single",
			intervals: []timeInterval{
				{start: base, stop: base.Add(10 * time.Minute)},
			},
			expected: 1,
		},
		{
			name: "non-overlapping",
			intervals: []timeInterval{
				{start: base, stop: base.Add(10 * time.Minute)},
				{start: base.Add(20 * time.Minute), stop: base.Add(30 * time.Minute)},
			},
			expected: 1,
		},
		{
			name: "partially overlapping",
			intervals: []timeInterval{
				{start: base, stop: base.Add(30 * time.Minute)},
				{start: base.Add(10 * time.Minute), stop: base.Add(40 * time.Minute)},
				{start: base.Add(35 * time.Minute), stop: base.Add(50 * time.Minute)},
			},
			expected: 2,
		},
		{
			name: "same-boundary intervals count as concurrent",
			intervals: []timeInterval{
				{start: base, stop: base.Add(10 * time.Minute)},
				{start: base.Add(10 * time.Minute), stop: base.Add(20 * time.Minute)},
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := computePeakConcurrent(tt.intervals)
			require.Equal(t, tt.expected, actual, "peak concurrency should match overlapping interval semantics")
		})
	}
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
		"cpu_limit", "memory_limit_mb", "disk_limit_mb", "image",
		"started_at", "stopped_at", "duration_ms", "container_minutes",
		"exit_reason", "created_at",
	}
	mock.ExpectQuery("SELECT .+ FROM container_usage_events WHERE org_id").
		WithArgs(orgID, sessionID, 500).
		WillReturnRows(
			pgxmock.NewRows(cols).AddRow(
				eventID, orgID, sessionID, "ctr-1", "docker",
				2.0, 4096, 10240, "143-sandbox:latest",
				now, &now, &dur, &mins,
				&reason, now,
			),
		)

	events, err := store.ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, eventID, events[0].ID)
	require.Equal(t, "ctr-1", events[0].ContainerID)
	require.Equal(t, 10240, events[0].DiskLimitMB)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_CloseOrphans(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	cutoff := time.Now().Add(-2 * time.Hour)

	mock.ExpectExec("UPDATE container_usage_events").
		WithArgs(cutoff).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	closed, err := store.CloseOrphans(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), closed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContainerUsageStore_CloseOpenByContainerID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewContainerUsageStore(mock)
	stoppedAt := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE container_usage_events").
		WithArgs(stoppedAt, "sandbox_gc_unreferenced", "container-123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	closed, err := store.CloseOpenByContainerID(context.Background(), "container-123", stoppedAt, "sandbox_gc_unreferenced")
	require.NoError(t, err, "CloseOpenByContainerID should close open usage rows for a destroyed container")
	require.Equal(t, int64(2), closed, "CloseOpenByContainerID should return the number of rows it closed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestContainerUsageStore_CountActive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewContainerUsageStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(7)))

	count, err := store.CountActive(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), count)
	require.NoError(t, mock.ExpectationsWereMet())
}
