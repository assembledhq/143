package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// All fixture writes stay in the isolated schema provided by the existing
// PostgreSQL helper; each parallel case has its own connection and schema.
func insertCodeReviewTrendPR(t *testing.T, conn *pgx.Conn, orgID, repositoryID uuid.UUID, first time.Time, approvedRound int, author string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	prID := uuid.New()
	_, err := conn.Exec(ctx, `INSERT INTO pull_requests (id,org_id,title,github_repo,github_pr_number) VALUES ($1,$2,'Trend fixture','acme/api',1)`, prID, orgID)
	require.NoError(t, err, "fixture should insert an isolated pull request")
	rounds := approvedRound
	if rounds == 0 {
		rounds = 1
	}
	for i := 1; i <= rounds; i++ {
		sessionID := uuid.New()
		_, err = conn.Exec(ctx, `INSERT INTO sessions (id,org_id,revision_context) VALUES ($1,$2,jsonb_build_object('pull_request_author',$3::text,'github_delivery_id',($1::uuid)::text,'request_context',jsonb_build_object('source','issue_comment','author_login',$3::text)))`, sessionID, orgID, author)
		require.NoError(t, err, "fixture should insert captured author and comment requester")
		decision := "needs_human_review"
		var reviewID *int64
		if i == approvedRound {
			decision = "approved"
			value := int64(i)
			reviewID = &value
		}
		// Completion is deliberately outside the first-request window. Every
		// same-head completed rerun contributes one round through posted approval.
		completed := first.Add(time.Duration(100+i) * 24 * time.Hour)
		_, err = conn.Exec(ctx, `INSERT INTO code_review_session_metadata (id,org_id,session_id,repository_id,pull_request_id,status,head_sha,decision,github_review_id,risk_reason_details,completed_at,created_at) VALUES ($1,$2,$3,$4,$5,'completed','same-head',$6,$7,'[{"code":"lines_limit_exceeded"}]',$8,$9)`, uuid.New(), orgID, sessionID, repositoryID, prID, decision, reviewID, completed, first.Add(time.Duration(i-1)*time.Hour))
		require.NoError(t, err, "fixture should insert each completed review as a distinct round")
	}
	return prID
}

func TestCodeReviewStore_GetReviewAnalyticsTrendPostgres(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	previous := start.AddDate(0, 0, -2)
	end := start.AddDate(0, 0, 2)
	upper := end.Add(-time.Microsecond)
	generated := end
	conn := codeReviewAnalyticsPostgresConn(t)
	orgID, otherOrgID, repositoryID, otherRepositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, start, 1, "current")
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, start.Add(time.Hour), 2, "current")
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, upper, 0, "current")
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, end, 4, "excluded-upper")
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, previous, 3, "previous")
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, previous.AddDate(0, 0, 1), 0, "previous")
	insertCodeReviewTrendPR(t, conn, otherOrgID, repositoryID, start, 4, "other-org")
	insertCodeReviewTrendPR(t, conn, orgID, otherRepositoryID, start, 4, "other-repository")
	// An older PR rerun inside either window must not join those cohorts.
	insertCodeReviewTrendPR(t, conn, orgID, repositoryID, previous.Add(-time.Hour), 5, "older")
	store := NewCodeReviewStore(conn)
	store.analyticsClock = func() time.Time { return generated }
	filters := CodeReviewAnalyticsFilters{RepositoryID: &repositoryID, CreatedAfter: &start, CreatedBefore: &upper}
	legacy, err := store.GetReviewAnalytics(context.Background(), orgID, filters)
	require.NoError(t, err, "legacy report should remain executable")
	filters.IncludeTrend = true
	actual, err := store.GetReviewAnalytics(context.Background(), orgID, filters)
	require.NoError(t, err, "optional trend statement should execute with both cohorts")
	trend := actual.Trend
	actual.Trend = nil
	require.Equal(t, legacy, actual, "trends must leave every legacy section current-only and byte-equivalent in value")
	median, average, p95 := 1.5, 1.5, 2.0
	prevMedian, prevAverage, prevP95 := 3.0, 3.0, 3.0
	require.Equal(t, &models.CodeReviewTrend{
		Status: models.CodeReviewTrendAvailable, Mode: models.CodeReviewTrendFinite, GeneratedAt: generated,
		BucketWidthSeconds:             86400,
		CurrentWindow:                  &models.CodeReviewTrendCurrentWindow{Start: start, End: end, ObservedEnd: end},
		PreviousWindow:                 &models.CodeReviewTrendPreviousWindow{Start: previous, End: start, ComparedEnd: start},
		LatestIncludedFirstRequestedAt: &upper,
		Points: []models.CodeReviewTrendPoint{
			{Index: 0, Current: models.CodeReviewTrendBucket{Start: start, End: start.AddDate(0, 0, 1), PRsReviewed: 2, ApprovedBy143: 2, MedianRoundsToApproval: &median, AverageRoundsToApproval: &average, P95RoundsToApproval: &p95}, Previous: &models.CodeReviewTrendBucket{Start: previous, End: previous.AddDate(0, 0, 1), PRsReviewed: 1, ApprovedBy143: 1, MedianRoundsToApproval: &prevMedian, AverageRoundsToApproval: &prevAverage, P95RoundsToApproval: &prevP95}},
			{Index: 1, Current: models.CodeReviewTrendBucket{Start: start.AddDate(0, 0, 1), End: end, PRsReviewed: 1}, Previous: &models.CodeReviewTrendBucket{Start: previous.AddDate(0, 0, 1), End: start, PRsReviewed: 1}},
		},
	}, trend, "each point should preserve exact counts, completed reruns, percentiles, bounds and tenant/repository scope")
}

