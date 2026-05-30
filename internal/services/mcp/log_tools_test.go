package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/stretchr/testify/require"
)

type mcpLogProvider struct {
	name          models.ProviderName
	supportsStats bool
	lastQuery     integration.LogQueryRequest
	lastContext   integration.LogContextRequest
	lastFields    integration.LogFieldsRequest
	lastStats     integration.LogStatsRequest
}

func (p *mcpLogProvider) Name() models.ProviderName { return p.name }
func (p *mcpLogProvider) SupportsStats() bool       { return p.supportsStats }
func (p *mcpLogProvider) QueryLogs(_ context.Context, req integration.LogQueryRequest) (*integration.LogQueryResult, error) {
	p.lastQuery = req
	start, end := timeBoundsForTest(req.Since, req.StartTime, req.EndTime)
	return &integration.LogQueryResult{
		Provider:  p.name,
		Query:     req.Query,
		StartTime: start,
		EndTime:   end,
		Entries: []integration.LogEntry{
			{Timestamp: start, Provider: p.name, Message: "log line"},
		},
	}, nil
}
func (p *mcpLogProvider) GetLogContext(_ context.Context, req integration.LogContextRequest) (*integration.LogContextResult, error) {
	p.lastContext = req
	return &integration.LogContextResult{Provider: p.name, Anchor: req.Anchor}, nil
}
func (p *mcpLogProvider) ListLogFields(_ context.Context, req integration.LogFieldsRequest) (*integration.LogFieldsResult, error) {
	p.lastFields = req
	return &integration.LogFieldsResult{Provider: p.name, Fields: []integration.LogField{{Name: "service"}}}, nil
}
func (p *mcpLogProvider) QueryLogStats(_ context.Context, req integration.LogStatsRequest) (*integration.LogStatsResult, error) {
	p.lastStats = req
	if !p.supportsStats {
		return nil, integration.ErrLogStatsUnsupported
	}
	start, end := timeBoundsForTest(req.Since, req.StartTime, req.EndTime)
	return &integration.LogStatsResult{
		Provider:  p.name,
		Query:     req.Query,
		StartTime: start,
		EndTime:   end,
		Series:    []integration.LogStatsSeries{{Buckets: []integration.LogStatsBucket{{Timestamp: start, Count: 2}}}},
	}, nil
}

func TestListToolsIncludesSharedLogToolsOnce(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderVictoriaLogs, supportsStats: true})
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderMezmo})
	tr := NewToolRegistry(reg)

	names := toolNames(tr.ListTools())
	require.Contains(t, names, "log_query", "ListTools should expose shared log_query")
	require.Contains(t, names, "log_context", "ListTools should expose shared log_context")
	require.Contains(t, names, "log_fields", "ListTools should expose shared log_fields")
	require.Contains(t, names, "log_stats", "ListTools should expose shared log_stats when any provider supports stats")
	require.NotContains(t, names, "victorialogs_log_query", "ListTools should not provider-prefix shared log tools")
	require.NotContains(t, names, "mezmo_log_query", "ListTools should not provider-prefix shared log tools")
}

func TestCallLogQueryValidatesAndDispatches(t *testing.T) {
	t.Parallel()

	provider := &mcpLogProvider{name: models.ProviderVictoriaLogs}
	reg := integration.NewRegistry()
	reg.RegisterLogProvider(provider)
	tr := NewToolRegistry(reg)

	result := tr.CallTool(context.Background(), "log_query", json.RawMessage(`{"query":"service:api","since":"1h","limit":5,"direction":"asc","fields":["message","level"]}`))
	require.False(t, result.IsError, "log_query should dispatch to the selected provider")

	var response integration.LogQueryResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &response), "log_query should return JSON")
	require.Equal(t, models.ProviderVictoriaLogs, response.Provider, "log_query should report the selected provider")
	require.Equal(t, "service:api", provider.lastQuery.Query, "log_query should pass provider-native query text unchanged")
	require.Equal(t, 5, *provider.lastQuery.Limit, "log_query should pass the requested limit")
	require.Equal(t, integration.LogDirectionAsc, *provider.lastQuery.Direction, "log_query should pass requested direction")
	require.Equal(t, []string{"message", "level"}, provider.lastQuery.Fields, "log_query should pass field projection")
}

