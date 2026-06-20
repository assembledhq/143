package connector

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct {
	capabilities []string
	result       ActionResult
	req          ActionRequest
}

func (p *fakeProvider) Name() string           { return "fake" }
func (p *fakeProvider) Version() string        { return "v0.1.0" }
func (p *fakeProvider) Capabilities() []string { return p.capabilities }
func (p *fakeProvider) HandleAction(_ context.Context, req ActionRequest) (ActionResult, error) {
	p.req = req
	return p.result, nil
}

func TestProviderRegistryDispatchesByCapability(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{
		capabilities: []string{"victorialogs.query"},
		result:       ActionResult{Metadata: ActionMetadata{ResultCount: 2}},
	}
	registry := NewProviderRegistry()
	require.NoError(t, registry.Register(provider), "Register should accept a provider with capabilities")
	req := ActionRequest{Capability: "victorialogs.query", ResourceID: uuid.New()}

	result, err := registry.Dispatch(context.Background(), req)
	require.NoError(t, err, "Dispatch should route supported capabilities")
	require.Equal(t, req.ResourceID, provider.req.ResourceID, "Dispatch should pass action request to provider")
	require.Equal(t, 2, result.Metadata.ResultCount, "Dispatch should return provider result metadata")

	_, err = registry.Dispatch(context.Background(), ActionRequest{Capability: "postgres.read_query"})
	require.ErrorIs(t, err, ErrCapabilityUnsupported, "Dispatch should reject unsupported capabilities")
}

type fakeResourceProvider struct {
	resourceID uuid.UUID
	result     ActionResult
	req        ActionRequest
}

func (p *fakeResourceProvider) Name() string           { return "fake-resource" }
func (p *fakeResourceProvider) Version() string        { return "v0.1.0" }
func (p *fakeResourceProvider) Capabilities() []string { return []string{"victorialogs.query"} }
func (p *fakeResourceProvider) HandleAction(_ context.Context, req ActionRequest) (ActionResult, error) {
	if req.ResourceID != p.resourceID {
		return ActionResult{}, ErrResourceUnauthorized
	}
	p.req = req
	return p.result, nil
}

func TestProviderRegistryDispatchesSameCapabilityByResource(t *testing.T) {
	t.Parallel()

	firstResourceID := uuid.New()
	secondResourceID := uuid.New()
	first := &fakeResourceProvider{
		resourceID: firstResourceID,
		result:     ActionResult{Metadata: ActionMetadata{ResultCount: 1}},
	}
	second := &fakeResourceProvider{
		resourceID: secondResourceID,
		result:     ActionResult{Metadata: ActionMetadata{ResultCount: 2}},
	}
	registry := NewProviderRegistry()
	require.NoError(t, registry.Register(first), "Register should accept first provider for capability")
	require.NoError(t, registry.Register(second), "Register should accept second provider for the same capability")

	firstResult, err := registry.Dispatch(context.Background(), ActionRequest{
		ResourceID: firstResourceID,
		Capability: "victorialogs.query",
	})
	require.NoError(t, err, "Dispatch should route first resource to its provider")
	require.Equal(t, 1, firstResult.Metadata.ResultCount, "Dispatch should return first provider result")
	require.Equal(t, firstResourceID, first.req.ResourceID, "Dispatch should call first provider for first resource")

	secondResult, err := registry.Dispatch(context.Background(), ActionRequest{
		ResourceID: secondResourceID,
		Capability: "victorialogs.query",
	})
	require.NoError(t, err, "Dispatch should route second resource to its provider")
	require.Equal(t, 2, secondResult.Metadata.ResultCount, "Dispatch should return second provider result")
	require.Equal(t, secondResourceID, second.req.ResourceID, "Dispatch should call second provider for second resource")
}

type fakeLogProvider struct {
	queryReq    integration.LogQueryRequest
	statsReq    integration.LogStatsRequest
	result      *integration.LogQueryResult
	statsResult *integration.LogStatsResult
}

func (p *fakeLogProvider) Name() models.ProviderName { return models.ProviderVictoriaLogs }
func (p *fakeLogProvider) QueryLogs(_ context.Context, req integration.LogQueryRequest) (*integration.LogQueryResult, error) {
	p.queryReq = req
	return p.result, nil
}
func (p *fakeLogProvider) GetLogContext(_ context.Context, req integration.LogContextRequest) (*integration.LogContextResult, error) {
	return &integration.LogContextResult{Provider: models.ProviderVictoriaLogs, Anchor: req.Anchor}, nil
}
func (p *fakeLogProvider) ListLogFields(_ context.Context, _ integration.LogFieldsRequest) (*integration.LogFieldsResult, error) {
	return &integration.LogFieldsResult{Provider: models.ProviderVictoriaLogs}, nil
}
func (p *fakeLogProvider) QueryLogStats(_ context.Context, req integration.LogStatsRequest) (*integration.LogStatsResult, error) {
	p.statsReq = req
	if p.statsResult != nil {
		return p.statsResult, nil
	}
	return &integration.LogStatsResult{Provider: models.ProviderVictoriaLogs}, nil
}

