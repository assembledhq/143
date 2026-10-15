package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

func TestCanReusePullRequestHealthEnrichment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*models.PullRequestHealthSnapshot)
		expected bool
	}{
		{name: "complete ready payloads", expected: true},
		{name: "optional producer fields absent", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "url", nil)
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "base_branch", nil)
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "head_branch", nil)
		}, expected: true},
		{name: "nonempty usable checks", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", []any{map[string]any{"name": "unit tests", "category": "test", "summary": "one failure", "annotations": []string{"widget_test.go:5 failed"}}})
		}, expected: true},
		{name: "pending", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusPending
		}},
		{name: "failed", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusFailed
		}},
		{name: "stale", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusStale
		}},
		{name: "not requested", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusNotRequested
		}},
		{name: "different org", mutate: func(s *models.PullRequestHealthSnapshot) { s.OrgID = uuid.New() }},
		{name: "different PR", mutate: func(s *models.PullRequestHealthSnapshot) { s.PullRequestID = uuid.New() }},
		{name: "different version", mutate: func(s *models.PullRequestHealthSnapshot) { s.Version++ }},
		{name: "missing snapshot head", mutate: func(s *models.PullRequestHealthSnapshot) { s.HeadSHA = "" }},
		{name: "missing snapshot base", mutate: func(s *models.PullRequestHealthSnapshot) { s.BaseSHA = "" }},
		{name: "empty conflict", mutate: func(s *models.PullRequestHealthSnapshot) { s.ConflictPayload = nil }},
		{name: "null conflict", mutate: func(s *models.PullRequestHealthSnapshot) { s.ConflictPayload = []byte(`null`) }},
		{name: "empty conflict object", mutate: func(s *models.PullRequestHealthSnapshot) { s.ConflictPayload = []byte(`{}`) }},
		{name: "conflict array", mutate: func(s *models.PullRequestHealthSnapshot) { s.ConflictPayload = []byte(`[]`) }},
		{name: "conflict other PR", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "pull_request_id", uuid.New())
		}},
		{name: "conflict other repository", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "repository", "acme/other")
		}},
		{name: "conflict other number", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "pull_request_num", 43)
		}},
		{name: "conflict other live head", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "head_sha", "other-head")
		}},
		{name: "conflict other live base", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.ConflictPayload = enrichmentPayloadWith(s.ConflictPayload, "base_sha", "other-base")
		}},
		{name: "empty failing tests", mutate: func(s *models.PullRequestHealthSnapshot) { s.FailingTestsPayload = nil }},
		{name: "null failing tests", mutate: func(s *models.PullRequestHealthSnapshot) { s.FailingTestsPayload = []byte(`null`) }},
		{name: "malformed failing tests", mutate: func(s *models.PullRequestHealthSnapshot) { s.FailingTestsPayload = []byte(`{`) }},
		{name: "empty failing tests object", mutate: func(s *models.PullRequestHealthSnapshot) { s.FailingTestsPayload = []byte(`{}`) }},
		{name: "failing tests other PR", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "pull_request_id", uuid.New())
		}},
		{name: "failing tests other repository", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "repository", "acme/other")
		}},
		{name: "failing tests other number", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "pull_request_num", 43)
		}},
		{name: "failing tests other live head", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "head_sha", "other-head")
		}},
		{name: "legacy payload without provider success marker", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "provider_reads_succeeded", nil)
		}},
		{name: "partial provider reads", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "provider_reads_succeeded", false)
		}},
		{name: "null provider success marker", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "provider_reads_succeeded", json.RawMessage(`null`))
		}},
		{name: "missing checks", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", nil)
		}},
		{name: "null checks", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", json.RawMessage(`null`))
		}},
		{name: "checks object", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", map[string]any{})
		}},
		{name: "null check", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", []any{nil})
		}},
		{name: "incomplete check", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", []any{map[string]any{}})
		}},
		{name: "invalid check category", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", []any{map[string]any{"name": "tests", "category": "invalid"}})
		}},
		{name: "unreadable repair check fields", mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "checks", []any{map[string]any{"name": "tests", "category": "test", "summary": 1}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr, snapshot := enrichmentReuseFixture(t)
			if tt.mutate != nil {
				tt.mutate(&snapshot)
			}
			require.Equal(t, tt.expected, canReusePullRequestHealthEnrichment(pr, snapshot, 5), "reuse must require the exact ready snapshot and usable repair payloads")
		})
	}
}

