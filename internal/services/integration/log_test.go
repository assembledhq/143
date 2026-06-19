package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

type registryLogProvider struct {
	name models.ProviderName
}

func (p registryLogProvider) Name() models.ProviderName { return p.name }
func (p registryLogProvider) QueryLogs(_ context.Context, _ LogQueryRequest) (*LogQueryResult, error) {
	return nil, nil
}
func (p registryLogProvider) GetLogContext(_ context.Context, _ LogContextRequest) (*LogContextResult, error) {
	return nil, nil
}
func (p registryLogProvider) ListLogFields(_ context.Context, _ LogFieldsRequest) (*LogFieldsResult, error) {
	return nil, nil
}
func (p registryLogProvider) QueryLogStats(_ context.Context, _ LogStatsRequest) (*LogStatsResult, error) {
	return nil, ErrLogStatsUnsupported
}

func TestRegistry_LogProviders(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.RegisterLogProvider(registryLogProvider{name: models.ProviderVictoriaLogs})
	r.RegisterLogProvider(registryLogProvider{name: models.ProviderMezmo})

	require.True(t, r.HasAny(), "registry with log providers should report integrations are configured")
	require.Len(t, r.LogProviders(), 2, "registry should return all registered log providers")

	provider, err := r.LogProvider(models.ProviderMezmo)
	require.NoError(t, err, "registry should retrieve a log provider by provider name")
	require.Equal(t, models.ProviderMezmo, provider.Name(), "registry should return the requested log provider")

	summary := r.Summary()
	require.ElementsMatch(t, []string{"victorialogs", "mezmo"}, summary["log_providers"], "registry summary should include log providers")
}

func TestResolveLogProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providers     []LogProvider
		selector      LogToolSelector
		defaultName   models.ProviderName
		expectedName  models.ProviderName
		expectedError error
	}{
		{
			name:         "single provider selected by default",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}},
			expectedName: models.ProviderVictoriaLogs,
		},
		{
			name:         "explicit provider selected",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			selector:     LogToolSelector{Provider: stringPtr("mezmo")},
			expectedName: models.ProviderMezmo,
		},
		{
			name:         "configured default selected",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			defaultName:  models.ProviderVictoriaLogs,
			expectedName: models.ProviderVictoriaLogs,
		},
		{
			name:          "missing providers",
			expectedError: ErrLogProviderUnconfigured,
		},
		{
			name:          "ambiguous providers",
			providers:     []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			expectedError: ErrLogProviderAmbiguous,
		},
		{
			name:          "unknown provider",
			providers:     []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}},
			selector:      LogToolSelector{Provider: stringPtr("mezmo")},
			expectedError: ErrLogProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := ResolveLogProvider(tt.providers, tt.selector, tt.defaultName)
			if tt.expectedError != nil {
				require.ErrorIs(t, err, tt.expectedError, "ResolveLogProvider should return the expected provider resolution error")
				return
			}

			require.NoError(t, err, "ResolveLogProvider should resolve a provider")
			require.Equal(t, tt.expectedName, provider.Name(), "ResolveLogProvider should choose the expected provider")
		})
	}
}

func TestLogCursorSigner(t *testing.T) {
	t.Parallel()

	signer := NewLogCursorSigner([]byte("test-secret"))
	expiresAt := time.Now().Add(time.Hour).UTC()
	constraints := LogCursorConstraints{
		Provider:       models.ProviderVictoriaLogs,
		Query:          "service:api",
		StartTime:      time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
		Direction:      LogDirectionDesc,
		Fields:         []string{"message", "level"},
		ExpiresAt:      expiresAt,
		ProviderCursor: "provider-page-2",
	}

	cursor, err := signer.Sign(constraints)
	require.NoError(t, err, "Sign should create an HMAC protected log cursor")

	got, err := signer.Verify(cursor, constraints)
	require.NoError(t, err, "Verify should accept an untampered cursor with matching constraints")
	require.Equal(t, "provider-page-2", got.ProviderCursor, "Verify should return the provider cursor payload")

	_, err = signer.Verify(cursor+"x", constraints)
	require.ErrorIs(t, err, ErrLogCursorInvalid, "Verify should reject tampered cursors before provider dispatch")

	mismatched := constraints
	mismatched.Query = "service:worker"
	_, err = signer.Verify(cursor, mismatched)
	require.ErrorIs(t, err, ErrLogCursorInvalid, "Verify should reject cursors with mismatched constraints")
}

