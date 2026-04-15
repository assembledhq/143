package models

import (
	"time"

	"github.com/google/uuid"
)

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