func TestCallLogToolRejectsMissingTimeBound(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderVictoriaLogs})
	tr := NewToolRegistry(reg)

	result := tr.CallTool(context.Background(), "log_query", json.RawMessage(`{"query":"service:api"}`))
	require.True(t, result.IsError, "log_query should reject unbounded requests")
	require.Contains(t, result.Content[0].Text, integration.ErrLogTimeBoundRequired.Error(), "log_query error should explain that a time bound is required")
}

func TestCallLogContextRequiresPortableTimestampScope(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderVictoriaLogs})
	tr := NewToolRegistry(reg)

	result := tr.CallTool(context.Background(), "log_context", json.RawMessage(`{"timestamp":"2026-05-28T10:00:00Z","since":"10m","before":3,"after":4}`))
	require.True(t, result.IsError, "log_context timestamp fallback should require query scope")
	require.Contains(t, result.Content[0].Text, "query", "log_context error should name the missing query scope")
}

func TestCallLogStatsUnsupported(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderMezmo})
	tr := NewToolRegistry(reg)

	result := tr.CallTool(context.Background(), "log_stats", json.RawMessage(`{"query":"level:error","since":"1h"}`))
	require.True(t, result.IsError, "log_stats should surface unsupported stats as a tool error")
	require.Contains(t, result.Content[0].Text, "mezmo", "log_stats unsupported message should name the selected provider")
	require.Contains(t, result.Content[0].Text, "not supported", "log_stats unsupported message should be clear")
}

func TestCallLogStatsParsesInterval(t *testing.T) {
	t.Parallel()

	provider := &mcpLogProvider{name: models.ProviderVictoriaLogs, supportsStats: true}
	reg := integration.NewRegistry()
	reg.RegisterLogProvider(provider)
	tr := NewToolRegistry(reg)

	result := tr.CallTool(context.Background(), "log_stats", json.RawMessage(`{"query":"level:error","since":"1h","interval":"5m","group_by":["service"]}`))
	require.False(t, result.IsError, "log_stats should accept a valid interval")
	require.NotNil(t, provider.lastStats.Interval, "log_stats should pass interval to the provider")
	require.Equal(t, 5*time.Minute, *provider.lastStats.Interval, "log_stats should parse interval durations")
}

// bareLogProvider implements LogProvider but NOT LogStatsProvider.
type bareLogProvider struct{ name models.ProviderName }

func (p *bareLogProvider) Name() models.ProviderName { return p.name }
func (p *bareLogProvider) QueryLogs(_ context.Context, req integration.LogQueryRequest) (*integration.LogQueryResult, error) {
	return &integration.LogQueryResult{Provider: p.name}, nil
}
func (p *bareLogProvider) GetLogContext(_ context.Context, req integration.LogContextRequest) (*integration.LogContextResult, error) {
	return &integration.LogContextResult{Provider: p.name}, nil
}
func (p *bareLogProvider) ListLogFields(_ context.Context, req integration.LogFieldsRequest) (*integration.LogFieldsResult, error) {
	return &integration.LogFieldsResult{Provider: p.name}, nil
}
func (p *bareLogProvider) QueryLogStats(_ context.Context, req integration.LogStatsRequest) (*integration.LogStatsResult, error) {
	return nil, integration.ErrLogStatsUnsupported
}

func TestListToolsOmitsLogStatsWhenNoProviderSupportsIt(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	reg.RegisterLogProvider(&bareLogProvider{name: models.ProviderMezmo})
	tr := NewToolRegistry(reg)

	names := toolNames(tr.ListTools())
	require.Contains(t, names, "log_query", "log_query should be present for any log provider")
	require.NotContains(t, names, "log_stats", "log_stats should be absent when no provider implements SupportsStats")
}

// truncatingLogProvider always returns a single entry with Truncated=true.
type truncatingLogProvider struct {
	name      models.ProviderName
	lastQuery integration.LogQueryRequest
}