func TestPRServiceEnrichPullRequestHealthReuseAndFallback(t *testing.T) {
	t.Parallel()
	databaseErr := errors.New("snapshot database unavailable")
	tests := []struct {
		name          string
		mutate        func(*models.PullRequestHealthSnapshot)
		missing       bool
		snapshotErr   error
		newerMirror   bool
		fetch         bool
		expectedError error
	}{
		{name: "ready avoids credentials HTTP and writes"},
		{name: "requested older ready version remains reusable", newerMirror: true},
		{name: "pending still fetches", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusPending
		}},
		{name: "failed still fetches", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusFailed
		}},
		{name: "stale still fetches", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) {
			s.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusStale
		}},
		{name: "missing snapshot preserves fetch", missing: true, fetch: true},
		{name: "legacy ready fetches once", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "provider_reads_succeeded", nil)
		}},
		{name: "degraded ready remains fetchable", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) {
			s.FailingTestsPayload = enrichmentPayloadWith(s.FailingTestsPayload, "provider_reads_succeeded", false)
		}},
		{name: "null ready still fetches", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) { s.ConflictPayload = []byte(`null`) }},
		{name: "incomplete ready still fetches", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) { s.FailingTestsPayload = []byte(`{}`) }},
		{name: "another tenant row cannot reuse", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) { s.OrgID = uuid.New() }},
		{name: "another PR row cannot reuse", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) { s.PullRequestID = uuid.New() }},
		{name: "another version cannot reuse", fetch: true, mutate: func(s *models.PullRequestHealthSnapshot) { s.Version++ }},
		{name: "database failure stops before credentials", snapshotErr: databaseErr, expectedError: databaseErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr, ready := enrichmentReuseFixture(t)
			if tt.newerMirror {
				head, base := "newer-head", "newer-base"
				pr.HeadSHA, pr.BaseSHA, pr.HealthVersion = &head, &base, 9
			}
			snapshot := ready
			if tt.mutate != nil {
				tt.mutate(&snapshot)
			}
			mock, service, requests, tokens := newEnrichmentReuseService(t)
			expectEnrichmentReuseLoad(mock, pr, snapshot, tt.missing, tt.snapshotErr)
			if tt.fetch {
				expectEnrichmentReuseRepository(mock, pr)
				expectEnrichmentReuseWrite(mock, pr, ready, nil)
			}
			err := service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5)
			if tt.expectedError != nil {
				require.ErrorIs(t, err, tt.expectedError, "snapshot read failures must stay retryable without spending provider quota")
			} else {
				require.NoError(t, err, "ready reuse and normal enrichment should succeed")
			}
			var expectedPaths []string
			var expectedCredentials []int64
			if tt.fetch {
				expectedPaths = enrichmentReuseHTTPPaths()
				expectedCredentials = []int64{123}
			}
			require.Equal(t, expectedPaths, requests.paths, "ready results must skip every provider request while fallback retains the full read path")
			require.Equal(t, expectedCredentials, tokens.installationIDs, "ready results and database failures must skip credential resolution")
			require.NoError(t, mock.ExpectationsWereMet(), "reads and writes must retain exact tenant PR and version filters")
		})
	}
}

