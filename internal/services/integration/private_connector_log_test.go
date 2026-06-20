package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakePrivateConnectorActionDispatcher struct {
	req    PrivateConnectorActionDispatchRequest
	result json.RawMessage
}

func (d *fakePrivateConnectorActionDispatcher) DispatchPrivateConnectorAction(_ context.Context, req PrivateConnectorActionDispatchRequest) (json.RawMessage, error) {
	d.req = req
	return d.result, nil
}

func TestPrivateConnectorLogProvider_QueryLogsDispatchesAction(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	connectorID := uuid.New()
	resourceID := uuid.New()
	payload, err := json.Marshal(LogQueryResult{
		Provider: models.ProviderVictoriaLogs,
		Query:    "service:api",
		Entries: []LogEntry{{
			Timestamp: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
			Message:   "failed",
		}},
	})
	require.NoError(t, err, "test log query payload should marshal")
	dispatcher := &fakePrivateConnectorActionDispatcher{result: payload}
	provider := NewPrivateConnectorLogProvider(PrivateConnectorLogConfig{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceID:  resourceID,
		Provider:    models.ProviderVictoriaLogs,
		Dispatcher:  dispatcher,
	})
	since := time.Hour
	limit := 500
	direction := LogDirectionAsc

	result, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query:     "service:api",
		Since:     &since,
		Limit:     &limit,
		Direction: &direction,
		Fields:    []string{"message", "level"},
	})

	require.NoError(t, err, "QueryLogs should dispatch through the private connector")
	require.Equal(t, models.ProviderVictoriaLogs, provider.Name(), "provider should expose the backing log provider name")
	require.Equal(t, orgID, dispatcher.req.OrgID, "dispatch request should preserve org scope")
	require.Equal(t, connectorID, dispatcher.req.ConnectorID, "dispatch request should preserve connector scope")
	require.Equal(t, resourceID, dispatcher.req.ResourceID, "dispatch request should target the configured private resource")
	require.Equal(t, "victorialogs.query", dispatcher.req.Capability, "QueryLogs should use the VictoriaLogs query capability")

	var params map[string]any
	require.NoError(t, json.Unmarshal(dispatcher.req.Params, &params), "dispatch params should be valid JSON")
	require.Equal(t, "service:api", params["query"], "dispatch params should include the provider-native query")
	require.Equal(t, "1h0m0s", params["since"], "dispatch params should encode bounded lookback")
	require.Equal(t, float64(500), params["limit"], "dispatch params should include the requested limit")
	require.Equal(t, "asc", params["direction"], "dispatch params should include result direction")
	require.Equal(t, []any{"message", "level"}, params["fields"], "dispatch params should include requested fields")
	require.Len(t, result.Entries, 1, "QueryLogs should decode connector payload into log results")
	require.True(t, provider.SupportsStats(), "VictoriaLogs private connector provider should advertise stats support")
}

func TestPrivateConnectorLogProvider_ContextAndStatsDispatchCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		call       func(provider *PrivateConnectorLogProvider) error
		payload    any
		capability string
	}{
		{
			name: "context",
			call: func(provider *PrivateConnectorLogProvider) error {
				timestamp := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
				query := "service:api"
				_, err := provider.GetLogContext(context.Background(), LogContextRequest{
					Anchor: LogAnchor{Timestamp: &timestamp},
					Query:  &query,
				})
				return err
			},
			payload:    LogContextResult{Provider: models.ProviderVictoriaLogs},
			capability: "victorialogs.context",
		},
		{
			name: "fields",
			call: func(provider *PrivateConnectorLogProvider) error {
				_, err := provider.ListLogFields(context.Background(), LogFieldsRequest{})
				return err
			},
			payload:    LogFieldsResult{Provider: models.ProviderVictoriaLogs},
			capability: "victorialogs.fields",
		},
		{
			name: "stats",
			call: func(provider *PrivateConnectorLogProvider) error {
				_, err := provider.QueryLogStats(context.Background(), LogStatsRequest{Query: "service:api"})
				return err
			},
			payload:    LogStatsResult{Provider: models.ProviderVictoriaLogs},
			capability: "victorialogs.stats",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload, err := json.Marshal(tt.payload)
			require.NoError(t, err, "test connector action payload should marshal")
			dispatcher := &fakePrivateConnectorActionDispatcher{result: payload}
			provider := NewPrivateConnectorLogProvider(PrivateConnectorLogConfig{
				OrgID:       uuid.New(),
				ConnectorID: uuid.New(),
				ResourceID:  uuid.New(),
				Provider:    models.ProviderVictoriaLogs,
				Dispatcher:  dispatcher,
			})

			require.NoError(t, tt.call(provider), "provider method should dispatch successfully")
			require.Equal(t, tt.capability, dispatcher.req.Capability, "provider method should dispatch the expected connector capability")
		})
	}
}