func TestCodeReviewStore_TrendGeometryPostgres(t *testing.T) {
	t.Parallel()
	utc := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	start := utc(2026, 10, 1, 0)
	tests := []struct {
		name                        string
		start, end, previous, clock time.Time
		first                       *time.Time
		expectedWidth               int64
		expectedCurrentLengths      []time.Duration
		expectedPreviousLengths     []time.Duration
		expectedUnequal             []bool
		expectedCounts              []int64
		expectedOverflow            int64
		expectedAdvanced            bool
		expectedCurrentPartial      []bool
		expectedPreviousPartial     []bool
	}{
		{name: "ordinary partial empty", start: start, end: start.AddDate(0, 0, 2), previous: start.AddDate(0, 0, -2), clock: start.Add(30 * time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{24 * time.Hour, 6 * time.Hour}, expectedPreviousLengths: []time.Duration{24 * time.Hour, 6 * time.Hour}, expectedUnequal: []bool{false, false}, expectedCounts: []int64{0, 0}, expectedCurrentPartial: []bool{false, true}, expectedPreviousPartial: []bool{false, true}},
		{name: "future empty", start: start, end: start.AddDate(0, 0, 2), previous: start.AddDate(0, 0, -2), clock: start.Add(-time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{}, expectedPreviousLengths: []time.Duration{}, expectedUnequal: []bool{}, expectedCounts: []int64{}},
		{name: "December November DST paired short becomes current-only", start: utc(2026, 12, 1, 5), end: utc(2027, 1, 1, 5), previous: utc(2026, 11, 1, 4), clock: utc(2027, 1, 1, 5), expectedWidth: 86400, expectedCurrentLengths: repeatTrendDuration(31, 24*time.Hour), expectedPreviousLengths: append(repeatTrendDuration(30, 24*time.Hour), 0), expectedUnequal: make([]bool, 31), expectedCounts: make([]int64, 31)},
		{name: "fall DST week merges one hour and flags unequal", start: utc(2026, 11, 1, 4), end: utc(2026, 11, 8, 5), previous: utc(2026, 10, 25, 4), clock: utc(2026, 11, 8, 5), expectedWidth: 86400, expectedCurrentLengths: append(repeatTrendDuration(6, 24*time.Hour), 25*time.Hour), expectedPreviousLengths: repeatTrendDuration(7, 24*time.Hour), expectedUnequal: []bool{false, false, false, false, false, false, true}, expectedCounts: make([]int64, 7)},
		{name: "half half split", start: start, end: start.Add(48 * time.Hour), previous: start.Add(-36 * time.Hour), clock: start.Add(48 * time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{24 * time.Hour, 12 * time.Hour, 12 * time.Hour}, expectedPreviousLengths: []time.Duration{24 * time.Hour, 12 * time.Hour, 0}, expectedUnequal: []bool{false, false, false}, expectedCounts: make([]int64, 3)},
		{name: "short unmatched piece flags unequal", start: start, end: start.Add(28 * time.Hour), previous: start.Add(-27 * time.Hour), clock: start.Add(28 * time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{28 * time.Hour}, expectedPreviousLengths: []time.Duration{27 * time.Hour}, expectedUnequal: []bool{true}, expectedCounts: make([]int64, 1)},
		{name: "both short partial pieces prefer current-only", start: start, end: start.Add(48 * time.Hour), previous: start.Add(-26 * time.Hour), clock: start.Add(28 * time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{24 * time.Hour, 4 * time.Hour}, expectedPreviousLengths: []time.Duration{24 * time.Hour, 0}, expectedUnequal: []bool{false, false}, expectedCounts: []int64{0, 0}, expectedCurrentPartial: []bool{false, true}},
		{name: "split would exceed cap uses weekly width", start: start, end: start.Add(90 * 24 * time.Hour), previous: start.Add(-89*24*time.Hour - 12*time.Hour), clock: start.Add(90 * 24 * time.Hour), expectedWidth: 604800, expectedCurrentLengths: append(repeatTrendDuration(12, 7*24*time.Hour), 6*24*time.Hour), expectedPreviousLengths: append(repeatTrendDuration(12, 7*24*time.Hour), 5*24*time.Hour+12*time.Hour), expectedUnequal: append(make([]bool, 12), true), expectedCounts: make([]int64, 13)},
		{name: "both short in sub-day range", start: start, end: start.Add(10 * time.Hour), previous: start.Add(-6 * time.Hour), clock: start.Add(10 * time.Hour), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{10 * time.Hour}, expectedPreviousLengths: []time.Duration{0}, expectedUnequal: []bool{false}, expectedCounts: []int64{0}},
		{name: "90 day plus one second keeps daily width", start: start, end: start.Add(90*24*time.Hour + time.Second), previous: start.Add(-90*24*time.Hour - time.Second), clock: start.Add(90*24*time.Hour + time.Second), expectedWidth: 86400, expectedCurrentLengths: append(repeatTrendDuration(89, 24*time.Hour), 24*time.Hour+time.Second), expectedPreviousLengths: append(repeatTrendDuration(89, 24*time.Hour), 24*time.Hour+time.Second), expectedUnequal: make([]bool, 90), expectedCounts: make([]int64, 90)},
		{name: "rolling overflow remains in last point", start: start, end: start.Add(24 * time.Hour), previous: start.Add(-24 * time.Hour), clock: start.Add(24 * time.Hour), first: trendTimePointer(start.Add(25 * time.Hour)), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{24 * time.Hour}, expectedPreviousLengths: []time.Duration{24 * time.Hour}, expectedUnequal: []bool{true}, expectedCounts: []int64{1}, expectedOverflow: 1, expectedAdvanced: true},
		{name: "data gives nonempty future a point", start: start, end: start.Add(48 * time.Hour), previous: start.Add(-48 * time.Hour), clock: start.Add(-time.Hour), first: trendTimePointer(start.Add(time.Hour)), expectedWidth: 86400, expectedCurrentLengths: []time.Duration{time.Hour + time.Microsecond}, expectedPreviousLengths: []time.Duration{time.Hour + time.Microsecond}, expectedUnequal: []bool{false}, expectedCounts: []int64{1}, expectedAdvanced: true, expectedCurrentPartial: []bool{true}, expectedPreviousPartial: []bool{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			if tt.first != nil {
				insertCodeReviewTrendPR(t, conn, orgID, repoID, *tt.first, 1, "current")
			}
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return tt.clock }
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{RepositoryID: &repoID, CreatedAfter: &tt.start, IncludeTrend: true, TrendCurrentStart: &tt.start, TrendCurrentEnd: &tt.end, TrendPreviousStart: &tt.previous, TrendPreviousEnd: &tt.start})
			require.NoError(t, err, "trend layout should execute against PostgreSQL")
			require.Equal(t, models.CodeReviewTrendAvailable, actual.Trend.Status, "representable geometry should have an available trend")
			require.Equal(t, tt.expectedWidth, actual.Trend.BucketWidthSeconds, "density should use the smallest qualifying fixed width")
			currentLengths := []time.Duration{}
			previousLengths := []time.Duration{}
			unequal := []bool{}
			counts := []int64{}
			currentPartial := []bool{}
			previousPartial := []bool{}
			for _, point := range actual.Trend.Points {
				currentLengths = append(currentLengths, point.Current.End.Sub(point.Current.Start))
				length := time.Duration(0)
				if point.Previous != nil {
					length = point.Previous.End.Sub(point.Previous.Start)
				}
				previousLengths = append(previousLengths, length)
				unequal = append(unequal, point.UnequalExposure)
				counts = append(counts, point.Current.PRsReviewed)
				currentPartial = append(currentPartial, point.Current.Partial)
				previousPartial = append(previousPartial, point.Previous != nil && point.Previous.Partial)
			}
			require.Equal(t, tt.expectedCurrentLengths, currentLengths, "current intervals should retain full data without one-hour stubs")
			require.Equal(t, tt.expectedPreviousLengths, previousLengths, "previous intervals should stop at common exposure and preserve short-piece exceptions")
			require.Equal(t, tt.expectedUnequal, unequal, "unequal durations or nominal overflow context should be flagged")
			require.Equal(t, tt.expectedCounts, counts, "all admitted current PRs should be assigned exactly once")
			expectedCurrentPartial, expectedPreviousPartial := tt.expectedCurrentPartial, tt.expectedPreviousPartial
			if expectedCurrentPartial == nil {
				expectedCurrentPartial = make([]bool, len(tt.expectedCurrentLengths))
			}
			if expectedPreviousPartial == nil {
				expectedPreviousPartial = make([]bool, len(tt.expectedPreviousLengths))
			}
			require.Equal(t, expectedCurrentPartial, currentPartial, "partial current flags should indicate observed-prefix clipping only")
			require.Equal(t, expectedPreviousPartial, previousPartial, "partial previous flags should distinguish prefix clipping from a completed shorter period")
			require.Equal(t, tt.expectedOverflow, actual.Trend.OverflowPRs, "overflow count should describe facts beyond full geometry")
			require.Equal(t, tt.expectedAdvanced, actual.Trend.ObservedEndAdvancedByData, "data-derived clock advancement should be explicit")
			require.LessOrEqual(t, len(actual.Trend.Points), 90, "layout including splits must obey the point cap")
		})
	}
}
func repeatTrendDuration(n int, d time.Duration) []time.Duration {
	result := make([]time.Duration, n)
	for i := range result {
		result[i] = d
	}
	return result
}
func trendTimePointer(t time.Time) *time.Time { return &t }

func TestCodeReviewStore_TrendAllTimePostgres(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		first          *time.Time
		clock          time.Time
		expectedWindow *models.CodeReviewTrendCurrentWindow
		expectedCount  int64
	}{
		{name: "empty", clock: start},
		{name: "history and ahead timestamp", first: trendTimePointer(start), clock: start.Add(-time.Hour), expectedWindow: &models.CodeReviewTrendCurrentWindow{Start: start, End: start.Add(time.Microsecond), ObservedEnd: start.Add(time.Microsecond)}, expectedCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			if tt.first != nil {
				insertCodeReviewTrendPR(t, conn, orgID, repoID, *tt.first, 1, "current")
			}
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return tt.clock }
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true})
			require.NoError(t, err, "alltime should resolve history and geometry in one statement")
			require.Equal(t, tt.expectedWindow, actual.Trend.CurrentWindow, "alltime window should use actual eligible history")
			require.Nil(t, actual.Trend.PreviousWindow, "alltime has no previous comparison")
			require.Equal(t, models.CodeReviewTrendAllTime, actual.Trend.Mode, "alltime should have explicit mode")
			var total int64
			for _, point := range actual.Trend.Points {
				total += point.Current.PRsReviewed
				require.Nil(t, point.Previous, "alltime points have no previous member")
			}
			require.Equal(t, tt.expectedCount, total, "alltime should retain every PR exactly once")
			require.Equal(t, tt.expectedCount, actual.Summary.PRsReviewed, "headline and points must agree")
		})
	}
}

