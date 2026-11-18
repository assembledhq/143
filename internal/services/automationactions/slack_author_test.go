package automationactions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestProviderSlackPRAuthor(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "author lookup must only read GitHub")
		require.Empty(t, r.URL.RawQuery, "the map must come from the default branch")
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			_, err := w.Write([]byte(`{"title":"Synthetic PR","state":"open","user":{"login":"Real-Author"}}`))
			require.NoError(t, err, "serve the bound PR author")
		case "/repos/owner/repo/contents/config/team.json":
			data := []byte(`[{"github_name":"real-author","slack_id":"U0123456789","private_field":"private-marker"},{"github_name":"agent-choice","slack_id":"U9876543210"},{"github_name":42,"slack_id":{"private_field":"ignored"}}]`)
			err := json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "encoding": "base64", "size": len(data),
				"content": base64.StdEncoding.EncodeToString(data), "download_url": "https://unexpected.invalid/ignore",
			})
			require.NoError(t, err, "serve the configured map envelope")
		default:
			t.Errorf("unexpected GitHub path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	slack := &testSlack{}
	tokenCalls := 0
	tokens := slackAuthorTokensFunc(func(ctx context.Context, _ int64) (string, error) {
		tokenCalls++
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "author lookup must establish its own shared deadline")
		require.LessOrEqual(t, time.Until(deadline), 2*time.Second, "all author reads share a bounded lookup budget")
		return "github-secret", nil
	})
	provider := NewProviders(tokens, testCredentials{}, slack)
	provider.githubURL = server.URL
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	provider.githubClient.Transport = transport
	scope := providerScope()
	scope.TargetID = uuid.New()
	scope.Config.SlackUserGroupID = "S0123456789"
	scope.Config.SlackPRAuthorMapPath = "config/team.json"
	payload := Payload{
		Request: models.AutomationActionRequest{PRNumber: 999, Text: "Fix <@U9876543210>"},
		PR:      PullRequest{Author: "agent-choice"},
	}
	receipt, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, payload)
	require.NoError(t, err, "the mapped live PR author should be mentioned")
	require.Equal(t, "<!subteam^S0123456789> <@U0123456789> Fix &lt;@U9876543210&gt;", slack.text, "only the server-bound author and configured group may become mentions")
	require.Equal(t, scope.Config.SlackChannelID+":123.456", receipt.ID, "author mention keeps the original receipt identity")
	require.Equal(t, 1, slack.calls, "author lookup must not duplicate Slack delivery")
	require.Equal(t, 1, tokenCalls, "PR and map reads reuse one installation token")
}

type slackAuthorResponse struct {
	prBody, fileBody     string
	prStatus, fileStatus int
}

func slackAuthorMapResponse(data string) string {
	return fmt.Sprintf(`{"type":"file","encoding":"base64","size":%d,"content":%q}`, len(data), base64.StdEncoding.EncodeToString([]byte(data)))
}

func slackAuthorProvider(t *testing.T, response slackAuthorResponse) (*Providers, models.AutomationActionScope, *testSlack) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "author lookup must only read GitHub")
		require.Empty(t, r.URL.RawQuery, "the map must use the default branch")
		status, body := response.fileStatus, response.fileBody
		if r.URL.Path == "/repos/owner/repo/pulls/42" {
			status, body = response.prStatus, response.prBody
			if body == "" {
				body = `{"title":"Synthetic PR","state":"open","user":{"login":"Real-Author"}}`
			}
		} else {
			require.Equal(t, "/repos/owner/repo/contents/config/team.json", r.URL.Path, "read only the configured mapping file")
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, err := w.Write([]byte(body))
		require.NoError(t, err, "serve the synthetic GitHub response")
	}))
	t.Cleanup(server.Close)
	slack := &testSlack{}
	provider := NewProviders(testTokens{}, testCredentials{}, slack)
	provider.githubURL = server.URL
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	provider.githubClient.Transport = transport
	scope := providerScope()
	scope.TargetID = uuid.New()
	scope.Config.SlackUserGroupID = "S0123456789"
	scope.Config.SlackPRAuthorMapPath = "config/team.json"
	return provider, scope, slack
}

func TestProviderSlackPRAuthorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, data, reason, mention string
	}{
		{name: "legacy ID", data: `[{"github_name":"REAL-AUTHOR","slack_id":"W0123456789"}]`, mention: "<@W0123456789>"},
		{name: "missing", data: `[]`, reason: "missing_mapping"},
		{name: "different author", data: `[{"github_name":"other","slack_id":"U0123456789"}]`, reason: "missing_mapping"},
		{name: "duplicate", data: `[{"github_name":"Real-Author","slack_id":"U0123456789"},{"github_name":"real-author","slack_id":"U9876543210"}]`, reason: "duplicate_mapping"},
		{name: "missing ID", data: `[{"github_name":"Real-Author"}]`, reason: "invalid_mapping"},
		{name: "number ID", data: `[{"github_name":"Real-Author","slack_id":123}]`, reason: "invalid_mapping"},
		{name: "group ID", data: `[{"github_name":"Real-Author","slack_id":"S0123456789"}]`, reason: "invalid_mapping"},
		{name: "mention markup", data: `[{"github_name":"Real-Author","slack_id":"U0123456789> <!here>"}]`, reason: "invalid_mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, scope, slack := slackAuthorProvider(t, slackAuthorResponse{fileBody: slackAuthorMapResponse(tt.data)})
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			_, err := provider.Send(logger.WithContext(context.Background()), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
			require.NoError(t, err, "an unmatched or invalid selected mapping should preserve the team alert")
			want := "<!subteam^S0123456789> "
			if tt.mention != "" {
				want += tt.mention + " "
			}
			require.Equal(t, want+"Alert", slack.text, "only one valid selected identity may become a mention")
			require.Equal(t, 1, slack.calls, "fallback sends the group alert once")
			if tt.reason == "" {
				require.Empty(t, logs.String(), "a valid identity should not need a fallback warning")
			} else {
				require.Contains(t, logs.String(), `"reason":"`+tt.reason+`"`, "fallback should expose a structured diagnostic reason")
			}
			require.NotContains(t, logs.String(), "Real-Author", "warnings must not expose mapping identities")
			require.NotContains(t, logs.String(), "U0123456789", "warnings must not expose Slack identities")
		})
	}
}

func TestProviderSlackPRAuthorLookupFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		response      slackAuthorResponse
		missingTokens bool
	}{
		{name: "missing credentials", missingTokens: true},
		{name: "PR unavailable", response: slackAuthorResponse{prStatus: http.StatusServiceUnavailable}},
		{name: "malformed PR", response: slackAuthorResponse{prBody: `{`}},
		{name: "missing PR author", response: slackAuthorResponse{prBody: `{"title":"Synthetic PR"}`}},
		{name: "map unavailable", response: slackAuthorResponse{fileStatus: http.StatusNotFound, fileBody: `{}`}},
		{name: "malformed envelope", response: slackAuthorResponse{fileBody: `{`}},
		{name: "directory", response: slackAuthorResponse{fileBody: `{"type":"dir","encoding":"base64","content":"W10="}`}},
		{name: "encoding", response: slackAuthorResponse{fileBody: `{"type":"file","encoding":"none","content":"[]"}`}},
		{name: "base64", response: slackAuthorResponse{fileBody: `{"type":"file","encoding":"base64","content":"bad!"}`}},
		{name: "JSON", response: slackAuthorResponse{fileBody: slackAuthorMapResponse(`[{`)}},
		{name: "object", response: slackAuthorResponse{fileBody: slackAuthorMapResponse(`{}`)}},
		{name: "null", response: slackAuthorResponse{fileBody: slackAuthorMapResponse(`null`)}},
		{name: "large file", response: slackAuthorResponse{fileBody: `{"type":"file","encoding":"base64","size":524289,"content":"W10="}`}},
		{name: "large content", response: slackAuthorResponse{fileBody: strings.Replace(slackAuthorMapResponse(strings.Repeat(" ", 524289)), `"size":524289`, `"size":0`, 1)}},
		{name: "invalid UTF8", response: slackAuthorResponse{fileBody: slackAuthorMapResponse(string([]byte{0xff}))}},
		{name: "negative size", response: slackAuthorResponse{fileBody: `{"type":"file","encoding":"base64","size":-1,"content":"W10="}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, scope, slack := slackAuthorProvider(t, tt.response)
			if tt.missingTokens {
				provider.tokens = nil
			}
			_, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
			var failure *SendFailure
			require.ErrorAs(t, err, &failure, "a failed author lookup must stop before delivery")
			require.Equal(t, "SLACK_PR_AUTHOR_LOOKUP_FAILED", failure.Code, "lookup failures have a stable retryable code")
			require.True(t, failure.Definitive, "failed reads establish that Slack was never called")
			require.Error(t, failure.Err, "preserve the underlying lookup error")
			require.Zero(t, slack.calls, "lookup failure must not send a partial alert")
		})
	}
}

type slackAuthorTokensFunc func(context.Context, int64) (string, error)

func (f slackAuthorTokensFunc) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	return f(ctx, installationID)
}

func TestProviderSlackPRAuthorScopeAndCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*models.AutomationActionScope)
	}{
		{name: "no map", change: func(s *models.AutomationActionScope) { s.Config.SlackPRAuthorMapPath = "" }},
		{name: "no target", change: func(s *models.AutomationActionScope) { s.TargetID = uuid.Nil }},
		{name: "no PR", change: func(s *models.AutomationActionScope) { s.PRNumber = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, scope, slack := slackAuthorProvider(t, slackAuthorResponse{})
			tt.change(&scope)
			provider.tokens = slackAuthorTokensFunc(func(context.Context, int64) (string, error) {
				t.Error("a run outside the author lookup scope must not request GitHub credentials")
				return "", errors.New("unexpected lookup")
			})
			_, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
			require.NoError(t, err, "other Slack run modes keep their existing behavior")
			require.Equal(t, "<!subteam^S0123456789> Alert", slack.text, "a non-target run does not acquire an author recipient")
		})
	}
	t.Run("canceled before send", func(t *testing.T) {
		t.Parallel()
		provider, scope, slack := slackAuthorProvider(t, slackAuthorResponse{})
		scope.TargetID = uuid.Nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := provider.Send(ctx, scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
		var failure *SendFailure
		require.ErrorAs(t, err, &failure, "known cancellation stops before calling Slack")
		require.True(t, failure.Definitive, "a canceled pre-send is safe to resume")
		require.ErrorIs(t, failure.Err, context.Canceled, "preserve the canceled parent context")
		require.Zero(t, slack.calls, "cancellation must not send an alert")
	})
	t.Run("bounded lookup", func(t *testing.T) {
		t.Parallel()
		provider, scope, slack := slackAuthorProvider(t, slackAuthorResponse{})
		provider.tokens = slackAuthorTokensFunc(func(ctx context.Context, _ int64) (string, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "lookup credentials must receive the shared deadline")
			require.LessOrEqual(t, time.Until(deadline), 2*time.Second, "author lookup must be bounded")
			<-ctx.Done()
			return "", ctx.Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := provider.Send(ctx, scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
		var failure *SendFailure
		require.ErrorAs(t, err, &failure, "a timed out lookup stops before Slack")
		require.True(t, failure.Definitive, "a timed out read establishes no delivery")
		require.ErrorIs(t, failure.Err, context.DeadlineExceeded, "preserve the shared lookup deadline failure")
		require.Zero(t, slack.calls, "a timed out lookup must not send an alert")
	})
}

func TestProviderSlackPRAuthorPostUncertainty(t *testing.T) {
	t.Parallel()
	response := slackAuthorResponse{fileBody: slackAuthorMapResponse(`[{"github_name":"Real-Author","slack_id":"U0123456789"}]`)}
	provider, scope, slack := slackAuthorProvider(t, response)
	slack.err = context.DeadlineExceeded
	_, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{Request: models.AutomationActionRequest{Text: "Alert"}})
	require.ErrorIs(t, err, context.DeadlineExceeded, "the Slack POST may have succeeded after the lookup completed")
	var failure *SendFailure
	require.False(t, errors.As(err, &failure), "a POST timeout must not inherit the definitive lookup-failure classification")
	require.Equal(t, 1, slack.calls, "classify the single attempted POST without retrying it")
}
