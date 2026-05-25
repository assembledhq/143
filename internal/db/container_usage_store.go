package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// ContainerUsageStore handles persistence of container usage events.
type ContainerUsageStore struct {
	db DBTX
}

// NewContainerUsageStore creates a new ContainerUsageStore.
func NewContainerUsageStore(db DBTX) *ContainerUsageStore {
	return &ContainerUsageStore{db: db}
}

// RecordStart inserts a new container usage event when a sandbox is created.
func (s *ContainerUsageStore) RecordStart(ctx context.Context, event *models.ContainerUsageEvent) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO container_usage_events (id, org_id, session_id, container_id, provider, cpu_limit, memory_limit_mb, disk_limit_mb, image, started_at)
		VALUES (@id, @org_id, @session_id, @container_id, @provider, @cpu_limit, @memory_limit_mb, @disk_limit_mb, @image, @started_at)`,
		pgx.NamedArgs{
			"id":              event.ID,
			"org_id":          event.OrgID,
			"session_id":      event.SessionID,
			"container_id":    event.ContainerID,
			"provider":        event.Provider,
			"cpu_limit":       event.CPULimit,
			"memory_limit_mb": event.MemoryLimitMB,
			"disk_limit_mb":   event.DiskLimitMB,
			"image":           event.Image,
			"started_at":      event.StartedAt,
		})
	if err != nil {
		return fmt.Errorf("record container start: %w", err)
	}
	return nil
}

// RecordStop updates a container usage event when the sandbox is destroyed.
// It computes duration_ms and container_minutes from started_at → stoppedAt.
// Returns an error if no matching event was found (e.g. RecordStart failed).
// lint:allow-no-orgid reason="lifecycle close by opaque event UUID; eventID is globally unique"
func (s *ContainerUsageStore) RecordStop(ctx context.Context, eventID uuid.UUID, stoppedAt time.Time, exitReason string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE container_usage_events
		SET stopped_at = @stopped_at,
		    duration_ms = EXTRACT(EPOCH FROM (@stopped_at::timestamptz - started_at)) * 1000,
		    container_minutes = EXTRACT(EPOCH FROM (@stopped_at::timestamptz - started_at)) / 60.0,
		    exit_reason = @exit_reason
		WHERE id = @id`,
		pgx.NamedArgs{
			"id":          eventID,
			"stopped_at":  stoppedAt,
			"exit_reason": exitReason,
		})
	if err != nil {
		return fmt.Errorf("record container stop: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("record container stop: no event found with id %s", eventID)
	}
	return nil
}

// GetUsageSummary returns aggregated container usage for an org in a time range.
func (s *ContainerUsageStore) GetUsageSummary(ctx context.Context, orgID uuid.UUID, start, end time.Time) (*models.UsageSummary, error) {
	// Total minutes and session count.
	var totalMinutes float64
	var totalSessions int
	// Use COALESCE to include still-running containers (stopped_at IS NULL)
	// by computing their duration as now() - started_at.
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(
			COALESCE(container_minutes, EXTRACT(EPOCH FROM (now() - started_at)) / 60.0)
		), 0), COUNT(DISTINCT session_id)
		FROM container_usage_events
		WHERE org_id = @org_id AND started_at >= @start AND started_at < @end`,
		pgx.NamedArgs{"org_id": orgID, "start": start, "end": end},
	).Scan(&totalMinutes, &totalSessions)
	if err != nil {
		return nil, fmt.Errorf("query usage totals: %w", err)
	}

	// Capacity breakdown.
	rows, err := s.db.Query(ctx, `
		SELECT cpu_limit, memory_limit_mb, disk_limit_mb,
		       COALESCE(SUM(
		           COALESCE(container_minutes, EXTRACT(EPOCH FROM (now() - started_at)) / 60.0)
		       ), 0) AS minutes,
		       COUNT(DISTINCT session_id) AS sessions
		FROM container_usage_events
		WHERE org_id = @org_id AND started_at >= @start AND started_at < @end
		GROUP BY cpu_limit, memory_limit_mb, disk_limit_mb
		ORDER BY cpu_limit, memory_limit_mb, disk_limit_mb`,
		pgx.NamedArgs{"org_id": orgID, "start": start, "end": end},
	)
	if err != nil {
		return nil, fmt.Errorf("query capacity breakdown: %w", err)
	}
	defer rows.Close()

	buckets := make([]models.CapacityBucket, 0)
	for rows.Next() {
		var b models.CapacityBucket
		if err := rows.Scan(&b.CPULimit, &b.MemoryLimitMB, &b.DiskLimitMB, &b.ContainerMinutes, &b.SessionCount); err != nil {
			return nil, fmt.Errorf("scan capacity bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capacity buckets: %w", err)
	}

	// Peak concurrent containers (sampled by overlapping time ranges).
	// NOTE: This self-join is O(n^2) in the number of events per org per period.
	// Acceptable for <10K events/period; replace with a rollup table at higher scale.
	var peakConcurrent int
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(MAX(concurrent), 0)
		FROM (
			SELECT COUNT(*) AS concurrent
			FROM container_usage_events e1
			JOIN container_usage_events e2
			  ON e2.org_id = e1.org_id
			 AND e2.started_at <= COALESCE(e1.stopped_at, now())
			 AND COALESCE(e2.stopped_at, now()) >= e1.started_at
			 AND e2.started_at < @end
			 AND e2.id != e1.id
			WHERE e1.org_id = @org_id AND e1.started_at >= @start AND e1.started_at < @end
			GROUP BY e1.id
		) sub`,
		pgx.NamedArgs{"org_id": orgID, "start": start, "end": end},
	).Scan(&peakConcurrent)
	if err != nil {
		return nil, fmt.Errorf("query peak concurrent: %w", err)
	}
	// The self-join counts overlapping peers; add 1 for the container itself.
	// Use totalSessions > 0 (not peakConcurrent > 0) so that a single
	// non-overlapping container correctly reports peak = 1.
	if totalSessions > 0 {
		peakConcurrent++
	}

	return &models.UsageSummary{
		OrgID:                 orgID,
		PeriodStart:           start,
		PeriodEnd:             end,
		TotalContainerMinutes: totalMinutes,
		TotalSessions:         totalSessions,
		PeakConcurrent:        peakConcurrent,
		ByCapacity:            buckets,
	}, nil
}

