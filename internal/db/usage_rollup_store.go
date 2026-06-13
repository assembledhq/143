package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// UsageRollupStore handles reading and writing pre-aggregated hourly usage data.
type UsageRollupStore struct {
	db DBTX
}

// ExportDailySessionCountRow holds exact per-day session counts for CSV export.
type ExportDailySessionCountRow struct {
	LocalDate    string
	UserEmail    string
	CapacityTier string
	Sessions     int
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

type executionKey struct {
	agentType       string
	modelUsed       string
	reasoningEffort string
	capacityKey     string
}

const (
	usageUnknownCapacityKey = "unknown"
	usageAllCapacityKey     = "__all__"
)

func normalizeUsageReasoning(reasoning *models.ReasoningEffort) string {
	if reasoning == nil || *reasoning == "" {
		return "default"
	}
	return string(*reasoning)
}

func normalizeUsageModel(modelUsed *string, tokenUsageRaw []byte) string {
	var payload struct {
		NativeUsage *struct {
			Model string `json:"model"`
		} `json:"native_usage"`
	}
	if len(tokenUsageRaw) > 0 && json.Unmarshal(tokenUsageRaw, &payload) == nil && payload.NativeUsage != nil {
		if model := strings.TrimSpace(payload.NativeUsage.Model); model != "" {
			return model
		}
	}
	if modelUsed != nil && strings.TrimSpace(*modelUsed) != "" {
		return strings.TrimSpace(*modelUsed)
	}
	return "unknown"
}

func usageTokenCostSQL(alias string) string {
	return `COALESCE(
				NULLIF(` + alias + `.token_usage->>'total_cost_usd', '')::double precision,
				CASE
					WHEN lower(` + alias + `.token_usage->'cost'->>'unit') = 'usd'
					THEN NULLIF(` + alias + `.token_usage->'cost'->>'amount', '')::double precision
				END,
				CASE
					WHEN lower(` + alias + `.token_usage->'native_cost'->>'unit') = 'usd'
					THEN NULLIF(` + alias + `.token_usage->'native_cost'->>'amount', '')::double precision
				END,
				0
			)`
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
			COALESCE(s.triggered_by_user_id, '00000000-0000-0000-0000-000000000000'::uuid) AS user_id,
			s.agent_type,
			s.model_override AS model_used,
			s.reasoning_effort,
			s.token_usage,
			e.cpu_limit,
			e.memory_limit_mb,
			e.disk_limit_mb,
			e.started_at,
			COALESCE(e.stopped_at, @now) AS stopped_at,
			COALESCE(e.container_minutes, EXTRACT(EPOCH FROM (COALESCE(e.stopped_at, @now) - e.started_at)) / 60.0) AS container_minutes,
			COALESCE(e.duration_ms, EXTRACT(EPOCH FROM (COALESCE(e.stopped_at, @now) - e.started_at)) * 1000) AS duration_ms
		FROM container_usage_events e
		JOIN sessions s ON s.id = e.session_id
		WHERE e.org_id = @org_id
		  AND e.started_at < @hour_end
		  AND COALESCE(e.stopped_at, @now) > @hour_start`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"hour_start": hour,
			"hour_end":   hourEnd,
			"now":        time.Now().UTC(),
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
	executionAgg := make(map[executionKey]*containerAggregate)

	for rows.Next() {
		var (
			eventID         uuid.UUID
			sessionID       uuid.UUID
			userID          uuid.UUID
			agentType       string
			modelUsed       *string
			reasoningEffort *models.ReasoningEffort
			tokenUsageRaw   []byte
			cpuLimit        float64
			memoryMB        int
			diskMB          int
			startedAt       time.Time
			stoppedAt       time.Time
			minutes         float64
			durationMs      float64
		)
		if err := rows.Scan(&eventID, &sessionID, &userID,
			&agentType, &modelUsed, &reasoningEffort, &tokenUsageRaw,
			&cpuLimit, &memoryMB, &diskMB,
			&startedAt, &stoppedAt, &minutes, &durationMs); err != nil {
			return fmt.Errorf("scan container event: %w", err)
		}

		tier := fmt.Sprintf("%.0fcpu_%dmb_%ddiskmb", cpuLimit, memoryMB, diskMB)
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
		// Only count a container start in the hour where it actually started,
		// not in every hour the container overlaps.
		if !startedAt.Before(hour) && startedAt.Before(hourEnd) {
			agg.totalStarts++
		}
		agg.sessionIDs[sessionID] = struct{}{}
		// Use clipped duration (within this hour) for avg/p95 stats, not the
		// full event duration which would inflate stats in every overlapping hour.
		clippedDurationSec := clippedStop.Sub(clippedStart).Seconds()
		if clippedDurationSec < 0 {
			clippedDurationSec = 0
		}
		agg.durations = append(agg.durations, clippedDurationSec)
		agg.intervals = append(agg.intervals, timeInterval{start: clippedStart, stop: clippedStop})

		execKey := executionKey{
			agentType:       agentType,
			modelUsed:       normalizeUsageModel(modelUsed, tokenUsageRaw),
			reasoningEffort: normalizeUsageReasoning(reasoningEffort),
			capacityKey:     tier,
		}
		execAgg, ok := executionAgg[execKey]
		if !ok {
			execAgg = &containerAggregate{sessionIDs: make(map[uuid.UUID]struct{})}
			executionAgg[execKey] = execAgg
		}
		execAgg.totalMinutes += clippedMinutes
		if !startedAt.Before(hour) && startedAt.Before(hourEnd) {
			execAgg.totalStarts++
		}
		execAgg.sessionIDs[sessionID] = struct{}{}
		execAgg.durations = append(execAgg.durations, clippedDurationSec)
		execAgg.intervals = append(execAgg.intervals, timeInterval{start: clippedStart, stop: clippedStop})

		allCapacityKey := executionKey{
			agentType:       agentType,
			modelUsed:       normalizeUsageModel(modelUsed, tokenUsageRaw),
			reasoningEffort: normalizeUsageReasoning(reasoningEffort),
			capacityKey:     usageAllCapacityKey,
		}
		allCapacityAgg, ok := executionAgg[allCapacityKey]
		if !ok {
			allCapacityAgg = &containerAggregate{sessionIDs: make(map[uuid.UUID]struct{})}
			executionAgg[allCapacityKey] = allCapacityAgg
		}
		allCapacityAgg.totalMinutes += clippedMinutes
		if !startedAt.Before(hour) && startedAt.Before(hourEnd) {
			allCapacityAgg.totalStarts++
		}
		allCapacityAgg.sessionIDs[sessionID] = struct{}{}
		allCapacityAgg.durations = append(allCapacityAgg.durations, clippedDurationSec)
		allCapacityAgg.intervals = append(allCapacityAgg.intervals, timeInterval{start: clippedStart, stop: clippedStop})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate container events: %w", err)
	}

	// Query token usage from sessions that completed during this hour.
	// Tokens are attributed to the completion hour (not creation hour) because
	// token_usage is only populated when a session finishes. Using created_at
	// would permanently miss tokens for sessions that start in one hour but
	// complete after that hour has already been rolled up.
	tokenRows, err := s.db.Query(ctx, `
		SELECT
			COALESCE(s.triggered_by_user_id, '00000000-0000-0000-0000-000000000000'::uuid) AS user_id,
			s.agent_type,
			s.model_override AS model_used,
			s.reasoning_effort,
			s.token_usage,
			COALESCE((
				SELECT format('%scpu_%smb_%sdiskmb', round(e.cpu_limit)::int, e.memory_limit_mb, e.disk_limit_mb)
				FROM container_usage_events e
				WHERE e.org_id = s.org_id
				  AND e.session_id = s.id
				  AND e.started_at < @hour_end
				  AND COALESCE(e.stopped_at, @now) > @hour_start
				GROUP BY round(e.cpu_limit)::int, e.memory_limit_mb, e.disk_limit_mb
				ORDER BY SUM(EXTRACT(EPOCH FROM (
					LEAST(COALESCE(e.stopped_at, @now), @hour_end) - GREATEST(e.started_at, @hour_start)
				))) DESC
				LIMIT 1
			), @unknown_capacity) AS capacity_key,
			COALESCE((s.token_usage->>'input_tokens')::bigint, 0) AS input_tokens,
			COALESCE((s.token_usage->>'output_tokens')::bigint, 0) AS output_tokens,
			`+usageTokenCostSQL("s")+` AS cost_usd
		FROM sessions s
		WHERE s.org_id = @org_id
		  AND s.token_usage IS NOT NULL
		  AND s.completed_at IS NOT NULL
		  AND date_trunc('hour', s.completed_at) = @hour`,
		pgx.NamedArgs{
			"org_id":           orgID,
			"hour":             hour,
			"hour_start":       hour,
			"hour_end":         hourEnd,
			"now":              time.Now().UTC(),
			"unknown_capacity": usageUnknownCapacityKey,
		},
	)
	if err != nil {
		return fmt.Errorf("rollup query token usage: %w", err)
	}
	defer tokenRows.Close()

	// Aggregate tokens by user_id.
	tokensByUser := make(map[uuid.UUID]*tokenAggregate)
	tokensByExecution := make(map[executionKey]*tokenAggregate)
	for tokenRows.Next() {
		var userID uuid.UUID
		var (
			agentType       string
			modelUsed       *string
			reasoningEffort *models.ReasoningEffort
			tokenUsageRaw   []byte
			capacityKey     string
		)
		var ta tokenAggregate
		if err := tokenRows.Scan(&userID, &agentType, &modelUsed, &reasoningEffort, &tokenUsageRaw, &capacityKey, &ta.inputTokens, &ta.outputTokens, &ta.costUSD); err != nil {
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
		key := executionKey{
			agentType:       agentType,
			modelUsed:       normalizeUsageModel(modelUsed, tokenUsageRaw),
			reasoningEffort: normalizeUsageReasoning(reasoningEffort),
			capacityKey:     capacityKey,
		}
		existingByExecution, ok := tokensByExecution[key]
		if !ok {
			copyTA := ta
			tokensByExecution[key] = &copyTA
		} else {
			existingByExecution.inputTokens += ta.inputTokens
			existingByExecution.outputTokens += ta.outputTokens
			existingByExecution.costUSD += ta.costUSD
		}
		allCapacityKey := executionKey{
			agentType:       agentType,
			modelUsed:       normalizeUsageModel(modelUsed, tokenUsageRaw),
			reasoningEffort: normalizeUsageReasoning(reasoningEffort),
			capacityKey:     usageAllCapacityKey,
		}
		existingAllCapacity, ok := tokensByExecution[allCapacityKey]
		if !ok {
			copyTA := ta
			tokensByExecution[allCapacityKey] = &copyTA
		} else {
			existingAllCapacity.inputTokens += ta.inputTokens
			existingAllCapacity.outputTokens += ta.outputTokens
			existingAllCapacity.costUSD += ta.costUSD
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
	type executionUpsertRow struct {
		key          executionKey
		minutes      float64
		sessions     int
		starts       int
		peak         int
		inputTokens  int64
		outputTokens int64
		costUSD      float64
	}

	var upserts []upsertRow
	executionUpserts := make([]executionUpsertRow, 0, len(executionAgg)+len(tokensByExecution))

	// Level 1: Per-user-tier (finest grain).
	// Token counts are intentionally zero at this level — tokens are attributed
	// at the per-user level (Level 2) since session token_usage isn't broken
	// down by capacity tier.
	for key, agg := range aggregates {
		// Skip sessions with no attributed user (e.g. automation-triggered).
		// They still contribute to per-tier and org totals below; we just can't
		// emit a per-user row because user_id has a FK to users(id).
		if key.userID == uuid.Nil {
			continue
		}
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
		if uid == uuid.Nil {
			continue
		}
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
	// Emit per-user rows for users that have token usage but no container
	// events in this hour. Without this, token spend is visible at the org
	// level but cannot be attributed to these users in per-user views.
	for uid, ta := range tokensByUser {
		if uid == uuid.Nil {
			continue
		}
		if _, hasContainer := userAgg[uid]; hasContainer {
			continue // already handled above
		}
		u := uid
		upserts = append(upserts, upsertRow{
			userID:       &u,
			capacityTier: nil,
			inputTokens:  ta.inputTokens,
			outputTokens: ta.outputTokens,
			costUSD:      ta.costUSD,
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

	for key, agg := range executionAgg {
		tokens := tokensByExecution[key]
		row := executionUpsertRow{
			key:      key,
			minutes:  agg.totalMinutes,
			sessions: len(agg.sessionIDs),
			starts:   agg.totalStarts,
			peak:     computePeakConcurrent(agg.intervals),
		}
		if tokens != nil {
			row.inputTokens = tokens.inputTokens
			row.outputTokens = tokens.outputTokens
			row.costUSD = tokens.costUSD
		}
		executionUpserts = append(executionUpserts, row)
	}
	for key, tokens := range tokensByExecution {
		if _, ok := executionAgg[key]; ok {
			continue
		}
		executionUpserts = append(executionUpserts, executionUpsertRow{
			key:          key,
			inputTokens:  tokens.inputTokens,
			outputTokens: tokens.outputTokens,
			costUSD:      tokens.costUSD,
		})
	}

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
			"hour_utc":                hour,
			"user_id":                 row.userID,
			"capacity_tier":           row.capacityTier,
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
	const executionUpsertSQL = `
		INSERT INTO usage_hourly_execution (
			org_id, hour_utc, agent_type, model_used, reasoning_effort, capacity_key,
			total_container_minutes, total_sessions, total_container_starts, peak_concurrent,
			total_input_tokens, total_output_tokens, total_tokens, total_llm_cost_usd, updated_at
		) VALUES (
			@org_id, @hour_utc, @agent_type, @model_used, @reasoning_effort, @capacity_key,
			@total_container_minutes, @total_sessions, @total_container_starts, @peak_concurrent,
			@total_input_tokens, @total_output_tokens, @total_tokens, @total_llm_cost_usd, now()
		)
		ON CONFLICT (org_id, hour_utc, agent_type, model_used, reasoning_effort, capacity_key) DO UPDATE SET
			total_container_minutes = EXCLUDED.total_container_minutes,
			total_sessions = EXCLUDED.total_sessions,
			total_container_starts = EXCLUDED.total_container_starts,
			peak_concurrent = EXCLUDED.peak_concurrent,
			total_input_tokens = EXCLUDED.total_input_tokens,
			total_output_tokens = EXCLUDED.total_output_tokens,
			total_tokens = EXCLUDED.total_tokens,
			total_llm_cost_usd = EXCLUDED.total_llm_cost_usd,
			updated_at = now()`
	for _, row := range executionUpserts {
		batch.Queue(executionUpsertSQL, pgx.NamedArgs{
			"org_id":                  orgID,
			"hour_utc":                hour,
			"agent_type":              row.key.agentType,
			"model_used":              row.key.modelUsed,
			"reasoning_effort":        row.key.reasoningEffort,
			"capacity_key":            row.key.capacityKey,
			"total_container_minutes": row.minutes,
			"total_sessions":          row.sessions,
			"total_container_starts":  row.starts,
			"peak_concurrent":         row.peak,
			"total_input_tokens":      row.inputTokens,
			"total_output_tokens":     row.outputTokens,
			"total_tokens":            row.inputTokens + row.outputTokens,
			"total_llm_cost_usd":      row.costUSD,
		})
	}

	// Wrap the batch in a transaction so a partial failure doesn't leave
	// inconsistent dimensional rows (e.g. per-user rows without an org-total).
	txStarter, canTx := s.db.(TxStarter)
	if canTx {
		tx, err := txStarter.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin upsert tx: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		br := tx.SendBatch(ctx, batch)
		for i := 0; i < len(upserts)+len(executionUpserts); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("upsert usage rollups (batch item %d): %w", i, err)
			}
		}
		_ = br.Close()
		return tx.Commit(ctx)
	}

	// Fallback for DBTX implementations that don't support transactions (tests).
	br := s.db.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < len(upserts)+len(executionUpserts); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert usage rollups (batch item %d): %w", i, err)
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

// GetActiveOrgIDs returns the distinct org IDs that have container usage events
// or token usage in the given [start, end) window.
// lint:allow-no-orgid reason="explicitly cross-org; enumerates orgs for rollup"
func (s *UsageRollupStore) GetActiveOrgIDs(ctx context.Context, start, end time.Time) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT org_id FROM container_usage_events
		WHERE started_at < @end AND COALESCE(stopped_at, now()) > @start
		UNION
		SELECT DISTINCT org_id FROM sessions
		WHERE token_usage IS NOT NULL AND completed_at >= @start AND completed_at < @end`,
		pgx.NamedArgs{"start": start, "end": end},
	)
	if err != nil {
		return nil, fmt.Errorf("query active org IDs: %w", err)
	}
	defer rows.Close()

	var orgIDs []uuid.UUID
	for rows.Next() {
		var orgID uuid.UUID
		if err := rows.Scan(&orgID); err != nil {
			return nil, fmt.Errorf("scan org_id: %w", err)
		}
		orgIDs = append(orgIDs, orgID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate org IDs: %w", err)
	}
	return orgIDs, nil
}

// RollupAllOrgs rolls up the given hour for all orgs that have activity.
// lint:allow-no-orgid reason="explicitly cross-org; iterates all orgs to roll up"
func (s *UsageRollupStore) RollupAllOrgs(ctx context.Context, hour time.Time) error {
	hour = hour.Truncate(time.Hour).UTC()
	hourEnd := hour.Add(time.Hour)

	orgIDs, err := s.GetActiveOrgIDs(ctx, hour, hourEnd)
	if err != nil {
		return fmt.Errorf("list active orgs: %w", err)
	}

	for _, orgID := range orgIDs {
		if err := s.RollupHour(ctx, orgID, hour); err != nil {
			return fmt.Errorf("rollup org %s hour %s: %w", orgID, hour.Format(time.RFC3339), err)
		}
	}
	return nil
}

// GetTimeseries returns hourly usage buckets for the given org and time range.
func (s *UsageRollupStore) GetTimeseries(ctx context.Context, orgID uuid.UUID, start, end time.Time, groupBy, stackBy string, userID *uuid.UUID, capacity *string, filters UsageExecutionFilters) ([]models.UsageTimeseriesBucket, error) {
	var query string
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
	}

	useExecution := stackBy != "" || filters.HasAny() || (capacity != nil && groupBy == "hour")
	executionSeriesBy := stackBy
	if executionSeriesBy == "" && groupBy == "capacity" {
		executionSeriesBy = "capacity"
	}
	switch {
	case useExecution:
		where := `
			WHERE uhe.org_id = @org_id AND uhe.hour_utc >= @start AND uhe.hour_utc < @end`
		where = applyUsageExecutionFilters(where, args, "uhe", filters)
		args["all_capacity"] = usageAllCapacityKey
		if executionSeriesBy == "capacity" || capacity != nil {
			where += ` AND uhe.capacity_key <> @all_capacity`
			args["unknown_capacity"] = usageUnknownCapacityKey
		} else {
			where += ` AND uhe.capacity_key = @all_capacity`
		}
		if capacity != nil {
			where += ` AND uhe.capacity_key = @capacity`
			args["capacity"] = *capacity
		}
		seriesKeyExpr := `NULL::text`
		seriesLabelExpr := `NULL::text`
		capacityExpr := `NULL::text`
		agentExpr := `NULL::text`
		modelExpr := `NULL::text`
		reasoningExpr := `NULL::text`
		if executionSeriesBy == "capacity" || capacity != nil {
			capacityExpr = `NULLIF(uhe.capacity_key, @unknown_capacity)`
		}
		switch executionSeriesBy {
		case "agent":
			seriesKeyExpr = `uhe.agent_type`
			seriesLabelExpr = breakdownLabelSQL("agent", "uhe.agent_type")
			agentExpr = `uhe.agent_type`
		case "model":
			seriesKeyExpr = `uhe.model_used`
			seriesLabelExpr = breakdownLabelSQL("model", "uhe.model_used")
			modelExpr = `uhe.model_used`
		case "reasoning":
			seriesKeyExpr = `uhe.reasoning_effort`
			seriesLabelExpr = breakdownLabelSQL("reasoning", "uhe.reasoning_effort")
			reasoningExpr = `uhe.reasoning_effort`
		case "capacity":
			seriesKeyExpr = `uhe.capacity_key`
			seriesLabelExpr = `NULLIF(uhe.capacity_key, @unknown_capacity)`
		}
		groupBy := []string{"uhe.hour_utc", "series_key", "series_label"}
		query = `
			SELECT
				uhe.hour_utc,
				NULL::uuid AS user_id,
				'' AS user_name,
				` + capacityExpr + ` AS capacity_tier,
				` + agentExpr + ` AS agent_type,
				` + modelExpr + ` AS model_used,
				` + reasoningExpr + ` AS reasoning_effort,
				` + seriesKeyExpr + ` AS series_key,
				` + seriesLabelExpr + ` AS series_label,
				SUM(uhe.total_container_minutes) AS total_container_minutes,
				SUM(uhe.total_sessions) AS total_sessions,
				SUM(uhe.total_container_starts) AS total_container_starts,
				MAX(uhe.peak_concurrent) AS peak_concurrent,
				0::double precision AS avg_duration_sec,
				0::double precision AS p95_duration_sec,
				SUM(uhe.total_input_tokens) AS total_input_tokens,
				SUM(uhe.total_output_tokens) AS total_output_tokens,
				SUM(uhe.total_tokens) AS total_tokens,
				SUM(uhe.total_llm_cost_usd) AS total_llm_cost_usd
			FROM usage_hourly_execution uhe
			` + where + `
			GROUP BY ` + strings.Join(groupBy, ", ") + `
			ORDER BY uhe.hour_utc, series_label`
	case groupBy == "user":
		query = `
			SELECT uh.hour_utc, uh.user_id, COALESCE(u.name, u.email, '') AS user_name, uh.capacity_tier,
				NULL::text AS agent_type, NULL::text AS model_used, NULL::text AS reasoning_effort, NULL::text AS series_key, NULL::text AS series_label,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_input_tokens + uh.total_output_tokens AS total_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
	case groupBy == "capacity":
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				NULL::text AS agent_type, NULL::text AS model_used, NULL::text AS reasoning_effort, NULL::text AS series_key, NULL::text AS series_label,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_input_tokens + uh.total_output_tokens AS total_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NOT NULL
			ORDER BY uh.hour_utc`
	default: // "hour" or empty — org-level totals
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				NULL::text AS agent_type, NULL::text AS model_used, NULL::text AS reasoning_effort, NULL::text AS series_key, NULL::text AS series_label,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_input_tokens + uh.total_output_tokens AS total_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NULL AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
	}

	// Add optional filters. user_id and capacity are mutually exclusive — the
	// handler layer ensures at most one is set. If both were provided, user_id
	// takes precedence via the if/else-if below.
	if userID != nil {
		query = `
			SELECT uh.hour_utc, uh.user_id, COALESCE(u.name, u.email, '') AS user_name, uh.capacity_tier,
				NULL::text AS agent_type, NULL::text AS model_used, NULL::text AS reasoning_effort, NULL::text AS series_key, NULL::text AS series_label,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_input_tokens + uh.total_output_tokens AS total_tokens, uh.total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id = @user_id AND uh.capacity_tier IS NULL
			ORDER BY uh.hour_utc`
		args["user_id"] = *userID
	} else if capacity != nil && !useExecution {
		query = `
			SELECT uh.hour_utc, uh.user_id, '' AS user_name, uh.capacity_tier,
				NULL::text AS agent_type, NULL::text AS model_used, NULL::text AS reasoning_effort, NULL::text AS series_key, NULL::text AS series_label,
				uh.total_container_minutes, uh.total_sessions, uh.total_container_starts,
				uh.peak_concurrent, uh.avg_duration_sec, uh.p95_duration_sec,
				uh.total_input_tokens, uh.total_output_tokens, uh.total_input_tokens + uh.total_output_tokens AS total_tokens, uh.total_llm_cost_usd
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
			&b.AgentType, &b.ModelUsed, &b.ReasoningEffort, &b.SeriesKey, &b.SeriesLabel,
			&b.TotalContainerMinutes, &b.TotalSessions, &b.TotalContainerStarts,
			&b.PeakConcurrent, &b.AvgDurationSec, &b.P95DurationSec,
			&b.TotalInputTokens, &b.TotalOutputTokens, &b.TotalTokens, &b.TotalLLMCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan timeseries bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

// GetBreakdown returns dimensional breakdown rows for the given org, range, and dimension.
func (s *UsageRollupStore) GetBreakdown(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension, sortBy string, limit int, filters UsageExecutionFilters) ([]models.UsageBreakdownRow, error) {
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
		"limit":  limit,
	}

	allowedSorts := map[string]string{
		"minutes_desc":  "ORDER BY total_container_minutes DESC",
		"sessions_desc": "ORDER BY total_sessions DESC",
		"tokens_desc":   "ORDER BY total_tokens DESC",
		"cost_desc":     "ORDER BY total_llm_cost_usd DESC",
	}
	orderClause, ok := allowedSorts[sortBy]
	if !ok {
		orderClause = allowedSorts["minutes_desc"]
	}

	if dimension == "user" {
		var grandTotalMinutes, grandTotalTokens, grandTotalCost float64
		if err := s.db.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(uh.total_container_minutes), 0),
				COALESCE(SUM(uh.total_input_tokens + uh.total_output_tokens), 0),
				COALESCE(SUM(uh.total_llm_cost_usd), 0)
			FROM usage_hourly uh
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL`, args).Scan(&grandTotalMinutes, &grandTotalTokens, &grandTotalCost); err != nil {
			return nil, fmt.Errorf("query breakdown grand total: %w", err)
		}
		query := fmt.Sprintf(`
			WITH session_counts AS (
				SELECT
					s.triggered_by_user_id AS user_id,
					COUNT(DISTINCT e.session_id) AS distinct_sessions
				FROM container_usage_events e
				JOIN sessions s
				  ON s.id = e.session_id
				 AND s.org_id = e.org_id
				WHERE e.org_id = @org_id
				  AND e.started_at < @end
				  AND COALESCE(e.stopped_at, now()) > @start
				  AND s.triggered_by_user_id IS NOT NULL
				GROUP BY s.triggered_by_user_id
			)
			SELECT
				uh.user_id::text AS key,
				COALESCE(u.name, u.email, uh.user_id::text) AS label,
				SUM(uh.total_container_minutes) AS total_container_minutes,
				COALESCE(sc.distinct_sessions, 0) AS total_sessions,
				SUM(uh.total_container_starts) AS total_container_starts,
				MAX(uh.peak_concurrent) AS peak_concurrent,
				SUM(uh.total_input_tokens) AS total_input_tokens,
				SUM(uh.total_output_tokens) AS total_output_tokens,
				SUM(uh.total_input_tokens + uh.total_output_tokens) AS total_tokens,
				SUM(uh.total_llm_cost_usd) AS total_llm_cost_usd
			FROM usage_hourly uh
			LEFT JOIN users u ON u.id = uh.user_id
			LEFT JOIN session_counts sc ON sc.user_id = uh.user_id
			WHERE uh.org_id = @org_id AND uh.hour_utc >= @start AND uh.hour_utc < @end
			  AND uh.user_id IS NOT NULL AND uh.capacity_tier IS NULL
			GROUP BY uh.user_id, u.name, u.email, sc.distinct_sessions
			%s
			LIMIT @limit`, orderClause)
		rows, err := s.db.Query(ctx, query, args)
		if err != nil {
			return nil, fmt.Errorf("query breakdown: %w", err)
		}
		defer rows.Close()
		return scanUsageBreakdownRows(rows, grandTotalMinutes, grandTotalTokens, grandTotalCost)
	}

	keyExpr := "uhe.capacity_key"
	switch dimension {
	case "agent":
		keyExpr = "uhe.agent_type"
	case "model":
		keyExpr = "uhe.model_used"
	case "reasoning":
		keyExpr = "uhe.reasoning_effort"
	}

	where := `
		WHERE uhe.org_id = @org_id AND uhe.hour_utc >= @start AND uhe.hour_utc < @end`
	where = applyUsageExecutionFilters(where, args, "uhe", filters)
	args["all_capacity"] = usageAllCapacityKey
	if dimension == "capacity" {
		where += ` AND uhe.capacity_key <> @all_capacity`
	} else {
		where += ` AND uhe.capacity_key = @all_capacity`
	}

	var grandTotalMinutes, grandTotalTokens, grandTotalCost float64
	if err := s.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(uhe.total_container_minutes), 0),
			COALESCE(SUM(uhe.total_tokens), 0),
			COALESCE(SUM(uhe.total_llm_cost_usd), 0)
		FROM usage_hourly_execution uhe
		`+where, args).Scan(&grandTotalMinutes, &grandTotalTokens, &grandTotalCost); err != nil {
		return nil, fmt.Errorf("query execution breakdown grand totals: %w", err)
	}

	var sessionCountsCTE string
	if dimension == "capacity" {
		sessionCountsCTE = `
			WITH session_counts AS (
				SELECT
					format('%scpu_%smb_%sdiskmb', round(e.cpu_limit)::int, e.memory_limit_mb, e.disk_limit_mb) AS key,
					COUNT(DISTINCT e.session_id) AS distinct_sessions
				FROM container_usage_events e
				JOIN sessions s
				  ON s.id = e.session_id
				 AND s.org_id = e.org_id
				WHERE e.org_id = @org_id
				  AND e.started_at < @end
				  AND COALESCE(e.stopped_at, now()) > @start`
		sessionCountsCTE = applySessionExecutionFilters(sessionCountsCTE, args, "s", filters)
		sessionCountsCTE += `
				GROUP BY format('%scpu_%smb_%sdiskmb', round(e.cpu_limit)::int, e.memory_limit_mb, e.disk_limit_mb)
			)`
	} else {
		sessionCountsCTE = `
			WITH session_counts AS (
				SELECT
					` + sessionDimensionKeySQL(dimension, "s") + ` AS key,
					COUNT(DISTINCT s.id) AS distinct_sessions
				FROM sessions s
				WHERE s.org_id = @org_id`
		sessionCountsCTE = applySessionExecutionFilters(sessionCountsCTE, args, "s", filters)
		sessionCountsCTE += `
				  AND (
					EXISTS (
						SELECT 1
						FROM container_usage_events e
						WHERE e.org_id = s.org_id
						  AND e.session_id = s.id
						  AND e.started_at < @end
						  AND COALESCE(e.stopped_at, now()) > @start
					)
					OR (
						s.token_usage IS NOT NULL
						AND s.completed_at IS NOT NULL
						AND s.completed_at >= @start
						AND s.completed_at < @end
					)
				  )
				GROUP BY ` + sessionDimensionKeySQL(dimension, "s") + `
			)`
	}

	query := sessionCountsCTE + fmt.Sprintf(`
		SELECT
			%s AS key,
			%s AS label,
			SUM(uhe.total_container_minutes) AS total_container_minutes,
			COALESCE(sc.distinct_sessions, 0) AS total_sessions,
			SUM(uhe.total_container_starts) AS total_container_starts,
			MAX(uhe.peak_concurrent) AS peak_concurrent,
			SUM(uhe.total_input_tokens) AS total_input_tokens,
			SUM(uhe.total_output_tokens) AS total_output_tokens,
			SUM(uhe.total_tokens) AS total_tokens,
			SUM(uhe.total_llm_cost_usd) AS total_llm_cost_usd
		FROM usage_hourly_execution uhe
		LEFT JOIN session_counts sc ON sc.key = %s
		%s
		GROUP BY %s, sc.distinct_sessions
		%s
		LIMIT @limit`,
		keyExpr,
		breakdownLabelSQL(dimension, keyExpr),
		keyExpr,
		where,
		keyExpr,
		orderClause,
	)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query breakdown: %w", err)
	}
	defer rows.Close()
	return scanUsageBreakdownRows(rows, grandTotalMinutes, grandTotalTokens, grandTotalCost)
}

// GetExportRows returns raw rows for CSV export, streaming-friendly.
func (s *UsageRollupStore) GetExportRows(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension string, filters UsageExecutionFilters) (pgx.Rows, error) {
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
		args["all_capacity"] = usageAllCapacityKey
		where := `WHERE uhe.org_id = @org_id AND uhe.hour_utc >= @start AND uhe.hour_utc < @end AND uhe.capacity_key <> @all_capacity`
		where = applyUsageExecutionFilters(where, args, "uhe", filters)
		query = `
			SELECT uhe.hour_utc, '' AS user_email, uhe.capacity_key,
				SUM(uhe.total_container_minutes), SUM(uhe.total_sessions), SUM(uhe.total_container_starts),
				MAX(uhe.peak_concurrent),
				SUM(uhe.total_input_tokens), SUM(uhe.total_output_tokens), SUM(uhe.total_llm_cost_usd)
			FROM usage_hourly_execution uhe
			` + where + `
			GROUP BY uhe.hour_utc, uhe.capacity_key
			ORDER BY uhe.hour_utc, uhe.capacity_key`
	case "agent", "model", "reasoning":
		args["all_capacity"] = usageAllCapacityKey
		keyExpr := "uhe.agent_type"
		if dimension == "model" {
			keyExpr = "uhe.model_used"
		} else if dimension == "reasoning" {
			keyExpr = "uhe.reasoning_effort"
		}
		where := `WHERE uhe.org_id = @org_id AND uhe.hour_utc >= @start AND uhe.hour_utc < @end AND uhe.capacity_key = @all_capacity`
		where = applyUsageExecutionFilters(where, args, "uhe", filters)
		query = `
			SELECT uhe.hour_utc, '' AS user_email, ` + keyExpr + `,
				SUM(uhe.total_container_minutes), SUM(uhe.total_sessions), SUM(uhe.total_container_starts),
				MAX(uhe.peak_concurrent),
				SUM(uhe.total_input_tokens), SUM(uhe.total_output_tokens), SUM(uhe.total_llm_cost_usd)
			FROM usage_hourly_execution uhe
			` + where + `
			GROUP BY uhe.hour_utc, ` + keyExpr + `
			ORDER BY uhe.hour_utc, ` + keyExpr
	default: // "none" — org totals
		if filters.HasAny() {
			args["all_capacity"] = usageAllCapacityKey
			where := `WHERE uhe.org_id = @org_id AND uhe.hour_utc >= @start AND uhe.hour_utc < @end AND uhe.capacity_key = @all_capacity`
			where = applyUsageExecutionFilters(where, args, "uhe", filters)
			query = `
				SELECT uhe.hour_utc, '' AS user_email, '' AS capacity_tier,
					SUM(uhe.total_container_minutes), SUM(uhe.total_sessions), SUM(uhe.total_container_starts),
					MAX(uhe.peak_concurrent),
					SUM(uhe.total_input_tokens), SUM(uhe.total_output_tokens), SUM(uhe.total_llm_cost_usd)
				FROM usage_hourly_execution uhe
				` + where + `
				GROUP BY uhe.hour_utc
				ORDER BY uhe.hour_utc`
		} else {
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
	}

	return s.db.Query(ctx, query, args)
}

// GetDailySessionCounts returns exact daily session counts keyed by the export dimension.
func (s *UsageRollupStore) GetDailySessionCounts(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension, tzName string, filters UsageExecutionFilters) ([]ExportDailySessionCountRow, error) {
	args := pgx.NamedArgs{
		"org_id": orgID,
		"start":  start,
		"end":    end,
		"tz":     tzName,
	}

	const daySeries = `
		WITH days AS (
			SELECT generate_series(
				date_trunc('day', @start AT TIME ZONE @tz),
				date_trunc('day', (@end - interval '1 microsecond') AT TIME ZONE @tz),
				interval '1 day'
			) AS local_day
		)
	`

	var query string
	// Each day-event join is clamped to the actual [start, end) window so that
	// partial boundary days don't include activity outside the requested range.
	switch dimension {
	case "user":
		query = daySeries + `
			SELECT
				to_char(days.local_day::date, 'YYYY-MM-DD') AS local_date,
				COALESCE(u.email, '') AS user_email,
				'' AS capacity_tier,
				COUNT(DISTINCT e.session_id) AS sessions
			FROM days
			JOIN container_usage_events e
			  ON e.org_id = @org_id
			 AND e.started_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
			 AND COALESCE(e.stopped_at, now()) > GREATEST((days.local_day AT TIME ZONE @tz), @start)
			JOIN sessions s
			  ON s.id = e.session_id
			 AND s.org_id = e.org_id
			LEFT JOIN users u
			  ON u.id = s.triggered_by_user_id
			 AND u.org_id = s.org_id
			GROUP BY days.local_day, COALESCE(u.email, '')
			ORDER BY days.local_day, user_email`
	case "capacity":
		// This query is NOT inside fmt.Sprintf, so use single '%' for Postgres
		// format() verbs. (Compare with GetBreakdown's CTE which uses '%%'
		// because it IS wrapped in fmt.Sprintf.)
		query = daySeries + `
			SELECT
				to_char(days.local_day::date, 'YYYY-MM-DD') AS local_date,
				'' AS user_email,
				format('%scpu_%smb_%sdiskmb', round(e.cpu_limit)::int, e.memory_limit_mb, e.disk_limit_mb) AS capacity_tier,
				COUNT(DISTINCT e.session_id) AS sessions
			FROM days
			JOIN container_usage_events e
			  ON e.org_id = @org_id
			 AND e.started_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
			 AND COALESCE(e.stopped_at, now()) > GREATEST((days.local_day AT TIME ZONE @tz), @start)
			JOIN sessions s
			  ON s.id = e.session_id
			 AND s.org_id = e.org_id
			WHERE 1=1`
		query = applySessionExecutionFilters(query, args, "s", filters)
		query += `
			GROUP BY days.local_day, capacity_tier
			ORDER BY days.local_day, capacity_tier`
	case "agent", "model", "reasoning":
		keyExpr := "s.agent_type"
		if dimension == "model" {
			keyExpr = normalizedSessionModelSQL("s")
		} else if dimension == "reasoning" {
			keyExpr = normalizedSessionReasoningSQL("s")
		}
		query = daySeries + `
			SELECT
				to_char(days.local_day::date, 'YYYY-MM-DD') AS local_date,
				'' AS user_email,
				` + keyExpr + ` AS capacity_tier,
				COUNT(DISTINCT s.id) AS sessions
			FROM days
			JOIN sessions s
			  ON s.org_id = @org_id
			 AND (
			    EXISTS (
			        SELECT 1
			        FROM container_usage_events e
			        WHERE e.org_id = s.org_id
			          AND e.session_id = s.id
			          AND e.started_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
			          AND COALESCE(e.stopped_at, now()) > GREATEST((days.local_day AT TIME ZONE @tz), @start)
			    )
			    OR (
			        s.token_usage IS NOT NULL
			        AND s.completed_at >= GREATEST((days.local_day AT TIME ZONE @tz), @start)
			        AND s.completed_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
			    )
			 )
			WHERE 1=1`
		query = applySessionExecutionFilters(query, args, "s", filters)
		query += `
			GROUP BY days.local_day, capacity_tier
			ORDER BY days.local_day, capacity_tier`
	default:
		if filters.HasAny() {
			query = daySeries + `
				SELECT
					to_char(days.local_day::date, 'YYYY-MM-DD') AS local_date,
					'' AS user_email,
					'' AS capacity_tier,
					COUNT(DISTINCT s.id) AS sessions
				FROM days
				JOIN sessions s
				  ON s.org_id = @org_id
				 AND (
					EXISTS (
						SELECT 1
						FROM container_usage_events e
						WHERE e.org_id = s.org_id
						  AND e.session_id = s.id
						  AND e.started_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
						  AND COALESCE(e.stopped_at, now()) > GREATEST((days.local_day AT TIME ZONE @tz), @start)
					)
					OR (
						s.token_usage IS NOT NULL
						AND s.completed_at >= GREATEST((days.local_day AT TIME ZONE @tz), @start)
						AND s.completed_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
					)
				 )
				WHERE 1=1`
			query = applySessionExecutionFilters(query, args, "s", filters)
			query += `
				GROUP BY days.local_day
				ORDER BY days.local_day`
		} else {
			query = daySeries + `
				SELECT
					to_char(days.local_day::date, 'YYYY-MM-DD') AS local_date,
					'' AS user_email,
					'' AS capacity_tier,
					COUNT(DISTINCT e.session_id) AS sessions
				FROM days
				JOIN container_usage_events e
				  ON e.org_id = @org_id
				 AND e.started_at < LEAST(((days.local_day + interval '1 day') AT TIME ZONE @tz), @end)
				 AND COALESCE(e.stopped_at, now()) > GREATEST((days.local_day AT TIME ZONE @tz), @start)
				GROUP BY days.local_day
				ORDER BY days.local_day`
		}
	}

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query daily session counts: %w", err)
	}
	defer rows.Close()

	var counts []ExportDailySessionCountRow
	for rows.Next() {
		var row ExportDailySessionCountRow
		if err := rows.Scan(&row.LocalDate, &row.UserEmail, &row.CapacityTier, &row.Sessions); err != nil {
			return nil, fmt.Errorf("scan daily session count row: %w", err)
		}
		counts = append(counts, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily session counts: %w", err)
	}

	return counts, nil
}

// RollupSummary holds aggregated summary metrics from the rollup table.
type RollupSummary struct {
	TotalContainerMinutes float64
	TotalSessions         int
	PeakConcurrent        int
	InputTokens           int64
	OutputTokens          int64
	CostUSD               float64
}

// GetRollupSummary returns summary metrics from org-level rollup rows over the
// given time range. This provides a single consistent data source for summary
// cards instead of mixing legacy raw queries with rollup-backed token totals.
//
// Session count uses COUNT(DISTINCT session_id) from raw events rather than
// SUM(total_sessions) from rollup rows, which would double-count sessions
// spanning multiple hours.
func (s *UsageRollupStore) GetRollupSummary(ctx context.Context, orgID uuid.UUID, start, end time.Time) (RollupSummary, error) {
	var rs RollupSummary
	now := time.Now().UTC()
	err := s.db.QueryRow(ctx, `
		WITH rollup AS (
			SELECT
				COALESCE(SUM(total_container_minutes), 0) AS minutes,
				COALESCE(MAX(peak_concurrent), 0) AS peak,
				COALESCE(SUM(total_input_tokens), 0) AS in_tok,
				COALESCE(SUM(total_output_tokens), 0) AS out_tok,
				COALESCE(SUM(total_llm_cost_usd), 0) AS cost
			FROM usage_hourly
			WHERE org_id = @org_id AND hour_utc >= @start AND hour_utc < @end
			  AND user_id IS NULL AND capacity_tier IS NULL
		),
		sessions AS (
			SELECT COUNT(DISTINCT e.session_id) AS cnt
			FROM container_usage_events e
			WHERE e.org_id = @org_id
			  AND e.started_at < @end
			  AND COALESCE(e.stopped_at, @now) > @start
		)
		SELECT r.minutes, s.cnt, r.peak, r.in_tok, r.out_tok, r.cost
		FROM rollup r, sessions s`,
		pgx.NamedArgs{"org_id": orgID, "start": start, "end": end, "now": now},
	).Scan(&rs.TotalContainerMinutes, &rs.TotalSessions, &rs.PeakConcurrent,
		&rs.InputTokens, &rs.OutputTokens, &rs.CostUSD)
	return rs, err
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

// GetLatestRollupHour returns the most recent hour_utc in usage_hourly.
// Returns the zero time if the table is empty. This is used to seed the
// reaper's rollup watermark on startup so it doesn't redundantly re-roll
// hours that were already materialized before the process restarted.
// lint:allow-no-orgid reason="cross-org scheduler watermark; returns a system-wide timestamp"
func (s *UsageRollupStore) GetLatestRollupHour(ctx context.Context) (time.Time, error) {
	var latest *time.Time
	// Use ORDER BY + LIMIT 1 instead of MAX() so the idx_usage_hourly_org_hour
	// index (org_id, hour_utc DESC) can satisfy this with a reverse index scan
	// rather than a full table scan.
	err := s.db.QueryRow(ctx, `SELECT hour_utc FROM usage_hourly ORDER BY hour_utc DESC LIMIT 1`).Scan(&latest)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("query latest rollup hour: %w", err)
	}
	if latest == nil {
		return time.Time{}, nil
	}
	return *latest, nil
}

// DeleteOlderThan removes rollup rows older than the given cutoff.
// lint:allow-no-orgid reason="cross-org retention cleanup of usage_hourly"
func (s *UsageRollupStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM usage_hourly WHERE hour_utc < @cutoff`,
		pgx.NamedArgs{"cutoff": cutoff})
	if err != nil {
		return 0, fmt.Errorf("delete old usage_hourly: %w", err)
	}
	executionTag, err := s.db.Exec(ctx, `DELETE FROM usage_hourly_execution WHERE hour_utc < @cutoff`,
		pgx.NamedArgs{"cutoff": cutoff})
	if err != nil {
		return 0, fmt.Errorf("delete old usage_hourly_execution: %w", err)
	}
	return tag.RowsAffected() + executionTag.RowsAffected(), nil
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

	// P95 via nearest-rank method. For small samples (n < 20) the result is the
	// max value, which is still a valid upper-bound estimate.
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

