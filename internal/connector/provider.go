package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
)

var (
	ErrCapabilityUnsupported = errors.New("connector capability unsupported")
	ErrResourceUnauthorized  = errors.New("connector resource unauthorized")
	ErrLogQueryDenied        = errors.New("log query denied")
)

type ActionMetadata struct {
	ResultCount int `json:"result_count,omitempty"`
	FieldCount  int `json:"field_count,omitempty"`
	DurationMs  int `json:"duration_ms,omitempty"`
}

type ActionResult struct {
	Payload  json.RawMessage `json:"payload,omitempty"`
	Metadata ActionMetadata  `json:"metadata"`
}

type Provider interface {
	Name() string
	Version() string
	Capabilities() []string
	HandleAction(ctx context.Context, req ActionRequest) (ActionResult, error)
}

type ProviderRegistry struct {
	mu           sync.RWMutex
	byCapability map[string][]Provider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{byCapability: make(map[string][]Provider)}
}

func (r *ProviderRegistry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("%w: provider is nil", ErrCapabilityUnsupported)
	}
	capabilities := provider.Capabilities()
	if len(capabilities) == 0 {
		return fmt.Errorf("%w: provider %s has no capabilities", ErrCapabilityUnsupported, provider.Name())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		r.byCapability[capability] = append(r.byCapability[capability], provider)
	}
	return nil
}

func (r *ProviderRegistry) Dispatch(ctx context.Context, req ActionRequest) (ActionResult, error) {
	r.mu.RLock()
	providers := append([]Provider(nil), r.byCapability[req.Capability]...)
	r.mu.RUnlock()
	if len(providers) == 0 {
		return ActionResult{}, ErrCapabilityUnsupported
	}
	var unauthorized error
	for _, provider := range providers {
		result, err := provider.HandleAction(ctx, req)
		if errors.Is(err, ErrResourceUnauthorized) {
			unauthorized = err
			continue
		}
		return result, err
	}
	if unauthorized != nil {
		return ActionResult{}, unauthorized
	}
	return ActionResult{}, ErrCapabilityUnsupported
}

func (r *ProviderRegistry) ReplaceWith(next *ProviderRegistry) {
	replacement := make(map[string][]Provider)
	if next != nil {
		next.mu.RLock()
		for capability, providers := range next.byCapability {
			replacement[capability] = append([]Provider(nil), providers...)
		}
		next.mu.RUnlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byCapability = replacement
}

type VictoriaLogsPolicy struct {
	MaxRows              int
	MaxQueryWindow       time.Duration
	MaxSeriesCardinality int
	MaxRequestsPerMinute int
	DefaultFilter        string
	RedactFields         []string
	AllowedFields        []string
	DeniedFields         []string
}

type VictoriaLogsProvider struct {
	resourceID uuid.UUID
	logs       integration.LogProvider
	policy     VictoriaLogsPolicy
	mu         sync.Mutex
	window     time.Time
	requests   int
}

func NewVictoriaLogsProvider(resourceID uuid.UUID, logs integration.LogProvider, policy VictoriaLogsPolicy) *VictoriaLogsProvider {
	if policy.MaxRows <= 0 {
		policy.MaxRows = integration.LogDefaultLimit
	}
	return &VictoriaLogsProvider{resourceID: resourceID, logs: logs, policy: policy}
}

func (p *VictoriaLogsProvider) Name() string    { return "victorialogs" }
func (p *VictoriaLogsProvider) Version() string { return "v1" }
func (p *VictoriaLogsProvider) Capabilities() []string {
	return []string{"victorialogs.query", "victorialogs.context", "victorialogs.fields", "victorialogs.stats"}
}

func (p *VictoriaLogsProvider) HandleAction(ctx context.Context, req ActionRequest) (ActionResult, error) {
	if req.ResourceID != p.resourceID {
		return ActionResult{}, ErrResourceUnauthorized
	}
	if err := p.checkRateLimit(time.Now().UTC()); err != nil {
		return ActionResult{}, err
	}
	start := time.Now()
	switch req.Capability {
	case "victorialogs.query":
		queryReq, err := parseVictoriaLogsQueryParams(req.Params, p.policy)
		if err != nil {
			return ActionResult{}, err
		}
		if err := enforceLogWindow(queryReq.Since, queryReq.StartTime, queryReq.EndTime, p.policy); err != nil {
			return ActionResult{}, err
		}
		result, err := p.logs.QueryLogs(ctx, queryReq)
		if err != nil {
			return ActionResult{}, err
		}
		redactLogQueryResult(result, p.policy.RedactFields)
		payload, err := json.Marshal(result)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Payload: payload, Metadata: ActionMetadata{ResultCount: len(result.Entries), DurationMs: int(time.Since(start).Milliseconds())}}, nil
	case "victorialogs.context":
		contextReq, err := parseVictoriaLogsContextParams(req.Params)
		if err != nil {
			return ActionResult{}, err
		}
		result, err := p.logs.GetLogContext(ctx, contextReq)
		if err != nil {
			return ActionResult{}, err
		}
		redactLogEntries(result.Before, p.policy.RedactFields)
		if result.Target != nil {
			redactLogEntry(result.Target, p.policy.RedactFields)
		}
		redactLogEntries(result.After, p.policy.RedactFields)
		payload, err := json.Marshal(result)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Payload: payload, Metadata: ActionMetadata{ResultCount: len(result.Before) + len(result.After), DurationMs: int(time.Since(start).Milliseconds())}}, nil
	case "victorialogs.fields":
		fieldsReq, err := parseVictoriaLogsFieldsParams(req.Params, p.policy)
		if err != nil {
			return ActionResult{}, err
		}
		result, err := p.logs.ListLogFields(ctx, fieldsReq)
		if err != nil {
			return ActionResult{}, err
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Payload: payload, Metadata: ActionMetadata{FieldCount: len(result.Fields), DurationMs: int(time.Since(start).Milliseconds())}}, nil
	case "victorialogs.stats":
		statsReq, err := parseVictoriaLogsStatsParams(req.Params, p.policy)
		if err != nil {
			return ActionResult{}, err
		}
		if err := enforceLogWindow(statsReq.Since, statsReq.StartTime, statsReq.EndTime, p.policy); err != nil {
			return ActionResult{}, err
		}
		result, err := p.logs.QueryLogStats(ctx, statsReq)
		if err != nil {
			return ActionResult{}, err
		}
		if p.policy.MaxSeriesCardinality > 0 && len(result.Series) > p.policy.MaxSeriesCardinality {
			return ActionResult{}, fmt.Errorf("%w: stats series cardinality %d exceeds max %d", ErrLogQueryDenied, len(result.Series), p.policy.MaxSeriesCardinality)
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Payload: payload, Metadata: ActionMetadata{ResultCount: len(result.Series), DurationMs: int(time.Since(start).Milliseconds())}}, nil
	default:
		return ActionResult{}, ErrCapabilityUnsupported
	}
}

