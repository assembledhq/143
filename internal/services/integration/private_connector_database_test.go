package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrivateConnectorDatabaseProviderDispatchesReadOnlyCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		call       func(provider *PrivateConnectorDatabaseProvider) error
		payload    any
		capability string
	}{
		{
			name: "query",
			call: func(provider *PrivateConnectorDatabaseProvider) error {
				limit := 10
				_, err := provider.QueryDatabase(context.Background(), DatabaseQueryRequest{Query: "SELECT 1", Limit: &limit})
				return err
			},
			payload:    DatabaseQueryResult{Columns: []string{"?column?"}, Rows: []map[string]any{{"?column?": 1}}},
			capability: "postgres.query",
		},
		{
			name: "schema",
			call: func(provider *PrivateConnectorDatabaseProvider) error {
				_, err := provider.InspectSchema(context.Background(), DatabaseSchemaRequest{Schema: "public"})
				return err
			},
			payload:    DatabaseSchemaResult{Tables: []DatabaseTableSchema{{Schema: "public", Name: "users"}}},
			capability: "postgres.schema",
		},
		{
			name: "explain",
			call: func(provider *PrivateConnectorDatabaseProvider) error {
				_, err := provider.ExplainQuery(context.Background(), DatabaseExplainRequest{Query: "SELECT 1"})
				return err
			},
			payload:    DatabaseExplainResult{Plan: json.RawMessage(`[{"Plan":{"Node Type":"Result"}}]`)},
			capability: "postgres.explain",
		},
		{
			name: "indexes",
			call: func(provider *PrivateConnectorDatabaseProvider) error {
				_, err := provider.InspectIndexes(context.Background(), DatabaseIndexesRequest{Schema: "public", Table: "users"})
				return err
			},
			payload:    DatabaseIndexesResult{Indexes: []DatabaseIndex{{Schema: "public", Table: "users", Name: "users_pkey"}}},
			capability: "postgres.indexes",
		},
		{
			name: "sample_rows",
			call: func(provider *PrivateConnectorDatabaseProvider) error {
				limit := 5
				_, err := provider.SampleRows(context.Background(), DatabaseSampleRowsRequest{Schema: "public", Table: "users", Limit: &limit})
				return err
			},
			payload:    DatabaseQueryResult{Columns: []string{"id"}, Rows: []map[string]any{{"id": "user-1"}}},
			capability: "postgres.sample_rows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload, err := json.Marshal(tt.payload)
			require.NoError(t, err, "test connector action payload should marshal")
			dispatcher := &fakePrivateConnectorActionDispatcher{result: payload}
			provider := NewPrivateConnectorDatabaseProvider(PrivateConnectorDatabaseConfig{
				OrgID:       uuid.New(),
				ConnectorID: uuid.New(),
				ResourceID:  uuid.New(),
				Provider:    models.ProviderPostgres,
				Dispatcher:  dispatcher,
			})

			require.NoError(t, tt.call(provider), "provider method should dispatch successfully")
			require.Equal(t, tt.capability, dispatcher.req.Capability, "provider method should dispatch the expected connector capability")
		})
	}
}