func (p *truncatingLogProvider) Name() models.ProviderName { return p.name }
func (p *truncatingLogProvider) SupportsStats() bool       { return false }
func (p *truncatingLogProvider) QueryLogs(_ context.Context, req integration.LogQueryRequest) (*integration.LogQueryResult, error) {
	p.lastQuery = req
	start, end := timeBoundsForTest(req.Since, req.StartTime, req.EndTime)
	ts := start.Add(time.Minute)
	return &integration.LogQueryResult{
		Provider:  p.name,
		Query:     req.Query,
		StartTime: start,
		EndTime:   end,
		Entries:   []integration.LogEntry{{Timestamp: ts, Provider: p.name, Message: "entry"}},
		Truncated: true,
	}, nil
}
func (p *truncatingLogProvider) GetLogContext(_ context.Context, req integration.LogContextRequest) (*integration.LogContextResult, error) {
	return &integration.LogContextResult{Provider: p.name, Anchor: req.Anchor}, nil
}
func (p *truncatingLogProvider) ListLogFields(_ context.Context, _ integration.LogFieldsRequest) (*integration.LogFieldsResult, error) {
	return &integration.LogFieldsResult{Provider: p.name}, nil
}
func (p *truncatingLogProvider) QueryLogStats(_ context.Context, _ integration.LogStatsRequest) (*integration.LogStatsResult, error) {
	return nil, integration.ErrLogStatsUnsupported
}

// oversizedContextProvider returns a LogContextResult with enough entries to exceed 400KB.
type oversizedContextProvider struct {
	name   models.ProviderName
	before []integration.LogEntry
	after  []integration.LogEntry
}

func (p *oversizedContextProvider) Name() models.ProviderName { return p.name }
func (p *oversizedContextProvider) SupportsStats() bool       { return false }
func (p *oversizedContextProvider) QueryLogs(_ context.Context, _ integration.LogQueryRequest) (*integration.LogQueryResult, error) {
	return &integration.LogQueryResult{Provider: p.name}, nil
}
func (p *oversizedContextProvider) GetLogContext(_ context.Context, req integration.LogContextRequest) (*integration.LogContextResult, error) {
	before := make([]integration.LogEntry, len(p.before))
	copy(before, p.before)
	after := make([]integration.LogEntry, len(p.after))
	copy(after, p.after)
	return &integration.LogContextResult{Provider: p.name, Anchor: req.Anchor, Before: before, After: after}, nil
}
func (p *oversizedContextProvider) ListLogFields(_ context.Context, _ integration.LogFieldsRequest) (*integration.LogFieldsResult, error) {
	return &integration.LogFieldsResult{Provider: p.name}, nil
}
func (p *oversizedContextProvider) QueryLogStats(_ context.Context, _ integration.LogStatsRequest) (*integration.LogStatsResult, error) {
	return nil, integration.ErrLogStatsUnsupported
}

func TestCallLogQueryCursorContinues(t *testing.T) {
	t.Parallel()

	provider := &truncatingLogProvider{name: models.ProviderVictoriaLogs}
	reg := integration.NewRegistry()
	reg.RegisterLogProvider(provider)
	tr := NewToolRegistry(reg)

	// First call: get a truncated result with a NextCursor.
	r1 := tr.CallTool(context.Background(), "log_query", json.RawMessage(`{"query":"service:api","since":"1h","limit":1}`))
	require.False(t, r1.IsError, "first log_query should succeed")
	var result1 integration.LogQueryResult
	require.NoError(t, json.Unmarshal([]byte(r1.Content[0].Text), &result1))
	require.True(t, result1.Truncated, "provider returns truncated")
	require.NotEmpty(t, result1.NextCursor, "truncated result should carry a NextCursor")

	// Second call: pass the cursor.
	args2 := fmt.Sprintf(`{"query":"service:api","since":"1h","cursor":%q}`, result1.NextCursor)
	r2 := tr.CallTool(context.Background(), "log_query", json.RawMessage(args2))
	require.False(t, r2.IsError, "cursor continuation should succeed")

	// Provider should receive a time-bounded request derived from the cursor, not a since-based one.
	require.NotNil(t, provider.lastQuery.StartTime, "cursor continuation should inject StartTime from cursor")
	require.NotNil(t, provider.lastQuery.EndTime, "cursor continuation should inject EndTime from cursor")
	require.Nil(t, provider.lastQuery.Since, "cursor continuation should clear Since")
}

