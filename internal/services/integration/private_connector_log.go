package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const (
	privateConnectorVictoriaLogsQueryCapability   = "victorialogs.query"
	privateConnectorVictoriaLogsContextCapability = "victorialogs.context"
	privateConnectorVictoriaLogsFieldsCapability  = "victorialogs.fields"
	privateConnectorVictoriaLogsStatsCapability   = "victorialogs.stats"
)

var ErrPrivateConnectorDispatcherMissing = errors.New("private connector dispatcher is not configured")

type PrivateConnectorActionDispatchRequest struct {
	OrgID       uuid.UUID       `json:"org_id"`
	ConnectorID uuid.UUID       `json:"connector_id"`
	ResourceID  uuid.UUID       `json:"resource_id"`
	Capability  string          `json:"capability"`
	Params      json.RawMessage `json:"params,omitempty"`
}

type PrivateConnectorActionDispatcher interface {
	DispatchPrivateConnectorAction(ctx context.Context, req PrivateConnectorActionDispatchRequest) (json.RawMessage, error)
}

type PrivateConnectorLogConfig struct {
	OrgID       uuid.UUID
	ConnectorID uuid.UUID
	ResourceID  uuid.UUID
	Provider    models.ProviderName
	Dispatcher  PrivateConnectorActionDispatcher
}

type PrivateConnectorLogProvider struct {
	cfg PrivateConnectorLogConfig
}

func NewPrivateConnectorLogProvider(cfg PrivateConnectorLogConfig) *PrivateConnectorLogProvider {
	if cfg.Provider == "" {
		cfg.Provider = models.ProviderVictoriaLogs
	}
	return &PrivateConnectorLogProvider{cfg: cfg}
}

func (p *PrivateConnectorLogProvider) Name() models.ProviderName { return p.cfg.Provider }

func (p *PrivateConnectorLogProvider) SupportsStats() bool {
	return p.cfg.Provider == models.ProviderVictoriaLogs
}

func (p *PrivateConnectorLogProvider) QueryLogs(ctx context.Context, req LogQueryRequest) (*LogQueryResult, error) {
	var params struct {
		Query      string   `json:"query"`
		Since      string   `json:"since,omitempty"`
		StartTime  string   `json:"start_time,omitempty"`
		EndTime    string   `json:"end_time,omitempty"`
		Limit      *int     `json:"limit,omitempty"`
		Direction  string   `json:"direction,omitempty"`
		Fields     []string `json:"fields,omitempty"`
		Cursor     *string  `json:"cursor,omitempty"`
		IncludeRaw *bool    `json:"include_raw,omitempty"`
	}
	params.Query = req.Query
	params.Since = durationString(req.Since)
	params.StartTime = timeString(req.StartTime)
	params.EndTime = timeString(req.EndTime)
	params.Limit = req.Limit
	if req.Direction != nil {
		params.Direction = string(*req.Direction)
	}
	params.Fields = req.Fields
	params.Cursor = req.Cursor
	params.IncludeRaw = req.IncludeRaw

	var result LogQueryResult
	if err := p.dispatch(ctx, privateConnectorVictoriaLogsQueryCapability, params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (p *PrivateConnectorLogProvider) GetLogContext(ctx context.Context, req LogContextRequest) (*LogContextResult, error) {
	var params struct {
		ID         *string  `json:"id,omitempty"`
		Cursor     *string  `json:"cursor,omitempty"`
		Timestamp  string   `json:"timestamp,omitempty"`
		Query      *string  `json:"query,omitempty"`
		Since      string   `json:"since,omitempty"`
		StartTime  string   `json:"start_time,omitempty"`
		EndTime    string   `json:"end_time,omitempty"`
		Before     *int     `json:"before,omitempty"`
		After      *int     `json:"after,omitempty"`
		Fields     []string `json:"fields,omitempty"`
		IncludeRaw *bool    `json:"include_raw,omitempty"`
	}
	params.ID = req.Anchor.ID
	params.Cursor = req.Anchor.Cursor
	params.Timestamp = timeString(req.Anchor.Timestamp)
	params.Query = req.Query
	params.Since = durationString(req.Since)
	params.StartTime = timeString(req.StartTime)
	params.EndTime = timeString(req.EndTime)
	params.Before = req.Before
	params.After = req.After
	params.Fields = req.Fields
	params.IncludeRaw = req.IncludeRaw

	var result LogContextResult
	if err := p.dispatch(ctx, privateConnectorVictoriaLogsContextCapability, params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (p *PrivateConnectorLogProvider) ListLogFields(ctx context.Context, req LogFieldsRequest) (*LogFieldsResult, error) {
	var params struct {
		Query *string `json:"query,omitempty"`
		Since string  `json:"since,omitempty"`
		Limit *int    `json:"limit,omitempty"`
	}
	params.Query = req.Query
	params.Since = durationString(req.Since)
	params.Limit = req.Limit

	var result LogFieldsResult
	if err := p.dispatch(ctx, privateConnectorVictoriaLogsFieldsCapability, params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (p *PrivateConnectorLogProvider) QueryLogStats(ctx context.Context, req LogStatsRequest) (*LogStatsResult, error) {
	var params struct {
		Query     string   `json:"query"`
		Since     string   `json:"since,omitempty"`
		StartTime string   `json:"start_time,omitempty"`
		EndTime   string   `json:"end_time,omitempty"`
		GroupBy   []string `json:"group_by,omitempty"`
		Interval  string   `json:"interval,omitempty"`
		Limit     *int     `json:"limit,omitempty"`
	}
	params.Query = req.Query
	params.Since = durationString(req.Since)
	params.StartTime = timeString(req.StartTime)
	params.EndTime = timeString(req.EndTime)
	params.GroupBy = req.GroupBy
	params.Interval = durationString(req.Interval)
	params.Limit = req.Limit

	var result LogStatsResult
	if err := p.dispatch(ctx, privateConnectorVictoriaLogsStatsCapability, params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (p *PrivateConnectorLogProvider) dispatch(ctx context.Context, capability string, params any, out any) error {
	if p.cfg.Dispatcher == nil {
		return ErrPrivateConnectorDispatcherMissing
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal private connector action params: %w", err)
	}
	payload, err := p.cfg.Dispatcher.DispatchPrivateConnectorAction(ctx, PrivateConnectorActionDispatchRequest{
		OrgID:       p.cfg.OrgID,
		ConnectorID: p.cfg.ConnectorID,
		ResourceID:  p.cfg.ResourceID,
		Capability:  capability,
		Params:      rawParams,
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode private connector action result: %w", err)
	}
	return nil
}

func durationString(value *time.Duration) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func timeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
