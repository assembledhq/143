package github

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type prCheckPageFixture struct {
	body string
	link string
	code int
}

func prCheckPageService(t *testing.T, endpoint string, pages []prCheckPageFixture) *PRService {
	t.Helper()

	calls := 0
	t.Cleanup(func() { require.Equal(t, len(pages), calls, "read each required page once") })
	return &PRService{
		baseURL: "https://github.example",
		logger:  zerolog.Nop(),
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			require.Less(t, calls, len(pages), "must not request another page after the inventory is complete")
			require.Equal(t, "github.example", r.URL.Host, "pagination must keep the configured GitHub host")
			require.Equal(t, "/repos/example/repo/commits/head/"+endpoint, r.URL.Path, "pagination must keep the original endpoint")
			require.Equal(t, "100", r.URL.Query().Get("per_page"), "all pages must use the same maximum page size")
			wantPage := ""
			if calls > 0 {
				wantPage = strconv.Itoa(calls + 1)
			}
			require.Equal(t, wantPage, r.URL.Query().Get("page"), "advance the locally constructed page number")
			page := pages[calls]
			calls++

			header := make(http.Header)
			if page.link != "" {
				header.Set("Link", page.link)
			}
			status := page.code
			if status == 0 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(page.body)), Request: r}, nil
		})},
	}
}

func TestListCheckRunsForRefPreservesProvidersAcrossPages(t *testing.T) {
	t.Parallel()

	service := prCheckPageService(t, "check-runs", []prCheckPageFixture{
		{body: `{"check_runs":[{"id":1,"name":"Verify","app":{"slug":"github-actions"}},{"id":2,"name":"Verify","app":{"slug":"other-ci"}}]}`,
			link: `<https://untrusted.example/different/endpoint?page=42>; rel="next"`},
		{body: `{"check_runs":[{"id":3,"name":" verify ","app":{"slug":" GITHUB-ACTIONS "}}]}`},
	})

	checks, err := service.listCheckRunsForRef(context.Background(), "token", "example", "repo", "head")
	require.NoError(t, err, "read both pages without following the untrusted Link URL")
	keys := make([]string, 0, len(checks))
	ids := make([]int64, 0, len(checks))
	for _, check := range checks {
		keys = append(keys, checkRunExternalKey(check.App.Slug, check.Name, check.ID))
		ids = append(ids, check.ID)
	}
	require.Equal(t, []string{" check-run-v2:other-ci:verify", " check-run-v2:github-actions:verify"}, keys, "persist independent state for checks from different providers")
	require.Equal(t, []int64{2, 3}, ids, "keep the newest same-provider check across pages")
}

func TestListCommitStatusesForRefPaginates(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		pages []prCheckPageFixture
		want  []gitHubCommitStatus
	}{
		{
			name:  "legacy single page without pagination metadata",
			pages: []prCheckPageFixture{{body: `{"statuses":[{"id":8,"context":"CI","state":"success"}]}`}},
			want:  []gitHubCommitStatus{{ID: 8, Context: "CI", State: "success"}},
		},
		{
			name: "total count without Link retains the newest status across pages",
			pages: []prCheckPageFixture{
				{body: `{"total_count":3,"statuses":[{"id":8,"context":"CI","state":"pending"}]}`},
				{body: `{"total_count":3,"statuses":[{"id":7,"context":" ci ","state":"success"},{"id":6,"context":"lint","state":"failure"}]}`},
			},
			want: []gitHubCommitStatus{{ID: 8, Context: "CI", State: "pending"}, {ID: 6, Context: "lint", State: "failure"}},
		},
		{
			name: "HTTP failure on a subsequent page",
			pages: []prCheckPageFixture{
				{body: `{"total_count":2,"statuses":[{"id":8,"context":"CI","state":"success"}]}`},
				{body: `{"message":"Not Found"}`, code: http.StatusNotFound},
			},
		},
		{
			name: "empty continuation cannot prove completeness",
			pages: []prCheckPageFixture{
				{body: `{"total_count":2,"statuses":[{"id":8,"context":"CI","state":"success"}]}`},
				{body: `{"total_count":2,"statuses":[]}`},
			},
		},
		{
			name: "repeated status ID cannot substitute for a missing item",
			pages: []prCheckPageFixture{
				{body: `{"total_count":2,"statuses":[{"id":8,"context":"CI","state":"success"}]}`},
				{body: `{"total_count":2,"statuses":[{"id":8,"context":"CI","state":"success"}]}`},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := prCheckPageService(t, "status", tt.pages)
			statuses, err := service.listCommitStatusesForRef(context.Background(), "token", "example", "repo", "head")
			if tt.want == nil {
				require.ErrorIs(t, err, errIncompleteCheckPagination, "fail closed when a later page cannot establish the complete inventory")
			} else {
				require.NoError(t, err, "complete provider pagination should succeed")
			}
			require.Equal(t, tt.want, statuses, "return the complete ordered inventory or no data")
		})
	}
}

func TestListCommitStatusesForRefDefersBeforeAnAdditionalPage(t *testing.T) {
	t.Parallel()

	service := prCheckPageService(t, "status", []prCheckPageFixture{
		{body: `{"total_count":2,"statuses":[{"id":8,"context":"CI","state":"success"}]}`},
	})
	// Without a shared budget controller, a webhook refresh must yield
	// instead of trusting a partial inventory or requesting the second page.
	ctx := WithPullRequestSyncReason(context.Background(), PullRequestSyncReasonIncomplete)
	statuses, err := service.listCommitStatusesForRef(ctx, "token", "example", "repo", "head")
	require.ErrorIs(t, err, ErrPullRequestCheckRefreshDeferred, "the continuation must yield to the installation budget")
	require.Nil(t, statuses, "a deferred continuation must not return partial data")
}