func TestLogCursorSignerDecode(t *testing.T) {
	t.Parallel()

	signer := NewLogCursorSigner([]byte("test-secret"))
	constraints := LogCursorConstraints{
		Provider:       models.ProviderVictoriaLogs,
		Query:          "service:api",
		StartTime:      time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
		Direction:      LogDirectionDesc,
		ExpiresAt:      time.Now().Add(time.Hour).UTC(),
		ProviderCursor: "2026-05-28T10:45:00Z",
	}

	cursor, err := signer.Sign(constraints)
	require.NoError(t, err)

	got, err := signer.Decode(cursor)
	require.NoError(t, err, "Decode should accept a valid cursor")
	require.Equal(t, constraints.Provider, got.Provider, "Decode should return correct provider")
	require.Equal(t, constraints.Query, got.Query, "Decode should return correct query")
	require.Equal(t, constraints.ProviderCursor, got.ProviderCursor, "Decode should return the provider cursor")

	// Decode should not enforce constraint matching (different query is fine).
	_, err = signer.Decode(cursor)
	require.NoError(t, err, "Decode should succeed regardless of current request parameters")

	// Tampered cursor should be rejected.
	_, err = signer.Decode(cursor + "x")
	require.ErrorIs(t, err, ErrLogCursorInvalid, "Decode should reject tampered cursors")
}

func TestRedactLogPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"message": "hello",
		"api_key": "secret",
		"nested": map[string]any{
			"Authorization": "Bearer secret",
			"safe":          "value",
		},
		"items": []any{
			map[string]any{"session_token": "secret", "count": float64(2)},
		},
	}

	redacted := RedactLogPayload(payload)
	require.Equal(t, "[REDACTED]", redacted["api_key"], "RedactLogPayload should redact sensitive top-level fields")
	nested := redacted["nested"].(map[string]any)
	require.Equal(t, "[REDACTED]", nested["Authorization"], "RedactLogPayload should redact sensitive nested fields")
	require.Equal(t, "value", nested["safe"], "RedactLogPayload should preserve non-sensitive nested fields")
	items := redacted["items"].([]any)
	item := items[0].(map[string]any)
	require.Equal(t, "[REDACTED]", item["session_token"], "RedactLogPayload should redact sensitive fields inside arrays")
	require.Equal(t, float64(2), item["count"], "RedactLogPayload should preserve safe fields inside arrays")
}

func TestNormalizeLogRecordsCapsRawPayloadFields(t *testing.T) {
	t.Parallel()

	includeRaw := true
	result := normalizeLogRecords(models.ProviderVictoriaLogs, []map[string]any{
		{
			"timestamp": "2026-05-28T12:00:00Z",
			"message":   "hello",
			"details":   strings.Repeat("x", logEntryFieldLimit+100),
			"nested": map[string]any{
				"safe": strings.Repeat("y", logEntryFieldLimit+100),
			},
		},
	}, nil, &includeRaw)

	require.Len(t, result, 1, "normalizeLogRecords should return one entry")
	require.Len(t, result[0].Raw["details"], logEntryFieldLimit, "raw top-level string fields should be capped")
	nested, ok := result[0].Raw["nested"].(map[string]any)
	require.True(t, ok, "raw nested objects should remain structured")
	require.Len(t, nested["safe"], logEntryFieldLimit, "raw nested string fields should be capped")
}

