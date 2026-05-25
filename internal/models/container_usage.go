package models

import (
	"time"

	"github.com/google/uuid"
)

// ContainerUsageEvent records a single container lifecycle for billing.
// One event is created when a sandbox container starts and updated when it stops.
type ContainerUsageEvent struct {
	ID               uuid.UUID  `db:"id" json:"id"`
	OrgID            uuid.UUID  `db:"org_id" json:"org_id"`
	SessionID        uuid.UUID  `db:"session_id" json:"session_id"`
	ContainerID      string     `db:"container_id" json:"container_id"`
	Provider         string     `db:"provider" json:"provider"`
	CPULimit         float64    `db:"cpu_limit" json:"cpu_limit"`
	MemoryLimitMB    int        `db:"memory_limit_mb" json:"memory_limit_mb"`
	DiskLimitMB      int        `db:"disk_limit_mb" json:"disk_limit_mb"`
	Image            string     `db:"image" json:"image"`
	StartedAt        time.Time  `db:"started_at" json:"started_at"`
	StoppedAt        *time.Time `db:"stopped_at" json:"stopped_at,omitempty"`
	DurationMs       *int64     `db:"duration_ms" json:"duration_ms,omitempty"`
	ContainerMinutes *float64   `db:"container_minutes" json:"container_minutes,omitempty"`
	ExitReason       *string    `db:"exit_reason" json:"exit_reason,omitempty"`
	CreatedAt        time.Time  `db:"created_at" json:"created_at"`
}

// UsageSummary is the aggregated billing data for an org over a time period.
type UsageSummary struct {
	OrgID                 uuid.UUID        `json:"org_id"`
	PeriodStart           time.Time        `json:"period_start"`
	PeriodEnd             time.Time        `json:"period_end"`
	TotalContainerMinutes float64          `json:"total_container_minutes"`
	TotalSessions         int              `json:"total_sessions"`
	PeakConcurrent        int              `json:"peak_concurrent"`
	ByCapacity            []CapacityBucket `json:"by_capacity"`
	TotalInputTokens      int64            `json:"total_input_tokens"`
	TotalOutputTokens     int64            `json:"total_output_tokens"`
	TotalLLMCostUSD       float64          `json:"total_llm_cost_usd"`
}

// CapacityBucket groups usage by resource tier (CPU + memory + disk).
type CapacityBucket struct {
	CPULimit         float64 `json:"cpu_limit"`
	MemoryLimitMB    int     `json:"memory_limit_mb"`
	DiskLimitMB      int     `json:"disk_limit_mb"`
	ContainerMinutes float64 `json:"container_minutes"`
	SessionCount     int     `json:"session_count"`
}
