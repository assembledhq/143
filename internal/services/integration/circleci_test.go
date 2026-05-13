package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCircleCI_ListFlakyTests_Hyphenated(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "tok-123", r.Header.Get("Circle-Token"),
			"CircleCI provider must auth via Circle-Token header")
		require.True(t, strings.HasSuffix(r.URL.Path,
			"/api/v2/insights/gh/octocat/hello/flaky-tests"),
			"endpoint path should include the project slug")
		require.Equal(t, "main", r.URL.Query().Get("branch"),
			"branch filter should be passed through")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"flaky-tests": [
				{
					"test-name": "TestFlaky",
					"classname": "pkg/foo",
					"file": "pkg/foo/foo_test.go",
					"job-name": "build",
					"job-number": 42,
					"workflow-name": "ci",
					"workflow-created-at": "2024-04-15T10:00:00Z",
					"pipeline-number": 99,
					"times-flaked": 3,
					"source": "junit"
				}
			]
		}`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok-123",
		ProjectSlug: "gh/octocat/hello",
	})

	tests, err := ci.ListFlakyTests(context.Background(), FlakyTestFilter{Branch: "main"})
	require.NoError(t, err)
	require.Len(t, tests, 1)
	require.Equal(t, "TestFlaky", tests[0].TestName)
	require.Equal(t, "pkg/foo", tests[0].Classname)
	require.Equal(t, 42, tests[0].LastJob.JobNumber)
	require.Equal(t, 3, tests[0].TimesFlaked)
	require.False(t, tests[0].LastFailureAt.IsZero(),
		"workflow-created-at should populate LastFailureAt")
	require.Contains(t, tests[0].LastJob.WebURL, "/jobs/42",
		"job WebURL should encode the job number")
}

func TestCircleCI_ListFlakyTests_SnakeCase(t *testing.T) {
	t.Parallel()

	// CircleCI docs show hyphens, but some live responses use snake_case.
	// Our UnmarshalJSON must accept either.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"flaky_tests": [
				{
					"test_name": "TestFlakySnake",
					"classname": "pkg/bar",
					"job_number": 7,
					"workflow_name": "ci",
					"workflow_created_at": "2024-04-15T10:00:00Z",
					"times_flaked": 2
				}
			]
		}`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	tests, err := ci.ListFlakyTests(context.Background(), FlakyTestFilter{})
	require.NoError(t, err)
	require.Len(t, tests, 1, "snake_case payload should still decode")
	require.Equal(t, "TestFlakySnake", tests[0].TestName)
	require.Equal(t, 7, tests[0].LastJob.JobNumber)
	require.Equal(t, 2, tests[0].TimesFlaked)
}

func TestCircleCI_GetTestResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path,
			"/api/v2/project/gh/octocat/hello/42/tests"),
			"endpoint path should include slug and job number")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"name":      "TestFlaky",
					"classname": "pkg/foo",
					"file":      "pkg/foo/foo_test.go",
					"result":    "failure",
					"run_time":  1.23,
					"message":   "expected true, got false",
					"source":    "junit",
				},
				{
					"name":      "TestOK",
					"classname": "pkg/foo",
					"result":    "success",
					"run_time":  0.45,
				},
			},
		})
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	results, err := ci.GetTestResults(context.Background(), JobRef{JobNumber: 42})
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "failure", results[0].Result)
	require.Equal(t, "expected true, got false", results[0].Message,
		"failure message should propagate so agents can read it")
	require.Equal(t, 42, results[0].JobNumber)
}

func TestCircleCI_GetTestResults_RejectsZeroJob(t *testing.T) {
	t.Parallel()

	ci := NewCircleCITestInsights(CircleCIConfig{
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})
	_, err := ci.GetTestResults(context.Background(), JobRef{})
	require.Error(t, err, "GetTestResults should refuse JobNumber=0")
}