func TestCodeReviewStore_TrendPreviousOnlyPostgres(t *testing.T) {
	t.Parallel()
	conn := codeReviewAnalyticsPostgresConn(t)
	orgID, repoID := uuid.New(), uuid.New()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	previous := start.Add(-24 * time.Hour)
	insertCodeReviewTrendPR(t, conn, orgID, repoID, previous, 2, "prior")
	store := NewCodeReviewStore(conn)
	store.analyticsClock = func() time.Time { return end }
	actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(86400)})
	require.NoError(t, err, "previous-only cohort should have a useful chart")
	require.Equal(t, int64(0), actual.Summary.PRsReviewed, "previous cohort must not enter the current headline")
	require.Equal(t, int64(0), actual.CommentRequestsTotal, "previous requests must not enter the current comment summary")
	require.Equal(t, []models.CodeReviewCommentRequestUserAnalytics{}, actual.CommentRequestsByUser, "previous requester must not enter current users")
	require.Equal(t, []models.CodeReviewAuthorAnalytics{}, actual.Authors, "previous authors must not enter current report")
	require.Equal(t, []models.CodeReviewNonApprovalReasonAnalytics{}, actual.NonApprovalReasons, "previous reasons must not enter current report")
	round := 2.0
	require.Equal(t, []models.CodeReviewTrendPoint{{Index: 0, Current: models.CodeReviewTrendBucket{Start: start, End: end}, Previous: &models.CodeReviewTrendBucket{Start: previous, End: start, PRsReviewed: 1, ApprovedBy143: 1, MedianRoundsToApproval: &round, AverageRoundsToApproval: &round, P95RoundsToApproval: &round}}}, actual.Trend.Points, "previous-only series should retain its exact approval statistics")
}
func trendIntPointer(n int64) *int64 { return &n }

