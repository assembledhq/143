package db

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// UsageRollupStore handles reading and writing pre-aggregated hourly usage data.
type UsageRollupStore struct {
	db DBTX
}

// NewUsageRollupStore creates a new UsageRollupStore.
func NewUsageRollupStore(db DBTX) *UsageRollupStore {
	return &UsageRollupStore{db: db}
}

// containerAggregate holds intermediate results during rollup computation.
type containerAggregate struct {
	totalMinutes float64
	totalStarts  int
	sessionIDs   map[uuid.UUID]struct{}
	durations    []float64 // seconds, for avg/p95
	// For peak concurrent: list of (start, stop) intervals within the hour.
	intervals []timeInterval
}

type timeInterval struct {
	start time.Time
	stop  time.Time
}

// tokenAggregate holds intermediate token results.
type tokenAggregate struct {
	inputTokens  int64
	outputTokens int64
	costUSD      float64
}

// RollupHour computes and upserts hourly aggregates for a single org and hour.
// It writes rows at multiple dimensional levels: org-total, per-user, per-tier.
func (s *UsageRollupStore) RollupHour(ctx context.Context, orgID uuid.UUID, hour time.Time) error {
	hour = hour.Truncate(time.Hour).UTC()
	hourEnd := hour.Add(time.Hour)

	// Query container usage events for this hour, joined with sessions for user_id.
	rows, err := s.db.Query(ctx, `
		SELECT
			e.id,
			e.session_id,
			s.created_by AS user_id,
			e.cpu_limit,
			e.memory_limit_mb,
			e.started_at,
			COALESCE(e.stopped_at, now()) AS stopped_at,
			COALESCE(e.container_minutes, EXTRACT(EPOCH FROM (COALESCE(e.stopped_at, now()) - e.started_at)) / 60.0) AS container_minutes,
			COALESCE(e.duration_ms, EXTRACT(EPOCH FROM (COALESCE(e.stopped_at, now()) - e.started_at)) * 1000) AS duration_ms
		FROM container_usage_events e
		JOIN sessions s ON s.id = e.session_id
		WHERE e.org_id = @org_id
		  AND e.started_at < @hour_end
		  AND COALESCE(e.stopped_at, now()) > @hour_start`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"hour_start": hour,
			"hour_end":   hourEnd,
		},
	)
	if err != nil {
		return fmt.Errorf("rollup query container events: %w", err)
	}
	defer rows.Close()

	// Aggregate by (user_id, capacity_tier).
	type dimKey struct {
		userID       uuid.UUID
		capacityTier string
	}
	aggregates := make(map[dimKey]*containerAggregate)

	for rows.Next() {
		var (
			eventID      uuid.UUID
			sessionID    uuid.UUID
			userID       uuid.UUID
			cpuLimit     float64
			memoryMB     int
			startedAt    time.Time
			stoppedAt    time.Time
			minutes      float64
			durationMs   float64
		)
		if err := rows.Scan(&eventID, &sessionID, &userID, &cpuLimit, &memoryMB,
			&startedAt, &stoppedAt, &minutes, &durationMs); err != nil {
			return fmt.Errorf("scan container event: %w", err)
		}

		tier := fmt.Sprintf("%.0fcpu_%dmb", cpuLimit, memoryMB)
		key := dimKey{userID: userID, capacityTier: tier}

		agg, ok := aggregates[key]
		if !ok {
			agg = &containerAggregate{
				sessionIDs: make(map[uuid.UUID]struct{}),
			}
			aggregates[key] = agg
		}

		// Clip the interval to the hour boundary for minutes attribution.
		clippedStart := startedAt
		if clippedStart.Before(hour) {
			clippedStart = hour
		}
		clippedStop := stoppedAt
		if clippedStop.After(hourEnd) {
			clippedStop = hourEnd
		}
		clippedMinutes := clippedStop.Sub(clippedStart).Minutes()
		if clippedMinutes < 0 {
			clippedMinutes = 0
		}

		agg.totalMinutes += clippedMinutes
		agg.totalStarts++
		agg.sessionIDs[sessionID] = struct{}{}
		agg.durations = append(agg.durations, durationMs/1000.0) // convert to seconds
		agg.intervals = append(agg.intervals, timeInterval{start: clippedStart, stop: clippedStop})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate container events: %w", err)
	}

	// Query token usage from sessions that were active during this hour.
	// Known limitation: tokens are attributed to the hour the session was created,
	// not when tokens were actually consumed. Sessions spanning multiple hours will
	// have all their tokens in the creation hour. This is acceptable for v1 dashboards.
	tokenRows, err := s.db.Query(ctx, `
		SELECT
			s.created_by AS user_id,
			COALESCE((s.token_usage->>'input_tokens')::bigint, 0) AS input_tokens,
			COALESCE((s.token_usage->>'output_tokens')::bigint, 0) AS output_tokens,
			COALESCE((s.token_usage->>'total_cost_usd')::double precision, 0) AS cost_usd
		FROM sessions s
		WHERE s.org_id = @org_id
		  AND s.token_usage IS NOT NULL
		  AND date_trunc('hour', s.created_at) = @hour`,
		pgx.NamedArgs{
			"org_id": orgID,
			"hour":   hour,
		},
	)
	if err != nil {
		return fmt.Errorf("rollup query token usage: %w", err)
	}
	defer tokenRows.Close()

	// Aggregate tokens by user_id.
	tokensByUser := make(map[uuid.UUID]*tokenAggregate)
	for tokenRows.Next() {
		var userID uuid.UUID
		var ta tokenAggregate
		if err := tokenRows.Scan(&userID, &ta.inputTokens, &ta.outputTokens, &ta.costUSD); err != nil {
			return fmt.Errorf("scan token usage: %w", err)
		}
		existing, ok := tokensByUser[userID]
		if !ok {
			tokensByUser[userID] = &ta
		} else {
			existing.inputTokens += ta.inputTokens
			existing.outputTokens += ta.outputTokens
			existing.costUSD += ta.costUSD
		}
	}
	if err := tokenRows.Err(); err != nil {
		return fmt.Errorf("iterate token rows: %w", err)
	}

	// Build upsert rows at multiple levels.
	type upsertRow struct {
		userID       *uuid.UUID
		capacityTier *string
		minutes      float64
		sessions     int
		starts       int
		peak         int
		avgDur       float64
		p95Dur       float64
		inputTokens  int64
		outputTokens int64
		costUSD      float64
	}

	var upserts []upsertRow

	// Level 1: Per-user-tier (finest grain).
	// Token counts are intentionally zero at this level — tokens are attributed
	// at the per-user level (Level 2) since session token_usage isn't broken
	// down by capacity tier.
	for key, agg := range aggregates {
		uid := key.userID
		tier := key.capacityTier
		peak := computePeakConcurrent(agg.intervals)
		avgDur, p95Dur := computeDurationStats(agg.durations)

		upserts = append(upserts, upsertRow{
			userID:       &uid,
			capacityTier: &tier,
			minutes:      agg.totalMinutes,
			sessions:     len(agg.sessionIDs),
			starts:       agg.totalStarts,
			peak:         peak,
			avgDur:       avgDur,
			p95Dur:       p95Dur,
		})
	}

	// Level 2: Per-user (aggregate across tiers).
	userAgg := make(map[uuid.UUID]*containerAggregate)
	for key, agg := range aggregates {
		ua, ok := userAgg[key.userID]
		if !ok {
			ua = &containerAggregate{
				sessionIDs: make(map[uuid.UUID]struct{}),
			}
			userAgg[key.userID] = ua
		}
		ua.totalMinutes += agg.totalMinutes
		ua.totalStarts += agg.totalStarts
		for sid := range agg.sessionIDs {
			ua.sessionIDs[sid] = struct{}{}
		}
		ua.durations = append(ua.durations, agg.durations...)
		ua.intervals = append(ua.intervals, agg.intervals...)
	}
	for uid, agg := range userAgg {
		peak := computePeakConcurrent(agg.intervals)
		avgDur, p95Dur := computeDurationStats(agg.durations)
		ta := tokensByUser[uid]
		var inTok, outTok int64
		var cost float64
		if ta != nil {
			inTok = ta.inputTokens
			outTok = ta.outputTokens
			cost = ta.costUSD
		}
		upserts = append(upserts, upsertRow{
			userID:       &uid,
			capacityTier: nil,
			minutes:      agg.totalMinutes,
			sessions:     len(agg.sessionIDs),
			starts:       agg.totalStarts,
			peak:         peak,
			avgDur:       avgDur,
			p95Dur:       p95Dur,
			inputTokens:  inTok,
			outputTokens: outTok,
			costUSD:      cost,
		})
	}

	// Level 3: Per-tier (aggregate across users).
	tierAgg := make(map[string]*containerAggregate)
	for key, agg := range aggregates {
		ta, ok := tierAgg[key.capacityTier]
		if !ok {
			ta = &containerAggregate{
				sessionIDs: make(map[uuid.UUID]struct{}),
			}
			tierAgg[key.capacityTier] = ta
		}
		ta.totalMinutes += agg.totalMinutes
		ta.totalStarts += agg.totalStarts
		for sid := range agg.sessionIDs {
			ta.sessionIDs[sid] = struct{}{}
		}
		ta.durations = append(ta.durations, agg.durations...)
		ta.intervals = append(ta.intervals, agg.intervals...)
	}
	for tier, agg := range tierAgg {
		peak := computePeakConcurrent(agg.intervals)
		avgDur, p95Dur := computeDurationStats(agg.durations)
		upserts = append(upserts, upsertRow{
			userID:       nil,
			capacityTier: &tier,
			minutes:      agg.totalMinutes,
			sessions:     len(agg.sessionIDs),
			starts:       agg.totalStarts,
			peak:         peak,
			avgDur:       avgDur,
			p95Dur:       p95Dur,
		})
	}

	// Level 4: Org-total (NULL user, NULL tier).
	var orgAgg containerAggregate
	orgAgg.sessionIDs = make(map[uuid.UUID]struct{})
	var orgTokens tokenAggregate
	for _, agg := range aggregates {
		orgAgg.totalMinutes += agg.totalMinutes
		orgAgg.totalStarts += agg.totalStarts
		for sid := range agg.sessionIDs {
			orgAgg.sessionIDs[sid] = struct{}{}
		}
		orgAgg.durations = append(orgAgg.durations, agg.durations...)
		orgAgg.intervals = append(orgAgg.intervals, agg.intervals...)
	}
	for _, ta := range tokensByUser {
		orgTokens.inputTokens += ta.inputTokens
		orgTokens.outputTokens += ta.outputTokens
		orgTokens.costUSD += ta.costUSD
	}
	peak := computePeakConcurrent(orgAgg.intervals)
	avgDur, p95Dur := computeDurationStats(orgAgg.durations)
	upserts = append(upserts, upsertRow{
		userID:       nil,
		capacityTier: nil,
		minutes:      orgAgg.totalMinutes,
		sessions:     len(orgAgg.sessionIDs),
		starts:       orgAgg.totalStarts,
		peak:         peak,
		avgDur:       avgDur,
		p95Dur:       p95Dur,
		inputTokens:  orgTokens.inputTokens,
		outputTokens: orgTokens.outputTokens,
		costUSD:      orgTokens.costUSD,
	})

	// Upsert all rows using a batch to avoid N individual round-trips.
	const upsertSQL = `
		INSERT INTO usage_hourly (
			org_id, hour_utc, user_id, capacity_tier,
			total_container_minutes, total_sessions, total_container_starts,
			peak_concurrent, avg_duration_sec, p95_duration_sec,
			total_input_tokens, total_output_tokens, total_llm_cost_usd,
			updated_at
		) VALUES (
			@org_id, @hour_utc, @user_id, @capacity_tier,
			@total_container_minutes, @total_sessions, @total_container_starts,
			@peak_concurrent, @avg_duration_sec, @p95_duration_sec,
			@total_input_tokens, @total_output_tokens, @total_llm_cost_usd,
			now()
		)
		ON CONFLICT (org_id, hour_utc, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'), COALESCE(capacity_tier, '')) DO UPDATE SET
			total_container_minutes = EXCLUDED.total_container_minutes,
			total_sessions = EXCLUDED.total_sessions,
			total_container_starts = EXCLUDED.total_container_starts,
			peak_concurrent = EXCLUDED.peak_concurrent,
			avg_duration_sec = EXCLUDED.avg_duration_sec,
			p95_duration_sec = EXCLUDED.p95_duration_sec,
			total_input_tokens = EXCLUDED.total_input_tokens,
			total_output_tokens = EXCLUDED.total_output_tokens,
			total_llm_cost_usd = EXCLUDED.total_llm_cost_usd,
			updated_at = now()`

	batch := &pgx.Batch{}
	for _, row := range upserts {
		batch.Queue(upsertSQL, pgx.NamedArgs{
			"org_id":                  orgID,
			"hour_utc":               hour,
			"user_id":                row.userID,
			"capacity_tier":          row.capacityTier,
			"total_container_minutes": row.minutes,
			"total_sessions":          row.sessions,
			"total_container_starts":  row.starts,
			"peak_concurrent":         row.peak,
			"avg_duration_sec":        row.avgDur,
			"p95_duration_sec":        row.p95Dur,
			"total_input_tokens":      row.inputTokens,
			"total_output_tokens":     row.outputTokens,
			"total_llm_cost_usd":      row.costUSD,
		})
	}

	br := s.db.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < len(upserts); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert usage_hourly (batch item %d): %w", i, err)
		}
	}

	return nil
}

