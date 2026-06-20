package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/stretchr/testify/require"
)

func TestDatabaseQueryTool(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	provider := &mcpDatabaseProvider{name: models.ProviderPostgres}
	reg.RegisterDatabaseProvider(provider)
	tools := NewToolRegistry(reg)

	names := make([]string, 0)
	for _, tool := range tools.ListTools() {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "database_query", "ListTools should expose database query when a database provider is registered")

	result := tools.CallTool(context.Background(), "database_query", json.RawMessage(`{"provider":"postgres","query":"SELECT 1","limit":1}`))

	require.False(t, result.IsError, "database_query should succeed")
	require.Equal(t, "SELECT 1", provider.req.Query, "database_query should pass query text")
	require.NotNil(t, provider.req.Limit, "database_query should pass limit")
	require.Equal(t, 1, *provider.req.Limit, "database_query should pass requested limit")
	require.Contains(t, result.Content[0].Text, `"provider": "postgres"`, "database_query should return provider result JSON")
}

func TestDatabaseReadOnlyInspectionTools(t *testing.T) {
	t.Parallel()

	reg := integration.NewRegistry()
	provider := &mcpDatabaseProvider{name: models.ProviderPostgres}
	reg.RegisterDatabaseProvider(provider)
	tools := NewToolRegistry(reg)

	names := make([]string, 0)
	for _, tool := range tools.ListTools() {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "database_schema", "ListTools should expose schema inspection when a database provider is registered")
	require.Contains(t, names, "database_explain", "ListTools should expose explain when a database provider is registered")
	require.Contains(t, names, "database_indexes", "ListTools should expose index inspection when a database provider is registered")
	require.Contains(t, names, "database_sample_rows", "ListTools should expose explicitly opted-in sample rows when a database provider is registered")

	schemaResult := tools.CallTool(context.Background(), "database_schema", json.RawMessage(`{"provider":"postgres","schema":"public"}`))
	require.False(t, schemaResult.IsError, "database_schema should succeed")
	require.Equal(t, "public", provider.schemaReq.Schema, "database_schema should pass schema filter")

	explainResult := tools.CallTool(context.Background(), "database_explain", json.RawMessage(`{"provider":"postgres","query":"SELECT * FROM users"}`))
	require.False(t, explainResult.IsError, "database_explain should succeed")
	require.Equal(t, "SELECT * FROM users", provider.explainReq.Query, "database_explain should pass query text")

	indexesResult := tools.CallTool(context.Background(), "database_indexes", json.RawMessage(`{"provider":"postgres","schema":"public","table":"users"}`))
	require.False(t, indexesResult.IsError, "database_indexes should succeed")
	require.Equal(t, "public", provider.indexesReq.Schema, "database_indexes should pass schema")
	require.Equal(t, "users", provider.indexesReq.Table, "database_indexes should pass table")

	sampleRowsResult := tools.CallTool(context.Background(), "database_sample_rows", json.RawMessage(`{"provider":"postgres","schema":"public","table":"users","limit":5}`))
	require.False(t, sampleRowsResult.IsError, "database_sample_rows should succeed when provider allows it")
	require.Equal(t, "public", provider.sampleRowsReq.Schema, "database_sample_rows should pass schema")
	require.Equal(t, "users", provider.sampleRowsReq.Table, "database_sample_rows should pass table")
	require.NotNil(t, provider.sampleRowsReq.Limit, "database_sample_rows should pass limit")
	require.Equal(t, 5, *provider.sampleRowsReq.Limit, "database_sample_rows should pass requested limit")
}

type mcpDatabaseProvider struct {
	name          models.ProviderName
	req           integration.DatabaseQueryRequest
	schemaReq     integration.DatabaseSchemaRequest
	explainReq    integration.DatabaseExplainRequest
	indexesReq    integration.DatabaseIndexesRequest
	sampleRowsReq integration.DatabaseSampleRowsRequest
}

func (p *mcpDatabaseProvider) Name() models.ProviderName { return p.name }

func (p *mcpDatabaseProvider) QueryDatabase(_ context.Context, req integration.DatabaseQueryRequest) (*integration.DatabaseQueryResult, error) {
	p.req = req
	return &integration.DatabaseQueryResult{
		Provider: p.name,
		Columns:  []string{"?column?"},
		Rows:     []map[string]any{{"?column?": 1}},
	}, nil
}

func (p *mcpDatabaseProvider) InspectSchema(_ context.Context, req integration.DatabaseSchemaRequest) (*integration.DatabaseSchemaResult, error) {
	p.schemaReq = req
	return &integration.DatabaseSchemaResult{
		Provider: p.name,
		Tables: []integration.DatabaseTableSchema{{
			Schema: "public",
			Name:   "users",
			Columns: []integration.DatabaseColumnSchema{{
				Name:     "id",
				DataType: "uuid",
			}},
		}},
	}, nil
}

func (p *mcpDatabaseProvider) ExplainQuery(_ context.Context, req integration.DatabaseExplainRequest) (*integration.DatabaseExplainResult, error) {
	p.explainReq = req
	return &integration.DatabaseExplainResult{Provider: p.name, Plan: json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan"}}]`)}, nil
}

func (p *mcpDatabaseProvider) InspectIndexes(_ context.Context, req integration.DatabaseIndexesRequest) (*integration.DatabaseIndexesResult, error) {
	p.indexesReq = req
	return &integration.DatabaseIndexesResult{
		Provider: p.name,
		Indexes: []integration.DatabaseIndex{{
			Schema: "public",
			Table:  "users",
			Name:   "users_pkey",
		}},
	}, nil
}

func (p *mcpDatabaseProvider) SampleRows(_ context.Context, req integration.DatabaseSampleRowsRequest) (*integration.DatabaseQueryResult, error) {
	p.sampleRowsReq = req
	return &integration.DatabaseQueryResult{
		Provider: p.name,
		Columns:  []string{"id"},
		Rows:     []map[string]any{{"id": "user-1"}},
	}, nil
}
