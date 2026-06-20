package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
)

func databaseToolDefinitions(providers []integration.DatabaseProvider) []Tool {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, string(provider.Name()))
	}
	providerProperty := SchemaProperty{Type: "string", Description: "Database provider to use. Configured providers: " + strings.Join(names, ", "), Enum: names}
	return []Tool{
		{
			Name:        "database_query",
			Description: "Run a bounded read-only SQL query through a configured private database connector.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": providerProperty,
					"query":    {Type: "string", Description: "SQL query to execute in a read-only transaction"},
					"limit":    {Type: "number", Description: "Max rows to return", Default: 100},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "database_schema",
			Description: "Inspect database schemas and table columns through a private database connector.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": providerProperty,
					"schema":   {Type: "string", Description: "Optional schema name to inspect"},
				},
			},
		},
		{
			Name:        "database_explain",
			Description: "Run EXPLAIN for a bounded read-only SQL query through a private database connector.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": providerProperty,
					"query":    {Type: "string", Description: "SQL query to explain"},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "database_indexes",
			Description: "Inspect indexes for configured private database tables.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": providerProperty,
					"schema":   {Type: "string", Description: "Optional schema name"},
					"table":    {Type: "string", Description: "Optional table name"},
				},
			},
		},
		{
			Name:        "database_sample_rows",
			Description: "Read a small sample from an explicitly opted-in private database table.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": providerProperty,
					"schema":   {Type: "string", Description: "Optional schema name"},
					"table":    {Type: "string", Description: "Table name"},
					"limit":    {Type: "number", Description: "Max sample rows", Default: 5},
				},
				Required: []string{"table"},
			},
		},
	}
}

func (tr *ToolRegistry) callDatabaseTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	switch name {
	case "database_query", "database_schema", "database_explain", "database_indexes", "database_sample_rows":
	default:
		return ErrorResult(fmt.Sprintf("unknown database tool: %s", name))
	}
	provider, err := tr.resolveDatabaseProvider(args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	switch name {
	case "database_query":
		return tr.callDatabaseQuery(ctx, provider, args)
	case "database_schema":
		return tr.callDatabaseSchema(ctx, provider, args)
	case "database_explain":
		return tr.callDatabaseExplain(ctx, provider, args)
	case "database_indexes":
		return tr.callDatabaseIndexes(ctx, provider, args)
	case "database_sample_rows":
		return tr.callDatabaseSampleRows(ctx, provider, args)
	default:
		return ErrorResult(fmt.Sprintf("unknown database tool: %s", name))
	}
}

func (tr *ToolRegistry) callDatabaseQuery(ctx context.Context, provider integration.DatabaseProvider, args json.RawMessage) *ToolCallResult {
	var req struct {
		Query string `json:"query"`
		Limit *int   `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	if strings.TrimSpace(req.Query) == "" {
		return ErrorResult("query is required")
	}
	result, err := provider.QueryDatabase(ctx, integration.DatabaseQueryRequest{Query: req.Query, Limit: req.Limit})
	if err != nil {
		return ErrorResult(fmt.Sprintf("database_query failed for %s: %s", provider.Name(), err))
	}
	return jsonResult(result)
}

func (tr *ToolRegistry) callDatabaseSchema(ctx context.Context, provider integration.DatabaseProvider, args json.RawMessage) *ToolCallResult {
	var req struct {
		Schema string `json:"schema,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	result, err := provider.InspectSchema(ctx, integration.DatabaseSchemaRequest{Schema: req.Schema})
	if err != nil {
		return ErrorResult(fmt.Sprintf("database_schema failed for %s: %s", provider.Name(), err))
	}
	return jsonResult(result)
}

func (tr *ToolRegistry) callDatabaseExplain(ctx context.Context, provider integration.DatabaseProvider, args json.RawMessage) *ToolCallResult {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	if strings.TrimSpace(req.Query) == "" {
		return ErrorResult("query is required")
	}
	result, err := provider.ExplainQuery(ctx, integration.DatabaseExplainRequest{Query: req.Query})
	if err != nil {
		return ErrorResult(fmt.Sprintf("database_explain failed for %s: %s", provider.Name(), err))
	}
	return jsonResult(result)
}

func (tr *ToolRegistry) callDatabaseIndexes(ctx context.Context, provider integration.DatabaseProvider, args json.RawMessage) *ToolCallResult {
	var req struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	result, err := provider.InspectIndexes(ctx, integration.DatabaseIndexesRequest{Schema: req.Schema, Table: req.Table})
	if err != nil {
		return ErrorResult(fmt.Sprintf("database_indexes failed for %s: %s", provider.Name(), err))
	}
	return jsonResult(result)
}

func (tr *ToolRegistry) callDatabaseSampleRows(ctx context.Context, provider integration.DatabaseProvider, args json.RawMessage) *ToolCallResult {
	var req struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table"`
		Limit  *int   `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments: %s", err))
	}
	if strings.TrimSpace(req.Table) == "" {
		return ErrorResult("table is required")
	}
	result, err := provider.SampleRows(ctx, integration.DatabaseSampleRowsRequest{Schema: req.Schema, Table: req.Table, Limit: req.Limit})
	if err != nil {
		return ErrorResult(fmt.Sprintf("database_sample_rows failed for %s: %s", provider.Name(), err))
	}
	return jsonResult(result)
}

func (tr *ToolRegistry) resolveDatabaseProvider(args json.RawMessage) (integration.DatabaseProvider, error) {
	var selector struct {
		Provider *string `json:"provider,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &selector); err != nil {
			return nil, err
		}
	}
	providers := tr.integrations.DatabaseProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("database provider not configured")
	}
	if selector.Provider != nil && strings.TrimSpace(*selector.Provider) != "" {
		return tr.integrations.DatabaseProvider(models.ProviderName(strings.TrimSpace(*selector.Provider)))
	}
	if len(providers) == 1 {
		return providers[0], nil
	}
	return nil, fmt.Errorf("database provider is ambiguous; specify provider")
}
