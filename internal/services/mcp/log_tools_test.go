package mcp

import (
	"context"
	"encoding/json"
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
