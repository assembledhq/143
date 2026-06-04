package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
)

func logToolDefinitions(providers []integration.LogProvider) []Tool {
	providerNames := integration.LogProviderNames(providers)
	providerDesc := strings.Join(providerNames, ", ")
	props := map[string]SchemaProperty{
		"provider":    {Type: "string", Description: "Log provider to use. Configured providers: " + providerDesc, Enum: providerNames},
		"query":       {Type: "string", Description: "Provider-native query text"},
		"since":       {Type: "string", Description: "Required unless start_time/end_time are set. Duration such as 15m, 1h, or 7d"},
		"start_time":  {Type: "string", Description: "RFC3339 lower safety bound"},
		"end_time":    {Type: "string", Description: "RFC3339 upper safety bound"},
		"limit":       {Type: "number", Description: "Max results, default 100, max 1000", Default: integration.LogDefaultLimit},
		"direction":   {Type: "string", Description: "Result order", Enum: []string{string(integration.LogDirectionDesc), string(integration.LogDirectionAsc)}, Default: string(integration.LogDirectionDesc)},
		"fields":      {Type: "array", Description: "Field names to include in the response", Items: &SchemaProperty{Type: "string"}},
		"include_raw": {Type: "boolean", Description: "Request redacted raw provider payloads when authorized", Default: false},
	}
	tools := []Tool{
		{
			Name:        "log_query",
			Description: "Run a read-only provider-native log query over a bounded time range. Always provide --since or --start_time/--end_time.",
			InputSchema: ToolSchema{Type: "object", Properties: props, Required: []string{"query"}},
		},
		{
			Name:        "log_context",
			Description: "Fetch neighboring logs around --cursor, --id, or a portable --timestamp plus --query and bounded time flags.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider":    props["provider"],
					"id":          {Type: "string", Description: "Stable event/log ID anchor"},
					"cursor":      {Type: "string", Description: "Opaque event cursor anchor"},
					"timestamp":   {Type: "string", Description: "RFC3339 timestamp anchor"},
					"query":       props["query"],
					"since":       props["since"],
					"start_time":  props["start_time"],
					"end_time":    props["end_time"],
					"before":      {Type: "number", Description: "Logs before target, default 20, max 100", Default: 20},
					"after":       {Type: "number", Description: "Logs after target, default 20, max 100", Default: 20},
					"fields":      props["fields"],
					"include_raw": props["include_raw"],
				},
			},
		},
		{
			Name:        "log_fields",
			Description: "List common indexed or queryable log fields for the selected provider.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider": props["provider"],
					"query":    props["query"],
					"since":    {Type: "string", Description: "Lookback window, default 24h, max 7d", Default: "24h"},
					"limit":    {Type: "number", Description: "Max field names or sampled records", Default: 100},
				},
			},
		},
	}
	if anyLogProviderSupportsStats(providers) {
		tools = append(tools, Tool{
			Name:        "log_stats",
			Description: "Run lightweight provider-native aggregate log stats over a bounded time range.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProperty{
					"provider":   props["provider"],
					"query":      props["query"],
					"since":      props["since"],
					"start_time": props["start_time"],
					"end_time":   props["end_time"],
					"group_by":   {Type: "array", Description: "Field names to group by", Items: &SchemaProperty{Type: "string"}},
					"interval":   {Type: "string", Description: "Optional time bucket interval, such as 5m or 1h"},
					"limit":      {Type: "number", Description: "Max grouped rows", Default: 100},
				},
				Required: []string{"query"},
			},
		})
	}
	return tools
}

func anyLogProviderSupportsStats(providers []integration.LogProvider) bool {
	for _, provider := range providers {
		statsProvider, ok := provider.(integration.LogStatsProvider)
		if ok && statsProvider.SupportsStats() {
			return true
		}
	}
	return false
}

const (
	logQueryResponseCap   = 200 * 1024 // 200 KB
	logContextResponseCap = 400 * 1024 // 400 KB
	logFieldsResponseCap  = 50 * 1024  // 50 KB
	logStatsResponseCap   = 100 * 1024 // 100 KB
)

