package db

import (
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
)

// ValidateCodeReviewTrendFilters validates the optional chart contract without
// changing legacy cohort parsing. Range sizes are resolved as unavailable data
// by the store, so an excessive range still returns the existing report.
func ValidateCodeReviewTrendFilters(f CodeReviewAnalyticsFilters) error {
	f = codeReviewTrendNormalizedFilters(f)
	if !f.IncludeTrend {
		return nil
	}
	if f.CreatedAfter == nil && f.CreatedBefore != nil {
		return fmt.Errorf("trend requires created_after when created_before is set")
	}
	if f.CreatedAfter != nil && f.CreatedBefore != nil && f.CreatedBefore.Before(*f.CreatedAfter) {
		return fmt.Errorf("trend date bounds are reversed")
	}
	geometry := []*time.Time{f.TrendCurrentStart, f.TrendCurrentEnd, f.TrendPreviousStart, f.TrendPreviousEnd}
	supplied := 0
	for _, value := range geometry {
		if value != nil {
			supplied++
		}
	}
	if supplied != 0 && supplied != 4 {
		return fmt.Errorf("trend requires all four geometry bounds")
	}
	if supplied != 0 && f.TrendSpanSeconds != nil {
		return fmt.Errorf("trend geometry and nominal span are mutually exclusive")
	}
	if f.CreatedAfter == nil {
		if supplied != 0 || f.TrendSpanSeconds != nil {
			return fmt.Errorf("all time cannot specify trend geometry")
		}
		return nil
	}
	if supplied == 0 && f.TrendSpanSeconds == nil && f.CreatedBefore == nil {
		return fmt.Errorf("start-only trend requires a nominal span or complete geometry")
	}
	if f.TrendSpanSeconds != nil && *f.TrendSpanSeconds <= 0 {
		return fmt.Errorf("trend_span_seconds must be positive")
	}
	if f.TrendSpanSeconds != nil && *f.TrendSpanSeconds <= codeReviewTrendMaxSeconds && f.CreatedBefore != nil {
		end := f.CreatedAfter.Add(time.Duration(*f.TrendSpanSeconds) * time.Second)
		if !f.CreatedBefore.Before(end) {
			return fmt.Errorf("created_before must precede the exclusive nominal trend end")
		}
	}
	if supplied == 4 {
		if !f.TrendCurrentStart.Equal(*f.CreatedAfter) {
			return fmt.Errorf("trend_current_start must equal created_after")
		}
		if !f.TrendPreviousEnd.Equal(*f.TrendCurrentStart) {
			return fmt.Errorf("trend_previous_end must equal trend_current_start")
		}
		if !f.TrendCurrentStart.Before(*f.TrendCurrentEnd) || !f.TrendPreviousStart.Before(*f.TrendPreviousEnd) {
			return fmt.Errorf("trend geometry bounds must be ordered")
		}
		if f.CreatedBefore != nil && !f.CreatedBefore.Before(*f.TrendCurrentEnd) {
			return fmt.Errorf("created_before must precede the exclusive trend_current_end")
		}
	}
	return nil
}

// Match pgx's timestamptz encoding before geometry arithmetic. Nanosecond
// inputs inside one PostgreSQL microsecond describe the same membership bound.
func codeReviewTrendNormalizedFilters(f CodeReviewAnalyticsFilters) CodeReviewAnalyticsFilters {
	normalize := func(value *time.Time) *time.Time {
		if value == nil {
			return nil
		}
		normalized := value.UTC().Truncate(time.Microsecond)
		return &normalized
	}
	f.CreatedAfter, f.CreatedBefore = normalize(f.CreatedAfter), normalize(f.CreatedBefore)
	f.TrendCurrentStart, f.TrendCurrentEnd = normalize(f.TrendCurrentStart), normalize(f.TrendCurrentEnd)
	f.TrendPreviousStart, f.TrendPreviousEnd = normalize(f.TrendPreviousStart), normalize(f.TrendPreviousEnd)
	return f
}

func codeReviewTrendRepresentable(t time.Time) bool { return t.Year() >= 1 && t.Year() <= 9999 }

// Seconds are checked before converting to time.Duration. Ten years can have
// three leap days; calendar span checks below use the actual start's year.
const codeReviewTrendMaxSeconds int64 = 3653 * 86400

