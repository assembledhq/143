package db

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type codeReviewTrendQueryCapture struct {
	DBTX
	query string
	args  []any
	calls int
}

func (c *codeReviewTrendQueryCapture) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	c.query, c.args = query, args
	c.calls++
	return c.DBTX.QueryRow(ctx, query, args...)
}

// Opt in explicitly; this records local fixture costs, not production latency.
// It creates 2,000 PRs over two windows and 6,000 completed review attempts, with
// production-shaped tenant/repository and first-attempt indexes.
func TestCodeReviewStore_TrendQueryCostPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("CODE_REVIEW_TREND_PERF") != "1" {
		t.Skip("set CODE_REVIEW_TREND_PERF=1 for isolated query-cost evidence")
	}
	conn := codeReviewAnalyticsPostgresConn(t)
	ctx := context.Background()
	orgID, repoID := uuid.New(), uuid.New()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	clock := start.Add(30 * 24 * time.Hour)
	_, err := conn.Exec(ctx, `
  CREATE INDEX fixture_metadata_reviews ON code_review_session_metadata(org_id,repository_id,created_at);
  CREATE INDEX fixture_metadata_first_attempt ON code_review_session_metadata(org_id,pull_request_id,created_at,id);
  CREATE TEMP TABLE fixture_prs AS SELECT i,gen_random_uuid() AS pr_id,$1::uuid AS org_id,$2::uuid AS repository_id,
   $3::timestamptz + (CASE WHEN i<=1000 THEN 0 ELSE -30 END + (i % 30)) * interval '1 day' + (i % 23)*interval '1 hour' AS first_at
  FROM generate_series(1,2000) AS i;
  INSERT INTO pull_requests SELECT pr_id,org_id,'Cost fixture','acme/api',i FROM fixture_prs;
  CREATE TEMP TABLE fixture_rounds AS SELECT f.*,round,gen_random_uuid() AS session_id
  FROM fixture_prs f CROSS JOIN LATERAL generate_series(1,1+(i%5)) AS round;
  INSERT INTO sessions(id,org_id,revision_context) SELECT session_id,org_id,jsonb_build_object('pull_request_author','user-'||(i%20)::text) FROM fixture_rounds;
  INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,status,head_sha,decision,github_review_id,completed_at,created_at)
  SELECT gen_random_uuid(),org_id,session_id,repository_id,pr_id,'completed','same-head',
   CASE WHEN round=1+(i%5) THEN 'approved' ELSE 'needs_human_review' END,
   CASE WHEN round=1+(i%5) THEN round ELSE NULL END,
   first_at + interval '100 days' + round*interval '1 hour',first_at+(round-1)*interval '1 hour'
  FROM fixture_rounds;
  ANALYZE sessions; ANALYZE pull_requests; ANALYZE code_review_session_metadata;`, pgx.QueryExecModeSimpleProtocol, orgID, repoID, start)
	require.NoError(t, err, "cost fixture should seed both cohort histories and production-shaped indexes")
	capture := &codeReviewTrendQueryCapture{DBTX: conn}
	store := NewCodeReviewStore(capture)
	store.analyticsClock = func() time.Time { return clock }
	tests := []struct {
		name   string
		trend  bool
		sortBy string
	}{
		{name: "legacy"}, {name: "trend", trend: true}, {name: "trend_author_sort", trend: true, sortBy: "approval_rate"},
	}
	// A single local connection owns one isolated schema, so measurements must be
	// sequential instead of parallel subtests to avoid interfering with each other.
	for _, tt := range tests {
		filters := CodeReviewAnalyticsFilters{RepositoryID: &repoID, CreatedAfter: &start, IncludeTrend: tt.trend, AuthorSortBy: tt.sortBy, TrendSpanSeconds: trendIntPointer(30 * 86400)}
		samples := make([]time.Duration, 0, 6)
		for n := 0; n < 7; n++ {
			before := capture.calls
			began := time.Now()
			report, queryErr := store.GetReviewAnalytics(ctx, orgID, filters)
			elapsed := time.Since(began)
			require.NoError(t, queryErr, "cost fixture analytics should execute without changing outcomes")
			require.Equal(t, before+1, capture.calls, "headline and trend must use exactly one database statement")
			require.Equal(t, int64(1000), report.Summary.PRsReviewed, "cost fixture current membership should remain exact")
			if tt.trend {
				var total int64
				for _, point := range report.Trend.Points {
					total += point.Current.PRsReviewed
				}
				require.Equal(t, int64(1000), total, "cost fixture points should sum to the current card count")
			}
			if n > 0 {
				samples = append(samples, elapsed)
			}
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		median := (samples[2] + samples[3]) / 2
		t.Logf("%s: median=%s min=%s max=%s local single-request duty at30s=%.4f%% at5s=%.4f%%", tt.name, median, samples[0], samples[5], 100*median.Seconds()/30, 100*median.Seconds()/5)
		var explainJSON []byte
		require.NoError(t, conn.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+capture.query, capture.args...).Scan(&explainJSON), "query plan should execute on the same isolated fixture")
		var explain []map[string]any
		require.NoError(t, json.Unmarshal(explainJSON, &explain), "query plan should decode for measured evidence")
		require.Len(t, explain, 1, "one statement should produce one plan")
		t.Logf("%s: planning_ms=%v execution_ms=%v root_plan=%v", tt.name, explain[0]["Planning Time"], explain[0]["Execution Time"], explain[0]["Plan"].(map[string]any)["Node Type"])
	}
}