func TestPRServiceEnrichPullRequestHealthRetryAfterPersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		firstWriteFails bool
		expectedFetches int
	}{
		{name: "completed write makes duplicate a no-op", expectedFetches: 1},
		{name: "failed write remains retryable before duplicate becomes a no-op", firstWriteFails: true, expectedFetches: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr, ready := enrichmentReuseFixture(t)
			pending := ready
			pending.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusPending
			pending.ConflictPayload, pending.FailingTestsPayload = nil, nil
			mock, service, requests, tokens := newEnrichmentReuseService(t)
			writeErr := errors.New("enrichment write unavailable")
			for attempt := 0; attempt < tt.expectedFetches; attempt++ {
				expectEnrichmentReuseLoad(mock, pr, pending, false, nil)
				expectEnrichmentReuseRepository(mock, pr)
				var expectedErr error
				if attempt == 0 && tt.firstWriteFails {
					expectedErr = writeErr
				}
				expectEnrichmentReuseWrite(mock, pr, ready, expectedErr)
				err := service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5)
				if expectedErr != nil {
					require.ErrorIs(t, err, expectedErr, "failed persistence must not turn the result into a cached success")
				} else {
					require.NoError(t, err, "normal enrichment should persist the completed result")
				}
			}
			expectEnrichmentReuseLoad(mock, pr, ready, false, nil)
			require.NoError(t, service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5), "a later duplicate should reuse the committed ready payloads")
			var expectedPaths []string
			var expectedCredentials []int64
			for range tt.expectedFetches {
				expectedPaths = append(expectedPaths, enrichmentReuseHTTPPaths()...)
				expectedCredentials = append(expectedCredentials, 123)
			}
			require.Equal(t, expectedPaths, requests.paths, "only attempts without persisted ready payloads should call GitHub")
			require.Equal(t, expectedCredentials, tokens.installationIDs, "duplicate after successful persistence must not resolve credentials again")
			require.NoError(t, mock.ExpectationsWereMet(), "duplicates must not rewrite the completed snapshot or current health row")
		})
	}
}

func TestPRServiceEnrichPullRequestHealthRetriesDegradedReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		annotations bool
	}{
		{name: "commit status error does not freeze missing failures"},
		{name: "annotation error does not freeze missing evidence", annotations: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr, ready := enrichmentReuseFixture(t)
			pending := ready
			pending.EnrichmentStatus = models.PullRequestHealthEnrichmentStatusPending
			mock, service, requests, tokens := newEnrichmentReuseService(t)
			requests.firstFailurePath = "/repos/acme/widget/commits/head-enrich/status"
			requests.responseBodies = map[string]string{
				requests.firstFailurePath: `{"statuses":[{"context":"unit tests","state":"failure","description":"one test failed","target_url":"https://example.test/check"}]}`,
			}
			degraded := ready
			degraded.FailingTestsPayload = enrichmentPayloadWith(degraded.FailingTestsPayload, "provider_reads_succeeded", false)
			ready.FailingTestsPayload = enrichmentPayloadWith(ready.FailingTestsPayload, "checks", []any{map[string]any{
				"name": "unit tests", "category": "test", "provider": "github_status", "summary": "one test failed", "details_url": "https://example.test/check",
			}})
			paths := enrichmentReuseHTTPPaths()
			if tt.annotations {
				requests.firstFailurePath = "/repos/acme/widget/check-runs/21/annotations"
				requests.responseBodies = map[string]string{
					"/repos/acme/widget/commits/head-enrich/check-runs": `{"check_runs":[{"id":21,"name":"unit tests","conclusion":"failure","status":"completed","output":{"title":"one test failed"}}]}`,
					requests.firstFailurePath:                           `[{"path":"widget_test.go","start_line":5,"message":"failed"}]`,
				}
				degraded.FailingTestsPayload = enrichmentPayloadWith(degraded.FailingTestsPayload, "checks", []any{map[string]any{"name": "unit tests", "category": "test", "summary": "one test failed"}})
				ready.FailingTestsPayload = enrichmentPayloadWith(ready.FailingTestsPayload, "checks", []any{map[string]any{"name": "unit tests", "category": "test", "summary": "one test failed", "annotations": []string{"widget_test.go:5 failed"}}})
				paths = append(paths, requests.firstFailurePath)
			}
			expectEnrichmentReuseLoad(mock, pr, pending, false, nil)
			expectEnrichmentReuseRepository(mock, pr)
			expectEnrichmentReuseWrite(mock, pr, degraded, nil)
			require.NoError(t, service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5), "best-effort provider failure should still persist Ready with an unsuccessful-read marker")
			expectEnrichmentReuseLoad(mock, pr, degraded, false, nil)
			expectEnrichmentReuseRepository(mock, pr)
			expectEnrichmentReuseWrite(mock, pr, ready, nil)
			require.NoError(t, service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5), "a later same-version call must recover provider evidence after a transient failure")
			expectEnrichmentReuseLoad(mock, pr, ready, false, nil)
			require.NoError(t, service.EnrichPullRequestHealth(context.Background(), pr.OrgID, pr.ID, 5), "the fully successful recovery should make the next duplicate a no-op")
			expectedPaths := append(append([]string{}, paths...), paths...)
			require.Equal(t, expectedPaths, requests.paths, "the degraded result must refetch once and then reuse the completed result")
			require.Equal(t, []int64{123, 123}, tokens.installationIDs, "only the two necessary provider attempts should resolve credentials")
			require.NoError(t, mock.ExpectationsWereMet(), "degraded evidence and recovered evidence must persist with distinct success markers")
		})
	}
}

