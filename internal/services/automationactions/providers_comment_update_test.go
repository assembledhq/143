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

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

func TestProvidersCommentUpdate(t *testing.T) {
	t.Parallel()
	const (
		commentPath = "/repos/owner/repo/issues/comments/123"
		issueURL    = "https://api.github.com/repos/owner/repo/issues/42"
		commentURL  = "https://github.com/owner/repo/pull/42#issuecomment-123"
	)
	head := strings.Repeat("a", 40)
	readComment := []string{"GET " + commentPath}
	checkHead := []string{"GET " + commentPath, "GET /repos/owner/repo/pulls/42"}
	patch := []string{"GET " + commentPath, "GET /repos/owner/repo/pulls/42", "PATCH " + commentPath}
	tests := []struct {
		name            string
		previousUpdate  bool
		unsafeID        bool
		incorrectBody   bool
		incorrectIssue  bool
		incorrectID     bool
		staleHead       bool
		closedPR        bool
		cancelAfterHead bool
		lookupStatus    int
		patchStatus     int
		patchTimeout    bool
		wrongReceiptID  bool
		wrongReceiptURL bool
		wantCalls       []string
		wantFailure     string
		wantDefinitive  bool
	}{
		{name: "replace original comment", wantCalls: patch},
		{name: "replace previously updated comment", previousUpdate: true, wantCalls: patch},
		{name: "provider ID cannot alter path", unsafeID: true, wantFailure: "GITHUB_COMMENT_TARGET_INVALID", wantDefinitive: true},
		{name: "changed body with original marker", incorrectBody: true, wantCalls: readComment, wantFailure: "GITHUB_COMMENT_TARGET_MISMATCH", wantDefinitive: true},
		{name: "comment belongs to another PR", incorrectIssue: true, wantCalls: readComment, wantFailure: "GITHUB_COMMENT_TARGET_MISMATCH", wantDefinitive: true},
		{name: "GET returns a different comment", incorrectID: true, wantCalls: readComment, wantFailure: "GITHUB_COMMENT_TARGET_MISMATCH", wantDefinitive: true},
		{name: "comment deleted", lookupStatus: http.StatusNotFound, wantCalls: readComment, wantFailure: "GITHUB_COMMENT_LOOKUP_FAILED", wantDefinitive: true},
		{name: "stale head", staleHead: true, wantCalls: checkHead, wantFailure: "GITHUB_COMMENT_STALE_HEAD", wantDefinitive: true},
		{name: "closed PR", closedPR: true, wantCalls: checkHead, wantFailure: "GITHUB_COMMENT_STALE_HEAD", wantDefinitive: true},
		{name: "cancellation after a valid head read", cancelAfterHead: true, wantCalls: checkHead, wantFailure: "GITHUB_COMMENT_SEND_CANCELED", wantDefinitive: true},
		{name: "PATCH denied", patchStatus: http.StatusForbidden, wantCalls: patch, wantFailure: "PROVIDER_HTTP_403", wantDefinitive: true},
		{name: "PATCH times out", patchTimeout: true, wantCalls: patch, wantFailure: "PROVIDER_TRANSPORT_UNKNOWN"},
		{name: "PATCH returns another ID", wrongReceiptID: true, wantCalls: patch, wantFailure: "GITHUB_COMMENT_RECEIPT_INVALID"},
		{name: "PATCH returns another comment URL", wrongReceiptURL: true, wantCalls: patch, wantFailure: "GITHUB_COMMENT_RECEIPT_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sourceID, updateID := uuid.New(), uuid.New()
			previousID := sourceID
			previousText := "Initial review"
			if tt.previousUpdate {
				previousID = uuid.New()
				previousText = "Previous review revision"
			}
			originalMarker := "\n\n<!-- 143-automation-action:" + sourceID.String() + " -->"
			oldBody := previousText + originalMarker
			if tt.previousUpdate {
				oldBody += "\n<!-- 143-automation-comment-update:" + previousID.String() + " -->"
			}
			if tt.incorrectBody {
				oldBody = "Changed outside automation" + originalMarker
			}

			var calls []string
			var patchBody map[string]string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &http.Client{Transport: actionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Equal(t, "api.github.com", req.URL.Host, "keep requests on the trusted GitHub API")
				require.Equal(t, "Bearer github-secret", req.Header.Get("Authorization"), "keep the installation credential on the server")
				status := http.StatusOK
				var body string
				switch req.Method + " " + req.URL.Path {
				case "GET " + commentPath:
					if tt.lookupStatus != 0 {
						status = tt.lookupStatus
					}
					id, issue := int64(123), issueURL
					if tt.incorrectID {
						id = 456
					}
					if tt.incorrectIssue {
						issue = "https://api.github.com/repos/owner/repo/issues/99"
					}
					raw, err := json.Marshal(map[string]any{"id": id, "issue_url": issue, "body": oldBody})
					require.NoError(t, err, "encode existing provider comment")
					body = string(raw)
				case "GET /repos/owner/repo/pulls/42":
					currentHead, state := head, "open"
					if tt.staleHead {
						currentHead = strings.Repeat("b", 40)
					}
					if tt.closedPR {
						state = "closed"
					}
					body = `{"title":"Review me","state":"` + state + `","user":{"login":"person"},"head":{"sha":"` + currentHead + `"}}`
				case "PATCH " + commentPath:
					require.NoError(t, json.NewDecoder(req.Body).Decode(&patchBody), "capture the single attempted update")
					if tt.patchTimeout {
						return nil, context.DeadlineExceeded
					}
					if tt.patchStatus != 0 {
						status = tt.patchStatus
					}
					id, url := int64(123), commentURL
					if tt.wrongReceiptID {
						id = 456
					}
					if tt.wrongReceiptURL {
						url = "https://github.com/owner/repo/pull/99#issuecomment-123"
					}
					raw, err := json.Marshal(map[string]any{"id": id, "html_url": url})
					require.NoError(t, err, "encode provider update receipt")
					body = string(raw)
				default:
					t.Fatalf("unexpected provider request: %s %s", req.Method, req.URL.Path)
				}
				responseBody := io.NopCloser(strings.NewReader(body))
				if tt.cancelAfterHead && req.URL.Path == "/repos/owner/repo/pulls/42" {
					responseBody = cancelOnClose{ReadCloser: responseBody, cancel: cancel}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: responseBody}, nil
			})}
			provider := NewProviders(testTokens{}, nil, nil, WithGitHubHTTPClient(client))
			scope := providerScope()
			scope.Config = models.AutomationActionConfig{Actions: []models.AutomationActionKind{models.AutomationActionCommentUpdate}, Repository: scope.RepositoryName}
			action := models.AutomationAction{ID: updateID, Kind: models.AutomationActionCommentUpdate}
			payload := Payload{
				Request: models.AutomationActionRequest{
					AutomationActionKey: models.AutomationActionKey{OperationKey: "review:42:new-head", ActionKey: "update"},
					Kind:                action.Kind,
					PRNumber:            42,
					HeadSHA:             head,
					SourceActionID:      sourceID.String(),
					Text:                "New review",
				},
				CommentTarget: &models.AutomationActionCommentTarget{SourceActionID: sourceID, CommentID: "123", PreviousActionID: previousID, PreviousText: previousText},
			}
			if tt.unsafeID {
				payload.CommentTarget.CommentID = "../456"
			}

			receipt, err := provider.Send(ctx, scope, action, payload)
			require.Equal(t, tt.wantCalls, calls, "update should verify the comment and current head without creating or retrying a write")
			if len(tt.wantCalls) == len(patch) {
				require.Equal(t, map[string]string{"body": "New review" + originalMarker + "\n<!-- 143-automation-comment-update:" + updateID.String() + " -->"}, patchBody, "update should preserve the create marker and replace the previous delivery marker")
			}
			if tt.wantFailure == "" {
				require.NoError(t, err, "successful update should return the same comment receipt")
				require.Equal(t, Receipt{ID: "123", URL: commentURL}, receipt, "receipt should retain the original comment destination")
				return
			}
			var failure *SendFailure
			require.ErrorAs(t, err, &failure, "classify the update failure without exposing comment content")
			require.Equal(t, tt.wantFailure, failure.Code, "persist only the safe failure code")
			require.Equal(t, tt.wantDefinitive, failure.Definitive, "a possibly delivered update must remain uncertain")
			require.Equal(t, Receipt{}, receipt, "an unconfirmed update must not return a receipt")
			if tt.cancelAfterHead {
				require.ErrorIs(t, err, context.Canceled, "preserve the cancellation cause before any write")
			}
		})
	}
}