func TestCodeReviewStore_TrendTimestampBoundariesPostgres(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	inclusive := start.Add(24*time.Hour - time.Millisecond)
	geometryEnd := start.Add(24 * time.Hour)
	previous := start.Add(-24 * time.Hour)
	tests := []struct {
		name             string
		request, clock   time.Time
		expectedCount    int64
		expectedEnd      time.Time
		expectedAdvanced bool
	}{
		{name: "exact inclusive upper admitted", request: inclusive, clock: geometryEnd, expectedCount: 1, expectedEnd: inclusive.Add(time.Microsecond)},
		{name: "next microsecond excluded", request: inclusive.Add(time.Microsecond), clock: geometryEnd, expectedEnd: inclusive.Add(time.Microsecond)},
		{name: "request after anchor admitted and advances extent", request: start.Add(14 * time.Hour), clock: start.Add(13 * time.Hour), expectedCount: 1, expectedEnd: start.Add(14*time.Hour + time.Microsecond), expectedAdvanced: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			insertCodeReviewTrendPR(t, conn, orgID, repoID, tt.request, 1, "current")
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return tt.clock }
			filters := CodeReviewAnalyticsFilters{CreatedAfter: &start, CreatedBefore: &inclusive, IncludeTrend: true, TrendCurrentStart: &start, TrendCurrentEnd: &geometryEnd, TrendPreviousStart: &previous, TrendPreviousEnd: &start}
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, filters)
			require.NoError(t, err, "exact timestamp boundaries should execute")
			require.Equal(t, tt.expectedCount, actual.Summary.PRsReviewed, "inclusive membership must be preserved to the microsecond")
			require.Equal(t, tt.expectedEnd, actual.Trend.CurrentWindow.ObservedEnd, "observed end must normalize one microsecond and honor admitted data")
			require.Equal(t, tt.expectedAdvanced, actual.Trend.ObservedEndAdvancedByData, "data advancement should reflect the single statement's facts")
			require.Equal(t, tt.expectedCount, actual.Trend.Points[0].Current.PRsReviewed, "point membership should exactly match cards at timestamp boundaries")
			require.Equal(t, tt.expectedEnd.Sub(start), actual.Trend.Points[0].Previous.End.Sub(previous), "previous exposure should use the exact normalized observed extent")
		})
	}
}

