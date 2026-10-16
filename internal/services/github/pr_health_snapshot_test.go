package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type codeReviewSnapshotRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codeReviewSnapshotRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSyncCodeReviewPullRequestStateReusesAuthoritativeResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		state            string
		merged           bool
		storedStatus     models.PullRequestStatus
		missingHead      bool
		failRoute        string
		expectedRequests map[string]int
		expectedError    bool
	}{
		{name: "fresh snapshot and complete health share one PR read", state: "open", expectedRequests: map[string]int{"pull": 1, "checks": 1, "statuses": 1}},
		{name: "closed PR does not retry null mergeability or run lifecycle cleanup", state: "closed", expectedRequests: map[string]int{"pull": 1}},
		{name: "merged PR with stale-open mirror stays ineligible without lifecycle cleanup", state: "closed", merged: true, expectedRequests: map[string]int{"pull": 1}},
		{name: "already closed PR stays ineligible without inaccessible checks", state: "closed", storedStatus: models.PullRequestStatusClosed, failRoute: "checks", expectedRequests: map[string]int{"pull": 1}},
		{name: "already merged PR stays ineligible without inaccessible statuses", state: "closed", storedStatus: models.PullRequestStatusMerged, failRoute: "statuses", expectedRequests: map[string]int{"pull": 1}},
		{name: "missing head rejects the snapshot before checks", state: "open", missingHead: true, expectedRequests: map[string]int{"pull": 1}, expectedError: true},
		{name: "PR failure cannot return a usable snapshot", state: "open", failRoute: "pull", expectedRequests: map[string]int{"pull": 1}, expectedError: true},
		{name: "check failure cannot return a usable snapshot", state: "open", failRoute: "checks", expectedRequests: map[string]int{"pull": 1, "checks": 1}, expectedError: true},
		{name: "status throttle cannot return a usable snapshot", state: "open", failRoute: "statuses", expectedRequests: map[string]int{"pull": 1, "checks": 1, "statuses": 1}, expectedError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test database should be isolated")
			t.Cleanup(mock.Close)
			orgID, prID := uuid.New(), uuid.New()
			now := time.Now().UTC()
			prRow := newPRTestRow(prID, nil, orgID, "acme/web", now, nil)
			if tt.storedStatus != "" {
				prRow[8] = tt.storedStatus
			}
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...))
			mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "acme/web"}).
				WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(uuid.New(), orgID, uuid.New(), int64(1), "acme/web", "main", false, nil, nil, "https://github.com/acme/web.git", int64(123), "active", nil, nil, []byte(`{}`), now, now))
			expectReserveCheckStateVersion(mock, orgID, prID, 0)
			if !tt.expectedError && tt.state == "open" {
				expectCodeReviewSnapshotHealthWrite(mock, orgID, prID)
			}
			expected := CodeReviewPullRequestSnapshot{
				Number: 42, HTMLURL: "https://github.com/acme/web/pull/42", State: tt.state,
				IsDraft: true, Title: "Improve widget", Body: "Current author intent", AuthorLogin: "developer",
				HeadSHA: "head-current", BaseSHA: "base-current", HeadRef: "widget", BaseRef: "release", FromFork: true,
			}
			var mu sync.Mutex
			requests := map[string]int{}
			reasons := []PullRequestSyncReason{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				route := "unexpected"
				switch r.URL.Path {
				case "/repos/acme/web/pulls/42":
					route = "pull"
				case "/repos/acme/web/commits/head-current/check-runs":
					route = "checks"
				case "/repos/acme/web/commits/head-current/status":
					route = "statuses"
				}
				mu.Lock()
				requests[route]++
				mu.Unlock()
				if route == tt.failRoute {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
					if _, err := w.Write([]byte(`{"message":"API rate limit exceeded"}`)); err != nil {
						t.Errorf("write synthetic failure: %v", err)
					}
					return
				}
				var payload any
				switch route {
				case "pull":
					head := expected.HeadSHA
					if tt.missingHead {
						head = ""
					}
					mergeable := any(true)
					mergeState := "clean"
					if tt.state == "closed" {
						mergeable = nil
						mergeState = "unknown"
					}
					payload = map[string]any{"number": expected.Number, "html_url": expected.HTMLURL, "state": expected.State, "draft": expected.IsDraft, "title": expected.Title, "body": expected.Body, "merged": tt.merged,
						"user": map[string]any{"login": expected.AuthorLogin}, "head": map[string]any{"sha": head, "ref": expected.HeadRef, "repo": map[string]any{"fork": true}},
						"base": map[string]any{"sha": expected.BaseSHA, "ref": expected.BaseRef}, "mergeable": mergeable, "mergeable_state": mergeState}
				case "checks":
					payload = map[string]any{"check_runs": []any{map[string]any{"id": 1, "name": "unit tests", "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}}}}
				case "statuses":
					payload = map[string]any{"statuses": []any{}}
				default:
					http.NotFound(w, r)
					return
				}
				if err := json.NewEncoder(w).Encode(payload); err != nil {
					t.Errorf("write synthetic response: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			base := client.Transport
			client.Transport = codeReviewSnapshotRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				reasons = append(reasons, PullRequestSyncReasonFromContext(r.Context()))
				mu.Unlock()
				return base.RoundTrip(r)
			})
			service := &PRService{
				tokenProvider: &Service{cache: map[int64]*cachedToken{123: {Token: "synthetic-token", ExpiresAt: now.Add(time.Hour)}}},
				pullRequests:  db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.New(io.Discard),
				baseURL: server.URL, httpClient: client,
			}
			// A running background read must not supply older metadata to a new review capture.
			release := make(chan struct{})
			defer close(release)
			service.prHealthSyncGroup.DoChan(orgID.String()+":"+prID.String(), func() (any, error) { <-release; return nil, nil })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			actual, err := service.SyncCodeReviewPullRequestState(ctx, orgID, prID)
			if tt.expectedError {
				require.Error(t, err, "incomplete provider health must prevent snapshot reuse")
				require.Equal(t, CodeReviewPullRequestSnapshot{}, actual, "failed refresh must not expose a partial usable snapshot")
			} else {
				require.NoError(t, err, "fresh authoritative health should expose its matching PR metadata")
				require.Equal(t, expected, actual, "all review metadata must come from the same provider response")
			}
			mu.Lock()
			actualRequests := requests
			actualReasons := reasons
			mu.Unlock()
			require.Equal(t, tt.expectedRequests, actualRequests, "capture refresh must avoid a duplicate PR read while preserving fresh checks and statuses")
			expectedReasons := make([]PullRequestSyncReason, 0)
			for _, count := range tt.expectedRequests {
				for range count {
					expectedReasons = append(expectedReasons, PullRequestSyncReasonCodeReview)
				}
			}
			require.Equal(t, expectedReasons, actualReasons, "assessment health must carry an explicit code-review reason on every provider call")
			require.NoError(t, mock.ExpectationsWereMet(), "refresh must preserve the existing durable health publication contract")
		})
	}
}