func TestIsSensitiveLogField(t *testing.T) {
	t.Parallel()

	sensitive := []string{
		"api_key", "signing_key", "auth", "auth_token", "oauth_token",
		"authorization", "password", "session_id", "secret", "private_key",
		"cookie", "credential", "access_token",
	}
	for _, key := range sensitive {
		require.True(t, isSensitiveLogField(key), "isSensitiveLogField(%q) should be true", key)
	}

	safe := []string{
		"author", "monkey", "turkey", "hockey", "message", "level",
		"service", "trace_id", "count", "authenticated", "unauthenticated",
	}
	for _, key := range safe {
		require.False(t, isSensitiveLogField(key), "isSensitiveLogField(%q) should be false", key)
	}
}

func TestValidateLogTimeBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		since         *time.Duration
		start         *time.Time
		end           *time.Time
		expectedError error
	}{
		{name: "since is accepted", since: durationPtr(time.Hour)},
		{name: "start and end are accepted", start: timePtr(time.Now().Add(-time.Hour)), end: timePtr(time.Now())},
		{name: "missing bounds", expectedError: ErrLogTimeBoundRequired},
		{name: "lookback too large", since: durationPtr(8 * 24 * time.Hour), expectedError: ErrLogTimeBoundRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := NormalizeLogTimeBounds(tt.since, tt.start, tt.end, 7*24*time.Hour, time.Now())
			if tt.expectedError != nil {
				require.True(t, errors.Is(err, tt.expectedError), "NormalizeLogTimeBounds should return the expected time-bound error")
				return
			}
			require.NoError(t, err, "NormalizeLogTimeBounds should accept bounded log requests")
		})
	}
}

// ndjsonBody encodes each record as a separate line (VictoriaLogs native format).
func ndjsonBody(records []map[string]any) []byte {
	var buf []byte
	for _, r := range records {
		line, _ := json.Marshal(r)
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return buf
}

func TestVictoriaLogsProviderSortsThenTrims(t *testing.T) {
	t.Parallel()

	// API returns 3 entries in descending order (newest first).
	// With limit=2 and direction=asc, we must return the 2 oldest entries in ascending order.
	records := []map[string]any{
		{"timestamp": "2026-05-28T12:03:00Z", "message": "newest"},
		{"timestamp": "2026-05-28T12:02:00Z", "message": "middle"},
		{"timestamp": "2026-05-28T12:01:00Z", "message": "oldest"},
	}
	body := ndjsonBody(records)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:   server.URL,
		HTTPClient: server.Client(),
	})

	limit := 2
	direction := LogDirectionAsc
	result, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query:     "*",
		Since:     durationPtr(time.Hour),
		Limit:     &limit,
		Direction: &direction,
	})
	require.NoError(t, err)
	require.True(t, result.Truncated, "should be truncated when 3 records exceed limit 2")
	require.Len(t, result.Entries, 2, "should return exactly limit entries")
	require.Equal(t, "oldest", result.Entries[0].Message, "ascending direction should start with the oldest entry")
	require.Equal(t, "middle", result.Entries[1].Message, "ascending direction should continue with the middle entry")
}

func TestVictoriaLogsContextFindsTargetWhenResultsExceedContextWindow(t *testing.T) {
	t.Parallel()

	// Build before=2, after=2 → old internal limit was 5. Return 6 entries so the
	// target (newest, index 5) would have been trimmed by the old tight limit.
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	records := make([]map[string]any, 6)
	for i := range records {
		records[i] = map[string]any{
			"timestamp": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"message":   fmt.Sprintf("entry-%d", i),
		}
	}
	body := ndjsonBody(records)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:   server.URL,
		HTTPClient: server.Client(),
	})

	targetTime := base.Add(5 * time.Minute)
	query := "*"
	before := 2
	after := 2
	result, err := provider.GetLogContext(context.Background(), LogContextRequest{
		Anchor: LogAnchor{Timestamp: &targetTime},
		Query:  &query,
		Since:  durationPtr(time.Hour),
		Before: &before,
		After:  &after,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Target, "target should be found even when it is the newest entry in the result set")
	require.Equal(t, "entry-5", result.Target.Message, "should identify the correct entry by closest timestamp")
	require.Len(t, result.Before, 2, "should return the requested number of before-context entries")
	require.Len(t, result.After, 0, "no entries after the newest target")
}