func TestCodeReviewStore_TrendUnavailablePostgres(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(11, 0, 0)
	ancient := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		filters        CodeReviewAnalyticsFilters
		expectedReason models.CodeReviewTrendUnavailableReason
	}{
		{name: "excessive finite bounds", filters: CodeReviewAnalyticsFilters{CreatedAfter: &start, CreatedBefore: &end, IncludeTrend: true}, expectedReason: models.CodeReviewTrendRangeTooLarge},
		{name: "previous shift outside JSON representable years", filters: CodeReviewAnalyticsFilters{CreatedAfter: &ancient, TrendSpanSeconds: trendIntPointer(86400), IncludeTrend: true}, expectedReason: models.CodeReviewTrendUnrepresentableRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			insertCodeReviewTrendPR(t, conn, orgID, repoID, start, 1, "current")
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return start }
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, tt.filters)
			require.NoError(t, err, "unavailable chart should preserve the existing report")
			require.Equal(t, int64(1), actual.Summary.PRsReviewed, "unavailable layout must not alter membership")
			require.Equal(t, &models.CodeReviewTrend{Status: models.CodeReviewTrendUnavailable, GeneratedAt: start, UnavailableReason: tt.expectedReason}, actual.Trend, "unavailable response should contain only its discriminator, anchor and typed reason")
		})
	}
}

