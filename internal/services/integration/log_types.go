package integration

import (
	"context"
	"time"

	"github.com/assembledhq/143/internal/models"
)

type LogProvider interface {
	Name() models.ProviderName
	QueryLogs(ctx context.Context, req LogQueryRequest) (*LogQueryResult, error)
	GetLogContext(ctx context.Context, req LogContextRequest) (*LogContextResult, error)
	ListLogFields(ctx context.Context, req LogFieldsRequest) (*LogFieldsResult, error)
	QueryLogStats(ctx context.Context, req LogStatsRequest) (*LogStatsResult, error)
}

type LogStatsProvider interface {
	LogProvider
	SupportsStats() bool
}

type LogToolSelector struct {
	Provider *string `json:"provider,omitempty"`
}

type LogQueryRequest struct {
	Query      string
	Since      *time.Duration
	StartTime  *time.Time
	EndTime    *time.Time
	Limit      *int
	Direction  *LogDirection
	Fields     []string
	Cursor     *string
	IncludeRaw *bool
}

type LogContextRequest struct {
	Anchor     LogAnchor
	Query      *string
	Since      *time.Duration
	StartTime  *time.Time
	EndTime    *time.Time
	Before     *int
	After      *int
	Fields     []string
	IncludeRaw *bool
}

type LogFieldsRequest struct {
	Query *string
	Since *time.Duration
	Limit *int
}

type LogStatsRequest struct {
	Query     string
	Since     *time.Duration
	StartTime *time.Time
	EndTime   *time.Time
	GroupBy   []string
	Interval  *time.Duration
	Limit     *int
}

type LogAnchor struct {
	ID        *string    `json:"id,omitempty"`
	Cursor    *string    `json:"cursor,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

type LogDirection string

const (
	LogDirectionDesc LogDirection = "desc"
	LogDirectionAsc  LogDirection = "asc"
)

type LogEntry struct {
	ID        string              `json:"id,omitempty"`
	Timestamp time.Time           `json:"timestamp"`
	Provider  models.ProviderName `json:"provider"`
	Message   string              `json:"message,omitempty"`
	Level     string              `json:"level,omitempty"`
	Service   string              `json:"service,omitempty"`
	TraceID   string              `json:"trace_id,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
	OrgID     string              `json:"org_id,omitempty"`
	Fields    map[string]any      `json:"fields,omitempty"`
	Raw       map[string]any      `json:"raw,omitempty"`
}

type LogQueryResult struct {
	Provider   models.ProviderName `json:"provider"`
	Query      string              `json:"query"`
	StartTime  time.Time           `json:"start_time"`
	EndTime    time.Time           `json:"end_time"`
	Entries    []LogEntry          `json:"entries"`
	NextCursor string              `json:"next_cursor,omitempty"`
	Truncated  bool                `json:"truncated"`
}

type LogContextResult struct {
	Provider   models.ProviderName `json:"provider"`
	Anchor     LogAnchor           `json:"anchor"`
	Before     []LogEntry          `json:"before"`
	Target     *LogEntry           `json:"target,omitempty"`
	After      []LogEntry          `json:"after"`
	PrevCursor string              `json:"prev_cursor,omitempty"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type LogFieldsResult struct {
	Provider models.ProviderName `json:"provider"`
	Fields   []LogField          `json:"fields"`
}

type LogField struct {
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`
	SampleValues []any  `json:"sample_values,omitempty"`
}

type LogStatsResult struct {
	Provider  models.ProviderName `json:"provider"`
	Query     string              `json:"query"`
	StartTime time.Time           `json:"start_time"`
	EndTime   time.Time           `json:"end_time"`
	Series    []LogStatsSeries    `json:"series"`
	Truncated bool                `json:"truncated"`
}

type LogStatsSeries struct {
	Group   map[string]string `json:"group,omitempty"`
	Buckets []LogStatsBucket  `json:"buckets"`
}

type LogStatsBucket struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	Count     int       `json:"count"`
}
