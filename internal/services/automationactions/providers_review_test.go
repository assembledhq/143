package automationactions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProvidersGitHubReview(t *testing.T) {
	t.Parallel()
	const reviewURL = "https://github.com/owner/repo/pull/42#pullrequestreview-314"
	head := strings.Repeat("a", 40)
	tests := []struct {
		name       string
		change     func(*models.AutomationActionScope, *Payload)
		status     int
		response   string
		transport  error
		wantCode   string
		definitive bool
	}{
		{name: "advisory review", status: 200},
		{name: "canonical repository casing", status: 200, response: `{"id":314,"state":"COMMENTED","commit_id":"` + head + `","html_url":"https://github.com/Owner/Repo/pull/42#pullrequestreview-314"}`},
		{name: "default summary", change: func(_ *models.AutomationActionScope, payload *Payload) { payload.Request.Text = "" }, status: 200},
		{name: "other repository", change: func(scope *models.AutomationActionScope, _ *Payload) { scope.Config.Repository = "other/repo" }, wantCode: "GITHUB_REVIEW_TARGET_INVALID", definitive: true},
		{name: "other target", change: func(scope *models.AutomationActionScope, _ *Payload) { scope.PRNumber = 43 }, wantCode: "GITHUB_REVIEW_TARGET_INVALID", definitive: true},
		{name: "invalid author", change: func(_ *models.AutomationActionScope, payload *Payload) { payload.PR.Author = "other @org/team" }, wantCode: "GITHUB_REVIEW_TARGET_INVALID", definitive: true},
		{name: "stale head", change: func(_ *models.AutomationActionScope, payload *Payload) { payload.PR.Head = strings.Repeat("b", 40) }, wantCode: "GITHUB_REVIEW_STALE_HEAD", definitive: true},
		{name: "invalid diff anchor", status: 422, response: `{"message":"Validation Failed"}`, wantCode: "PROVIDER_HTTP_422", definitive: true},
		{name: "timeout may have sent", status: 200, transport: context.DeadlineExceeded, wantCode: "PROVIDER_TRANSPORT_UNKNOWN"},
		{name: "server error may have sent", status: 500, response: `{}`, wantCode: "PROVIDER_HTTP_500"},
		{name: "malformed response may have sent", status: 200, response: `{"id":`, wantCode: "PROVIDER_RESPONSE_UNKNOWN"},
		{name: "wrong PR receipt", status: 200, response: `{"id":314,"state":"COMMENTED","commit_id":"` + head + `","html_url":"https://github.com/owner/repo/pull/43#pullrequestreview-314"}`, wantCode: "GITHUB_REVIEW_RECEIPT_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := providerScope()
			scope.Config.Actions = []models.AutomationActionKind{models.AutomationActionGitHubReview}
			action := models.AutomationAction{ID: uuid.New(), Kind: models.AutomationActionGitHubReview}
			marker := "\n\n<!-- 143-automation-review:" + action.ID.String() + " -->"
			payload := Payload{
				Request: models.AutomationActionRequest{
					AutomationActionKey: models.AutomationActionKey{OperationKey: "review:42", ActionKey: "inline"},
					Kind:                action.Kind,
					PRNumber:            42,
					HeadSHA:             head,
					Text:                "  Two UI findings.  ",
					Comments: []models.AutomationActionReviewComment{
						{Path: "ui/Card.tsx", Line: 10, Body: "Use the shared Card."},
						{Path: "ui/colors.css", Line: 42, Body: "Use a theme token."},
					},
				},
				PR: PullRequest{Head: head, Open: true, Author: "pr-author"},
			}
			if tt.change != nil {
				tt.change(&scope, &payload)
			}
			var calls int
			client := &http.Client{Transport: actionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "POST https://api.github.com/repos/owner/repo/pulls/42/reviews", req.Method+" "+req.URL.String(), "publish one review on the trusted pull request")
				require.Equal(t, "Bearer github-secret", req.Header.Get("Authorization"), "use server-side installation credentials")
				var received map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&received), "decode the review request sent to GitHub")
				summary := "@pr-author Two UI findings."
				if payload.Request.Text == "" {
					summary = "@pr-author Automated review findings."
				}
				require.Equal(t, map[string]any{
					"commit_id": head,
					"event":     "COMMENT",
					"body":      summary + marker,
					"comments": []any{
						map[string]any{"path": "ui/Card.tsx", "line": float64(10), "side": "RIGHT", "body": "Use the shared Card." + marker},
						map[string]any{"path": "ui/colors.css", "line": float64(42), "side": "RIGHT", "body": "Use a theme token." + marker},
					},
				}, received, "mention the verified author once and submit both inline findings as advisory comments at the pinned head")
				if tt.transport != nil {
					return nil, tt.transport
				}
				body := tt.response
				if body == "" {
					body = `{"id":314,"state":"COMMENTED","commit_id":"` + head + `","html_url":"` + reviewURL + `"}`
				}
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			provider := NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client))
			receipt, err := provider.Send(t.Context(), scope, action, payload)
			wantCalls := 0
			if tt.status != 0 {
				wantCalls = 1
			}
			require.Equal(t, wantCalls, calls, "send exactly one provider request only for the authorized target")
			if tt.wantCode == "" {
				require.NoError(t, err, "return a receipt after a matching GitHub review")
				require.Equal(t, Receipt{ID: "314", URL: reviewURL}, receipt, "record the canonical review permalink")
				return
			}
			var failure *SendFailure
			require.ErrorAs(t, err, &failure, "classify unsuccessful or uncertain review delivery")
			require.Equal(t, tt.wantCode, failure.Code, "record a safe provider error code")
			require.Equal(t, tt.definitive, failure.Definitive, "never retry a review with uncertain delivery")
			require.Equal(t, Receipt{}, receipt, "only confirmed delivery receives a review permalink")
		})
	}
}
