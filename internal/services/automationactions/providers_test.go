package automationactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type testTokens struct{}

func (testTokens) GetInstallationToken(context.Context, int64) (string, error) {
	return "github-secret", nil
}

type testCredentials struct{}

func (testCredentials) Get(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if provider == models.ProviderSlack {
		return &models.DecryptedCredential{Config: models.SlackConfig{AccessToken: "slack-secret"}}, nil
	}
	return &models.DecryptedCredential{Config: models.NotionConfig{AccessToken: "notion-secret"}}, nil
}

type testSlack struct {
	channel, text string
	err           error
	calls         int
}

func (s *testSlack) PostMessage(_ context.Context, token, channel, thread, text string) (ingestion.SlackPostedMessage, error) {
	s.channel, s.text = channel, text
	s.calls++
	return ingestion.SlackPostedMessage{Channel: channel, Timestamp: "123.456"}, s.err
}
func providerScope() models.AutomationActionScope {
	return models.AutomationActionScope{Actor: models.AutomationActionActor{OrgID: uuid.New()}, RepositoryName: "owner/repo", PRNumber: 42, Config: models.AutomationActionConfig{Actions: models.AutomationActionKinds(), Repository: "owner/repo", Label: "design-review-requested", Team: "design-reviewers", NotionDataSourceID: "379d5706-2bc0-8021-a0e6-000b04d8902d", SlackChannelID: "C0BMANGRNTZ", NotionProperties: map[string]models.AutomationActionPropertyType{"Report": "title", "Link": "url", "Date": "date", "Summary": "rich_text", "State": "select"}}}
}