// RollupRange rolls up all hours in [start, end) for the given org.
func (s *UsageRollupStore) RollupRange(ctx context.Context, orgID uuid.UUID, start, end time.Time) error {
	start = start.Truncate(time.Hour).UTC()
	end = end.Truncate(time.Hour).UTC()

	for h := start; h.Before(end); h = h.Add(time.Hour) {
		if err := s.RollupHour(ctx, orgID, h); err != nil {
			return fmt.Errorf("rollup hour %s: %w", h.Format(time.RFC3339), err)
		}
	}
	return nil
}

// RollupAllOrgs rolls up the given hour for all orgs that have activity.
func (s *UsageRollupStore) RollupAllOrgs(ctx context.Context, hour time.Time) error {
	hour = hour.Truncate(time.Hour).UTC()
	hourEnd := hour.Add(time.Hour)

	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT org_id FROM container_usage_events
		WHERE started_at < @hour_end AND COALESCE(stopped_at, now()) > @hour_start
		UNION
		SELECT DISTINCT org_id FROM sessions
		WHERE token_usage IS NOT NULL AND date_trunc('hour', created_at) = @hour`,
		pgx.NamedArgs{"hour_start": hour, "hour_end": hourEnd},
	)
	if err != nil {
		return fmt.Errorf("list active orgs: %w", err)
	}
	defer rows.Close()

	var orgIDs []uuid.UUID
	for rows.Next() {
		var orgID uuid.UUID
		if err := rows.Scan(&orgID); err != nil {
			return fmt.Errorf("scan org_id: %w", err)
		}
		orgIDs = append(orgIDs, orgID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate orgs: %w", err)
	}

	for _, orgID := range orgIDs {
		if err := s.RollupHour(ctx, orgID, hour); err != nil {
			return fmt.Errorf("rollup org %s hour %s: %w", orgID, hour.Format(time.RFC3339), err)
		}
	}
	return nil
}

// GetTimeseries returns hourly usage buckets for the given org and time range.
func (s *UsageRollupStore) GetTimeseries(ctx context.Context, orgID uuid.UUID, start, end time.Time, groupBy string, userID *uuid.UUID, capacity *string) ([]models.UsageTimeseriesBucket, error) {
	var query string
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
	}

	switch groupBy {
	case "user":
		query = `
			SELECT uh.hour_utc, uh.user_id, COALESCE(u.name, u.email, '') AS user_name, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
	case "capacity":
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NOT NULL
			ORDER BY uh.hour_utc`
	default: // "hour" or empty — org-level totals
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
	}

	// Add optional filters.
	if userID != nil {
		query = `
			SELECT uh.hour_utc, uh.user_id, COALESCE(u.name, u.email, '') AS user_name, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id = @user_id AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
		args["user_id"] = *userID
	} else if capacity != nil {
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier = @capacity
			ORDER BY uh.hour_utc`
		args["capacity"] = *capacity
	}

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query timeseries: %w", err)
	}
	defer rows.Close()

	var buckets []models.UsageTimeseriesBucket
	for rows.Next() {
		var b models.UsageTimeseriesBucket
		if err := rows.Scan(
			&b.HourUTC, &b.UserID, &b.UserName, &b.CapacityTier,
			&b.TotalContainerMinutes, &b.TotalSessions, &b.TotalContainerStarts,
			&b.PeakConcurrent, &b.AvgDurationSec, &b.P95DurationSec,
			&b.TotalInputTokens, &b.TotalOutputTokens, &b.TotalLLMCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan timeseries bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// GetBreakdown returns dimensional breakdown rows for the given org, range, and dimension.
func (s *UsageRollupStore) GetBreakdown(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension, sortBy string, limit int) ([]models.UsageBreakdownRow, error) {
	var query string
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
		"limit":  limit,
	}

	// SAFETY: orderClause is built from a fixed set of switch cases — never interpolate user input directly.
	orderClause := "ORDER BY total_container_minutes DESC"
	switch sortBy {
	case "sessions_desc":
		orderClause = "ORDER BY total_sessions DESC"
	case "tokens_desc":
		orderClause = "ORDER BY total_input_tokens + total_output_tokens DESC"
	}

	switch dimension {
	case "capacity":
		query = fmt.Sprintf(`
			SELECT
				uh.capacity_tier AS key,
				uh.capacity_tier AS label,
				SUM(uh.total_container_minutes) AS total_container_minutes,
				SUM(uh.total_sessions) AS total_sessions,
				SUM(uh.total_container_starts) AS total_container_starts,
				MAX(uh.peak_concurrent) AS peak_concurrent,
				SUM(uh.total_input_tokens) AS total_input_tokens,
				SUM(uh.total_output_tokens) AS total_output_tokens,
				SUM(uh.total_llm_cost_usd) AS total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NOT NULL
			GROUP BY uh.capacity_tier
			%s
			LIMIT @limit`, orderClause)
	default: // "user"
		query = fmt.Sprintf(`
			SELECT
				uh.user_id::text AS key,
				COALESCE(u.name, u.email, uh.user_id::text) AS label,
				SUM(uh.total_container_minutes) AS total_container_minutes,
				SUM(uh.total_sessions) AS total_sessions,
				SUM(uh.total_container_starts) AS total_container_starts,
				MAX(uh.peak_concurrent) AS peak_concurrent,
				SUM(uh.total_input_tokens) AS total_input_tokens,
				SUM(uh.total_output_tokens) AS total_output_tokens,
				SUM(uh.total_llm_cost_usd) AS total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL
			GROUP BY uh.user_id, u.name, u.email
			%s
			LIMIT @limit`, orderClause)
	}

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query breakdown: %w", err)
	}
	defer rows.Close()

	var result []models.UsageBreakdownRow
	var totalMinutes float64
	for rows.Next() {
		var row models.UsageBreakdownRow
		if err := rows.Scan(
			&row.Key, &row.Label,
			&row.TotalContainerMinutes, &row.TotalSessions, &row.TotalContainerStarts,
			&row.PeakConcurrent,
			&row.TotalInputTokens, &row.TotalOutputTokens, &row.TotalLLMCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan breakdown row: %w", err)
		}
		totalMinutes += row.TotalContainerMinutes
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate breakdown: %w", err)
	}

	// Compute percentages.
	if totalMinutes > 0 {
		for i := range result {
			result[i].Percentage = math.Round(result[i].TotalContainerMinutes/totalMinutes*1000) / 10
		}
	}

	return result, nil
}

// GetExportRows returns raw rows for CSV export, streaming-friendly.
func (s *UsageRollupStore) GetExportRows(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension string) (pgx.Rows, error) {
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
	}

	var query string
	switch dimension {
	case "user":
		query = `
			SELECT uh.hour_utc, COALESCE(u.email, '') AS user_email, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc, u.email`
	case "capacity":
		query = `
			SELECT uh.hour_utc, '' AS user_email, uh.capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NOT NULL
			ORDER BY uh.hour_utc, uh.capacity_tier`
	default: // "none" — org totals
		query = `
			SELECT uh.hour_utc, '' AS user_email, '' AS capacity_tier,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
	}

	return s.db.Query(ctx, query, args)
}