func (p *VictoriaLogsProvider) checkRateLimit(now time.Time) error {
	if p.policy.MaxRequestsPerMinute <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	window := now.Truncate(time.Minute)
	if !window.Equal(p.window) {
		p.window = window
		p.requests = 0
	}
	p.requests++
	if p.requests > p.policy.MaxRequestsPerMinute {
		return fmt.Errorf("%w: request rate exceeds %d per minute", ErrLogQueryDenied, p.policy.MaxRequestsPerMinute)
	}
	return nil
}

func parseVictoriaLogsQueryParams(raw json.RawMessage, policy VictoriaLogsPolicy) (integration.LogQueryRequest, error) {
	var params struct {
		Query      string   `json:"query"`
		Since      string   `json:"since"`
		StartTime  string   `json:"start_time"`
		EndTime    string   `json:"end_time"`
		Limit      *int     `json:"limit"`
		Direction  string   `json:"direction"`
		Fields     []string `json:"fields"`
		IncludeRaw *bool    `json:"include_raw"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return integration.LogQueryRequest{}, err
	}
	limit := policy.MaxRows
	if params.Limit != nil && *params.Limit > 0 && *params.Limit < limit {
		limit = *params.Limit
	}
	req := integration.LogQueryRequest{
		Query:      mergeDefaultLogFilter(policy.DefaultFilter, params.Query),
		Limit:      &limit,
		Fields:     filterLogFields(params.Fields, policy),
		IncludeRaw: params.IncludeRaw,
	}
	if params.Since != "" {
		d, err := time.ParseDuration(params.Since)
		if err != nil {
			return integration.LogQueryRequest{}, err
		}
		req.Since = &d
	}
	if params.StartTime != "" {
		start, err := time.Parse(time.RFC3339, params.StartTime)
		if err != nil {
			return integration.LogQueryRequest{}, err
		}
		req.StartTime = &start
	}
	if params.EndTime != "" {
		end, err := time.Parse(time.RFC3339, params.EndTime)
		if err != nil {
			return integration.LogQueryRequest{}, err
		}
		req.EndTime = &end
	}
	if params.Direction != "" {
		dir := integration.LogDirection(params.Direction)
		req.Direction = &dir
	}
	return req, nil
}

func parseVictoriaLogsContextParams(raw json.RawMessage) (integration.LogContextRequest, error) {
	var params struct {
		ID        *string  `json:"id"`
		Timestamp *string  `json:"timestamp"`
		Query     *string  `json:"query"`
		Since     string   `json:"since"`
		Before    *int     `json:"before"`
		After     *int     `json:"after"`
		Fields    []string `json:"fields"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return integration.LogContextRequest{}, err
	}
	req := integration.LogContextRequest{Query: params.Query, Before: params.Before, After: params.After, Fields: params.Fields}
	if params.ID != nil {
		req.Anchor.ID = params.ID
	}
	if params.Timestamp != nil {
		t, err := time.Parse(time.RFC3339, *params.Timestamp)
		if err != nil {
			return integration.LogContextRequest{}, err
		}
		req.Anchor.Timestamp = &t
	}
	if params.Since != "" {
		d, err := time.ParseDuration(params.Since)
		if err != nil {
			return integration.LogContextRequest{}, err
		}
		req.Since = &d
	}
	return req, nil
}