func TestProviderInspect(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("a", 40)
	tests := []struct {
		name, repository, response string
		status                     int
		expected                   PullRequest
		expectErr                  bool
	}{
		{"open with configured label", "owner/repo", `{"title":"Review me","html_url":"https://untrusted.example/","state":"open","user":{"login":"person"},"head":{"sha":"` + head + `"},"labels":[{"name":"other"},{"name":"design-review-requested"}]}`, 200, PullRequest{Head: head, Open: true, Title: "Review me", URL: "https://github.com/owner/repo/pull/42", Author: "person"}, false},
		{"closed without configured label", "owner/repo", `{"title":"Closed","state":"closed","user":{"login":"person"},"head":{"sha":"` + head + `"},"labels":[{"name":"other"}]}`, 200, PullRequest{Head: head, Title: "Closed", URL: "https://github.com/owner/repo/pull/42", Author: "person"}, false},
		{"missing author", "owner/repo", `{"title":"Review me"}`, 200, PullRequest{}, true},
		{"missing title", "owner/repo", `{"user":{"login":"person"}}`, 200, PullRequest{}, true},
		{"provider rejection", "owner/repo", `{}`, 403, PullRequest{}, true},
		{"path traversal", "../repo", `{}`, 200, PullRequest{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "GET", r.Method, "PR inspection must be read-only")
				require.Equal(t, "/repos/owner/repo/pulls/42", r.URL.Path, "inspection must use the trusted PR identity")
				w.WriteHeader(tt.status)
				_, err := io.WriteString(w, tt.response)
				require.NoError(t, err, "serve PR metadata")
			}))
			defer server.Close()
			p := NewProviders(testTokens{}, nil, nil)
			p.githubURL = server.URL
			scope := providerScope()
			scope.RepositoryName = tt.repository
			actual, err := p.Inspect(context.Background(), scope)
			if tt.expectErr {
				require.Error(t, err, "invalid or unavailable PR metadata must stop delivery")
			} else {
				require.NoError(t, err, "valid metadata should be inspected")
			}
			require.Equal(t, tt.expected, actual, "inspection should preserve freshness evidence and construct the trusted PR URL")
			if tt.repository == "../repo" {
				require.Equal(t, 0, calls, "invalid repository paths must not reach the provider")
			}
		})
	}
}
func TestProvidersIndependentActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind                   models.AutomationActionKind
		method, path, response string
		receipt                Receipt
	}{
		{models.AutomationActionLabel, "POST", "/repos/owner/repo/issues/42/labels", `[{"name":"design-review-requested"}]`, Receipt{ID: "design-review-requested", URL: "https://github.com/owner/repo/pull/42"}},
		{models.AutomationActionTeam, "POST", "/repos/owner/repo/pulls/42/requested_reviewers", `{}`, Receipt{ID: "design-reviewers", URL: "https://github.com/owner/repo/pull/42"}},
		{models.AutomationActionComment, "POST", "/repos/owner/repo/issues/42/comments", `{"id":123,"html_url":"https://github.com/owner/repo/pull/42#issuecomment-123"}`, Receipt{ID: "123", URL: "https://github.com/owner/repo/pull/42#issuecomment-123"}},
		{models.AutomationActionNotion, "POST", "/v1/pages", `{"id":"379d5706-2bc0-8021-a0e6-000b04d8902d","url":"https://www.notion.so/receipt"}`, Receipt{ID: "379d5706-2bc0-8021-a0e6-000b04d8902d", URL: "https://www.notion.so/receipt"}},
		{models.AutomationActionSlack, "", "", "", Receipt{ID: "C0BMANGRNTZ:123.456"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			t.Parallel()
			var gotPath, gotMethod, gotAuth, gotVersion string
			var body map[string]any
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					require.Equal(t, "/repos/owner/repo/pulls/42/requested_reviewers", r.URL.Path, "only team precondition reads are expected")
					_, err := io.WriteString(w, `{"teams":[]}`)
					require.NoError(t, err, "write team response")
					return
				}
				writes++
				gotPath, gotMethod, gotAuth, gotVersion = r.URL.Path, r.Method, r.Header.Get("Authorization"), r.Header.Get("Notion-Version")
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "decode provider payload")
				_, err := io.WriteString(w, tt.response)
				require.NoError(t, err, "write provider response")
			}))
			defer server.Close()
			slack := &testSlack{}
			p := NewProviders(testTokens{}, testCredentials{}, slack)
			p.githubURL, p.notionURL = server.URL, server.URL
			scope := providerScope()
			id := uuid.New()
			payload := Payload{PR: PullRequest{URL: "https://github.com/owner/repo/pull/42"}, Request: models.AutomationActionRequest{Text: "Needs <@U123> attention | report", Properties: map[string]string{"Report": "Daily report", "Summary": strings.Repeat("😀", 2100), "State": "Complete", "Link": "https://example.org/report", "Date": "2026-09-22"}}}
			receipt, err := p.Send(context.Background(), scope, models.AutomationAction{ID: id, Kind: tt.kind}, payload)
			require.NoError(t, err, "provider should return a confirmed receipt")
			require.Equal(t, tt.receipt, receipt, "store provider identity")
			require.Equal(t, tt.method, gotMethod, "use fixed method")
			require.Equal(t, tt.path, gotPath, "use configured destination")
			switch tt.kind {
			case models.AutomationActionLabel:
				require.Equal(t, map[string]any{"labels": []any{scope.Config.Label}}, body, "send only configured label")
			case models.AutomationActionTeam:
				require.Equal(t, map[string]any{"team_reviewers": []any{scope.Config.Team}}, body, "send only configured team")
			case models.AutomationActionComment:
				require.Equal(t, map[string]any{"body": payload.Request.Text + "\n\n<!-- 143-automation-action:" + id.String() + " -->"}, body, "append stable opaque marker")
			case models.AutomationActionNotion:
				require.Equal(t, "2025-09-03", gotVersion, "use data source API version")
				require.Equal(t, "Bearer notion-secret", gotAuth, "keep Notion credentials server side")
				require.Equal(t, map[string]any{"type": "data_source_id", "data_source_id": scope.Config.NotionDataSourceID}, body["parent"], "create row in configured source")
				props := body["properties"].(map[string]any)
				require.Equal(t, map[string]any{"select": map[string]any{"name": "Complete"}}, props["State"], "record verdict without claiming other deliveries")
				fragments := props["Summary"].(map[string]any)["rich_text"].([]any)
				require.Equal(t, 2, len(fragments), "split Unicode into valid rich text fragments")
			case models.AutomationActionSlack:
				require.Equal(t, scope.Config.SlackChannelID, slack.channel, "send only configured channel")
				require.Equal(t, "Needs &lt;@U123&gt; attention | report", slack.text, "escape user-controlled mention markup")
			}
			if tt.kind == models.AutomationActionSlack {
				require.Equal(t, 0, writes, "Slack uses the existing client")
			} else {
				require.Equal(t, 1, writes, "adapter never retries creates")
			}
		})
	}
}
func TestProviderSlackConfiguredGroup(t *testing.T) {
	t.Parallel()
	for _, group := range []string{"", "S0123456789"} {
		t.Run("group="+group, func(t *testing.T) {
			t.Parallel()
			scope := providerScope()
			scope.Config.SlackUserGroupID = group
			slack := &testSlack{}
			provider := NewProviders(testTokens{}, testCredentials{}, slack)
			payload := Payload{Request: models.AutomationActionRequest{Text: "Review <!subteam^S9876543210> <@U123> <!here> &amp; <!channel>"}}
			receipt, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, payload)
			require.NoError(t, err, "send to the configured Slack destination")
			want := "Review &lt;!subteam^S9876543210&gt; &lt;@U123&gt; &lt;!here&gt; &amp;amp; &lt;!channel&gt;"
			if group != "" {
				want = "<!subteam^S0123456789> " + want
			}
			require.Equal(t, want, slack.text, "only the configured group can produce a mention")
			require.Equal(t, scope.Config.SlackChannelID, slack.channel, "the group does not change the configured channel")
			require.Equal(t, scope.Config.SlackChannelID+":123.456", receipt.ID, "keep the original Slack receipt identity")
		})
	}
}