func enrichmentReuseFixture(t *testing.T) (models.PullRequest, models.PullRequestHealthSnapshot) {
	t.Helper()
	pr := models.PullRequest{ID: uuid.New(), OrgID: uuid.New(), GitHubRepo: "acme/widget", GitHubPRNumber: 42, GitHubPRURL: "https://github.com/acme/widget/pull/42"}
	conflict, err := json.Marshal(map[string]any{
		"pull_request_id": pr.ID, "repository": pr.GitHubRepo, "pull_request_num": 42, "url": pr.GitHubPRURL,
		"base_branch": "main", "head_branch": "feature", "base_sha": "base-enrich", "head_sha": "head-enrich", "merge_state": models.PullRequestMergeStateClean, "behind_base": false,
	})
	require.NoError(t, err, "synthetic conflict fixture must be encodable")
	failing, err := json.Marshal(map[string]any{"pull_request_id": pr.ID, "repository": pr.GitHubRepo, "pull_request_num": 42, "url": pr.GitHubPRURL, "head_sha": "head-enrich", "checks": []any{}, "provider_reads_succeeded": true})
	require.NoError(t, err, "synthetic failing-check fixture must be encodable")
	return pr, models.PullRequestHealthSnapshot{PullRequestID: pr.ID, OrgID: pr.OrgID, Version: 5, HeadSHA: "head-enrich", BaseSHA: "base-enrich", ConflictPayload: conflict, FailingTestsPayload: failing, EnrichmentStatus: models.PullRequestHealthEnrichmentStatusReady}
}

func enrichmentPayloadWith(raw json.RawMessage, key string, value any) json.RawMessage {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		panic(err)
	}
	if value == nil {
		delete(payload, key)
	} else {
		payload[key] = value
	}
	result, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return result
}

type enrichmentReuseRequests struct {
	mu               sync.Mutex
	paths            []string
	responseBodies   map[string]string
	firstFailurePath string
	failed           bool
}

type enrichmentReuseTokens struct {
	installationIDs []int64
}

func (s *enrichmentReuseTokens) GetInstallationToken(_ context.Context, installationID int64) (string, error) {
	s.installationIDs = append(s.installationIDs, installationID)
	return "synthetic-installation-token", nil
}