func TestCodeReviewStore_TrendPreviousRoundHistoryPostgres(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	previous := start.Add(-24 * time.Hour)
	tests := []struct {
		name          string
		alter         string
		expectedRound float64
	}{
		{name: "same-head reruns", expectedRound: 3},
		{name: "completion order", alter: `UPDATE code_review_session_metadata SET completed_at=CASE WHEN decision='approved' THEN $2::timestamptz + interval '101.5 days' ELSE completed_at END WHERE org_id=$1`, expectedRound: 2},
		{name: "noncompleted attempt excluded", alter: `UPDATE code_review_session_metadata SET status='failed',completed_at=NULL WHERE org_id=$1 AND created_at=$2`, expectedRound: 2},
		{name: "unposted approval excluded", alter: `UPDATE code_review_session_metadata SET decision='approved' WHERE org_id=$1 AND created_at=$2`, expectedRound: 3},
		{name: "postapproval reviews excluded", alter: `UPDATE code_review_session_metadata SET decision='approved',github_review_id=10 WHERE org_id=$1 AND created_at=$2`, expectedRound: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			insertCodeReviewTrendPR(t, conn, orgID, repoID, previous, 3, "previous")
			if tt.alter != "" {
				_, err := conn.Exec(context.Background(), tt.alter, orgID, previous)
				require.NoError(t, err, "fixture should model alternate previous review history")
			}
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return start.Add(24 * time.Hour) }
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, TrendSpanSeconds: trendIntPointer(86400)})
			require.NoError(t, err, "previous approval history should use completed-session ordering")
			require.Equal(t, []models.CodeReviewTrendPoint{{Index: 0, Current: models.CodeReviewTrendBucket{Start: start, End: start.Add(24 * time.Hour)}, Previous: &models.CodeReviewTrendBucket{Start: previous, End: start, PRsReviewed: 1, ApprovedBy143: 1, MedianRoundsToApproval: &tt.expectedRound, AverageRoundsToApproval: &tt.expectedRound, P95RoundsToApproval: &tt.expectedRound}}}, actual.Trend.Points, "previous cohort should count completed reruns only through first posted approval")
		})
	}
}

