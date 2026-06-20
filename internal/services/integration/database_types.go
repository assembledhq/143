package integration

import (
	"context"
	"encoding/json"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type DatabaseProvider interface {
	Name() models.ProviderName
	QueryDatabase(ctx context.Context, req DatabaseQueryRequest) (*DatabaseQueryResult, error)
	InspectSchema(ctx context.Context, req DatabaseSchemaRequest) (*DatabaseSchemaResult, error)
	ExplainQuery(ctx context.Context, req DatabaseExplainRequest) (*DatabaseExplainResult, error)
	InspectIndexes(ctx context.Context, req DatabaseIndexesRequest) (*DatabaseIndexesResult, error)
	SampleRows(ctx context.Context, req DatabaseSampleRowsRequest) (*DatabaseQueryResult, error)
}

type DatabaseQueryRequest struct {
	Query string
	Limit *int
}

type DatabaseQueryResult struct {
	Provider  models.ProviderName `json:"provider"`
	Columns   []string            `json:"columns"`
	Rows      []map[string]any    `json:"rows"`
	Truncated bool                `json:"truncated"`
}

type DatabaseSchemaRequest struct {
	Schema string
}

type DatabaseSchemaResult struct {
	Provider models.ProviderName   `json:"provider"`
	Tables   []DatabaseTableSchema `json:"tables"`
}

type DatabaseTableSchema struct {
	Schema  string                 `json:"schema"`
	Name    string                 `json:"name"`
	Columns []DatabaseColumnSchema `json:"columns"`
}

type DatabaseColumnSchema struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

type DatabaseExplainRequest struct {
	Query string
}

type DatabaseExplainResult struct {
	Provider models.ProviderName `json:"provider"`
	Plan     json.RawMessage     `json:"plan"`
}

type DatabaseIndexesRequest struct {
	Schema string
	Table  string
}

type DatabaseIndexesResult struct {
	Provider models.ProviderName `json:"provider"`
	Indexes  []DatabaseIndex     `json:"indexes"`
}

type DatabaseIndex struct {
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty"`
}

type DatabaseSampleRowsRequest struct {
	Schema string
	Table  string
	Limit  *int
}

type PrivateConnectorDatabaseConfig struct {
	OrgID       uuid.UUID
	ConnectorID uuid.UUID
	ResourceID  uuid.UUID
	Provider    models.ProviderName
	Dispatcher  PrivateConnectorActionDispatcher
}

type PrivateConnectorDatabaseProvider struct {
	cfg PrivateConnectorDatabaseConfig
}

func NewPrivateConnectorDatabaseProvider(cfg PrivateConnectorDatabaseConfig) *PrivateConnectorDatabaseProvider {
	if cfg.Provider == "" {
		cfg.Provider = models.ProviderPostgres
	}
	return &PrivateConnectorDatabaseProvider{cfg: cfg}
}

func (p *PrivateConnectorDatabaseProvider) Name() models.ProviderName { return p.cfg.Provider }

func (p *PrivateConnectorDatabaseProvider) QueryDatabase(ctx context.Context, req DatabaseQueryRequest) (*DatabaseQueryResult, error) {
	if p.cfg.Dispatcher == nil {
		return nil, ErrPrivateConnectorDispatcherMissing
	}
	params, err := json.Marshal(struct {
		Query string `json:"query"`
		Limit *int   `json:"limit,omitempty"`
	}{Query: req.Query, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	payload, err := p.cfg.Dispatcher.DispatchPrivateConnectorAction(ctx, PrivateConnectorActionDispatchRequest{
		OrgID:       p.cfg.OrgID,
		ConnectorID: p.cfg.ConnectorID,
		ResourceID:  p.cfg.ResourceID,
		Capability:  "postgres.query",
		Params:      params,
	})
	if err != nil {
		return nil, err
	}
	var result DatabaseQueryResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	result.Provider = p.cfg.Provider
	return &result, nil
}

func (p *PrivateConnectorDatabaseProvider) InspectSchema(ctx context.Context, req DatabaseSchemaRequest) (*DatabaseSchemaResult, error) {
	payload, err := p.dispatch(ctx, "postgres.schema", struct {
		Schema string `json:"schema,omitempty"`
	}{Schema: req.Schema})
	if err != nil {
		return nil, err
	}
	var result DatabaseSchemaResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	result.Provider = p.cfg.Provider
	return &result, nil
}

func (p *PrivateConnectorDatabaseProvider) ExplainQuery(ctx context.Context, req DatabaseExplainRequest) (*DatabaseExplainResult, error) {
	payload, err := p.dispatch(ctx, "postgres.explain", struct {
		Query string `json:"query"`
	}{Query: req.Query})
	if err != nil {
		return nil, err
	}
	var result DatabaseExplainResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	result.Provider = p.cfg.Provider
	return &result, nil
}

func (p *PrivateConnectorDatabaseProvider) InspectIndexes(ctx context.Context, req DatabaseIndexesRequest) (*DatabaseIndexesResult, error) {
	payload, err := p.dispatch(ctx, "postgres.indexes", struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table,omitempty"`
	}{Schema: req.Schema, Table: req.Table})
	if err != nil {
		return nil, err
	}
	var result DatabaseIndexesResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	result.Provider = p.cfg.Provider
	return &result, nil
}

func (p *PrivateConnectorDatabaseProvider) SampleRows(ctx context.Context, req DatabaseSampleRowsRequest) (*DatabaseQueryResult, error) {
	payload, err := p.dispatch(ctx, "postgres.sample_rows", struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table"`
		Limit  *int   `json:"limit,omitempty"`
	}{Schema: req.Schema, Table: req.Table, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	var result DatabaseQueryResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	result.Provider = p.cfg.Provider
	return &result, nil
}

func (p *PrivateConnectorDatabaseProvider) dispatch(ctx context.Context, capability string, params any) (json.RawMessage, error) {
	if p.cfg.Dispatcher == nil {
		return nil, ErrPrivateConnectorDispatcherMissing
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return p.cfg.Dispatcher.DispatchPrivateConnectorAction(ctx, PrivateConnectorActionDispatchRequest{
		OrgID:       p.cfg.OrgID,
		ConnectorID: p.cfg.ConnectorID,
		ResourceID:  p.cfg.ResourceID,
		Capability:  capability,
		Params:      raw,
	})
}
