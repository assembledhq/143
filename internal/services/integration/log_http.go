package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
)

const logProviderHTTPBodyLimit = 2 << 20

type VictoriaLogsConfig struct {
	QueryURL          string
	AuthToken         string
	SharedOrgID       string
	HTTPClient        *http.Client
	CursorSigningKey  []byte
	MultiTenantShared bool
}

type VictoriaLogsProvider struct {
	cfg    VictoriaLogsConfig
	client *http.Client
}

func NewVictoriaLogsProvider(cfg VictoriaLogsConfig) *VictoriaLogsProvider {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &VictoriaLogsProvider{cfg: cfg, client: client}
}

func (p *VictoriaLogsProvider) Name() models.ProviderName { return models.ProviderVictoriaLogs }
func (p *VictoriaLogsProvider) SupportsStats() bool       { return true }

func (p *VictoriaLogsProvider) QueryLogs(ctx context.Context, req LogQueryRequest) (*LogQueryResult, error) {
	start, end, err := NormalizeLogTimeBounds(req.Since, req.StartTime, req.EndTime, LogMaxLookback, time.Now())
	if err != nil {
		return nil, err
	}
	limit := intValue(req.Limit, LogDefaultLimit)
	direction := LogDirectionDesc
	if req.Direction != nil {
		direction = *req.Direction
	}
	query := p.scopedQuery(req.Query)
	values := url.Values{}
	values.Set("query", query)
	values.Set("limit", strconv.Itoa(limit+1))
	values.Set("start", start.Format(time.RFC3339Nano))
	values.Set("end", end.Format(time.RFC3339Nano))

	records, err := p.doQuery(ctx, values)
	if err != nil {
		return nil, err
	}
	entries := normalizeLogRecords(models.ProviderVictoriaLogs, records, req.Fields, req.IncludeRaw)
	entries, truncated := trimEntries(entries, limit)
	sortLogEntries(entries, direction)
	return &LogQueryResult{
		Provider:  p.Name(),
		Query:     req.Query,
		StartTime: start,
		EndTime:   end,
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

func (p *VictoriaLogsProvider) GetLogContext(ctx context.Context, req LogContextRequest) (*LogContextResult, error) {
	if req.Anchor.Timestamp == nil {
		return nil, fmt.Errorf("%w: victorialogs requires --timestamp plus --query in v1", ErrLogAnchorInsufficient)
	}
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		return nil, fmt.Errorf("%w: victorialogs requires --query with timestamp context", ErrLogAnchorInsufficient)
	}
	limit := intValue(req.Before, 20) + intValue(req.After, 20) + 1
	queryReq := LogQueryRequest{
		Query:      *req.Query,
		Since:      req.Since,
		StartTime:  req.StartTime,
		EndTime:    req.EndTime,
		Limit:      &limit,
		Direction:  ptr(LogDirectionAsc),
		Fields:     req.Fields,
		IncludeRaw: req.IncludeRaw,
	}
	result, err := p.QueryLogs(ctx, queryReq)
	if err != nil {
		return nil, err
	}
	before, target, after := splitContextEntries(result.Entries, *req.Anchor.Timestamp, intValue(req.Before, 20), intValue(req.After, 20))
	return &LogContextResult{Provider: p.Name(), Anchor: req.Anchor, Before: before, Target: target, After: after}, nil
}

func (p *VictoriaLogsProvider) ListLogFields(ctx context.Context, req LogFieldsRequest) (*LogFieldsResult, error) {
	limit := intValue(req.Limit, 100)
	query := "*"
	if req.Query != nil && strings.TrimSpace(*req.Query) != "" {
		query = *req.Query
	}
	queryReq := LogQueryRequest{Query: query, Since: req.Since, Limit: &limit}
	result, err := p.QueryLogs(ctx, queryReq)
	if err != nil {
		return nil, err
	}
	return &LogFieldsResult{Provider: p.Name(), Fields: collectLogFields(result.Entries, limit)}, nil
}

func (p *VictoriaLogsProvider) QueryLogStats(ctx context.Context, req LogStatsRequest) (*LogStatsResult, error) {
	start, end, err := NormalizeLogTimeBounds(req.Since, req.StartTime, req.EndTime, LogMaxLookback, time.Now())
	if err != nil {
		return nil, err
	}
	query := p.scopedQuery(req.Query)
	if len(req.GroupBy) > 0 {
		query += " | stats by (" + strings.Join(req.GroupBy, ",") + ") count()"
	} else {
		query += " | stats count()"
	}
	values := url.Values{}
	values.Set("query", query)
	values.Set("start", start.Format(time.RFC3339Nano))
	values.Set("end", end.Format(time.RFC3339Nano))
	records, err := p.doQuery(ctx, values)
	if err != nil {
		return nil, err
	}
	return &LogStatsResult{
		Provider:  p.Name(),
		Query:     req.Query,
		StartTime: start,
		EndTime:   end,
		Series:    statsFromRecords(records, req.GroupBy),
	}, nil
}

func (p *VictoriaLogsProvider) scopedQuery(query string) string {
	if p.cfg.MultiTenantShared && p.cfg.SharedOrgID != "" {
		return fmt.Sprintf(`{org_id="%s"} | %s`, strings.ReplaceAll(p.cfg.SharedOrgID, `"`, `\"`), query)
	}
	return query
}

func (p *VictoriaLogsProvider) doQuery(ctx context.Context, values url.Values) ([]map[string]any, error) {
	if strings.TrimSpace(p.cfg.QueryURL) == "" {
		return nil, ErrLogProviderUnconfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.QueryURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.AuthToken)
	}
	return doLogHTTPRequest(p.client, req)
}