// CloseOrphans closes container usage events that were never stopped (e.g. due
// to server crash). It sets stopped_at = now() and exit_reason = "orphaned" for
// any event started before the cutoff that still has stopped_at IS NULL.
// Returns the number of rows updated.
// lint:allow-no-orgid reason="system-wide cleanup of orphaned events across all orgs"
func (s *ContainerUsageStore) CloseOrphans(ctx context.Context, startedBefore time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE container_usage_events
		SET stopped_at = now(),
		    duration_ms = EXTRACT(EPOCH FROM (now() - started_at)) * 1000,
		    container_minutes = EXTRACT(EPOCH FROM (now() - started_at)) / 60.0,
		    exit_reason = 'orphaned'
		WHERE stopped_at IS NULL AND started_at < @cutoff`,
		pgx.NamedArgs{"cutoff": startedBefore})
	if err != nil {
		return 0, fmt.Errorf("close orphaned usage events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CloseOpenByContainerID closes open usage events for a container destroyed
// outside the ordinary orchestrator defer path, such as worker-local sandbox
// GC. The container ID is globally opaque enough for this system cleanup path.
// lint:allow-no-orgid reason="system cleanup by opaque Docker container ID across all orgs"
func (s *ContainerUsageStore) CloseOpenByContainerID(ctx context.Context, containerID string, stoppedAt time.Time, exitReason string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE container_usage_events
		SET stopped_at = @stopped_at,
		    duration_ms = EXTRACT(EPOCH FROM (@stopped_at::timestamptz - started_at)) * 1000,
		    container_minutes = EXTRACT(EPOCH FROM (@stopped_at::timestamptz - started_at)) / 60.0,
		    exit_reason = @exit_reason
		WHERE container_id = @container_id
		  AND stopped_at IS NULL`,
		pgx.NamedArgs{
			"stopped_at":   stoppedAt,
			"exit_reason":  exitReason,
			"container_id": containerID,
		})
	if err != nil {
		return 0, fmt.Errorf("close open usage by container id: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountActive returns the number of container usage events that have not been
// stopped yet. Used by the observable gauge to report the true active count.
// lint:allow-no-orgid reason="system-wide gauge counting active containers across all orgs"
func (s *ContainerUsageStore) CountActive(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM container_usage_events WHERE stopped_at IS NULL`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active containers: %w", err)
	}
	return count, nil
}

// ListBySession returns all container usage events for a given session.
func (s *ContainerUsageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.ContainerUsageEvent, error) {
	const maxResults = 500
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, session_id, container_id, provider, cpu_limit, memory_limit_mb, disk_limit_mb, image,
		       started_at, stopped_at, duration_ms, container_minutes, exit_reason, created_at
		FROM container_usage_events
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY started_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "limit": maxResults},
	)
	if err != nil {
		return nil, fmt.Errorf("list usage by session: %w", err)
	}
	defer rows.Close()

	events := make([]models.ContainerUsageEvent, 0)
	for rows.Next() {
		var e models.ContainerUsageEvent
		if err := rows.Scan(
			&e.ID, &e.OrgID, &e.SessionID, &e.ContainerID, &e.Provider,
			&e.CPULimit, &e.MemoryLimitMB, &e.DiskLimitMB, &e.Image,
			&e.StartedAt, &e.StoppedAt, &e.DurationMs, &e.ContainerMinutes,
			&e.ExitReason, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan usage event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