func TestCircleCI_GetRecentFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/flaky-tests"):
			_, _ = w.Write([]byte(`{
				"flaky-tests": [
					{"test-name": "TestFlaky", "classname": "pkg/foo", "job-number": 100, "workflow-created-at": "2024-04-15T10:00:00Z"},
					{"test-name": "TestFlaky", "classname": "pkg/foo", "job-number": 101, "workflow-created-at": "2024-04-15T11:00:00Z"},
					{"test-name": "TestUnrelated", "classname": "pkg/bar", "job-number": 200}
				]
			}`))
		case strings.Contains(r.URL.Path, "/100/tests"):
			_, _ = w.Write([]byte(`{"items":[
				{"name":"TestFlaky","classname":"pkg/foo","result":"failure","message":"first failure"},
				{"name":"TestSomethingElse","classname":"pkg/foo","result":"success"}
			]}`))
		case strings.Contains(r.URL.Path, "/101/tests"):
			_, _ = w.Write([]byte(`{"items":[
				{"name":"TestFlaky","classname":"pkg/foo","result":"failure","message":"second failure"}
			]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	failures, err := ci.GetRecentFailures(context.Background(), "pkg/foo", "TestFlaky", 10)
	require.NoError(t, err)
	require.Len(t, failures, 2,
		"should follow each flaky occurrence into its job tests endpoint and collect matching failures")
	require.Equal(t, "first failure", failures[0].Message)
	require.Equal(t, "second failure", failures[1].Message)
}

func TestCircleCI_ListFlakyTests_RequiresProjectSlug(t *testing.T) {
	t.Parallel()
	ci := NewCircleCITestInsights(CircleCIConfig{AuthToken: "tok"})
	_, err := ci.ListFlakyTests(context.Background(), FlakyTestFilter{})
	require.Error(t, err, "missing project_slug should be a clear error, not a 404")
}

func TestCircleCI_GetTestResults_FollowsPagination(t *testing.T) {
	t.Parallel()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		token := r.URL.Query().Get("page-token")
		switch calls {
		case 1:
			require.Equal(t, "", token, "first page must not send a page-token")
			_, _ = w.Write([]byte(`{"items":[{"name":"A","result":"failure","message":"a"}],"next_page_token":"PAGE2"}`))
		case 2:
			require.Equal(t, "PAGE2", token, "second call should pass the prior next_page_token")
			_, _ = w.Write([]byte(`{"items":[{"name":"B","result":"failure","message":"b"}],"next_page_token":""}`))
		default:
			t.Fatalf("unexpected extra call %d", calls)
		}
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	results, err := ci.GetTestResults(context.Background(), JobRef{JobNumber: 42})
	require.NoError(t, err)
	require.Len(t, results, 2, "pagination should collect both pages")
	require.Equal(t, "A", results[0].TestName)
	require.Equal(t, "B", results[1].TestName)
	require.Equal(t, 2, calls)
}

func TestCircleCI_Unauthorized_ReturnsSentinel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Permission denied"}`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "bad-tok",
		ProjectSlug: "gh/octocat/hello",
	})

	_, err := ci.ListFlakyTests(context.Background(), FlakyTestFilter{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCircleCIUnauthorized,
		"401 should wrap ErrCircleCIUnauthorized so tool dispatch can prompt for a reconnect")
	require.Contains(t, err.Error(), "Permission denied",
		"error should preserve the upstream body, not just the status code")
}

func TestCircleCI_NonOKErrorIncludesBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid project slug"}`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	_, err := ci.ListFlakyTests(context.Background(), FlakyTestFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid project slug",
		"non-2xx errors should include the upstream message for debugging")
}