// TokenTotals holds aggregated token counts from the rollup table.
type TokenTotals struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// GetTokenTotals returns aggregated token totals from org-level rollup rows
// (user_id IS NULL AND capacity_tier IS NULL) over the given time range.
func (s *UsageRollupStore) GetTokenTotals(ctx context.Context, orgID uuid.UUID, start, end time.Time) (TokenTotals, error) {
	var t TokenTotals
	err := s.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(total_input_tokens), 0),
			COALESCE(SUM(total_output_tokens), 0),
			COALESCE(SUM(total_llm_cost_usd), 0)
		FROM usage_hourly
		WHERE org_id = @org_id AND hour_utc >= @start AND hour_utc < @end
		  AND user_id IS NULL AND capacity_tier IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "start": start, "end": end},
	).Scan(&t.InputTokens, &t.OutputTokens, &t.CostUSD)
	return t, err
}

// DeleteOlderThan removes rollup rows older than the given cutoff.
func (s *UsageRollupStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM usage_hourly WHERE hour_utc < @cutoff`,
		pgx.NamedArgs{"cutoff": cutoff})
	if err != nil {
		return 0, fmt.Errorf("delete old usage_hourly: %w", err)
	}
	return tag.RowsAffected(), nil
}

// computePeakConcurrent finds the maximum number of overlapping intervals.
func computePeakConcurrent(intervals []timeInterval) int {
	if len(intervals) == 0 {
		return 0
	}

	type event struct {
		t     time.Time
		delta int // +1 for start, -1 for stop
	}

	events := make([]event, 0, len(intervals)*2)
	for _, iv := range intervals {
		events = append(events,
			event{t: iv.start, delta: 1},
			event{t: iv.stop, delta: -1},
		)
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].t.Equal(events[j].t) {
			return events[i].delta > events[j].delta // starts before stops at same time
		}
		return events[i].t.Before(events[j].t)
	})

	var peak, current int
	for _, ev := range events {
		current += ev.delta
		if current > peak {
			peak = current
		}
	}
	return peak
}

// computeDurationStats returns average and p95 duration in seconds.
func computeDurationStats(durations []float64) (avg, p95 float64) {
	if len(durations) == 0 {
		return 0, 0
	}

	var sum float64
	for _, d := range durations {
		sum += d
	}
	avg = sum / float64(len(durations))

	sorted := make([]float64, len(durations))
	copy(sorted, durations)
	sort.Float64s(sorted)

	idx := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	p95 = sorted[idx]

	return avg, p95
}