type MezmoConfig struct {
	BaseURL    string
	APIKey     string
	Dataset    string
	HTTPClient *http.Client
}

type MezmoProvider struct {
	cfg    MezmoConfig
	client *http.Client
}

func NewMezmoProvider(cfg MezmoConfig) *MezmoProvider {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.mezmo.com"
	}
	return &MezmoProvider{cfg: cfg, client: client}
}

func (p *MezmoProvider) Name() models.ProviderName { return models.ProviderMezmo }
func (p *MezmoProvider) SupportsStats() bool       { return false }

func (p *MezmoProvider) QueryLogs(ctx context.Context, req LogQueryRequest) (*LogQueryResult, error) {
	start, end, err := NormalizeLogTimeBounds(req.Since, req.StartTime, req.EndTime, LogMaxLookback, time.Now())
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey == "" {
		return nil, ErrLogProviderUnconfigured
	}
	limit := intValue(req.Limit, LogDefaultLimit)
	body := map[string]any{
		"query":      req.Query,
		"start_time": start.Format(time.RFC3339Nano),
		"end_time":   end.Format(time.RFC3339Nano),
		"limit":      limit + 1,
	}
	if p.cfg.Dataset != "" {
		body["dataset"] = p.cfg.Dataset
	}
	records, err := p.post(ctx, "/v1/logs/search", body)
	if err != nil {
		return nil, err
	}
	direction := LogDirectionDesc
	if req.Direction != nil {
		direction = *req.Direction
	}
	entries := normalizeLogRecords(models.ProviderMezmo, records, req.Fields, req.IncludeRaw)
	entries, truncated := trimEntries(entries, limit)
	sortLogEntries(entries, direction)
	return &LogQueryResult{Provider: p.Name(), Query: req.Query, StartTime: start, EndTime: end, Entries: entries, Truncated: truncated}, nil
}

func (p *MezmoProvider) GetLogContext(ctx context.Context, req LogContextRequest) (*LogContextResult, error) {
	if req.Anchor.Timestamp == nil || req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		return nil, fmt.Errorf("%w: mezmo requires --timestamp and --query in v1", ErrLogAnchorInsufficient)
	}
	limit := intValue(req.Before, 20) + intValue(req.After, 20) + 1
	queryReq := LogQueryRequest{Query: *req.Query, Since: req.Since, StartTime: req.StartTime, EndTime: req.EndTime, Limit: &limit, Direction: ptr(LogDirectionAsc), Fields: req.Fields, IncludeRaw: req.IncludeRaw}
	result, err := p.QueryLogs(ctx, queryReq)
	if err != nil {
		return nil, err
	}
	before, target, after := splitContextEntries(result.Entries, *req.Anchor.Timestamp, intValue(req.Before, 20), intValue(req.After, 20))
	return &LogContextResult{Provider: p.Name(), Anchor: req.Anchor, Before: before, Target: target, After: after}, nil
}

func (p *MezmoProvider) ListLogFields(ctx context.Context, req LogFieldsRequest) (*LogFieldsResult, error) {
	limit := intValue(req.Limit, 100)
	query := "*"
	if req.Query != nil && strings.TrimSpace(*req.Query) != "" {
		query = *req.Query
	}
	result, err := p.QueryLogs(ctx, LogQueryRequest{Query: query, Since: req.Since, Limit: &limit})
	if err != nil {
		return nil, err
	}
	return &LogFieldsResult{Provider: p.Name(), Fields: collectLogFields(result.Entries, limit)}, nil
}

func (p *MezmoProvider) QueryLogStats(context.Context, LogStatsRequest) (*LogStatsResult, error) {
	return nil, ErrLogStatsUnsupported
}

func (p *MezmoProvider) post(ctx context.Context, path string, body map[string]any) ([]map[string]any, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	return doLogHTTPRequest(p.client, req)
}

func doLogHTTPRequest(client *http.Client, req *http.Request) ([]map[string]any, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrLogProviderUnauthorized
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrLogRateLimited
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, logProviderHTTPBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("log provider returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return parseLogHTTPRecords(body)
}

func parseLogHTTPRecords(body []byte) ([]map[string]any, error) {
	var envelope struct {
		Data    []map[string]any `json:"data"`
		Entries []map[string]any `json:"entries"`
		Logs    []map[string]any `json:"logs"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		switch {
		case envelope.Data != nil:
			return envelope.Data, nil
		case envelope.Entries != nil:
			return envelope.Entries, nil
		case envelope.Logs != nil:
			return envelope.Logs, nil
		}
		var records []map[string]any
		if json.Unmarshal(body, &records) == nil {
			return records, nil
		}
	}
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}
