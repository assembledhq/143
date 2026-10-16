package automationactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type actionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f actionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func actionRateLimitController(t *testing.T, now time.Time) *ratelimit.Controller {
	t.Helper()
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, AppID: 1, Environment: "test", Logger: zerolog.Nop(),
		InstallationAllowlist: []int64{42, 43}, Now: func() time.Time { return now },
	})
	require.NoError(t, err, "construct isolated action cooldown controller")
	return controller
}

func blockActionInstallation(t *testing.T, controller *ratelimit.Controller, now time.Time) *ratelimit.Deferral {
	t.Helper()
	permit, err := controller.Before(context.Background(), ratelimit.Scope{InstallationID: 42})
	require.NoError(t, err, "seed the known installation before its quota is exhausted")
	result := controller.Observe(context.Background(), permit, ratelimit.Observation{
		StatusCode: http.StatusForbidden, Header: http.Header{
			"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Resource": {"core"},
			"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(time.Minute).Unix(), 10)},
		},
	})
	require.NotNil(t, result.Deferral, "exhausted quota must establish the shared cooldown")
	return result.Deferral
}

func TestGitHubActionsHonorKnownCooldownBeforeOutbound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind models.AutomationActionKind
	}{
		{name: "inspection"},
		{name: "label", kind: models.AutomationActionLabel},
		{name: "team precondition", kind: models.AutomationActionTeam},
		{name: "comment", kind: models.AutomationActionComment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			controller := actionRateLimitController(t, now)
			blocked := blockActionInstallation(t, controller, now)
			outbound := 0
			client := githubtelemetry.NewControlledHTTPClient(time.Minute, zerolog.Nop(), controller, "automation_action", actionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				outbound++
				return nil, errors.New("unexpected outbound request")
			}))
			provider := NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client))
			scope := providerScope()
			scope.InstallationID = 42
			var err error
			if tt.kind == "" {
				_, err = provider.Inspect(context.Background(), scope)
			} else {
				_, err = provider.Send(context.Background(), scope, models.AutomationAction{Kind: tt.kind}, Payload{})
			}
			deferral, ok := ratelimit.AsDeferral(err)
			require.True(t, ok, "preserve the typed cooldown deadline across adapter failures")
			require.Equal(t, blocked, deferral, "use the installation's authoritative cooldown")
			require.Equal(t, 0, outbound, "suppressed reads and mutations must consume no provider requests")
			if tt.kind != "" {
				var failure *SendFailure
				require.ErrorAs(t, err, &failure, "a suppressed mutation must carry delivery certainty")
				require.True(t, failure.Definitive, "local suppression proves that no mutation occurred")
			}
		})
	}
}

func TestGitHubActionMetadataAndInstallationIsolation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	controller := actionRateLimitController(t, now)
	blockActionInstallation(t, controller, now)
	var logs bytes.Buffer
	var metadata githubtelemetry.RequestMetadata
	outbound := 0
	base := actionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		outbound++
		metadata, _ = githubtelemetry.RequestMetadataFromContext(r.Context())
		require.Equal(t, "Bearer github-secret", r.Header.Get("Authorization"), "use the installation token without modifying its scope")
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":123,"html_url":"https://github.com/owner/repo/pull/42#issuecomment-123"}`))}, nil
	})
	client := githubtelemetry.NewControlledHTTPClient(time.Minute, zerolog.New(&logs), controller, "automation_action", base)
	provider := NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client))
	scope := providerScope()
	scope.InstallationID = 43
	scope.Actor.SessionID = uuid.New()
	ctx := githubtelemetry.WithRequestMetadata(context.Background(), githubtelemetry.RequestMetadata{InstallationID: 42, Caller: "inherited", SyncReason: "stale_reconcile"})
	receipt, err := provider.Send(ctx, scope, models.AutomationAction{ID: uuid.New(), Kind: models.AutomationActionComment}, Payload{Request: models.AutomationActionRequest{Text: "private action body"}})
	require.NoError(t, err, "one installation's cooldown must not suppress another installation using the same token fixture")
	require.Equal(t, Receipt{ID: "123", URL: "https://github.com/owner/repo/pull/42#issuecomment-123"}, receipt, "retain the confirmed mutation receipt")
	require.Equal(t, 1, outbound, "send the mutation exactly once")
	require.Equal(t, githubtelemetry.RequestMetadata{Kind: githubtelemetry.RequestKindAPI, AuthType: githubtelemetry.AuthTypeAppInstallation, InstallationID: 43, Caller: "automation_action", OrgID: scope.Actor.OrgID.String(), SessionID: scope.Actor.SessionID.String(), Repository: "owner/repo"}, metadata, "attribute the authenticated installation and authorized action scope explicitly")
	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event), "successful automation mutation should emit request telemetry")
	require.Equal(t, "automation_action", event["github_caller"], "attribute observed requests to automation actions")
	require.Equal(t, float64(43), event["github_installation_id"], "log the authenticated installation rather than a token-derived guess")
	require.NotContains(t, logs.String(), "github-secret", "telemetry must exclude credentials")
	require.NotContains(t, logs.String(), "private action body", "telemetry must exclude action content")
	require.Equal(t, 4*time.Second, provider.githubClient.Timeout, "shared transport must retain the bounded action timeout")
	require.Equal(t, time.Minute, client.Timeout, "installing an action client must not mutate the shared client")
	require.ErrorIs(t, provider.githubClient.CheckRedirect(nil, nil), http.ErrUseLastResponse, "action requests must not follow provider redirects")
}

