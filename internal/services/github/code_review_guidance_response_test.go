package github

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type guidanceResponseBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *guidanceResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *guidanceResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestGitHubRESTResponseReadLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		limit  int64
	}{
		{name: "success overflow", status: http.StatusOK, limit: 32},
		{name: "rate limit status survives overflow", status: http.StatusTooManyRequests, limit: 32},
		{name: "provider unavailable status survives overflow", status: http.StatusServiceUnavailable, limit: 32},
		{name: "existing wrapper remains unbounded", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := `{"message":"` + strings.Repeat("x", 256) + `"}`
			body := &guidanceResponseBody{reader: strings.NewReader(payload)}
			svc := &PRService{baseURL: "https://api.example.test", logger: zerolog.Nop(), httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Retry-After": []string{"60"}}, Body: body, Request: request}, nil
			})}}
			var response githubRESTResponse
			var err error
			if tt.limit == 0 {
				response, err = svc.doGitHubRESTRequest(t.Context(), "fixture-token", http.MethodGet, "/fixture", nil, "application/vnd.github+json", "")
			} else {
				response, err = svc.doGitHubRESTRequestWithResponseLimit(t.Context(), "fixture-token", http.MethodGet, "/fixture", nil, "application/vnd.github+json", "", tt.limit)
			}
			if tt.limit == 0 {
				require.NoError(t, err, "the existing REST wrapper must retain complete response behavior")
				require.Equal(t, payload, string(response.body), "unbounded callers must receive all original bytes")
				require.Equal(t, len(payload), body.read, "the compatibility wrapper must not truncate responses")
			} else {
				require.Equal(t, int(tt.limit+1), body.read, "bounded requests must stop after the overflow probe byte")
				if tt.status < 400 {
					require.ErrorIs(t, err, errGitHubRESTResponseLimit, "oversized success responses must expose the read limit cause")
				} else {
					var apiErr *GitHubAPIError
					require.ErrorAs(t, err, &apiErr, "oversized provider errors must retain retry classification")
					require.Equal(t, tt.status, apiErr.StatusCode, "the actual provider status must survive the body cap")
					require.Equal(t, "60", apiErr.Header.Get("Retry-After"), "retry headers must remain available")
					require.NotErrorIs(t, err, errGitHubRESTResponseLimit, "HTTP failures must not be replaced by a permanent capture limit")
				}
			}
			require.True(t, body.closed, "all bounded and unbounded response bodies must close")
		})
	}
}

func TestReviewGuidanceTreeResponseReadLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fallback bool
		status   int
	}{
		{name: "initial recursive tree", status: http.StatusOK},
		{name: "fallback remaining bytes", fallback: true, status: http.StatusOK},
		{name: "initial rate limit", status: http.StatusTooManyRequests},
		{name: "fallback provider unavailable", fallback: true, status: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := &guidanceResponseBody{reader: strings.NewReader(strings.Repeat("x", 256))}
			svc := &PRService{baseURL: "https://api.example.test", logger: zerolog.Nop(), httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: body, Request: request}, nil
			})}}
			var err error
			if tt.fallback {
				initial := reviewGuidanceTree{SHA: strings.Repeat("a", 40), Truncated: true, EntryCount: 1, ResponseBytes: 10}
				entries, resolveErr := svc.resolveReviewGuidanceTree(t.Context(), "fixture-token", "/repos/acme/widget", "fixture-cache", "main", initial, reviewGuidanceTraversalLimits{Requests: 128, Entries: 1000000, ResponseBytes: 42, Depth: 128})
				err = resolveErr
				require.Nil(t, entries, "bounded failure must never return partial discovery")
			} else {
				_, err = readReviewGuidanceResource(t.Context(), svc, "fixture-token", "/repos/acme/widget/git/trees/main?recursive=1", "fixture-cache", decodeReviewGuidanceTree, 32)
			}
			require.Equal(t, 33, body.read, "initial and fallback tree requests must enforce their remaining byte allowance during reads")
			require.True(t, body.closed, "tree response bodies must close on overflow")
			if tt.status < 400 {
				require.ErrorIs(t, err, models.ErrIncompleteCodeReviewGuidance, "tree overflow must stop deterministic capture without publication")
			} else {
				var apiErr *GitHubAPIError
				require.ErrorAs(t, err, &apiErr, "tree capture must preserve provider errors for retry")
				require.Equal(t, tt.status, apiErr.StatusCode, "tree capture must retain the provider status")
				require.NotErrorIs(t, err, models.ErrIncompleteCodeReviewGuidance, "provider failure must not become permanent guidance failure")
			}
		})
	}
}