func parseVictoriaLogsFieldsParams(raw json.RawMessage, policy VictoriaLogsPolicy) (integration.LogFieldsRequest, error) {
	var params struct {
		Query *string `json:"query"`
		Since string  `json:"since"`
		Limit *int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return integration.LogFieldsRequest{}, err
	}
	limit := policy.MaxRows
	if params.Limit != nil && *params.Limit > 0 && *params.Limit < limit {
		limit = *params.Limit
	}
	req := integration.LogFieldsRequest{Query: params.Query, Limit: &limit}
	if params.Since != "" {
		d, err := time.ParseDuration(params.Since)
		if err != nil {
			return integration.LogFieldsRequest{}, err
		}
		req.Since = &d
	}
	return req, nil
}

func parseVictoriaLogsStatsParams(raw json.RawMessage, policy VictoriaLogsPolicy) (integration.LogStatsRequest, error) {
	var params struct {
		Query    string   `json:"query"`
		Since    string   `json:"since"`
		GroupBy  []string `json:"group_by"`
		Interval string   `json:"interval"`
		Limit    *int     `json:"limit"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return integration.LogStatsRequest{}, err
	}
	limit := policy.MaxRows
	if params.Limit != nil && *params.Limit > 0 && *params.Limit < limit {
		limit = *params.Limit
	}
	req := integration.LogStatsRequest{Query: mergeDefaultLogFilter(policy.DefaultFilter, params.Query), GroupBy: params.GroupBy, Limit: &limit}
	if params.Since != "" {
		d, err := time.ParseDuration(params.Since)
		if err != nil {
			return integration.LogStatsRequest{}, err
		}
		req.Since = &d
	}
	if params.Interval != "" {
		d, err := time.ParseDuration(params.Interval)
		if err != nil {
			return integration.LogStatsRequest{}, err
		}
		req.Interval = &d
	}
	return req, nil
}

func enforceLogWindow(since *time.Duration, start, end *time.Time, policy VictoriaLogsPolicy) error {
	if policy.MaxQueryWindow <= 0 {
		return nil
	}
	if since != nil && *since > policy.MaxQueryWindow {
		return fmt.Errorf("%w: query window %s exceeds max %s", ErrLogQueryDenied, since.String(), policy.MaxQueryWindow.String())
	}
	if start != nil && end != nil && end.Sub(*start) > policy.MaxQueryWindow {
		return fmt.Errorf("%w: query window %s exceeds max %s", ErrLogQueryDenied, end.Sub(*start).String(), policy.MaxQueryWindow.String())
	}
	return nil
}

func mergeDefaultLogFilter(defaultFilter, query string) string {
	defaultFilter = strings.TrimSpace(defaultFilter)
	query = strings.TrimSpace(query)
	switch {
	case defaultFilter == "":
		return query
	case query == "":
		return defaultFilter
	default:
		return "(" + defaultFilter + ") AND (" + query + ")"
	}
}

func filterLogFields(fields []string, policy VictoriaLogsPolicy) []string {
	if len(fields) == 0 {
		return fields
	}
	allowed := make(map[string]struct{}, len(policy.AllowedFields))
	for _, field := range policy.AllowedFields {
		if value := strings.ToLower(strings.TrimSpace(field)); value != "" {
			allowed[value] = struct{}{}
		}
	}
	denied := make(map[string]struct{}, len(policy.DeniedFields))
	for _, field := range policy.DeniedFields {
		if value := strings.ToLower(strings.TrimSpace(field)); value != "" {
			denied[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		normalized := strings.ToLower(strings.TrimSpace(field))
		if normalized == "" {
			continue
		}
		if _, ok := denied[normalized]; ok {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[normalized]; !ok {
				continue
			}
		}
		out = append(out, strings.TrimSpace(field))
	}
	return out
}

func redactLogQueryResult(result *integration.LogQueryResult, fields []string) {
	if result == nil {
		return
	}
	redactLogEntries(result.Entries, fields)
}

func redactLogEntries(entries []integration.LogEntry, fields []string) {
	for i := range entries {
		redactLogEntry(&entries[i], fields)
	}
}

func redactLogEntry(entry *integration.LogEntry, fields []string) {
	redact := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		redact[strings.ToLower(field)] = struct{}{}
	}
	for key := range entry.Fields {
		if _, ok := redact[strings.ToLower(key)]; ok {
			entry.Fields[key] = "[REDACTED]"
		}
	}
	for key := range entry.Raw {
		if _, ok := redact[strings.ToLower(key)]; ok {
			entry.Raw[key] = "[REDACTED]"
		}
	}
}
