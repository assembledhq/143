package handlers

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// usageMembershipStore is the subset of OrganizationMembershipStore that
// ExportCSV needs to authorize an explicit ?org_id= query param when the
// X-Active-Org-ID header is absent (window.open download has no header API).
type usageMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

// UsageHandler exposes container usage data for billing dashboards.
type UsageHandler struct {
	usageStore  *db.ContainerUsageStore
	rollupStore usageRollupReader
	memberships usageMembershipStore
}

type usageRollupReader interface {
	GetRollupSummary(ctx context.Context, orgID uuid.UUID, start, end time.Time) (db.RollupSummary, error)
	GetTokenTotals(ctx context.Context, orgID uuid.UUID, start, end time.Time) (db.TokenTotals, error)
	GetTimeseries(ctx context.Context, orgID uuid.UUID, start, end time.Time, groupBy, stackBy string, userID *uuid.UUID, capacity *string, filters db.UsageExecutionFilters) ([]models.UsageTimeseriesBucket, error)
	GetBreakdown(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension, sortBy string, limit int, filters db.UsageExecutionFilters) ([]models.UsageBreakdownRow, error)
	GetExportRows(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension string, filters db.UsageExecutionFilters) (pgx.Rows, error)
	GetDailySessionCounts(ctx context.Context, orgID uuid.UUID, start, end time.Time, dimension, tzName string, filters db.UsageExecutionFilters) ([]db.ExportDailySessionCountRow, error)
}

// NewUsageHandler creates a UsageHandler.
func NewUsageHandler(usageStore *db.ContainerUsageStore, opts ...UsageHandlerOption) *UsageHandler {
	h := &UsageHandler{usageStore: usageStore}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// UsageHandlerOption configures optional dependencies on UsageHandler.
type UsageHandlerOption func(*UsageHandler)

// WithRollupStore sets the rollup store for timeseries/breakdown/export endpoints.
func WithRollupStore(rs usageRollupReader) UsageHandlerOption {
	return func(h *UsageHandler) { h.rollupStore = rs }
}

// WithMembershipStore wires the membership store used by ExportCSV to
// authorize an explicit ?org_id= query param. Required for the CSV download
// to pick the right org for multi-org users (window.open cannot send
// X-Active-Org-ID).
func WithMembershipStore(ms usageMembershipStore) UsageHandlerOption {
	return func(h *UsageHandler) { h.memberships = ms }
}

var (
	errUsageExportOrgInvalid   = errors.New("invalid usage export org")
	errUsageExportOrgForbidden = errors.New("forbidden usage export org")
	errUsageExportUnauthorized = errors.New("unauthorized usage export request")
)

// exportOrgID resolves the org for a CSV export, honouring an optional
// ?org_id= query param when the X-Active-Org-ID header is absent
// (window.open downloads have no header API). Membership-validated, falls
// back to the auth middleware's resolved active org. Same shape as
// pull_requests.streamOrgIDFromRequest and sessions.streamOrgID.
func (h *UsageHandler) exportOrgID(r *http.Request) (uuid.UUID, error) {
	orgID := middleware.OrgIDFromContext(r.Context())
	requestedRaw := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedRaw == "" {
		return orgID, nil
	}

	requestedOrgID, err := uuid.Parse(requestedRaw)
	if err != nil {
		return uuid.Nil, errUsageExportOrgInvalid
	}
	if requestedOrgID == orgID {
		return requestedOrgID, nil
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return uuid.Nil, errUsageExportUnauthorized
	}
	if h.memberships == nil {
		return uuid.Nil, errors.New("membership store not configured")
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, requestedOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errUsageExportOrgForbidden
		}
		return uuid.Nil, err
	}
	return requestedOrgID, nil
}

// maxTimeRange is the maximum allowed duration for usage queries.
const maxTimeRange = 90 * 24 * time.Hour

func parseExecutionFilters(r *http.Request) db.UsageExecutionFilters {
	return db.UsageExecutionFilters{
		Agent:     db.NormalizeUsageExecutionFilterValue(r.URL.Query().Get("agent")),
		Model:     db.NormalizeUsageExecutionFilterValue(r.URL.Query().Get("model")),
		Reasoning: db.NormalizeUsageExecutionFilterValue(r.URL.Query().Get("reasoning")),
	}
}

// parseTimeRange extracts start/end from query params with defaults and validation.
func parseTimeRange(w http.ResponseWriter, r *http.Request) (start, end time.Time, ok bool) {
	now := time.Now().UTC()
	return parseTimeRangeWithDefaults(w, r, now.AddDate(0, 0, -30), now)
}