func (tr *ToolRegistry) callLogTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	provider, err := tr.resolveLogProvider(args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	switch name {
	case "log_query":
		req, err := parseLogQueryRequest(args)
		if err != nil {
			return ErrorResult(err.Error())
		}
		// Decode cursor and inject original time bounds for pagination continuation.
		if req.Cursor != nil && tr.cursorSigner != nil {
			constraints, cerr := tr.cursorSigner.Decode(*req.Cursor)
			if cerr != nil {
				return ErrorResult(integration.ErrLogCursorInvalid.Error())
			}
			if !logCursorMatchesRequest(constraints, req, provider.Name()) {
				return ErrorResult(integration.ErrLogCursorInvalid.Error())
			}
			providerTS, perr := time.Parse(time.RFC3339Nano, constraints.ProviderCursor)
			if perr != nil {
				return ErrorResult(integration.ErrLogCursorInvalid.Error())
			}
			// Shift the time window past the last returned entry.
			dir := constraints.Direction
			start := constraints.StartTime
			end := constraints.EndTime
			if dir == integration.LogDirectionDesc {
				end = providerTS.Add(-time.Nanosecond)
			} else {
				start = providerTS.Add(time.Nanosecond)
			}
			req.StartTime = &start
			req.EndTime = &end
			req.Since = nil
		}
		result, err := provider.QueryLogs(ctx, req)
		if err != nil {
			return logToolError("log_query", provider.Name(), err)
		}
		// Sign a next-page cursor when the result is truncated.
		if result.Truncated && tr.cursorSigner != nil && len(result.Entries) > 0 {
			last := result.Entries[len(result.Entries)-1]
			dir := integration.LogDirectionDesc
			if req.Direction != nil {
				dir = *req.Direction
			}
			nc := integration.LogCursorConstraints{
				Provider:       provider.Name(),
				Query:          req.Query,
				StartTime:      result.StartTime,
				EndTime:        result.EndTime,
				Direction:      dir,
				Fields:         req.Fields,
				ExpiresAt:      time.Now().Add(24 * time.Hour),
				ProviderCursor: last.Timestamp.UTC().Format(time.RFC3339Nano),
			}
			if cursor, serr := tr.cursorSigner.Sign(nc); serr == nil {
				result.NextCursor = cursor
			}
		}
		capLogQueryResult(result, logQueryResponseCap)
		return jsonResult(result)
	case "log_context":
		req, err := parseLogContextRequest(args)
		if err != nil {
			return ErrorResult(err.Error())
		}
		result, err := provider.GetLogContext(ctx, req)
		if err != nil {
			return logToolError("log_context", provider.Name(), err)
		}
		beforeTrimmed, afterTrimmed := capLogContextResult(result, logContextResponseCap)
		// Sign prev/next cursors so the agent can fetch the trimmed context window.
		if tr.cursorSigner != nil && req.Query != nil {
			if start, end, nerr := integration.NormalizeLogTimeBounds(req.Since, req.StartTime, req.EndTime, integration.LogMaxLookback, time.Now()); nerr == nil {
				if beforeTrimmed && len(result.Before) > 0 {
					nc := integration.LogCursorConstraints{
						Provider:       provider.Name(),
						Query:          *req.Query,
						StartTime:      start,
						EndTime:        end,
						Direction:      integration.LogDirectionDesc,
						Fields:         req.Fields,
						ExpiresAt:      time.Now().Add(24 * time.Hour),
						ProviderCursor: result.Before[0].Timestamp.UTC().Format(time.RFC3339Nano),
					}
					if cursor, serr := tr.cursorSigner.Sign(nc); serr == nil {
						result.PrevCursor = cursor
					}
				}
				if afterTrimmed && len(result.After) > 0 {
					last := result.After[len(result.After)-1]
					nc := integration.LogCursorConstraints{
						Provider:       provider.Name(),
						Query:          *req.Query,
						StartTime:      start,
						EndTime:        end,
						Direction:      integration.LogDirectionAsc,
						Fields:         req.Fields,
						ExpiresAt:      time.Now().Add(24 * time.Hour),
						ProviderCursor: last.Timestamp.UTC().Format(time.RFC3339Nano),
					}
					if cursor, serr := tr.cursorSigner.Sign(nc); serr == nil {
						result.NextCursor = cursor
					}
				}
			}
		}
		return jsonResult(result)
	case "log_fields":
		req, err := parseLogFieldsRequest(args)
		if err != nil {
			return ErrorResult(err.Error())
		}
		result, err := provider.ListLogFields(ctx, req)
		if err != nil {
			return logToolError("log_fields", provider.Name(), err)
		}
		capLogFieldsResult(result, logFieldsResponseCap)
		return jsonResult(result)
	case "log_stats":
		req, err := parseLogStatsRequest(args)
		if err != nil {
			return ErrorResult(err.Error())
		}
		result, err := provider.QueryLogStats(ctx, req)
		if err != nil {
			return logToolError("log_stats", provider.Name(), err)
		}
		capLogStatsResult(result, logStatsResponseCap)
		return jsonResult(result)
	default:
		return ErrorResult(fmt.Sprintf("unknown log tool: %s", name))
	}
}