func newEnrichmentReuseService(t *testing.T) (pgxmock.PgxPoolIface, *PRService, *enrichmentReuseRequests, *enrichmentReuseTokens) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "enrichment test should have an isolated database")
	t.Cleanup(mock.Close)
	requests, tokens := &enrichmentReuseRequests{}, &enrichmentReuseTokens{}
	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.New(io.Discard), baseURL: "https://api.example.test",
		cachedResolver: identity.NewResolver(tokens, zerolog.New(io.Discard)),
		httpClient: &http.Client{Transport: codeReviewSnapshotRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests.mu.Lock()
			requests.paths = append(requests.paths, r.URL.Path)
			fail := r.URL.Path == requests.firstFailurePath && !requests.failed
			if fail {
				requests.failed = true
			}
			requests.mu.Unlock()
			if fail {
				return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"temporary failure"}`)), Request: r}, nil
			}
			body := ""
			switch r.URL.Path {
			case "/repos/acme/widget/pulls/42":
				body = `{"number":42,"state":"open","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head-enrich"},"base":{"ref":"main","sha":"base-enrich"}}`
			case "/repos/acme/widget/commits/head-enrich/check-runs":
				body = `{"check_runs":[]}`
			case "/repos/acme/widget/commits/head-enrich/status":
				body = `{"statuses":[]}`
			case "/repos/acme/widget/check-runs/21/annotations":
				body = `[]`
			default:
				return nil, errors.New("unexpected GitHub request")
			}
			if response, ok := requests.responseBodies[r.URL.Path]; ok {
				body = response
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})},
	}
	return mock, service, requests, tokens
}

func enrichmentReuseHTTPPaths() []string {
	return []string{"/repos/acme/widget/pulls/42", "/repos/acme/widget/commits/head-enrich/check-runs", "/repos/acme/widget/commits/head-enrich/status"}
}

func expectEnrichmentReuseLoad(mock pgxmock.PgxPoolIface, pr models.PullRequest, snapshot models.PullRequestHealthSnapshot, missing bool, snapshotErr error) {
	now := time.Now().UTC()
	prRow := newPRTestRow(pr.ID, nil, pr.OrgID, pr.GitHubRepo, now, nil)
	prRow[12], prRow[14], prRow[20] = pr.HeadSHA, pr.BaseSHA, pr.HealthVersion
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgx.NamedArgs{"id": pr.ID, "org_id": pr.OrgID}).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...))
	query := mock.ExpectQuery("SELECT .+ FROM pull_request_health_snapshots WHERE org_id = .+ AND pull_request_id = .+ AND version = .+").
		WithArgs(pgx.NamedArgs{"org_id": pr.OrgID, "pull_request_id": pr.ID, "version": int64(5)})
	if snapshotErr != nil {
		query.WillReturnError(snapshotErr)
		return
	}
	rows := pgxmock.NewRows(prHealthSnapshotTestColumns)
	if !missing {
		rows.AddRow(snapshot.PullRequestID, snapshot.OrgID, snapshot.Version, snapshot.HeadSHA, snapshot.BaseSHA, []byte(`{}`), snapshot.ConflictPayload, snapshot.FailingTestsPayload, 0, snapshot.EnrichmentStatus, nil, now)
	}
	query.WillReturnRows(rows)
}

func expectEnrichmentReuseRepository(mock pgxmock.PgxPoolIface, pr models.PullRequest) {
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": pr.OrgID, "full_name": pr.GitHubRepo}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(uuid.New(), pr.OrgID, uuid.New(), int64(1), pr.GitHubRepo, "main", false, nil, nil, "https://github.com/acme/widget.git", int64(123), "active", nil, nil, []byte(`{}`), now, now))
}

type enrichmentJSONArg json.RawMessage

func (a enrichmentJSONArg) Match(value any) bool {
	var raw []byte
	switch v := value.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		return false
	}
	var expected, actual any
	return json.Unmarshal(a, &expected) == nil && json.Unmarshal(raw, &actual) == nil && reflect.DeepEqual(expected, actual)
}

func expectEnrichmentReuseWrite(mock pgxmock.PgxPoolIface, pr models.PullRequest, ready models.PullRequestHealthSnapshot, writeErr error) {
	mock.ExpectBegin()
	query := mock.ExpectExec("UPDATE pull_request_health_snapshots").WithArgs(pgx.NamedArgs{
		"org_id": pr.OrgID, "pull_request_id": pr.ID, "version": int64(5), "conflict_payload": enrichmentJSONArg(ready.ConflictPayload), "failing_tests_payload": enrichmentJSONArg(ready.FailingTestsPayload),
		"payload_size_bytes": len(ready.ConflictPayload) + len(ready.FailingTestsPayload), "enrichment_status": models.PullRequestHealthEnrichmentStatusReady, "enriched_at": pgxmock.AnyArg(),
	})
	if writeErr != nil {
		query.WillReturnError(writeErr)
		mock.ExpectRollback()
		return
	}
	query.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE pull_request_health_current").WithArgs(pgx.NamedArgs{
		"org_id": pr.OrgID, "pull_request_id": pr.ID, "version": int64(5), "enrichment_status": models.PullRequestHealthEnrichmentStatusReady, "enriched_at": pgxmock.AnyArg(),
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
}