func TestVictoriaLogsProviderEnforcesPolicy(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	logs := &fakeLogProvider{
		result: &integration.LogQueryResult{
			Provider:  models.ProviderVictoriaLogs,
			Query:     "service:api",
			StartTime: time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
			Entries: []integration.LogEntry{{
				Timestamp: time.Date(2026, 6, 19, 11, 30, 0, 0, time.UTC),
				Message:   "failed",
				Fields: map[string]any{
					"authorization": "Bearer secret",
					"service":       "api",
				},
			}},
		},
	}
	provider := NewVictoriaLogsProvider(resourceID, logs, VictoriaLogsPolicy{
		MaxRows:      50,
		RedactFields: []string{"authorization"},
	})
	params, err := json.Marshal(map[string]any{
		"query": "service:api",
		"since": "1h",
		"limit": 500,
	})
	require.NoError(t, err, "test params should marshal")

	result, err := provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "victorialogs.query",
		Params:     params,
	})
	require.NoError(t, err, "VictoriaLogs provider should handle query actions")
	require.NotNil(t, logs.queryReq.Limit, "VictoriaLogs provider should pass an explicit limit")
	require.Equal(t, 50, *logs.queryReq.Limit, "VictoriaLogs provider should cap requested rows to policy")
	require.Equal(t, 1, result.Metadata.ResultCount, "VictoriaLogs provider should report result count")

	var payload integration.LogQueryResult
	require.NoError(t, json.Unmarshal(result.Payload, &payload), "VictoriaLogs query payload should be the normalized log result")
	require.Equal(t, "[REDACTED]", payload.Entries[0].Fields["authorization"], "VictoriaLogs provider should redact configured fields")
	require.Equal(t, "api", payload.Entries[0].Fields["service"], "VictoriaLogs provider should preserve safe fields")

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: uuid.New(),
		Capability: "victorialogs.query",
		Params:     params,
	})
	require.ErrorIs(t, err, ErrResourceUnauthorized, "VictoriaLogs provider should reject unknown resources")
}

func TestVictoriaLogsProviderAppliesDefaultFilterAndFieldPolicy(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	logs := &fakeLogProvider{result: &integration.LogQueryResult{Provider: models.ProviderVictoriaLogs}}
	provider := NewVictoriaLogsProvider(resourceID, logs, VictoriaLogsPolicy{
		MaxRows:       50,
		DefaultFilter: "environment:production",
		AllowedFields: []string{"service", "level"},
		DeniedFields:  []string{"authorization"},
	})
	params, err := json.Marshal(map[string]any{
		"query":  "service:api",
		"since":  "15m",
		"fields": []string{"service", "authorization", "trace_id"},
	})
	require.NoError(t, err, "test params should marshal")

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "victorialogs.query",
		Params:     params,
	})

	require.NoError(t, err, "VictoriaLogs provider should accept query inside policy")
	require.Equal(t, "(environment:production) AND (service:api)", logs.queryReq.Query, "VictoriaLogs provider should combine default and requested filters")
	require.Equal(t, []string{"service"}, logs.queryReq.Fields, "VictoriaLogs provider should enforce allowed and denied fields")
}

func TestVictoriaLogsProviderRejectsQueriesBeyondMaxWindow(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	provider := NewVictoriaLogsProvider(resourceID, &fakeLogProvider{}, VictoriaLogsPolicy{
		MaxRows:        50,
		MaxQueryWindow: time.Hour,
	})
	params, err := json.Marshal(map[string]any{"query": "service:api", "since": "2h"})
	require.NoError(t, err, "test params should marshal")

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "victorialogs.query",
		Params:     params,
	})

	require.ErrorIs(t, err, ErrLogQueryDenied, "VictoriaLogs provider should reject windows beyond policy")
}

func TestVictoriaLogsProviderRejectsStatsBeyondCardinalityPolicy(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	logs := &fakeLogProvider{statsResult: &integration.LogStatsResult{
		Provider: models.ProviderVictoriaLogs,
		Series: []integration.LogStatsSeries{
			{Group: map[string]string{"service": "api"}},
			{Group: map[string]string{"service": "worker"}},
		},
	}}
	provider := NewVictoriaLogsProvider(resourceID, logs, VictoriaLogsPolicy{
		MaxRows:              50,
		MaxSeriesCardinality: 1,
	})
	params, err := json.Marshal(map[string]any{"query": "service:*", "since": "15m"})
	require.NoError(t, err, "test params should marshal")

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "victorialogs.stats",
		Params:     params,
	})

	require.ErrorIs(t, err, ErrLogQueryDenied, "VictoriaLogs provider should reject stats cardinality beyond policy")
}
