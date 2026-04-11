package models

import (
	"time"

	"github.com/google/uuid"
)

// UsageHourly is a pre-aggregated hourly usage row from the usage_hourly table.
type UsageHourly struct {
	ID                    uuid.UUID  `db:"id" json:"id"`
	OrgID                 uuid.UUID  `db:"org_id" json:"org_id"`
	HourUTC               time.Time  `db:"hour_utc" json:"hour_utc"`
	UserID                *uuid.UUID `db:"user_id" json:"user_id,omitempty"`
	CapacityTier          *string    `db:"capacity_tier" json:"capacity_tier,omitempty"`
	TotalContainerMinutes float64    `db:"total_container_minutes" json:"total_container_minutes"`
	TotalSessions         int        `db:"total_sessions" json:"total_sessions"`
	TotalContainerStarts  int        `db:"total_container_starts" json:"total_container_starts"`
	PeakConcurrent        int        `db:"peak_concurrent" json:"peak_concurrent"`
	AvgDurationSec        float64    `db:"avg_duration_sec" json:"avg_duration_sec"`
	P95DurationSec        float64    `db:"p95_duration_sec" json:"p95_duration_sec"`
	TotalInputTokens      int64      `db:"total_input_tokens" json:"total_input_tokens"`
	TotalOutputTokens     int64      `db:"total_output_tokens" json:"total_output_tokens"`
	TotalLLMCostUSD       float64    `db:"total_llm_cost_usd" json:"total_llm_cost_usd"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

// UsageTimeseriesBucket is the API response shape for timeseries data.
type UsageTimeseriesBucket struct {
	HourUTC               time.Time  `json:"hour_utc"`
	UserID                *uuid.UUID `json:"user_id,omitempty"`
	UserName              string     `json:"user_name,omitempty"`
	CapacityTier          *string    `json:"capacity_tier,omitempty"`
	TotalContainerMinutes float64    `json:"total_container_minutes"`
	TotalSessions         int        `json:"total_sessions"`
	TotalContainerStarts  int        `json:"total_container_starts"`
	PeakConcurrent        int        `json:"peak_concurrent"`
	AvgDurationSec        float64    `json:"avg_duration_sec"`
	P95DurationSec        float64    `json:"p95_duration_sec"`
	TotalInputTokens      int64      `json:"total_input_tokens"`
	TotalOutputTokens     int64      `json:"total_output_tokens"`
	TotalLLMCostUSD       float64    `json:"total_llm_cost_usd"`
}

// UsageTimeseriesResponse is the API response for the timeseries endpoint.
type UsageTimeseriesResponse struct {
	Buckets     []UsageTimeseriesBucket `json:"buckets"`
	PeriodStart time.Time               `json:"period_start"`
	PeriodEnd   time.Time               `json:"period_end"`
}

// UsageBreakdownRow is a single row in the dimensional breakdown response.
type UsageBreakdownRow struct {
	Key                   string  `json:"key"`
	Label                 string  `json:"label"`
	TotalContainerMinutes float64 `json:"total_container_minutes"`
	TotalSessions         int     `json:"total_sessions"`
	TotalContainerStarts  int     `json:"total_container_starts"`
	PeakConcurrent        int     `json:"peak_concurrent"`
	TotalInputTokens      int64   `json:"total_input_tokens"`
	TotalOutputTokens     int64   `json:"total_output_tokens"`
	TotalLLMCostUSD       float64 `json:"total_llm_cost_usd"`
	Percentage            float64 `json:"percentage"`
}
