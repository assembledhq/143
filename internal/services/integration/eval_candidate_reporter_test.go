package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalEvalCandidateReporter_AddCandidateUsesBootstrapRoute(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "reporter should send sandbox token")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"candidate_id":"cand-1","candidate_index":2,"bootstrap_run_id":"boot-1","status":"proposed"}}`))
		require.NoError(t, err, "test server should write response")
	}))
	defer server.Close()

	reporter := NewInternalEvalCandidateReporter("test-token", server.URL, "boot-1")
	result, err := reporter.AddCandidate(context.Background(), AddEvalCandidateParams{
		PRNumber:          1,
		PRTitle:           "Fix",
		BaseCommitSHA:     "abc123",
		SolutionCommitSHA: "def456",
		SolutionDiff:      "diff --git",
		IssueDescription:  "Fix the bug in a reproducible way.",
		ScoringCriteria:   json.RawMessage(`[{"name":"tests","grader_type":"code_check","weight":1}]`),
		Complexity:        "simple",
		FitnessScore:      0.8,
		FitnessReasoning:  "Good regression.",
	})

	require.NoError(t, err, "AddCandidate should succeed")
	require.Equal(t, "/api/v1/internal/evals/bootstrap/boot-1/candidates", gotPath, "reporter should prefer the explicit bootstrap route")
	require.Equal(t, "cand-1", result.CandidateID, "reporter should decode wrapped API response")
}