func TestGitHubCooldownDoesNotInterceptNotionActions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	controller := actionRateLimitController(t, now)
	blockActionInstallation(t, controller, now)
	githubOutbound, notionOutbound := 0, 0
	client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "automation_action", actionRoundTripFunc(func(*http.Request) (*http.Response, error) {
		githubOutbound++
		return nil, errors.New("Notion request reached GitHub transport")
	}))
	provider := NewProviders(testTokens{}, testCredentials{}, nil, WithGitHubHTTPClient(client))
	provider.client.Transport = actionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		notionOutbound++
		require.Equal(t, "api.notion.com", r.URL.Host, "retain the fixed Notion endpoint")
		require.Equal(t, "2025-09-03", r.Header.Get("Notion-Version"), "retain Notion's configured API version")
		body := `{"properties":{"Report":{"type":"title"}}}`
		if r.Method == http.MethodPost {
			body = `{"id":"379d5706-2bc0-8021-a0e6-000b04d8902d","url":"https://www.notion.so/receipt"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	scope := providerScope()
	scope.InstallationID = 42
	request := models.AutomationActionRequest{Kind: models.AutomationActionNotion, Properties: map[string]string{"Report": "Daily"}}
	require.NoError(t, provider.Preflight(context.Background(), scope, request), "GitHub cooldowns must not block Notion preflight")
	receipt, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionNotion}, Payload{Request: request})
	require.NoError(t, err, "GitHub cooldowns must not block Notion writes")
	require.Equal(t, Receipt{ID: "379d5706-2bc0-8021-a0e6-000b04d8902d", URL: "https://www.notion.so/receipt"}, receipt, "retain Notion's confirmed provider receipt")
	require.Equal(t, 2, notionOutbound, "preflight and create must use the existing Notion client")
	require.Equal(t, 0, githubOutbound, "Notion must never use the GitHub controlled transport")
}

func TestGitHubActionProviderThrottleAndPermissionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body           string
		status               int
		header               http.Header
		deferred, definitive bool
	}{
		{name: "permission rejection", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`, definitive: true},
		{name: "secondary throttle header", status: http.StatusTooManyRequests, body: `{"message":"Too many requests"}`, header: http.Header{"Retry-After": {"60"}}, deferred: true, definitive: true},
		{name: "secondary throttle body", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit"}`, deferred: true, definitive: true},
		{name: "server uncertainty", status: http.StatusBadGateway, body: `{"message":"upstream unavailable"}`},
		{name: "transport uncertainty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller := actionRateLimitController(t, time.Now().UTC())
			outbound := 0
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "automation_action", actionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				outbound++
				if tt.status == 0 {
					return nil, errors.New("connection interrupted after dispatch")
				}
				header := tt.header.Clone()
				if header == nil {
					header = make(http.Header)
				}
				return &http.Response{StatusCode: tt.status, Header: header, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			}))
			provider := NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client))
			scope := providerScope()
			scope.InstallationID = 42
			receipt, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionComment}, Payload{})
			var failure *SendFailure
			require.ErrorAs(t, err, &failure, "retain action send certainty alongside provider throttle metadata")
			require.Equal(t, tt.definitive, failure.Definitive, "only explicit provider rejection can make mutation retries safe")
			_, deferred := ratelimit.AsDeferral(err)
			require.Equal(t, tt.deferred, deferred, "permission rejections and transport failures must not masquerade as throttles")
			require.Equal(t, Receipt{}, receipt, "rejected or uncertain requests must not fabricate a receipt")
			require.Equal(t, 1, outbound, "never replay an action mutation inside the provider adapter")
			if tt.status > 0 {
				require.Equal(t, "PROVIDER_HTTP_"+strconv.Itoa(tt.status), failure.Code, "preserve the provider response status for callers")
			}
		})
	}
}