func TestCallLogQueryCursorRejectsConstraintMismatch(t *testing.T) {
	t.Parallel()

	provider := &truncatingLogProvider{name: models.ProviderVictoriaLogs}
	reg := integration.NewRegistry()
	reg.RegisterLogProvider(provider)
	tr := NewToolRegistry(reg)

	// Obtain a valid cursor for query "service:api".
	r1 := tr.CallTool(context.Background(), "log_query", json.RawMessage(`{"query":"service:api","since":"1h"}`))
	require.False(t, r1.IsError)
	var result1 integration.LogQueryResult
	require.NoError(t, json.Unmarshal([]byte(r1.Content[0].Text), &result1))
	require.NotEmpty(t, result1.NextCursor)

	// Pass the cursor with a different query — should be rejected.
	args := fmt.Sprintf(`{"query":"service:worker","since":"1h","cursor":%q}`, result1.NextCursor)
	r2 := tr.CallTool(context.Background(), "log_query", json.RawMessage(args))
	require.True(t, r2.IsError, "cursor with mismatched query should be rejected")
	require.Contains(t, r2.Content[0].Text, "cursor", "error should mention cursor")
}

func TestCallLogContextCapSignsCursors(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	makeEntries := func(n int, startOffset time.Duration) []integration.LogEntry {
		entries := make([]integration.LogEntry, n)
		for i := range entries {
			entries[i] = integration.LogEntry{
				Timestamp: base.Add(startOffset + time.Duration(i)*time.Second),
				Provider:  models.ProviderVictoriaLogs,
				Message:   strings.Repeat("x", 5000), // 5KB per entry → 50 entries = 250KB per side
			}
		}
		return entries
	}

	provider := &oversizedContextProvider{
		name:   models.ProviderVictoriaLogs,
		before: makeEntries(50, -51*time.Second),
		after:  makeEntries(50, time.Second),
	}
	reg := integration.NewRegistry()
	reg.RegisterLogProvider(provider)
	tr := NewToolRegistry(reg)

	args := `{"query":"service:api","since":"1h","timestamp":"2026-05-28T12:00:00Z","before":50,"after":50}`
	result := tr.CallTool(context.Background(), "log_context", json.RawMessage(args))
	require.False(t, result.IsError, "log_context should not error")

	var r integration.LogContextResult
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &r))
	require.NotEmpty(t, r.PrevCursor, "PrevCursor should be signed when Before entries were trimmed")
	require.NotEmpty(t, r.NextCursor, "NextCursor should be signed when After entries were trimmed")
}

func TestCapLogQueryResultTruncates(t *testing.T) {
	t.Parallel()

	entries := make([]integration.LogEntry, 100)
	for i := range entries {
		entries[i] = integration.LogEntry{
			Timestamp: time.Now(),
			Provider:  models.ProviderVictoriaLogs,
			Message:   strings.Repeat("x", 3000), // 3KB each → 300KB total, exceeds 200KB cap
		}
	}
	r := &integration.LogQueryResult{Provider: models.ProviderVictoriaLogs, Entries: entries}

	capLogQueryResult(r, logQueryResponseCap)

	data, _ := json.Marshal(r)
	require.LessOrEqual(t, len(data), logQueryResponseCap, "capped result should fit within cap")
	require.True(t, r.Truncated, "Truncated should be true after cap")
	require.Less(t, len(r.Entries), 100, "entries should be reduced below the original count")
}

func toolNames(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func timeBoundsForTest(since *time.Duration, start *time.Time, end *time.Time) (time.Time, time.Time) {
	if start != nil && end != nil {
		return *start, *end
	}
	if since != nil {
		now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
		return now.Add(-*since), now
	}
	return time.Time{}, time.Time{}
}
