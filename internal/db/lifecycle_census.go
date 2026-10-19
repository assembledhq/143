package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LifecycleCensusLimit bounds transferred observations, not all PostgreSQL work.
const LifecycleCensusLimit = 1000

// LifecycleCensusWriter is a raw observation, private to collection, not JSON.
// In particular, arbitrary role/build/application strings are not report fields.
type LifecycleCensusWriter struct {
	Role     string
	BuildSHA string
	Version  int
}

type LifecycleCensusWriterSnapshot struct {
	ObservedAt time.Time
	Truncated  bool
	Rows       []LifecycleCensusWriter
}

type LifecycleCensusConnection struct {
	ApplicationName string
	BackendType     *string
	State           *string
	XactStart       *time.Time
	DetailsVisible  bool
}

type LifecycleCensusConnectionSnapshot struct {
	ObservedAt     time.Time
	FullVisibility *bool
	Truncated      bool
	Rows           []LifecycleCensusConnection
}

type LifecycleCensusStore struct{ db DBTX }

func NewLifecycleCensusStore(database DBTX) *LifecycleCensusStore {
	return &LifecycleCensusStore{db: database}
}

// Writers samples unexpired passive leases using the database statement clock.
// The left join preserves that clock even when no lease qualifies.
// lint:allow-no-orgid reason="read-only global passive writer telemetry contains no tenant-owned data"
func (s *LifecycleCensusStore) Writers(ctx context.Context) (LifecycleCensusWriterSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT statement_timestamp() AS observed_at,
		       writer.instance_id IS NOT NULL AS present,
		       writer.role, writer.build_sha, writer.lifecycle_writer_version AS version
		FROM (SELECT 1) AS observation
		LEFT JOIN LATERAL (
			SELECT instance_id, role, build_sha, lifecycle_writer_version, expires_at
			FROM lifecycle_writer_instances
			WHERE expires_at > statement_timestamp()
			ORDER BY expires_at, instance_id
			LIMIT 1001
		) AS writer ON true
		ORDER BY writer.expires_at, writer.instance_id`)
	if err != nil {
		return LifecycleCensusWriterSnapshot{}, fmt.Errorf("read passive writer observations: %w", err)
	}
	type row struct {
		ObservedAt time.Time
		Present    bool
		Role       *string
		BuildSHA   *string
		Version    *int
	}
	observations, err := pgx.CollectRows(rows, pgx.RowToStructByName[row])
	if err != nil {
		return LifecycleCensusWriterSnapshot{}, fmt.Errorf("scan passive writer observations: %w", err)
	}
	result := LifecycleCensusWriterSnapshot{Rows: []LifecycleCensusWriter{}}
	for _, item := range observations {
		result.ObservedAt = item.ObservedAt
		if !item.Present {
			continue
		}
		if len(result.Rows) == LifecycleCensusLimit {
			result.Truncated = true
			break
		}
		if item.Role == nil || item.BuildSHA == nil || item.Version == nil {
			return LifecycleCensusWriterSnapshot{}, fmt.Errorf("incomplete passive writer observation")
		}
		result.Rows = append(result.Rows, LifecycleCensusWriter{Role: *item.Role, BuildSHA: *item.BuildSHA, Version: *item.Version})
	}
	if result.ObservedAt.IsZero() {
		return LifecycleCensusWriterSnapshot{}, fmt.Errorf("missing passive writer observation clock")
	}
	return result, nil
}

// Connections retains masked backend types as unknown rather than losing them
// through a client-backend filter. pg_read_all_stats USAGE includes effective
// inherited privileges and superusers. Per-role visibility is not global proof.
// lint:allow-no-orgid reason="read-only current-database activity telemetry is global and excludes tenant data and query text"
func (s *LifecycleCensusStore) Connections(ctx context.Context) (LifecycleCensusConnectionSnapshot, error) {
	rows, err := s.db.Query(ctx, `
		WITH observation AS MATERIALIZED (
			SELECT statement_timestamp() AS observed_at,
			       pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') AS full_visibility
		)
		SELECT observation.observed_at, observation.full_visibility,
		       activity.pid IS NOT NULL AS present,
		       activity.application_name, activity.backend_type, activity.state, activity.xact_start,
		       COALESCE(observation.full_visibility OR pg_has_role(activity.usesysid, 'USAGE'), false) AS details_visible
		FROM observation
		LEFT JOIN LATERAL (
			SELECT pid, usesysid, application_name, backend_type, state, xact_start
			FROM pg_stat_activity
			WHERE datid = (SELECT oid FROM pg_database WHERE datname = current_database())
			  AND pid <> pg_backend_pid()
			  AND (backend_type = 'client backend' OR backend_type IS NULL)
			ORDER BY pid
			LIMIT 1001
		) AS activity ON true
		ORDER BY activity.pid`)
	if err != nil {
		return LifecycleCensusConnectionSnapshot{}, fmt.Errorf("read passive connection observations: %w", err)
	}
	type row struct {
		ObservedAt      time.Time
		FullVisibility  *bool
		Present         bool
		ApplicationName *string
		BackendType     *string
		State           *string
		XactStart       *time.Time
		DetailsVisible  bool
	}
	observations, err := pgx.CollectRows(rows, pgx.RowToStructByName[row])
	if err != nil {
		return LifecycleCensusConnectionSnapshot{}, fmt.Errorf("scan passive connection observations: %w", err)
	}
	result := LifecycleCensusConnectionSnapshot{Rows: []LifecycleCensusConnection{}}
	for _, item := range observations {
		result.ObservedAt, result.FullVisibility = item.ObservedAt, item.FullVisibility
		if !item.Present {
			continue
		}
		if len(result.Rows) == LifecycleCensusLimit {
			result.Truncated = true
			break
		}
		name := ""
		if item.ApplicationName != nil {
			name = *item.ApplicationName
		}
		result.Rows = append(result.Rows, LifecycleCensusConnection{ApplicationName: name, BackendType: item.BackendType, State: item.State, XactStart: item.XactStart, DetailsVisible: item.DetailsVisible})
	}
	if result.ObservedAt.IsZero() {
		return LifecycleCensusConnectionSnapshot{}, fmt.Errorf("missing passive connection observation clock")
	}
	return result, nil
}