// parseTimeRangeWithDefaults extracts start/end from query params, falling back
// to the provided defaults. Shared by endpoints with different default ranges.
func parseTimeRangeWithDefaults(w http.ResponseWriter, r *http.Request, defaultStart, defaultEnd time.Time) (start, end time.Time, ok bool) {
	start = defaultStart
	end = defaultEnd

	if s := r.URL.Query().Get("start"); s != "" {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "start must be RFC3339 format")
			return time.Time{}, time.Time{}, false
		}
		start = parsed
	}
	if e := r.URL.Query().Get("end"); e != "" {
		parsed, err := time.Parse(time.RFC3339, e)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "end must be RFC3339 format")
			return time.Time{}, time.Time{}, false
		}
		end = parsed
	}

	if !start.Before(end) {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "start must be before end")
		return time.Time{}, time.Time{}, false
	}

	if end.Sub(start) > maxTimeRange {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "time range must not exceed 90 days")
		return time.Time{}, time.Time{}, false
	}

	return start, end, true
}

// GetSummary returns aggregated container usage for the org over a time period.
//
//	GET /api/v1/usage?start=2026-04-01T00:00:00Z&end=2026-05-01T00:00:00Z
//
// Defaults to the current calendar month if start/end are omitted.
func (h *UsageHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	now := time.Now().UTC()
	defaultStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	defaultEnd := defaultStart.AddDate(0, 1, 0)

	start, end, ok := parseTimeRangeWithDefaults(w, r, defaultStart, defaultEnd)
	if !ok {
		return
	}

	// Prefer rollup-backed summary for consistency with the rest of the dashboard.
	// Fall back to legacy raw queries if the rollup store is unavailable.
	if h.rollupStore != nil {
		rs, err := h.rollupStore.GetRollupSummary(r.Context(), orgID, start, end)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch usage summary", err)
			return
		}
		summary := &models.UsageSummary{
			OrgID:                 orgID,
			PeriodStart:           start,
			PeriodEnd:             end,
			TotalContainerMinutes: rs.TotalContainerMinutes,
			TotalSessions:         rs.TotalSessions,
			PeakConcurrent:        rs.PeakConcurrent,
			TotalInputTokens:      rs.InputTokens,
			TotalOutputTokens:     rs.OutputTokens,
			TotalLLMCostUSD:       rs.CostUSD,
		}
		writeJSON(w, http.StatusOK, models.SingleResponse[*models.UsageSummary]{Data: summary})
		return
	}

	summary, err := h.usageStore.GetUsageSummary(r.Context(), orgID, start, end)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch usage summary", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.UsageSummary]{Data: summary})
}

// ListBySession returns all container usage events for a given session.
//
//	GET /api/v1/sessions/{id}/usage
func (h *UsageHandler) ListBySession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "invalid session ID")
		return
	}

	events, err := h.usageStore.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch session usage", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ContainerUsageEvent]{
		Data: events,
	})
}

// GetTimeseries returns hourly-bucketed usage data from the rollup table.
//
//	GET /api/v1/usage/timeseries?start=...&end=...&group_by=hour&user_id=...&capacity=...
func (h *UsageHandler) GetTimeseries(w http.ResponseWriter, r *http.Request) {
	if h.rollupStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "usage rollup not available")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	start, end, ok := parseTimeRange(w, r)
	if !ok {
		return
	}

	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "hour"
	}
	switch groupBy {
	case "hour", "user", "capacity":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "group_by must be one of: hour, user, capacity")
		return
	}

	var userID *uuid.UUID
	if uid := r.URL.Query().Get("user_id"); uid != "" {
		parsed, err := uuid.Parse(uid)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "invalid user_id")
			return
		}
		userID = &parsed
	}

	var capacity *string
	if c := r.URL.Query().Get("capacity"); c != "" {
		capacity = &c
	}
	filters := parseExecutionFilters(r)
	stackBy := r.URL.Query().Get("stack_by")
	if stackBy != "" {
		switch stackBy {
		case "agent", "model", "reasoning", "capacity":
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "stack_by must be one of: agent, model, reasoning, capacity")
			return
		}
	}

	// user_id and capacity filters override group_by — reject conflicting combos
	// so callers don't get silently surprising results.
	if userID != nil && capacity != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "user_id and capacity filters are mutually exclusive")
		return
	}
	if userID != nil && filters.HasAny() {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "user_id cannot be combined with execution filters")
		return
	}
	if groupBy == "user" && (filters.HasAny() || stackBy != "") {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "group_by=user cannot be combined with execution filters or stack_by")
		return
	}
	if groupBy == "capacity" && stackBy != "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "group_by=capacity cannot be combined with stack_by")
		return
	}

	buckets, err := h.rollupStore.GetTimeseries(r.Context(), orgID, start, end, groupBy, stackBy, userID, capacity, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch usage timeseries", err)
		return
	}
	if buckets == nil {
		buckets = []models.UsageTimeseriesBucket{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.UsageTimeseriesResponse]{
		Data: models.UsageTimeseriesResponse{
			Buckets:     buckets,
			PeriodStart: start,
			PeriodEnd:   end,
		},
	})
}