func TestCodeReviewStore_TrendCompletedCalendarPrefixPostgres(t *testing.T) {
	t.Parallel()
	conn := codeReviewAnalyticsPostgresConn(t)
	orgID, repoID := uuid.New(), uuid.New()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	previous := start.AddDate(0, -1, 0)
	prefixEnd := previous.Add(30 * 24 * time.Hour)
	insertCodeReviewTrendPR(t, conn, orgID, repoID, prefixEnd.Add(-time.Microsecond), 1, "included-prior")
	insertCodeReviewTrendPR(t, conn, orgID, repoID, prefixEnd, 2, "excluded-prior")
	store := NewCodeReviewStore(conn)
	store.analyticsClock = func() time.Time { return end.AddDate(0, 1, 0) }
	actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: trendTimePointer(end.Add(-time.Microsecond)), TrendCurrentStart: &start, TrendCurrentEnd: &end, TrendPreviousStart: &previous, TrendPreviousEnd: &start})
	require.NoError(t, err, "completed calendar range should keep the same common-prefix population")
	require.Equal(t, &models.CodeReviewTrendPreviousWindow{Start: previous, End: start, ComparedEnd: prefixEnd}, actual.Trend.PreviousWindow, "longer previous month should remain clipped after the current month finishes")
	expected := make([]models.CodeReviewTrendPoint, 30)
	round := 1.0
	for i := range expected {
		expected[i] = models.CodeReviewTrendPoint{Index: i, Current: models.CodeReviewTrendBucket{Start: start.Add(time.Duration(i) * 24 * time.Hour), End: start.Add(time.Duration(i+1) * 24 * time.Hour)}, Previous: &models.CodeReviewTrendBucket{Start: previous.Add(time.Duration(i) * 24 * time.Hour), End: previous.Add(time.Duration(i+1) * 24 * time.Hour)}}
	}
	expected[29].Previous.PRsReviewed = 1
	expected[29].Previous.ApprovedBy143 = 1
	expected[29].Previous.MedianRoundsToApproval = &round
	expected[29].Previous.AverageRoundsToApproval = &round
	expected[29].Previous.P95RoundsToApproval = &round
	require.Equal(t, expected, actual.Trend.Points, "exact common-end timestamp should be excluded while the previous microsecond is retained")
}

func TestCodeReviewStore_TrendSubmicrosecondMembershipPostgres(t *testing.T) {
	t.Parallel()
	conn := codeReviewAnalyticsPostgresConn(t)
	orgID, repoID := uuid.New(), uuid.New()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	raw := start.Add(999 * time.Nanosecond)
	insertCodeReviewTrendPR(t, conn, orgID, repoID, start, 1, "current")
	insertCodeReviewTrendPR(t, conn, orgID, repoID, start.Add(-time.Microsecond), 2, "previous")
	store := NewCodeReviewStore(conn)
	store.analyticsClock = func() time.Time { return start.Add(time.Second) }
	actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &raw, CreatedBefore: &raw})
	require.NoError(t, err, "equal submicrosecond membership bounds should infer a representable one-microsecond chart")
	currentRound, previousRound := 1.0, 2.0
	require.Equal(t, []models.CodeReviewTrendPoint{{Index: 0, Current: models.CodeReviewTrendBucket{Start: start, End: start.Add(time.Microsecond), PRsReviewed: 1, ApprovedBy143: 1, MedianRoundsToApproval: &currentRound, AverageRoundsToApproval: &currentRound, P95RoundsToApproval: &currentRound}, Previous: &models.CodeReviewTrendBucket{Start: start.Add(-time.Microsecond), End: start, PRsReviewed: 1, ApprovedBy143: 1, MedianRoundsToApproval: &previousRound, AverageRoundsToApproval: &previousRound, P95RoundsToApproval: &previousRound}}}, actual.Trend.Points, "normalized previous span must not collapse or lose current facts")
	require.Equal(t, int64(1), actual.Summary.PRsReviewed, "current membership should match pgx's microsecond encoding")
}

func TestCodeReviewStore_TrendWideAllTimePostgres(t *testing.T) {
	t.Parallel()
	conn := codeReviewAnalyticsPostgresConn(t)
	orgID, repoID := uuid.New(), uuid.New()
	first := time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := latest.Add(time.Microsecond)
	insertCodeReviewTrendPR(t, conn, orgID, repoID, first, 1, "first")
	insertCodeReviewTrendPR(t, conn, orgID, repoID, latest, 1, "latest")
	store := NewCodeReviewStore(conn)
	store.analyticsClock = func() time.Time { return latest }
	actual, err := store.GetReviewAnalytics(context.Background(), orgID, CodeReviewAnalyticsFilters{IncludeTrend: true})
	require.NoError(t, err, "wide alltime history should remain available without saturating durations")
	require.Equal(t, models.CodeReviewTrendAvailable, actual.Trend.Status, "alltime history is not subject to finite ten-year limit")
	// Use checked Unix-second arithmetic rather than time.Duration for the
	// thousand-year span; each returned nominal width is still a whole-day grid.
	seconds := end.Unix() - first.Unix()
	width := ((seconds + 89*86400 - 1) / (89 * 86400)) * 86400
	fullBins := seconds / width
	if seconds%width >= width/2 {
		fullBins++
	}
	expected := make([]models.CodeReviewTrendPoint, int(fullBins))
	one := 1.0
	for i := range expected {
		bucketStart := time.Unix(first.Unix()+int64(i)*width, 0).UTC()
		bucketEnd := time.Unix(first.Unix()+int64(i+1)*width, 0).UTC()
		if i == len(expected)-1 {
			bucketEnd = end
		}
		expected[i] = models.CodeReviewTrendPoint{Index: i, Current: models.CodeReviewTrendBucket{Start: bucketStart, End: bucketEnd}}
		if i == 0 || i == len(expected)-1 {
			expected[i].Current.PRsReviewed = 1
			expected[i].Current.ApprovedBy143 = 1
			expected[i].Current.MedianRoundsToApproval = &one
			expected[i].Current.AverageRoundsToApproval = &one
			expected[i].Current.P95RoundsToApproval = &one
		}
	}
	require.Equal(t, width, actual.Trend.BucketWidthSeconds, "wide history should resolve bounded whole-day density in SQL")
	require.Equal(t, expected, actual.Trend.Points, "wide elapsed arithmetic must preserve raw final microsecond and both historical PRs")
	require.Equal(t, int64(2), actual.Summary.PRsReviewed, "wide trend's two retained points must match the headline")
}