func TestCircleCI_GetRecentFailures_SurfacesUnauthorized(t *testing.T) {
	t.Parallel()

	// flaky-tests returns happy data, but the per-job /tests call 401s.
	// Previously GetRecentFailures would silently return [] — the agent
	// would believe "no flakes." With the fix, it must bubble the auth error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/flaky-tests") {
			_, _ = w.Write([]byte(`{"flaky-tests":[{"test-name":"TF","classname":"c","job-number":1}]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	_, err := ci.GetRecentFailures(context.Background(), "c", "TF", 5)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCircleCIUnauthorized,
		"if every job dive 401s, the unauthorized error must propagate — agents must not see []")
}

func TestCircleCI_GetRecentFailures_SurfacesAllDivesFailed(t *testing.T) {
	t.Parallel()

	// Per-job /tests returns 500 every time. Previously we returned [] —
	// the agent would believe "no flakes." With the fix, we surface the
	// underlying error so the agent doesn't draw the wrong conclusion.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/flaky-tests") {
			_, _ = w.Write([]byte(`{"flaky-tests":[{"test-name":"TF","classname":"c","job-number":1}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream down`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	_, err := ci.GetRecentFailures(context.Background(), "c", "TF", 5)
	require.Error(t, err, "if no successful dive returns failures, the dive error must surface")
}

// Concurrent dives must still return the same matching failures and respect
// the unauthorized sentinel as the previous serial implementation.
func TestCircleCI_GetRecentFailures_ConcurrentDives(t *testing.T) {
	t.Parallel()

	const wantTestName = "TF"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/flaky-tests") {
			_, _ = w.Write([]byte(`{"flaky-tests":[
				{"test-name":"TF","classname":"c","job-number":1,"workflow-created-at":"2024-01-01T00:00:00Z"},
				{"test-name":"TF","classname":"c","job-number":2,"workflow-created-at":"2024-01-02T00:00:00Z"},
				{"test-name":"TF","classname":"c","job-number":3,"workflow-created-at":"2024-01-03T00:00:00Z"}
			]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"items":[{"name":"TF","classname":"c","result":"failure","message":"boom-%s"}]}`, r.URL.Path)
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})

	failures, err := ci.GetRecentFailures(context.Background(), "c", wantTestName, 5)
	require.NoError(t, err)
	require.Len(t, failures, 3, "all three matching dives should contribute")
	require.Equal(t, wantTestName, failures[0].TestName)
}

// maxJobDives must cap the dive fan-out even when the flaky-tests list
// contains more matching jobs than the cap.
func TestCircleCI_GetRecentFailures_RespectsMaxJobDives(t *testing.T) {
	t.Parallel()

	var diveCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/flaky-tests") {
			b := strings.Builder{}
			b.WriteString(`{"flaky-tests":[`)
			for i := 1; i <= 10; i++ {
				if i > 1 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"test-name":"TF","classname":"c","job-number":%d}`, i)
			}
			b.WriteString(`]}`)
			_, _ = w.Write([]byte(b.String()))
			return
		}
		atomic.AddInt32(&diveCalls, 1)
		_, _ = w.Write([]byte(`{"items":[{"name":"TF","classname":"c","result":"failure","message":"x"}]}`))
	}))
	defer server.Close()

	ci := NewCircleCITestInsights(CircleCIConfig{
		BaseURL:     server.URL,
		AuthToken:   "tok",
		ProjectSlug: "gh/octocat/hello",
	})
	ci.maxJobDives = 3

	failures, err := ci.GetRecentFailures(context.Background(), "c", "TF", 50)
	require.NoError(t, err)
	require.Len(t, failures, 3, "cap should limit which jobs we dive into")
	require.Equal(t, int32(3), atomic.LoadInt32(&diveCalls),
		"maxJobDives must short-circuit the dive scheduling, not just the result count")
}

func TestCircleCI_IsFailureResult_OnlyDocumentedValues(t *testing.T) {
	t.Parallel()
	require.True(t, isFailureResult("failure"))
	require.True(t, isFailureResult("error"))
	require.True(t, isFailureResult("FAILURE"))
	require.False(t, isFailureResult("success"))
	require.False(t, isFailureResult("skipped"))
	require.False(t, isFailureResult("failed"),
		"'failed' is not part of CircleCI's documented enum (success/failure/skipped/error)")
}