// GetBreakdown returns a dimensional breakdown of usage.
//
//	GET /api/v1/usage/breakdown?start=...&end=...&dimension=user&sort=minutes_desc&limit=50
func (h *UsageHandler) GetBreakdown(w http.ResponseWriter, r *http.Request) {
	if h.rollupStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "usage rollup not available")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	start, end, ok := parseTimeRange(w, r)
	if !ok {
		return
	}

	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "user"
	}
	switch dimension {
	case "user", "capacity", "agent", "model", "reasoning":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "dimension must be one of: user, capacity, agent, model, reasoning")
		return
	}
	filters := parseExecutionFilters(r)
	if dimension == "user" && filters.HasAny() {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "execution filters are not supported for user breakdowns")
		return
	}

	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "minutes_desc"
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	rows, err := h.rollupStore.GetBreakdown(r.Context(), orgID, start, end, dimension, sortBy, limit, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch usage breakdown", err)
		return
	}
	if rows == nil {
		rows = []models.UsageBreakdownRow{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.UsageBreakdownRow]{Data: rows})
}

// ExportCSV streams usage data as a CSV download.
// This is a read-only GET endpoint so CSRF is not required; the browser will
// include auth cookies automatically via same-origin window.open().
//
// NOTE: Because the response is streamed, the 200 status and CSV headers are
// written before all rows are scanned. If an error occurs mid-stream, the HTTP
// status cannot be changed. An "ERROR" sentinel row is appended so clients can
// detect truncation, but they won't see an HTTP error status code.
//
//	GET /api/v1/usage/export?start=...&end=...&granularity=daily&dimension=none&tz=America/Los_Angeles
func (h *UsageHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	if h.rollupStore == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "usage rollup not available")
		return
	}

	orgID, err := h.exportOrgID(r)
	if err != nil {
		switch {
		case errors.Is(err, errUsageExportOrgInvalid):
			writeError(w, r, http.StatusBadRequest, "INVALID_ORG", "invalid org_id")
		case errors.Is(err, errUsageExportOrgForbidden):
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "access denied")
		case errors.Is(err, errUsageExportUnauthorized):
			writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing user")
		default:
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to authorize export", err)
		}
		return
	}
	start, end, ok := parseTimeRange(w, r)
	if !ok {
		return
	}

	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "none"
	}
	switch dimension {
	case "none", "user", "capacity", "agent", "model", "reasoning":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "dimension must be one of: none, user, capacity, agent, model, reasoning")
		return
	}
	filters := parseExecutionFilters(r)
	if dimension == "user" && filters.HasAny() {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "execution filters are not supported for user exports")
		return
	}

	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "daily"
	}
	switch granularity {
	case "daily", "hourly":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "granularity must be one of: daily, hourly")
		return
	}

	tzName := r.URL.Query().Get("tz")
	if tzName == "" {
		tzName = "UTC"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "invalid timezone")
		return
	}

	rows, err := h.rollupStore.GetExportRows(r.Context(), orgID, start, end, dimension, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to export usage", err)
		return
	}
	defer rows.Close()

	filename := fmt.Sprintf("usage-%s-to-%s.csv", start.Format("2006-01-02"), end.Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	logger := zerolog.Ctx(r.Context())
	cw := csv.NewWriter(w)

	// Write header.
	header := []string{"date"}
	if granularity == "hourly" {
		header = append(header, "hour_utc")
	}
	if dimension == "user" {
		header = append(header, "user_email")
	}
	if dimension == "capacity" {
		header = append(header, "capacity_tier")
	}
	if dimension == "agent" {
		header = append(header, "agent")
	}
	if dimension == "model" {
		header = append(header, "model")
	}
	if dimension == "reasoning" {
		header = append(header, "reasoning")
	}
	header = append(header, "container_minutes", "sessions", "container_starts", "peak_concurrent", "input_tokens", "output_tokens", "llm_cost_usd")
	if err := cw.Write(header); err != nil {
		logger.Error().Err(err).Msg("csv: failed to write header")
		return
	}

	// Track daily aggregation for daily granularity.
	type dailyKey struct {
		date     string
		email    string
		capacity string
	}
	type dailyRow struct {
		minutes  float64
		sessions int
		starts   int
		peak     int
		inTok    int64
		outTok   int64
		cost     float64
	}
	dailyAgg := make(map[dailyKey]*dailyRow)
	var dailyOrder []dailyKey
	var scanErr error

	for rows.Next() {
		var (
			hourUTC      time.Time
			userEmail    string
			dimensionKey string
			minutes      float64
			sessions     int
			starts       int
			peak         int
			inTok        int64
			outTok       int64
			cost         float64
		)
		if err := rows.Scan(&hourUTC, &userEmail, &dimensionKey, &minutes, &sessions, &starts, &peak, &inTok, &outTok, &cost); err != nil {
			logger.Error().Err(err).Msg("csv: failed to scan export row")
			scanErr = err
			break
		}

		localTime := hourUTC.In(loc)
		dateStr := localTime.Format("2006-01-02")

		if granularity == "hourly" {
			record := []string{dateStr, hourUTC.Format(time.RFC3339)}
			if dimension == "user" {
				record = append(record, userEmail)
			}
			if dimension == "capacity" {
				record = append(record, dimensionKey)
			}
			if dimension == "agent" || dimension == "model" || dimension == "reasoning" {
				record = append(record, dimensionKey)
			}
			record = append(record,
				fmt.Sprintf("%.2f", minutes),
				strconv.Itoa(sessions),
				strconv.Itoa(starts),
				strconv.Itoa(peak),
				strconv.FormatInt(inTok, 10),
				strconv.FormatInt(outTok, 10),
				fmt.Sprintf("%.2f", cost),
			)
			if err := cw.Write(record); err != nil {
				logger.Error().Err(err).Msg("csv: failed to write hourly record")
				return
			}
		} else {
			key := dailyKey{date: dateStr, email: userEmail, capacity: dimensionKey}
			agg, ok := dailyAgg[key]
			if !ok {
				agg = &dailyRow{}
				dailyAgg[key] = agg
				dailyOrder = append(dailyOrder, key)
			}
			agg.minutes += minutes
			// Sessions are summed as a rough fallback; the accurate count
			// comes from GetDailySessionCounts below which uses COUNT(DISTINCT).
			agg.sessions += sessions
			agg.starts += starts
			if peak > agg.peak {
				agg.peak = peak
			}
			agg.inTok += inTok
			agg.outTok += outTok
			agg.cost += cost
		}
	}

	// If a scan error occurred, write an error indicator row so the client
	// sees that the export was truncated rather than silently missing data.
	if scanErr != nil {
		_ = cw.Write([]string{"ERROR", "Export truncated due to internal error"})
		cw.Flush()
		return
	}

	if granularity == "daily" {
		// Fetch accurate daily session counts from raw events instead of
		// summing hourly rollups, which would double-count sessions that
		// span multiple hours.
		dailyCounts, err := h.rollupStore.GetDailySessionCounts(r.Context(), orgID, start, end, dimension, tzName, filters)
		if err != nil {
			logger.Error().Err(err).Msg("csv: failed to fetch daily session counts")
			// Write a warning row so the consumer knows session counts are approximate.
			_ = cw.Write([]string{"# WARNING: session counts are approximate (summed from hourly rollups)"})
		} else {
			type countKey struct {
				date     string
				email    string
				capacity string
			}
			countMap := make(map[countKey]int, len(dailyCounts))
			for _, c := range dailyCounts {
				countMap[countKey{date: c.LocalDate, email: c.UserEmail, capacity: c.CapacityTier}] = c.Sessions
			}
			for _, key := range dailyOrder {
				if count, ok := countMap[countKey(key)]; ok {
					dailyAgg[key].sessions = count
				}
			}
		}

		for _, key := range dailyOrder {
			agg := dailyAgg[key]
			record := []string{key.date}
			if dimension == "user" {
				record = append(record, key.email)
			}
			if dimension == "capacity" {
				record = append(record, key.capacity)
			}
			if dimension == "agent" || dimension == "model" || dimension == "reasoning" {
				record = append(record, key.capacity)
			}
			record = append(record,
				fmt.Sprintf("%.2f", agg.minutes),
				strconv.Itoa(agg.sessions),
				strconv.Itoa(agg.starts),
				strconv.Itoa(agg.peak),
				strconv.FormatInt(agg.inTok, 10),
				strconv.FormatInt(agg.outTok, 10),
				fmt.Sprintf("%.2f", agg.cost),
			)
			if err := cw.Write(record); err != nil {
				logger.Error().Err(err).Msg("csv: failed to write daily record")
				return
			}
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		logger.Error().Err(err).Msg("csv: flush error")
	}
}