func TestVictoriaLogsProviderRejectsInvalidGroupByField(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\n"))
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:   server.URL,
		HTTPClient: server.Client(),
	})

	_, err := provider.QueryLogStats(context.Background(), LogStatsRequest{
		Query:   "*",
		Since:   durationPtr(time.Hour),
		GroupBy: []string{"service) count(), count("},
	})
	require.Error(t, err, "QueryLogStats should reject group_by field names with injection characters")
	require.Contains(t, err.Error(), "invalid character", "error should describe the invalid character")
}

func TestVictoriaLogsStatsTruncatesAndSetsFlag(t *testing.T) {
	t.Parallel()

	// Return 3 series records; ask for limit=2.
	records := []map[string]any{
		{"service": "api", "count": float64(10)},
		{"service": "worker", "count": float64(5)},
		{"service": "cron", "count": float64(1)},
	}
	body := ndjsonBody(records)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:   server.URL,
		HTTPClient: server.Client(),
	})

	limit := 2
	result, err := provider.QueryLogStats(context.Background(), LogStatsRequest{
		Query:   "*",
		Since:   durationPtr(time.Hour),
		GroupBy: []string{"service"},
		Limit:   &limit,
	})
	require.NoError(t, err)
	require.True(t, result.Truncated, "Truncated should be set when series exceed limit")
	require.Len(t, result.Series, 2, "series should be capped at limit")
}

