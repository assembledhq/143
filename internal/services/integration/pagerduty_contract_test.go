package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type pagerDutyTestTransport func(*http.Request) (*http.Response, error)

func (f pagerDutyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func pagerDutyTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestPagerDutyRequestAndResponseContracts(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	incidentJSON := `{"id":"PI1","incident_number":42,"title":"Database down","description":"Connection failed","status":"triggered","urgency":"high","html_url":"https://example.test/incidents/PI1","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z","service":{"id":"PS1","summary":"API"},"priority":{"summary":"P1"},"escalation_policy":{"summary":"Primary"},"assignments":[{"assignee":{"summary":"Ada"}},{"assignee":{}}],"teams":[{"id":"PT1","summary":"Platform"},{}]}`
	summary := IncidentSummary{ID: "PI1", Number: 42, Title: "Database down", Status: "triggered", Urgency: "high", Priority: "P1", ServiceID: "PS1", ServiceName: "API", CreatedAt: stamp, UpdatedAt: stamp, WebURL: "https://example.test/incidents/PI1"}
	tests := []struct {
		name, method, path, response, body string
		query                              url.Values
		call                               func(context.Context, *PagerDutyIncidentProvider) (any, error)
		expected                           any
	}{
		{name: "incident list defaults", method: http.MethodGet, path: "/incidents", query: url.Values{"limit": {"25"}, "statuses[]": {"triggered", "acknowledged"}}, response: `{"incidents":[` + incidentJSON + `]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidents(ctx, IncidentFilter{})
		}, expected: []IncidentSummary{summary}},
		{name: "incident list bounded filters", method: http.MethodGet, path: "/incidents", query: url.Values{"limit": {"100"}, "statuses[]": {"resolved"}, "urgencies[]": {"low"}, "service_ids[]": {"PS1"}, "since": {"2026-01-02T03:04:05Z"}}, response: `{"incidents":[]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidents(ctx, IncidentFilter{Limit: 10000, Statuses: []string{" resolved ", " "}, Urgency: " low ", Service: " PS1 ", Since: stamp})
		}, expected: []IncidentSummary{}},
		{name: "incident detail trims and escapes id", method: http.MethodGet, path: "/incidents/PI%2F1", response: `{"incident":` + incidentJSON + `}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.GetIncident(ctx, " PI/1 ")
		}, expected: &IncidentDetail{IncidentSummary: summary, Description: "Connection failed", EscalationPolicy: "Primary", AssignedUserNames: []string{"Ada"}, Teams: []string{"Platform"}}},
		{name: "note write trims content and receipt", method: http.MethodPost, path: "/incidents/PI1/notes", body: `{"note":{"content":"Investigating"}}`, response: `{"note":{"id":" PN1 "}}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.AddIncidentNote(ctx, " PI1 ", " Investigating ")
		}, expected: "PN1"},
		{name: "note list defaults", method: http.MethodGet, path: "/incidents/PI1/notes", query: url.Values{"limit": {"50"}}, response: `{"notes":[{"id":"PN1","content":"Investigating","user":{"id":"PU1","summary":"Ada"},"created_at":"2026-01-02T03:04:05Z"}]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidentNotes(ctx, " PI1 ", 0)
		}, expected: []IncidentNote{{ID: "PN1", IncidentID: "PI1", Content: "Investigating", UserID: "PU1", UserName: "Ada", CreatedAt: stamp}}},
		{name: "empty note list explicit limit", method: http.MethodGet, path: "/incidents/PI1/notes", query: url.Values{"limit": {"7"}}, response: `{"notes":[]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidentNotes(ctx, "PI1", 7)
		}, expected: []IncidentNote{}},
		{name: "log list defaults", method: http.MethodGet, path: "/incidents/PI1/log_entries", query: url.Values{"limit": {"100"}}, response: `{"log_entries":[{"id":"PL1","type":"acknowledge_log_entry","summary":"Acknowledged","agent":{"id":"PU1","summary":"Ada"},"created_at":"2026-01-02T03:04:05Z"}]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidentLogEntries(ctx, " PI1 ", -1)
		}, expected: []IncidentLogEntry{{ID: "PL1", IncidentID: "PI1", Type: "acknowledge_log_entry", Summary: "Acknowledged", AgentID: "PU1", AgentName: "Ada", CreatedAt: stamp}}},
		{name: "empty log list explicit limit", method: http.MethodGet, path: "/incidents/PI1/log_entries", query: url.Values{"limit": {"3"}}, response: `{"log_entries":[]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidentLogEntries(ctx, "PI1", 3)
		}, expected: []IncidentLogEntry{}},
		{name: "service detail skips empty team ids", method: http.MethodGet, path: "/services/PS1", response: `{"service":{"id":"PS1","name":" API ","summary":"Fallback","description":"Backend","html_url":"https://example.test/services/PS1","escalation_policy":{"summary":"Primary"},"teams":[{"id":"PT1"},{}]}}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.GetService(ctx, " PS1 ")
		}, expected: &IncidentService{ID: "PS1", Name: "API", Description: "Backend", HTMLURL: "https://example.test/services/PS1", EscalationPolicy: "Primary", TeamIDs: []string{"PT1"}}},
		{name: "service uses summary fallback", method: http.MethodGet, path: "/services/PS1", response: `{"service":{"id":"PS1","name":" ","summary":" API "}}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) { return p.GetService(ctx, "PS1") }, expected: &IncidentService{ID: "PS1", Name: "API", TeamIDs: []string{}}},
		{name: "on call defaults", method: http.MethodGet, path: "/oncalls", query: url.Values{"limit": {"20"}}, response: `{"oncalls":[]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListOnCalls(ctx, OnCallFilter{})
		}, expected: []OnCall{}},
		{name: "on call schedule filter", method: http.MethodGet, path: "/oncalls", query: url.Values{"limit": {"6"}, "schedule_ids[]": {"PSCH1"}}, response: `{"oncalls":[{"user":{"id":"PU1","summary":"Ada"},"schedule":{"id":"PSCH1","summary":"Weekday"},"escalation_policy":{"id":"PE1","summary":"Primary"},"start":"2026-01-02T03:04:05Z","end":"2026-01-02T03:04:05Z"}]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListOnCalls(ctx, OnCallFilter{Limit: 6, ScheduleID: " PSCH1 "})
		}, expected: []OnCall{{UserID: "PU1", UserName: "Ada", ScheduleID: "PSCH1", ScheduleName: "Weekday", EscalationPolicyID: "PE1", EscalationPolicyName: "Primary", Start: stamp, End: stamp}}},
		{name: "status update accepts empty response", method: http.MethodPost, path: "/incidents/PI1/status_updates", body: `{"message":"Recovered"}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return nil, p.CreateIncidentStatusUpdate(ctx, " PI1 ", " Recovered ")
		}},
		{name: "missing title uses description", method: http.MethodGet, path: "/incidents", query: url.Values{"limit": {"2"}, "statuses[]": {"triggered", "acknowledged"}}, response: `{"incidents":[{"id":"PI2","description":"Fallback title"}]}`, call: func(ctx context.Context, p *PagerDutyIncidentProvider) (any, error) {
			return p.ListIncidents(ctx, IncidentFilter{Limit: 2})
		}, expected: []IncidentSummary{{ID: "PI2", Title: "Fallback title"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			p := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{AccessToken: "test-token", BaseURL: "https://pagerduty.test/", WritebackEnabled: true})
			p.httpClient.Transport = pagerDutyTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tt.method, r.Method, "provider should use the endpoint's HTTP method")
				require.Equal(t, "pagerduty.test", r.URL.Host, "provider should use the configured API host")
				require.Equal(t, tt.path, r.URL.EscapedPath(), "opaque IDs should be escaped as one path component")
				query := tt.query
				if query == nil {
					query = url.Values{}
				}
				require.Equal(t, query, r.URL.Query(), "provider should send bounded limits and exact filters")
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "requests should carry the configured credential")
				require.Equal(t, "application/vnd.pagerduty+json;version=2", r.Header.Get("Accept"), "requests should select PagerDuty API v2")
				if tt.body != "" {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err, "request body should be readable")
					require.JSONEq(t, tt.body, string(body), "writeback should serialize the exact trimmed payload")
					require.Equal(t, "application/json", r.Header.Get("Content-Type"), "writeback payload should declare JSON")
				} else {
					require.Empty(t, r.Header.Get("Content-Type"), "read requests should not declare a JSON body")
				}
				return pagerDutyTestResponse(http.StatusOK, tt.response), nil
			})
			actual, err := tt.call(t.Context(), p)
			require.NoError(t, err, "valid PagerDuty responses should map to public result models")
			require.Equal(t, tt.expected, actual, "provider should preserve every returned field")
			require.Equal(t, 1, calls, "successful requests should not be retried")
		})
	}
}

func TestPagerDutyRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, expected string
		call           func(*PagerDutyIncidentProvider) error
	}{
		{"incident id", "incident_id is required", func(p *PagerDutyIncidentProvider) error { _, err := p.GetIncident(t.Context(), " "); return err }},
		{"note incident id", "incident_id is required", func(p *PagerDutyIncidentProvider) error {
			_, err := p.AddIncidentNote(t.Context(), " ", "note")
			return err
		}},
		{"note content", "note is required", func(p *PagerDutyIncidentProvider) error {
			_, err := p.AddIncidentNote(t.Context(), "PI1", " ")
			return err
		}},
		{"note list id", "incident_id is required", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListIncidentNotes(t.Context(), " ", 1)
			return err
		}},
		{"log list id", "incident_id is required", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListIncidentLogEntries(t.Context(), " ", 1)
			return err
		}},
		{"service id", "service_id is required", func(p *PagerDutyIncidentProvider) error { _, err := p.GetService(t.Context(), " "); return err }},
		{"status update id", "incident_id is required", func(p *PagerDutyIncidentProvider) error {
			return p.CreateIncidentStatusUpdate(t.Context(), " ", "update")
		}},
		{"status update body", "body is required", func(p *PagerDutyIncidentProvider) error { return p.CreateIncidentStatusUpdate(t.Context(), "PI1", " ") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{WritebackEnabled: true})
			p.httpClient.Transport = pagerDutyTestTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid inputs must not issue HTTP requests")
				return nil, errors.New("unexpected request")
			})
			require.EqualError(t, tt.call(p), tt.expected, "input validation should reject blank identifiers and content")
		})
	}
}

func TestPagerDutyDependencyErrorsPreserveOperationContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, expected string
		call           func(*PagerDutyIncidentProvider) error
	}{
		{"incidents", "list pagerduty incidents", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListIncidents(t.Context(), IncidentFilter{})
			return err
		}},
		{"incident", "get pagerduty incident", func(p *PagerDutyIncidentProvider) error { _, err := p.GetIncident(t.Context(), "PI1"); return err }},
		{"note write", "add pagerduty incident note", func(p *PagerDutyIncidentProvider) error {
			_, err := p.AddIncidentNote(t.Context(), "PI1", "note")
			return err
		}},
		{"notes", "list pagerduty incident notes", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListIncidentNotes(t.Context(), "PI1", 2)
			return err
		}},
		{"logs", "list pagerduty incident log entries", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListIncidentLogEntries(t.Context(), "PI1", 2)
			return err
		}},
		{"service", "get pagerduty service", func(p *PagerDutyIncidentProvider) error { _, err := p.GetService(t.Context(), "PS1"); return err }},
		{"on calls", "list pagerduty on-calls", func(p *PagerDutyIncidentProvider) error {
			_, err := p.ListOnCalls(t.Context(), OnCallFilter{})
			return err
		}},
		{"related source", "get pagerduty incident", func(p *PagerDutyIncidentProvider) error {
			_, err := p.FindRelatedIncidents(t.Context(), "PI1", 7)
			return err
		}},
		{"status write", "create pagerduty incident status update", func(p *PagerDutyIncidentProvider) error {
			return p.CreateIncidentStatusUpdate(t.Context(), "PI1", "update")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			p := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{WritebackEnabled: true})
			p.httpClient.Transport = pagerDutyTestTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return pagerDutyTestResponse(http.StatusForbidden, "denied"), nil
			})
			err := tt.call(p)
			require.ErrorContains(t, err, tt.expected+": pagerduty API returned 403: denied", "dependency errors should identify the failed operation")
			require.Equal(t, 1, calls, "authorization failures should not be retried")
		})
	}
}

func TestPagerDutyRelatedIncidentsUseServiceAndExcludeSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		days     int
		failList bool
	}{{"default lookback", 0, false}, {"explicit lookback", 7, false}, {"related list failure", 7, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			start := time.Now()
			p := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{})
			p.httpClient.Transport = pagerDutyTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					require.Equal(t, "/incidents/PI1", r.URL.Path, "related lookup should load the source incident first")
					return pagerDutyTestResponse(200, `{"incident":{"id":"PI1","service":{"id":"PS1"}}}`), nil
				}
				require.Equal(t, "/incidents", r.URL.Path, "related lookup should search incident history")
				query := r.URL.Query()
				since, err := time.Parse(time.RFC3339, query.Get("since"))
				require.NoError(t, err, "related lookback should be an RFC3339 timestamp")
				days := tt.days
				if days <= 0 {
					days = 90
				}
				window := time.Duration(days) * 24 * time.Hour
				require.False(t, since.Before(start.Add(-window-time.Second)), "lookback should not exceed the selected history window plus RFC3339 rounding")
				require.False(t, since.After(time.Now().Add(-window)), "lookback should cover the selected number of days at request time")
				query.Del("since")
				require.Equal(t, url.Values{"service_ids[]": {"PS1"}, "limit": {"50"}, "statuses[]": {"triggered", "acknowledged", "resolved"}}, query, "related search should include historical incidents for the source service")
				if tt.failList {
					return pagerDutyTestResponse(403, "denied"), nil
				}
				return pagerDutyTestResponse(200, `{"incidents":[{"id":"PI1"},{"id":"PI2","title":"Related"}]}`), nil
			})
			actual, err := p.FindRelatedIncidents(t.Context(), "PI1", tt.days)
			if tt.failList {
				require.ErrorContains(t, err, "find related pagerduty incidents", "related list failures should propagate")
				require.Nil(t, actual, "failed related lookup should not return a partial list")
			} else {
				require.NoError(t, err, "related incident lookup should succeed")
				require.Equal(t, []IncidentSummary{{ID: "PI2", Title: "Related"}}, actual, "source incident should not appear in its related list")
			}
			require.Equal(t, 2, calls, "related lookup should use exactly one detail and one list request")
		})
	}
}

func TestPagerDutyHTTPRetriesAndCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                       string
		status                     int
		network, cancel, malformed bool
		succeedAt, calls           int
		expected                   string
	}{
		{name: "transient server recovery", status: 503, succeedAt: 2, calls: 2},
		{name: "rate limit recovery", status: 429, succeedAt: 2, calls: 2},
		{name: "bounded server retries", status: 503, calls: 3, expected: "pagerduty API returned 503: unavailable"},
		{name: "bounded network retries", network: true, calls: 3, expected: "connection failed"},
		{name: "cancel during rate limit backoff", status: 429, cancel: true, calls: 1},
		{name: "cancel during transport backoff", network: true, cancel: true, calls: 1},
		{name: "invalid JSON is not retried", status: 200, malformed: true, calls: 1, expected: "invalid character"},
		{name: "empty client error", status: 404, calls: 1, expected: "pagerduty API returned 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			p := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{AccessToken: "token", WritebackEnabled: true})
			p.httpClient.Transport = pagerDutyTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err, "each retry should have a readable request body")
				require.JSONEq(t, `{"note":{"content":"Checking"}}`, string(body), "retries should replay the full writeback payload")
				if tt.cancel {
					cancel()
				}
				if tt.network {
					return nil, errors.New("connection failed")
				}
				if tt.succeedAt == calls {
					return pagerDutyTestResponse(200, `{"note":{"id":"PN1"}}`), nil
				}
				if tt.malformed {
					return pagerDutyTestResponse(200, "invalid JSON"), nil
				}
				if tt.status == 404 {
					return pagerDutyTestResponse(404, ""), nil
				}
				return pagerDutyTestResponse(tt.status, "unavailable"), nil
			})
			actual, err := p.AddIncidentNote(ctx, "PI1", "Checking")
			switch {
			case tt.cancel:
				require.ErrorIs(t, err, context.Canceled, "cancellation should stop backoff immediately")
			case tt.expected != "":
				require.ErrorContains(t, err, tt.expected, "terminal HTTP failure should retain the dependency diagnostic")
			default:
				require.NoError(t, err, "transient failure should recover within the retry budget")
				require.Equal(t, "PN1", actual, "successful retry should return the note receipt")
			}
			if err != nil {
				require.Empty(t, actual, "failed writes should not return a receipt")
			}
			require.Equal(t, tt.calls, calls, "retry count should obey classification, cancellation, and the attempt bound")
		})
	}
}

func TestPagerDutyBackoffAndRetryAfterBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, header  string
		attempt       int
		parsed, delay time.Duration
	}{
		{"default first", "", 1, 0, 500 * time.Millisecond},
		{"default second", "", 2, 0, time.Second},
		{"server delay", " 2 ", 1, 2 * time.Second, 2 * time.Second},
		{"server cap", "999", 1, 999 * time.Second, 10 * time.Second},
		{"negative ignored", "-1", 1, 0, 500 * time.Millisecond},
		{"invalid ignored", "later", 1, 0, 500 * time.Millisecond},
		{"zero falls back", "0", 2, 0, time.Second},
		{"exponential cap", "", 8, 0, 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsed := parseRetryAfter(tt.header)
			require.Equal(t, tt.parsed, parsed, "Retry-After should accept only nonnegative delta-seconds")
			require.Equal(t, tt.delay, pagerDutyBackoff(tt.attempt, parsed), "backoff should prefer server hints and remain bounded")
		})
	}
}
