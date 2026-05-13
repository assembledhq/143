package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