func TestVictoriaLogsStatsIncludesIntervalBucket(t *testing.T) {
	t.Parallel()

	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr, "test server should read request body")
		params, parseErr := url.ParseQuery(string(body))
		require.NoError(t, parseErr, "test server should parse form body")
		capturedQuery = params.Get("query")
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"_time":"2026-05-28T12:00:00Z","service":"api","count":2}` + "\n"))
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:   server.URL,
		HTTPClient: server.Client(),
	})

	interval := 5 * time.Minute
	_, err := provider.QueryLogStats(context.Background(), LogStatsRequest{
		Query:    "*",
		Since:    durationPtr(time.Hour),
		GroupBy:  []string{"service"},
		Interval: &interval,
	})
	require.NoError(t, err, "QueryLogStats should accept an interval")
	require.Contains(t, capturedQuery, "stats by (_time:5m, service) count()", "stats query should bucket by the requested interval")
}

func TestVictoriaLogsMultiTenantScopesQuery(t *testing.T) {
	t.Parallel()

	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		params, _ := url.ParseQuery(string(body))
		capturedQuery = params.Get("query")
		w.Header().Set("Content-Type", "application/x-ndjson")
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:          server.URL,
		HTTPClient:        server.Client(),
		SharedOrgID:       "org-abc",
		MultiTenantShared: true,
	})

	_, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query: "service:api",
		Since: durationPtr(time.Hour),
	})
	require.NoError(t, err)
	require.Contains(t, capturedQuery, `org_id="org-abc"`, "multi-tenant query should include org_id filter")
	require.Contains(t, capturedQuery, "service:api", "multi-tenant query should preserve the original query")
}

func TestMezmoProviderNormalizesLogEntries(t *testing.T) {
	t.Parallel()

	// Mezmo returns results under a "lines" key.
	mezmoBody, err := json.Marshal(map[string]any{
		"lines": []map[string]any{
			{
				"timestamp": "2026-05-28T12:00:00Z",
				"level":     "error",
				"message":   "connection refused",
				"service":   "api",
			},
		},
	})
	require.NoError(t, err, "test should build a valid Mezmo response")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, writeErr := w.Write(mezmoBody)
		require.NoError(t, writeErr, "test server should write Mezmo response")
	}))
	defer server.Close()

	provider := NewMezmoProvider(MezmoConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})

	result, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query: "level:error",
		Since: durationPtr(time.Hour),
	})
	require.NoError(t, err, "QueryLogs should accept a Mezmo lines response")
	require.Len(t, result.Entries, 1, "should return one normalized entry")
	require.Equal(t, "connection refused", result.Entries[0].Message, "entry message should be normalized from Mezmo payload")
	require.Equal(t, "error", result.Entries[0].Level, "entry level should be normalized from Mezmo payload")
	require.Equal(t, "api", result.Entries[0].Service, "entry service should be normalized from Mezmo payload")
	require.Equal(t, models.ProviderMezmo, result.Entries[0].Provider, "entry provider should identify Mezmo")
}

func TestMezmoProviderUsesExportAPI(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 10, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 10, 5, 0, 0, 0, time.UTC)
	limit := 2
	direction := LogDirectionAsc

	mezmoBody, err := json.Marshal(map[string]any{
		"lines": []map[string]any{
			{
				"timestamp": start.Add(time.Minute).Format(time.RFC3339),
				"message":   "first match",
			},
		},
	})
	require.NoError(t, err, "test should build a valid Mezmo response")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "Mezmo log queries should use the documented export method")
		require.Equal(t, "/v2/export", r.URL.Path, "Mezmo log queries should use the documented v2 export path")
		require.Equal(t, "Token test-key", r.Header.Get("Authorization"), "Mezmo log queries should send the documented access-token auth header")

		query := r.URL.Query()
		require.Equal(t, strconv.FormatInt(start.Unix(), 10), query.Get("from"), "Mezmo export should receive the lower time bound as a unix timestamp")
		require.Equal(t, strconv.FormatInt(end.Unix(), 10), query.Get("to"), "Mezmo export should receive the upper time bound as a unix timestamp")
		require.Equal(t, "3", query.Get("size"), "Mezmo export should request one extra record for truncation detection")
		require.Equal(t, "head", query.Get("prefer"), "ascending queries should ask Mezmo for the first matching lines")
		require.Equal(t, "service:api", query.Get("query"), "Mezmo export should receive the provider-native query")

		w.Header().Set("Content-Type", "application/json")
		_, writeErr := w.Write(mezmoBody)
		require.NoError(t, writeErr, "test server should write Mezmo response")
	}))
	defer server.Close()

	provider := NewMezmoProvider(MezmoConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})

	result, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query:     "service:api",
		StartTime: &start,
		EndTime:   &end,
		Limit:     &limit,
		Direction: &direction,
	})
	require.NoError(t, err, "QueryLogs should query Mezmo export successfully")
	require.Len(t, result.Entries, 1, "QueryLogs should return the exported line")
	require.Equal(t, "first match", result.Entries[0].Message, "QueryLogs should normalize the exported line")
}

func TestMezmoProviderRejectsDatasetScope(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Fail(t, "Mezmo provider should reject dataset scopes before sending a request")
	}))
	defer server.Close()

	provider := NewMezmoProvider(MezmoConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Dataset:    "production",
		HTTPClient: server.Client(),
	})

	_, err := provider.QueryLogs(context.Background(), LogQueryRequest{
		Query: "service:api",
		Since: durationPtr(time.Hour),
	})
	require.Error(t, err, "QueryLogs should reject unsupported Mezmo dataset scopes")
	require.Contains(t, err.Error(), "dataset scoping is not supported", "error should explain that Mezmo dataset scoping is unsupported")
}

func TestVictoriaLogsListFieldsNative(t *testing.T) {
	t.Parallel()

	fieldNamesBody := []byte(`{"field_name":"level"}
{"field_name":"service"}
{"field_name":"trace_id"}
`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(fieldNamesBody)
	}))
	defer server.Close()

	provider := NewVictoriaLogsProvider(VictoriaLogsConfig{
		QueryURL:      server.URL,
		FieldNamesURL: server.URL,
		HTTPClient:    server.Client(),
	})

	result, err := provider.ListLogFields(context.Background(), LogFieldsRequest{
		Since: durationPtr(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, result.Fields, 3, "should return all field names from native endpoint")
	names := make([]string, len(result.Fields))
	for i, f := range result.Fields {
		names[i] = f.Name
	}
	require.ElementsMatch(t, []string{"level", "service", "trace_id"}, names)
}

func stringPtr(v string) *string                 { return &v }
func durationPtr(v time.Duration) *time.Duration { return &v }
func timePtr(v time.Time) *time.Time             { return &v }