func codeReviewTrendQueryArgs(f CodeReviewAnalyticsFilters, generatedAt time.Time) (pgx.NamedArgs, error) {
	f = codeReviewTrendNormalizedFilters(f)
	if err := ValidateCodeReviewTrendFilters(f); err != nil {
		return nil, err
	}
	args := pgx.NamedArgs{
		"trend_generated_at": generatedAt.UTC(), "trend_all_time": f.CreatedAfter == nil,
		"trend_current_start": nil, "trend_current_end": nil,
		"trend_previous_start": nil, "trend_previous_end": nil,
		"trend_membership_end": nil, "trend_unavailable_reason": "",
	}
	if f.CreatedAfter == nil {
		return args, nil
	}
	start := f.CreatedAfter.UTC()
	end, previousStart, previousEnd := start, start, start
	reason := models.CodeReviewTrendUnavailableReason("")
	if !codeReviewTrendRepresentable(start) {
		reason = models.CodeReviewTrendUnrepresentableRange
	}
	if f.TrendCurrentStart != nil {
		end = f.TrendCurrentEnd.UTC()
		previousStart = f.TrendPreviousStart.UTC()
		previousEnd = f.TrendPreviousEnd.UTC()
	} else if f.TrendSpanSeconds != nil {
		if *f.TrendSpanSeconds > codeReviewTrendMaxSeconds {
			reason = models.CodeReviewTrendRangeTooLarge
		} else {
			span := time.Duration(*f.TrendSpanSeconds) * time.Second
			end = start.Add(span)
			previousStart = start.Add(-span)
		}
	} else {
		// PostgreSQL's inclusive timestamp is normalized by one microsecond after
		// discarding sub-microsecond precision exactly as pgx's timestamp encoding.
		end = f.CreatedBefore.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
		if end.Unix()-start.Unix() > codeReviewTrendMaxSeconds {
			reason = models.CodeReviewTrendRangeTooLarge
		} else {
			previousStart = start.Add(-end.Sub(start))
		}
	}
	if reason == "" && (end.After(start.AddDate(10, 0, 0)) || previousEnd.After(previousStart.AddDate(10, 0, 0))) {
		reason = models.CodeReviewTrendRangeTooLarge
	}
	if reason == "" && (!codeReviewTrendRepresentable(end) || !codeReviewTrendRepresentable(previousStart) || !codeReviewTrendRepresentable(previousEnd)) {
		reason = models.CodeReviewTrendUnrepresentableRange
	}
	if reason != "" {
		args["trend_unavailable_reason"] = string(reason)
		return args, nil
	}
	args["trend_current_start"] = start
	args["trend_current_end"] = end
	args["trend_previous_start"] = previousStart
	args["trend_previous_end"] = previousEnd
	if f.CreatedBefore != nil {
		args["trend_membership_end"] = f.CreatedBefore.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
	}
	return args, nil
}