// logCursorMatchesRequest returns true when the cursor's constraints are
// consistent with the current request — same provider, query, direction, and
// field list. Time bounds are intentionally excluded because they are replaced
// by the cursor's stored bounds on continuation.
func logCursorMatchesRequest(c integration.LogCursorConstraints, req integration.LogQueryRequest, provider models.ProviderName) bool {
	if c.Provider != provider || c.Query != req.Query {
		return false
	}
	dir := integration.LogDirectionDesc
	if req.Direction != nil {
		dir = *req.Direction
	}
	if c.Direction != dir {
		return false
	}
	got := append([]string(nil), c.Fields...)
	want := append([]string(nil), req.Fields...)
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

func capLogQueryResult(r *integration.LogQueryResult, maxBytes int) {
	for len(r.Entries) > 1 {
		data, _ := json.Marshal(r)
		if len(data) <= maxBytes {
			return
		}
		r.Entries = r.Entries[:len(r.Entries)-1]
		r.Truncated = true
	}
}

// capLogContextResult trims the outermost before/after entries alternately
// until the marshalled result fits within maxBytes. It returns whether Before
// or After entries were trimmed so the caller can sign continuation cursors.
func capLogContextResult(r *integration.LogContextResult, maxBytes int) (beforeTrimmed, afterTrimmed bool) {
	for {
		data, _ := json.Marshal(r)
		if len(data) <= maxBytes {
			return
		}
		trimmed := false
		if len(r.After) > 0 {
			r.After = r.After[:len(r.After)-1]
			afterTrimmed = true
			trimmed = true
		}
		if len(r.Before) > 0 {
			r.Before = r.Before[1:]
			beforeTrimmed = true
			trimmed = true
		}
		if !trimmed {
			return
		}
	}
}

func capLogFieldsResult(r *integration.LogFieldsResult, maxBytes int) {
	for len(r.Fields) > 1 {
		data, _ := json.Marshal(r)
		if len(data) <= maxBytes {
			return
		}
		r.Fields = r.Fields[:len(r.Fields)-1]
	}
}

func capLogStatsResult(r *integration.LogStatsResult, maxBytes int) {
	for len(r.Series) > 1 {
		data, _ := json.Marshal(r)
		if len(data) <= maxBytes {
			return
		}
		r.Series = r.Series[:len(r.Series)-1]
		r.Truncated = true
	}
}

func (tr *ToolRegistry) resolveLogProvider(args json.RawMessage) (integration.LogProvider, error) {
	var selector integration.LogToolSelector
	if err := json.Unmarshal(args, &selector); err != nil && len(args) > 0 {
		return nil, fmt.Errorf("invalid arguments: %s", err)
	}
	return integration.ResolveLogProvider(tr.integrations.LogProviders(), selector, models.ProviderName(os.Getenv("LOG_PROVIDER_DEFAULT")))
}

func parseLogQueryRequest(args json.RawMessage) (integration.LogQueryRequest, error) {
	var p logQueryParams
	if err := json.Unmarshal(args, &p); err != nil {
		return integration.LogQueryRequest{}, fmt.Errorf("invalid arguments: %s", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return integration.LogQueryRequest{}, errors.New("query is required")
	}
	start, end, since, err := parseLogTimeParams(p.Since, p.StartTime, p.EndTime)
	if err != nil {
		return integration.LogQueryRequest{}, err
	}
	limit, err := boundedIntPtr("limit", p.Limit, integration.LogDefaultLimit, integration.LogMaxLimit)
	if err != nil {
		return integration.LogQueryRequest{}, err
	}
	direction := integration.LogDirectionDesc
	if p.Direction != "" {
		direction = integration.LogDirection(p.Direction)
	}
	if direction != integration.LogDirectionAsc && direction != integration.LogDirectionDesc {
		return integration.LogQueryRequest{}, fmt.Errorf("direction must be %q or %q", integration.LogDirectionDesc, integration.LogDirectionAsc)
	}
	return integration.LogQueryRequest{
		Query:      p.Query,
		Since:      since,
		StartTime:  start,
		EndTime:    end,
		Limit:      limit,
		Direction:  &direction,
		Fields:     p.Fields,
		Cursor:     emptyStringToNil(p.Cursor),
		IncludeRaw: p.IncludeRaw,
	}, nil
}

func parseLogContextRequest(args json.RawMessage) (integration.LogContextRequest, error) {
	var p logContextParams
	if err := json.Unmarshal(args, &p); err != nil {
		return integration.LogContextRequest{}, fmt.Errorf("invalid arguments: %s", err)
	}
	anchor, err := parseLogAnchor(p.ID, p.Cursor, p.Timestamp)
	if err != nil {
		return integration.LogContextRequest{}, err
	}
	before, err := boundedIntPtr("before", p.Before, 20, 100)
	if err != nil {
		return integration.LogContextRequest{}, err
	}
	after, err := boundedIntPtr("after", p.After, 20, 100)
	if err != nil {
		return integration.LogContextRequest{}, err
	}
	var start, end *time.Time
	var since *time.Duration
	if anchor.Timestamp != nil {
		if strings.TrimSpace(p.Query) == "" {
			return integration.LogContextRequest{}, fmt.Errorf("%w: timestamp context requires query", integration.ErrLogAnchorInsufficient)
		}
		start, end, since, err = parseLogTimeParams(p.Since, p.StartTime, p.EndTime)
		if err != nil {
			return integration.LogContextRequest{}, err
		}
	} else if p.Since != "" || p.StartTime != "" || p.EndTime != "" {
		start, end, since, err = parseLogTimeParams(p.Since, p.StartTime, p.EndTime)
		if err != nil {
			return integration.LogContextRequest{}, err
		}
	}
	return integration.LogContextRequest{
		Anchor:     anchor,
		Query:      emptyStringToNil(p.Query),
		Since:      since,
		StartTime:  start,
		EndTime:    end,
		Before:     before,
		After:      after,
		Fields:     p.Fields,
		IncludeRaw: p.IncludeRaw,
	}, nil
}

func parseLogFieldsRequest(args json.RawMessage) (integration.LogFieldsRequest, error) {
	var p struct {
		Query string `json:"query"`
		Since string `json:"since"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil && len(args) > 0 {
		return integration.LogFieldsRequest{}, fmt.Errorf("invalid arguments: %s", err)
	}
	since := integration.LogDefaultFieldsSince
	if p.Since != "" {
		parsed, err := parseLogDuration(p.Since)
		if err != nil {
			return integration.LogFieldsRequest{}, err
		}
		if parsed <= 0 || parsed > integration.LogMaxLookback {
			return integration.LogFieldsRequest{}, fmt.Errorf("%w: since must be >0 and <= %s", integration.ErrLogTimeBoundRequired, integration.LogMaxLookback)
		}
		since = parsed
	}
	limit, err := boundedIntPtr("limit", p.Limit, 100, integration.LogMaxLimit)
	if err != nil {
		return integration.LogFieldsRequest{}, err
	}
	return integration.LogFieldsRequest{Query: emptyStringToNil(p.Query), Since: &since, Limit: limit}, nil
}

func parseLogStatsRequest(args json.RawMessage) (integration.LogStatsRequest, error) {
	var p logStatsParams
	if err := json.Unmarshal(args, &p); err != nil {
		return integration.LogStatsRequest{}, fmt.Errorf("invalid arguments: %s", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return integration.LogStatsRequest{}, errors.New("query is required")
	}
	start, end, since, err := parseLogTimeParams(p.Since, p.StartTime, p.EndTime)
	if err != nil {
		return integration.LogStatsRequest{}, err
	}
	limit, err := boundedIntPtr("limit", p.Limit, 100, integration.LogMaxLimit)
	if err != nil {
		return integration.LogStatsRequest{}, err
	}
	var interval *time.Duration
	if p.Interval != "" {
		parsed, err := parseLogDuration(p.Interval)
		if err != nil {
			return integration.LogStatsRequest{}, err
		}
		if parsed <= 0 || parsed > integration.LogMaxLookback {
			return integration.LogStatsRequest{}, fmt.Errorf("interval must be >0 and <= %s", integration.LogMaxLookback)
		}
		interval = &parsed
	}
	return integration.LogStatsRequest{
		Query:     p.Query,
		Since:     since,
		StartTime: start,
		EndTime:   end,
		GroupBy:   p.GroupBy,
		Interval:  interval,
		Limit:     limit,
	}, nil
}

func parseLogTimeParams(sinceRaw, startRaw, endRaw string) (*time.Time, *time.Time, *time.Duration, error) {
	var since *time.Duration
	var start *time.Time
	var end *time.Time
	if sinceRaw != "" {
		parsed, err := parseLogDuration(sinceRaw)
		if err != nil {
			return nil, nil, nil, err
		}
		since = &parsed
	}
	if startRaw != "" {
		parsed, err := time.Parse(time.RFC3339, startRaw)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("start_time must be RFC3339: %w", err)
		}
		start = &parsed
	}
	if endRaw != "" {
		parsed, err := time.Parse(time.RFC3339, endRaw)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("end_time must be RFC3339: %w", err)
		}
		end = &parsed
	}
	if _, _, err := integration.NormalizeLogTimeBounds(since, start, end, integration.LogMaxLookback, time.Now()); err != nil {
		return nil, nil, nil, err
	}
	return start, end, since, nil
}

func parseLogDuration(value string) (time.Duration, error) {
	if len(value) > 1 && value[len(value)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(value, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("duration must be a Go duration or day value like 7d: %w", err)
	}
	return d, nil
}

func parseLogAnchor(id, cursor, timestamp string) (integration.LogAnchor, error) {
	anchor := integration.LogAnchor{
		ID:     emptyStringToNil(id),
		Cursor: emptyStringToNil(cursor),
	}
	if timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return integration.LogAnchor{}, fmt.Errorf("timestamp must be RFC3339: %w", err)
		}
		anchor.Timestamp = &parsed
	}
	if anchor.ID == nil && anchor.Cursor == nil && anchor.Timestamp == nil {
		return integration.LogAnchor{}, fmt.Errorf("%w: provide --id, --cursor, or --timestamp", integration.ErrLogAnchorInsufficient)
	}
	return anchor, nil
}

func boundedIntPtr(name string, value int, defaultValue int, maxValue int) (*int, error) {
	if value == 0 {
		value = defaultValue
	}
	if value < 0 || value > maxValue {
		return nil, fmt.Errorf("%s must be between 0 and %d", name, maxValue)
	}
	return &value, nil
}

func emptyStringToNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func logToolError(action string, provider models.ProviderName, err error) *ToolCallResult {
	if errors.Is(err, integration.ErrLogStatsUnsupported) {
		return ErrorResult(fmt.Sprintf("%s is not supported by %s", action, provider))
	}
	return ErrorResult(fmt.Sprintf("%s failed for %s: %s", action, provider, err))
}

type logQueryParams struct {
	Provider   string   `json:"provider"`
	Query      string   `json:"query"`
	Since      string   `json:"since"`
	StartTime  string   `json:"start_time"`
	EndTime    string   `json:"end_time"`
	Limit      int      `json:"limit"`
	Direction  string   `json:"direction"`
	Fields     []string `json:"fields"`
	Cursor     string   `json:"cursor"`
	IncludeRaw *bool    `json:"include_raw"`
}

type logContextParams struct {
	Provider   string   `json:"provider"`
	ID         string   `json:"id"`
	Cursor     string   `json:"cursor"`
	Timestamp  string   `json:"timestamp"`
	Query      string   `json:"query"`
	Since      string   `json:"since"`
	StartTime  string   `json:"start_time"`
	EndTime    string   `json:"end_time"`
	Before     int      `json:"before"`
	After      int      `json:"after"`
	Fields     []string `json:"fields"`
	IncludeRaw *bool    `json:"include_raw"`
}

type logStatsParams struct {
	Provider  string   `json:"provider"`
	Query     string   `json:"query"`
	Since     string   `json:"since"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
	GroupBy   []string `json:"group_by"`
	Interval  string   `json:"interval"`
	Limit     int      `json:"limit"`
}