func normalizedSessionModelSQL(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s.token_usage->'native_usage'->>'model', ''), NULLIF(%s.model_override, ''), 'unknown')", alias, alias)
}

func normalizedSessionReasoningSQL(alias string) string {
	return fmt.Sprintf("COALESCE(NULLIF(%s.reasoning_effort::text, ''), 'default')", alias)
}

func applyUsageExecutionFilters(where string, args pgx.NamedArgs, alias string, filters UsageExecutionFilters) string {
	if filters.Agent != nil {
		where += fmt.Sprintf(" AND %s.agent_type = @agent", alias)
		args["agent"] = *filters.Agent
	}
	if filters.Model != nil {
		where += fmt.Sprintf(" AND %s.model_used = @model", alias)
		args["model"] = *filters.Model
	}
	if filters.Reasoning != nil {
		where += fmt.Sprintf(" AND %s.reasoning_effort = @reasoning", alias)
		args["reasoning"] = *filters.Reasoning
	}
	return where
}

func applySessionExecutionFilters(where string, args pgx.NamedArgs, alias string, filters UsageExecutionFilters) string {
	if filters.Agent != nil {
		where += fmt.Sprintf(" AND %s.agent_type = @agent", alias)
		args["agent"] = *filters.Agent
	}
	if filters.Model != nil {
		where += " AND " + normalizedSessionModelSQL(alias) + " = @model"
		args["model"] = *filters.Model
	}
	if filters.Reasoning != nil {
		where += " AND " + normalizedSessionReasoningSQL(alias) + " = @reasoning"
		args["reasoning"] = *filters.Reasoning
	}
	return where
}