// The optional CTEs extend the original report statement. Current PR facts and
// every legacy section retain their original cohort predicates; the previous
// cohort gets only round facts, and only through the compared elapsed prefix.
const codeReviewTrendCTEs = `,
 trend_input AS (
  SELECT @trend_generated_at::timestamptz AS generated_at,
   @trend_all_time::boolean AS all_time,
   @trend_current_start::timestamptz AS requested_start,
   @trend_current_end::timestamptz AS requested_end,
   @trend_previous_start::timestamptz AS previous_start,
   @trend_previous_end::timestamptz AS previous_end,
   @trend_membership_end::timestamptz AS membership_end,
   @trend_unavailable_reason::text AS input_reason
 ),
 trend_extents AS (
  SELECT MIN(first_requested_at) AS earliest, MAX(first_requested_at) AS latest
  FROM cohort
 ),
 trend_safe_extents AS (
  -- PostgreSQL accepts timestamps well beyond RFC3339's year range. Guard
  -- before adding the microsecond: its maximum finite timestamp cannot advance.
  -- Keep raw latest for the unavailable classification and report preservation.
  SELECT e.*, CASE WHEN isfinite(latest)
   AND EXTRACT(YEAR FROM latest AT TIME ZONE 'UTC') BETWEEN 1 AND 9999
   THEN latest END AS safe_latest
  FROM trend_extents e
 ),
 trend_range AS (
  SELECT i.*, e.latest,
   CASE WHEN all_time THEN e.earliest ELSE requested_start END AS current_start,
   CASE WHEN all_time THEN GREATEST(generated_at, e.safe_latest + interval '1 microsecond') ELSE requested_end END AS current_end,
   CASE WHEN all_time THEN GREATEST(generated_at, e.safe_latest + interval '1 microsecond')
    ELSE LEAST(requested_end, COALESCE(membership_end, requested_end),
     GREATEST(requested_start, generated_at, e.safe_latest + interval '1 microsecond')) END AS observed_end,
   e.safe_latest + interval '1 microsecond' > GREATEST(generated_at, requested_start) AS data_advanced
  FROM trend_input i CROSS JOIN trend_safe_extents e
 ),
 trend_checked_geometry AS (
  SELECT r.*,
   CASE WHEN input_reason <> '' THEN input_reason
    WHEN current_start IS NOT NULL AND (
     NOT isfinite(current_start) OR NOT isfinite(current_end) OR
     EXTRACT(YEAR FROM current_start AT TIME ZONE 'UTC') NOT BETWEEN 1 AND 9999 OR
     EXTRACT(YEAR FROM current_end AT TIME ZONE 'UTC') NOT BETWEEN 1 AND 9999 OR
     NOT isfinite(latest) OR EXTRACT(YEAR FROM latest AT TIME ZONE 'UTC') NOT BETWEEN 1 AND 9999) THEN 'unrepresentable_range'
    WHEN latest IS NOT NULL AND (current_start IS NULL OR current_end <= current_start) THEN 'inconsistent_geometry'
    ELSE '' END AS unavailable_reason
  FROM trend_range r
 ),
 trend_geometry AS (
  SELECT r.*,
   CASE WHEN unavailable_reason = '' THEN EXTRACT(EPOCH FROM current_end-current_start)::numeric END AS full_seconds,
   CASE WHEN unavailable_reason = '' THEN GREATEST(0, EXTRACT(EPOCH FROM observed_end-current_start))::numeric END AS observed_seconds,
   CASE WHEN unavailable_reason = '' AND NOT all_time THEN LEAST(
    GREATEST(0, EXTRACT(EPOCH FROM observed_end-current_start)),
    EXTRACT(EPOCH FROM previous_end-previous_start))::numeric END AS common_seconds
  FROM trend_checked_geometry r
 ),
 trend_previous_cohort AS MATERIALIZED (
  SELECT f.pull_request_id, f.first_requested_at
  FROM first_attempt f
  JOIN pull_requests pr ON pr.id = f.pull_request_id AND pr.org_id = @org_id
  CROSS JOIN trend_geometry g
  WHERE g.unavailable_reason = '' AND NOT g.all_time
   AND f.first_requested_at >= g.previous_start
   AND f.first_requested_at < g.previous_start + g.common_seconds * interval '1 second'
 ),
 trend_previous_rounds AS (
  SELECT m.pull_request_id, m.decision, m.github_review_id,
   ROW_NUMBER() OVER (PARTITION BY m.pull_request_id ORDER BY m.completed_at, m.id)::bigint AS round_number
  FROM code_review_session_metadata m
  JOIN trend_previous_cohort c ON c.pull_request_id = m.pull_request_id
  WHERE m.org_id = @org_id __TREND_REPOSITORY_PREDICATE__
   AND m.status = 'completed' AND m.completed_at IS NOT NULL
 ),
 trend_previous_approvals AS (
  SELECT pull_request_id, MIN(round_number) FILTER (
   WHERE decision = 'approved' AND github_review_id IS NOT NULL) AS approval_round
  FROM trend_previous_rounds GROUP BY pull_request_id
 ),
 trend_facts AS MATERIALIZED (
  SELECT 'current'::text AS period, f.pull_request_id, c.first_requested_at, f.approval_round
  FROM pr_facts f JOIN cohort c ON c.pull_request_id = f.pull_request_id
  UNION ALL
  SELECT 'previous', c.pull_request_id, c.first_requested_at, a.approval_round
  FROM trend_previous_cohort c LEFT JOIN trend_previous_approvals a ON a.pull_request_id = c.pull_request_id
 ),
 trend_width_candidates AS (
  SELECT width, rank FROM (VALUES (86400::bigint, 1), (604800::bigint, 2), (2592000::bigint, 3)) AS widths(width, rank)
  UNION ALL
  SELECT GREATEST(86400, CEIL(full_seconds / 89 / 86400)::bigint * 86400), 4
  FROM trend_geometry WHERE unavailable_reason = '' AND full_seconds > 0
 ),
 trend_layout_candidates AS (
  SELECT w.*, g.*,
   GREATEST(1, FLOOR(full_seconds / width)::bigint +
    CASE WHEN MOD(full_seconds,width) >= width / 2.0 THEN 1 ELSE 0 END) AS full_bins
  FROM trend_width_candidates w CROSS JOIN trend_geometry g
  WHERE g.unavailable_reason = '' AND g.full_seconds > 0
 ),
 trend_candidate_bins AS (
  SELECT l.width, l.rank, l.full_bins, l.common_seconds,
   index * l.width AS lo,
   LEAST(l.observed_seconds, CASE WHEN index = l.full_bins-1 THEN l.full_seconds ELSE (index+1)*l.width END) AS hi,
   CASE WHEN index = l.full_bins-1 THEN l.full_seconds ELSE (index+1)*l.width END AS nominal_hi
  FROM trend_layout_candidates l CROSS JOIN LATERAL generate_series(0, LEAST(l.full_bins,90)-1) AS index
  WHERE l.full_bins <= 90 AND index*l.width < l.observed_seconds
 ),
 trend_selected_width AS (
  SELECT l.width, l.rank
  FROM trend_layout_candidates l
  WHERE l.full_bins <= 90 AND l.full_bins + (SELECT COUNT(*) FROM trend_candidate_bins b
   WHERE b.rank=l.rank AND b.common_seconds > b.lo AND b.common_seconds < b.hi
    AND b.common_seconds-b.lo >= b.width/2.0 AND b.hi-b.common_seconds >= b.width/2.0) <= 90
  ORDER BY l.rank LIMIT 1
 ),
 trend_base_bins AS (
  SELECT b.*, (common_seconds > lo AND common_seconds < hi
   AND common_seconds-lo >= b.width/2.0 AND hi-common_seconds >= b.width/2.0) AS split
  FROM trend_candidate_bins b JOIN trend_selected_width w ON w.rank=b.rank
 ),
 trend_segments AS (
  SELECT b.width, b.nominal_hi,
   CASE WHEN b.split AND segment=1 THEN b.common_seconds ELSE b.lo END AS lo,
   CASE WHEN b.split AND segment=0 THEN b.common_seconds ELSE b.hi END AS hi
  FROM trend_base_bins b CROSS JOIN (VALUES (0),(1)) AS parts(segment)
  WHERE segment=0 OR b.split
 ),
 trend_points AS MATERIALIZED (
  SELECT (ROW_NUMBER() OVER (ORDER BY lo)-1)::integer AS index,
   current_start + lo * interval '1 second' AS current_start,
   CASE WHEN hi = observed_seconds THEN observed_end ELSE current_start + hi * interval '1 second' END AS current_end,
   hi = observed_seconds AND hi < nominal_hi AS current_partial,
   CASE WHEN common_seconds > lo AND NOT (
    common_seconds < hi AND common_seconds-lo < width/2.0)
    THEN previous_start + lo * interval '1 second' END AS previous_start,
   CASE WHEN common_seconds > lo AND NOT (
    common_seconds < hi AND common_seconds-lo < width/2.0)
    THEN previous_start + LEAST(hi,common_seconds) * interval '1 second' END AS previous_end,
   LEAST(hi,common_seconds) = common_seconds AND common_seconds < EXTRACT(EPOCH FROM previous_end-previous_start)
    AND common_seconds < nominal_hi AS previous_partial,
   lo, hi
  FROM trend_segments CROSS JOIN trend_geometry
 ),
 trend_bucket_ranges AS (
  SELECT p.index, 'current'::text AS period, p.current_start AS start, p.current_end AS finish, p.current_partial AS partial,
   p.hi = g.observed_seconds AS final_bucket
  FROM trend_points p CROSS JOIN trend_geometry g
  UNION ALL
  SELECT p.index, 'previous', p.previous_start, p.previous_end, p.previous_partial, false
  FROM trend_points p WHERE p.previous_start IS NOT NULL
 ),
 trend_buckets AS (
  SELECT b.index, b.period, b.start, b.finish, b.partial,
   COUNT(f.pull_request_id)::bigint AS prs_reviewed,
   COUNT(f.approval_round)::bigint AS approved_by_143,
   (percentile_cont(0.5) WITHIN GROUP (ORDER BY f.approval_round))::double precision AS median_rounds_to_approval,
   AVG(f.approval_round)::double precision AS average_rounds_to_approval,
   (percentile_disc(0.95) WITHIN GROUP (ORDER BY f.approval_round))::double precision AS p95_rounds_to_approval,
   COUNT(f.pull_request_id) FILTER (WHERE b.period='current' AND f.first_requested_at >= g.current_end)::bigint AS overflow_prs
  FROM trend_bucket_ranges b CROSS JOIN trend_geometry g
  LEFT JOIN trend_facts f ON f.period=b.period AND f.first_requested_at >= b.start
   AND (f.first_requested_at < b.finish OR (b.final_bucket AND f.first_requested_at >= g.current_end))
  GROUP BY b.index,b.period,b.start,b.finish,b.partial
 ),
 trend_json_points AS (
  SELECT c.index, jsonb_build_object(
   'index',c.index,
   'current',jsonb_build_object('start',to_char(c.start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'end',to_char(c.finish AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'prs_reviewed',c.prs_reviewed,'approved_by_143',c.approved_by_143,
    'median_rounds_to_approval',c.median_rounds_to_approval,'average_rounds_to_approval',c.average_rounds_to_approval,
    'p95_rounds_to_approval',c.p95_rounds_to_approval,'partial',c.partial,'overflow_prs',c.overflow_prs),
   'previous',CASE WHEN p.index IS NULL THEN NULL ELSE jsonb_build_object(
    'start',to_char(p.start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'end',to_char(p.finish AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'prs_reviewed',p.prs_reviewed,'approved_by_143',p.approved_by_143,
    'median_rounds_to_approval',p.median_rounds_to_approval,'average_rounds_to_approval',p.average_rounds_to_approval,
    'p95_rounds_to_approval',p.p95_rounds_to_approval,'partial',p.partial,'overflow_prs',0) END,
   'unequal_exposure',p.index IS NOT NULL AND (c.finish-c.start <> p.finish-p.start OR c.overflow_prs > 0)
  ) AS point
  FROM trend_buckets c LEFT JOIN trend_buckets p ON p.index=c.index AND p.period='previous'
  WHERE c.period='current'
 ),
 trend_result AS (
  SELECT CASE WHEN unavailable_reason <> '' THEN jsonb_build_object(
   'status','unavailable','generated_at',to_char(generated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'unavailable_reason',unavailable_reason)
  ELSE jsonb_build_object(
   'status','available','mode',CASE WHEN all_time THEN 'all_time' ELSE 'finite' END,'generated_at',to_char(generated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
   'bucket_width_seconds',COALESCE((SELECT width FROM trend_selected_width),86400),
   'current_window',CASE WHEN current_start IS NULL THEN NULL ELSE jsonb_build_object(
    'start',to_char(current_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'end',to_char(current_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'observed_end',to_char(observed_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) END,
   'previous_window',CASE WHEN all_time THEN NULL ELSE jsonb_build_object(
    'start',to_char(previous_start AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'end',to_char(previous_end AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'compared_end',to_char((previous_start + common_seconds * interval '1 second') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) END,
   'observed_end_advanced_by_data',COALESCE(data_advanced,false),
   'overflow_prs',(SELECT COUNT(*) FROM cohort WHERE first_requested_at >= g.current_end),
   'latest_included_first_requested_at',to_char(latest AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
   'points',COALESCE((SELECT jsonb_agg(point ORDER BY index) FROM trend_json_points),'[]'::jsonb)
  ) END AS payload
  FROM trend_geometry g
 )`