func TestGitHubActionServiceRetainsDeliveryCertainty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                   string
		exhaustInspection, throttleMutation, interruptMutation bool
		status                                                 models.AutomationActionStatus
		code                                                   string
		mutations                                              int
		deferred                                               bool
	}{
		{name: "cooldown begins after freshness read", exhaustInspection: true, status: models.AutomationActionFailed, code: "GITHUB_RATE_LIMITED", deferred: true},
		{name: "provider rejects mutation", throttleMutation: true, status: models.AutomationActionFailed, code: "PROVIDER_HTTP_429", mutations: 1, deferred: true},
		{name: "transport outcome is unknown", interruptMutation: true, status: models.AutomationActionUnknown, code: "PROVIDER_TRANSPORT_UNKNOWN", mutations: 1},
		{name: "confirmed receipt is reused", status: models.AutomationActionSucceeded, mutations: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			controller := actionRateLimitController(t, now)
			store, _, request := actionFixture()
			request.Kind, request.PRNumber, request.HeadSHA = models.AutomationActionComment, 42, strings.Repeat("a", 40)
			actor := models.AutomationActionActor{OrgID: uuid.New(), RunID: uuid.New()}
			store.scope.Actor, store.scope.InstallationID = actor, 42
			reads, mutations := 0, 0
			client := githubtelemetry.NewControlledHTTPClient(time.Second, zerolog.Nop(), controller, "automation_action", actionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				header := make(http.Header)
				if r.Method == http.MethodGet {
					reads++
					if tt.exhaustInspection {
						header.Set("X-RateLimit-Remaining", "0")
						header.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(time.Minute).Unix(), 10))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"title":"Review me","state":"open","user":{"login":"person"},"head":{"sha":"` + request.HeadSHA + `"}}`))}, nil
				}
				mutations++
				if tt.interruptMutation {
					return nil, errors.New("connection interrupted after mutation dispatch")
				}
				if tt.throttleMutation {
					header.Set("Retry-After", "60")
					return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(strings.NewReader(`{"message":"secondary rate limit"}`))}, nil
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: header, Body: io.NopCloser(strings.NewReader(`{"id":123,"html_url":"https://github.com/owner/repo/pull/42#issuecomment-123"}`))}, nil
			}))
			service := New(store, NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client)), zerolog.Nop())
			result, err := service.Execute(context.Background(), actor, request.AutomationActionKey, &request)
			_, deferred := ratelimit.AsDeferral(err)
			require.Equal(t, tt.deferred, deferred, "retain typed throttle information after recording the action outcome")
			if !tt.deferred {
				require.NoError(t, err, "recorded success or uncertainty should return the authoritative action state")
			}
			require.Equal(t, tt.status, result.Actions[0].Status, "known unsent requests must remain retryable while uncertain mutations remain fenced")
			require.Equal(t, tt.code, *result.Actions[0].LastErrorCode, "preserve the reason for the terminal attempt without inventing delivery uncertainty")
			wantReceipt := ""
			if tt.status == models.AutomationActionSucceeded {
				wantReceipt = "123"
			}
			require.Equal(t, wantReceipt, *result.Actions[0].ProviderObjectID, "only confirmed provider receipts establish delivery")
			actionID := result.Actions[0].ID
			resumed, resumeErr := service.Execute(context.Background(), actor, request.AutomationActionKey, nil)
			if tt.deferred {
				_, ok := ratelimit.AsDeferral(resumeErr)
				require.True(t, ok, "resuming during a known cooldown must still preserve the deferral")
			} else {
				require.NoError(t, resumeErr, "resuming a completed or uncertain mutation returns its recorded state")
			}
			require.Equal(t, actionID, resumed.Actions[0].ID, "resume must retain the original durable action identity")
			require.Equal(t, tt.status, resumed.Actions[0].Status, "resume must preserve certainty while the cooldown is active")
			require.Equal(t, 1, reads, "known cooldowns and recorded mutation outcomes must suppress duplicate freshness reads")
			require.Equal(t, tt.mutations, mutations, "never replay suppressed, successful, or uncertain mutations")
		})
	}
}