func sessionDimensionKeySQL(dimension, alias string) string {
	switch dimension {
	case "agent":
		return alias + ".agent_type"
	case "model":
		return normalizedSessionModelSQL(alias)
	case "reasoning":
		return normalizedSessionReasoningSQL(alias)
	default:
		return alias + ".agent_type"
	}
}

func breakdownLabelSQL(dimension, keyExpr string) string {
	switch dimension {
	case "agent":
		return `CASE ` + keyExpr + `
			WHEN 'codex' THEN 'Codex'
			WHEN 'claude_code' THEN 'Claude Code'
			WHEN 'gemini_cli' THEN 'Gemini CLI'
			WHEN 'amp' THEN 'Amp'
			WHEN 'pi' THEN 'Pi'
			WHEN 'opencode' THEN 'OpenCode'
			ELSE ` + keyExpr + ` END`
	case "model":
		return `CASE WHEN ` + keyExpr + ` = 'unknown' THEN 'Unknown' ELSE ` + keyExpr + ` END`
	case "reasoning":
		return `CASE
			WHEN ` + keyExpr + ` = 'default' THEN 'Default'
			WHEN ` + keyExpr + ` = 'xhigh' THEN 'XHigh'
			ELSE initcap(` + keyExpr + `) END`
	default:
		return keyExpr
	}
}

func scanUsageBreakdownRows(rows pgx.Rows, grandTotalMinutes, grandTotalTokens, grandTotalCost float64) ([]models.UsageBreakdownRow, error) {
	var result []models.UsageBreakdownRow
	for rows.Next() {
		var row models.UsageBreakdownRow
		if err := rows.Scan(
			&row.Key, &row.Label,
			&row.TotalContainerMinutes, &row.TotalSessions, &row.TotalContainerStarts,
			&row.PeakConcurrent,
			&row.TotalInputTokens, &row.TotalOutputTokens, &row.TotalTokens, &row.TotalLLMCostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan breakdown row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate breakdown: %w", err)
	}
	for i := range result {
		if grandTotalMinutes > 0 {
			result[i].Percentage = math.Round(result[i].TotalContainerMinutes/grandTotalMinutes*1000) / 10
		}
		if grandTotalTokens > 0 {
			result[i].ShareOfTokens = math.Round(float64(result[i].TotalTokens)/grandTotalTokens*1000) / 10
		}
		if grandTotalCost > 0 {
			result[i].ShareOfTokenCost = math.Round(result[i].TotalLLMCostUSD/grandTotalCost*1000) / 10
		}
	}
	return result, nil
}