func TestProviderSlackPRReviewLinks(t *testing.T) {
	t.Parallel()
	const reviewURL = "https://github.com/owner/repo/pull/42#pullrequestreview-123"
	const markdownLink = "[PR #42](" + reviewURL + ")"
	const renderedLink = "<" + reviewURL + "|PR #42>"
	tests := []struct {
		name, text, want string
	}{
		{name: "review link", text: markdownLink, want: renderedLink},
		{
			name: "repository case",
			text: "[PR #42](https://github.com/OWNER/Repo/pull/42#pullrequestreview-123)",
			want: "<https://github.com/OWNER/Repo/pull/42#pullrequestreview-123|PR #42>",
		},
		{
			name: "format and mentions",
			text: "*:large_yellow_circle: Design review · Medium · 1 finding*\n" + markdownLink +
				" — title\n*Fix* Use `Card`. <!here> <@U123> <!subteam^S9876543210> &",
			want: "*:large_yellow_circle: Design review · Medium · 1 finding*\n" + renderedLink +
				" — title\n*Fix* Use `Card`. &lt;!here&gt; &lt;@U123&gt; &lt;!subteam^S9876543210&gt; &amp;",
		},
		{name: "different repository", text: "[PR #42](https://github.com/other/repo/pull/42#pullrequestreview-123)"},
		{name: "different PR", text: "[PR #41](" + reviewURL + ")"},
		{name: "other link label", text: "[Review](" + reviewURL + ")"},
		{name: "nested label", text: "[PR #42 [review]](" + reviewURL + ")"},
		{name: "leading zero PR", text: "[PR #042](https://github.com/owner/repo/pull/042#pullrequestreview-123)"},
		{name: "zero review", text: "[PR #42](https://github.com/owner/repo/pull/42#pullrequestreview-0)"},
		{name: "leading zero review", text: "[PR #42](https://github.com/owner/repo/pull/42#pullrequestreview-0123)"},
		{name: "query", text: "[PR #42](https://github.com/owner/repo/pull/42?x=1#pullrequestreview-123)"},
		{name: "userinfo", text: "[PR #42](https://user@github.com/owner/repo/pull/42#pullrequestreview-123)"},
		{name: "port", text: "[PR #42](https://github.com:443/owner/repo/pull/42#pullrequestreview-123)"},
		{name: "other host", text: "[PR #42](https://github.com.evil.test/owner/repo/pull/42#pullrequestreview-123)"},
		{name: "http", text: "[PR #42](http://github.com/owner/repo/pull/42#pullrequestreview-123)"},
		{name: "encoded path", text: "[PR #42](https://github.com/owner/%72epo/pull/42#pullrequestreview-123)"},
		{name: "control character", text: "[PR #42](https://github.com/owner/repo/pull/42#pullrequestreview-123\n)"},
		{name: "Slack delimiter", text: "[PR #42](https://github.com/owner/repo/pull/42#pullrequestreview-123|<!here>)", want: "[PR #42](https://github.com/owner/repo/pull/42#pullrequestreview-123|&lt;!here&gt;)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := providerScope()
			scope.Config.SlackUserGroupID = "S0123456789"
			slack := &testSlack{}
			provider := NewProviders(testTokens{}, testCredentials{}, slack)
			payload := Payload{Request: models.AutomationActionRequest{Text: tt.text}}
			receipt, err := provider.Send(context.Background(), scope, models.AutomationAction{Kind: models.AutomationActionSlack}, payload)
			require.NoError(t, err, "render links without changing delivery behavior")
			want := tt.want
			if want == "" {
				want = tt.text
			}
			require.Equal(t, "<!subteam^S0123456789> "+want, slack.text, "only the scoped PR review link receives native Slack markup")
			require.Equal(t, scope.Config.SlackChannelID, slack.channel, "retain the configured channel")
			require.Equal(t, scope.Config.SlackChannelID+":123.456", receipt.ID, "retain the original receipt identity")
		})
	}
}

func TestProvidersFailureClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		body       string
		definitive bool
	}{
		{"denied", 403, `{"error":"secret"}`, true}, {"rate limited", 429, `{}`, true}, {"method rejected", 405, `{}`, true}, {"gone", 410, `{}`, true}, {"legally blocked", 451, `{}`, true}, {"timeout", 408, `{}`, false}, {"server error", 502, `{}`, false}, {"redirect", 307, `{}`, false}, {"malformed receipt", 201, `oops`, false}, {"empty receipt", 201, `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://example.invalid/leak")
				w.WriteHeader(tt.status)
				_, err := io.WriteString(w, tt.body)
				require.NoError(t, err, "serve provider failure")
			}))
			defer server.Close()
			p := NewProviders(testTokens{}, testCredentials{}, nil)
			p.githubURL = server.URL
			_, err := p.Send(context.Background(), providerScope(), models.AutomationAction{ID: uuid.New(), Kind: models.AutomationActionComment}, Payload{})
			var failure *SendFailure
			require.ErrorAs(t, err, &failure, "classify provider uncertainty")
			require.Equal(t, tt.definitive, failure.Definitive, "retry only explicit rejection")
			require.NotContains(t, err.Error(), "secret", "provider bodies must not leak")
			require.Equal(t, 1, calls, "never retry or follow redirect")
		})
	}
}
func TestProviderPreflightAndExistingTeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		missing bool
	}{{"valid mapping", false}, {"wrong schema", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "GET", r.Method, "preflight and existing team must not write")
				if strings.HasSuffix(r.URL.Path, "requested_reviewers") {
					_, err := io.WriteString(w, `{"teams":[{"slug":"design-reviewers"}]}`)
					require.NoError(t, err, "serve requested team")
					return
				}
				body := `{"properties":{"Report":{"type":"title"},"Link":{"type":"url"},"Date":{"type":"date"},"Summary":{"type":"rich_text"},"State":{"type":"select","select":{"options":[{"name":"Complete"}]}},"Author":{"type":"rich_text"}}}`
				if tt.missing {
					body = `{"properties":{}}`
				}
				_, err := fmt.Fprint(w, body)
				require.NoError(t, err, "serve schema")
			}))
			defer server.Close()
			p := NewProviders(testTokens{}, testCredentials{}, nil)
			p.githubURL, p.notionURL = server.URL, server.URL
			err := p.Preflight(context.Background(), providerScope(), models.AutomationActionRequest{Kind: models.AutomationActionNotion, Properties: map[string]string{"Report": "Daily", "State": "Complete"}})
			if tt.missing {
				require.Error(t, err, "schema mismatch must block reservation")
			} else {
				require.NoError(t, err, "known properties must pass")
			}
			receipt, err := p.Send(context.Background(), providerScope(), models.AutomationAction{Kind: models.AutomationActionTeam}, Payload{})
			require.NoError(t, err, "existing team needs no request")
			require.Equal(t, Receipt{ID: "design-reviewers"}, receipt, "existing team is a confirmed effect")
		})
	}
}

func TestSlackDeliveryFailureCertainty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		definitive bool
	}{
		{"rejected", &ingestion.SlackMessageError{Code: "not_in_channel"}, true},
		{"rate limited", ingestion.ErrSlackRateLimited, true},
		{"internal error", &ingestion.SlackMessageError{Code: "internal_error"}, false},
		{"fatal partial error", &ingestion.SlackMessageError{Code: "fatal_error"}, false},
		{"unknown error", &ingestion.SlackMessageError{Code: "new_error"}, false},
		{"restricted", &ingestion.SlackMessageError{Code: "restricted_action"}, true},
		{"read only", &ingestion.SlackMessageError{Code: "restricted_action_read_only_channel"}, true},
		{"encryption restriction", &ingestion.SlackMessageError{Code: "ekm_access_denied"}, true},
		{"invalid arguments", &ingestion.SlackMessageError{Code: "invalid_arguments"}, true},
		{"body rate limit", &ingestion.SlackMessageError{Code: "ratelimited"}, true},
		{"timeout", context.DeadlineExceeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := NewProviders(testTokens{}, testCredentials{}, &testSlack{err: tt.err})
			_, err := p.Send(context.Background(), providerScope(), models.AutomationAction{Kind: models.AutomationActionSlack}, Payload{})
			require.Error(t, err, "surface failed delivery")
			var failure *SendFailure
			definite := errors.As(err, &failure) && failure.Definitive
			require.Equal(t, tt.definitive, definite, "only proven rejection permits resume")
		})
	}
}