func TestCodeReviewStore_TrendUnrepresentableHistoryPostgres(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name             string
		first            time.Time
		specialTimestamp string
		finite           bool
	}{
		{name: "unrepresentable alltime year", first: future},
		{name: "unrepresentable rolling member", first: future, finite: true},
		{name: "infinite alltime history", first: clock, specialTimestamp: "infinity"},
		{name: "maximum PostgreSQL alltime timestamp", first: clock, specialTimestamp: "294276-12-31 23:59:59.999999+00"},
		{name: "maximum PostgreSQL rolling timestamp", first: clock, specialTimestamp: "294276-12-31 23:59:59.999999+00", finite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := codeReviewAnalyticsPostgresConn(t)
			orgID, repoID := uuid.New(), uuid.New()
			prID := insertCodeReviewTrendPR(t, conn, orgID, repoID, tt.first, 1, "history")
			if tt.specialTimestamp != "" {
				_, err := conn.Exec(context.Background(), `UPDATE code_review_session_metadata SET created_at=$3::text::timestamptz WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID, tt.specialTimestamp)
				require.NoError(t, err, "fixture should model unrepresentable database history")
			}
			store := NewCodeReviewStore(conn)
			store.analyticsClock = func() time.Time { return clock }
			filters := CodeReviewAnalyticsFilters{IncludeTrend: true}
			if tt.finite {
				filters.CreatedAfter = &clock
				filters.TrendSpanSeconds = trendIntPointer(86400)
			}
			legacyFilters := filters
			legacyFilters.IncludeTrend = false
			legacy, err := store.GetReviewAnalytics(context.Background(), orgID, legacyFilters)
			require.NoError(t, err, "legacy report should handle all PostgreSQL timestamp history")
			actual, err := store.GetReviewAnalytics(context.Background(), orgID, filters)
			require.NoError(t, err, "unrepresentable chart history should not break the existing report")
			require.Equal(t, int64(1), actual.Summary.PRsReviewed, "unrepresentable history must retain headline membership")
			require.Equal(t, &models.CodeReviewTrend{Status: models.CodeReviewTrendUnavailable, GeneratedAt: clock, UnavailableReason: models.CodeReviewTrendUnrepresentableRange}, actual.Trend, "unrepresentable chart history should return the typed unavailable discriminator")
			actual.Trend = nil
			require.Equal(t, legacy, actual, "unavailable trend must preserve every legacy report section exactly")
		})
	}
}

func TestCodeReviewStore_TrendRejectsIncompatibleNominalMembership(t *testing.T) {
	t.Parallel()
	conn := codeReviewAnalyticsPostgresConn(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := start.Add(10 * 24 * time.Hour)
	_, err := NewCodeReviewStore(conn).GetReviewAnalytics(context.Background(), uuid.New(), CodeReviewAnalyticsFilters{IncludeTrend: true, CreatedAfter: &start, CreatedBefore: &before, TrendSpanSeconds: trendIntPointer(86400)})
	require.EqualError(t, err, "created_before must precede the exclusive nominal trend end", "direct store callers should enforce finite membership containment before querying")
}