func expectCodeReviewSnapshotHealthWrite(mock pgxmock.PgxPoolIface, orgID, prID uuid.UUID) {
	identity := pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").WithArgs(identity).WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	expectCheckStateReconciliation(mock, orgID, prID, "head-current", 1)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT id[\\s\\S]+FROM pull_requests[\\s\\S]+FOR UPDATE").WithArgs(identity).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").WithArgs(identity).WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	mock.ExpectExec("INSERT INTO pull_request_health_snapshots").WithArgs(pgx.NamedArgs{
		"pull_request_id": prID, "org_id": orgID, "version": int64(1), "head_sha": "head-current", "base_sha": "base-current",
		"summary_json": pgxmock.AnyArg(), "enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
	}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_request_repair_runs").WithArgs(pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": prID, "version": int64(1), "head_sha": "head-current",
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("INSERT INTO pull_request_health_current").WithArgs(pgx.NamedArgs{
		"pull_request_id": prID, "org_id": orgID, "version": int64(1), "head_sha": "head-current", "base_sha": "base-current",
		"summary_json": pgxmock.AnyArg(), "summary_preview_json": pgxmock.AnyArg(), "enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		"enriched_at": (*time.Time)(nil), "check_state_version": int64(0), "created_at": pgxmock.AnyArg(), "updated_at": pgxmock.AnyArg(),
	}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE pull_requests").WithArgs(pgx.NamedArgs{
		"pull_request_id": prID, "org_id": orgID, "head_sha": "head-current", "base_sha": "base-current",
		"merge_state": models.PullRequestMergeStateClean, "has_conflicts": false, "failing_test_count": 0,
		"needs_agent_action": false, "version": int64(1), "mark_github_synced": true,
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE pull_requests SET ci_status").WithArgs(pgx.NamedArgs{
		"id": prID, "org_id": orgID, "ci_status": models.PullRequestCIStatusSuccess,
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
}
