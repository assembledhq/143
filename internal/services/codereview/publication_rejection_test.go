package codereview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/stretchr/testify/require"
)

func TestSubmitReviewPublicationRejection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		existing       bool
		failAt         int
		status         int
		transportError bool
		rejected       bool
		writes         []string
	}{
		{name: "new review rejected", failAt: 1, status: 422, rejected: true, writes: []string{"POST /repos/acme/repo/pulls/2/reviews"}},
		{name: "new review server error", failAt: 1, status: 503, writes: []string{"POST /repos/acme/repo/pulls/2/reviews"}},
		{name: "inline rejected", existing: true, failAt: 1, status: 422, rejected: true, writes: []string{"POST /repos/acme/repo/pulls/2/comments"}},
		{name: "second inline rejected after first posted", existing: true, failAt: 2, status: 422, rejected: true, writes: []string{"POST /repos/acme/repo/pulls/2/comments", "POST /repos/acme/repo/pulls/2/comments"}},
		{name: "inline response lost", existing: true, failAt: 1, transportError: true, writes: []string{"POST /repos/acme/repo/pulls/2/comments"}},
		{name: "inline server error", existing: true, failAt: 1, status: 503, writes: []string{"POST /repos/acme/repo/pulls/2/comments"}},
		{name: "summary rejected", existing: true, failAt: 3, status: 422, rejected: true, writes: []string{"POST /repos/acme/repo/pulls/2/comments", "POST /repos/acme/repo/pulls/2/comments", "PUT /repos/acme/repo/pulls/2/reviews/10"}},
		{name: "approval rejected after summary published stays uncertain", existing: true, failAt: 4, status: 422, writes: []string{"POST /repos/acme/repo/pulls/2/comments", "POST /repos/acme/repo/pulls/2/comments", "PUT /repos/acme/repo/pulls/2/reviews/10", "POST /repos/acme/repo/pulls/2/reviews"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var writes []string
			client := &http.Client{Transport: assessmentPublicationTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, "[]"
				if r.Method != http.MethodGet {
					writes = append(writes, r.Method+" "+r.URL.Path)
					status, body = 200, `{"id":10,"html_url":"https://github.test/review/10"}`
					if len(writes) == tt.failAt {
						if tt.transportError {
							return nil, io.ErrUnexpectedEOF
						}
						status, body = tt.status, `{"message":"Validation Failed"}`
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			s := NewGitHubSubmitter(&tokenStub{token: "test"}, WithGitHubSubmitterHTTPClient(client))
			req := SubmitReviewRequest{InstallationID: 1, Repository: "acme/repo", PullNumber: 2, HeadSHA: "head", OutputKey: "new-output", Decision: SubmitReviewDecisionApproved, Body: "summary", RequirePublicationReceipt: true}
			if tt.existing {
				req.ExistingReviewID = 10
				req.Comments = []SubmitReviewComment{{Path: "file.go", Line: 1, Body: "first", DedupeKey: "first"}, {Path: "file.go", Line: 2, Body: "second", DedupeKey: "second"}}
			}
			_, err := s.SubmitReview(context.Background(), req)
			require.Error(t, err, "the simulated publication failure must reach the controller")
			require.Equal(t, tt.rejected, errors.Is(err, ErrReviewPublicationRejected), "only definitive rejection before summary publication can clear this attempt's uncertainty")
			require.Equal(t, tt.writes, writes, "publication must stop at the failed stage")
			if !tt.transportError {
				var apiErr *ghservice.GitHubAPIError
				require.ErrorAs(t, err, &apiErr, "the original GitHub error must remain available to retry classification")
				require.Equal(t, tt.status, apiErr.StatusCode, "preserve the provider rejection status")
			}
		})
	}
}
