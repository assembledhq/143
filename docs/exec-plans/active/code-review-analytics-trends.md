# Code review analytics trends and previous-window comparison

Status: Implementation and validation complete. Merge and deployment pending. Last updated: 2026-10-09.

This plan follows the user's request to select an Analytics metric card and see
its trend over the selected period alongside the previous window. It builds on
the completed-review round definition and six headline metrics in
[PR-centric code review analytics](../../design/implemented/122-pr-centric-code-review-analytics.md).
The inspected checkout is `7beeca99`; the Analytics contracts are also unchanged
on inspected main `720803ff` relative to the original `5f963e0f` baseline.
Implementation changes are being verified against the acceptance cases below.

The repository has no `.agent/PLANS.md` or `docs/AGENTS.md` at this revision.
This document uses the existing `docs/exec-plans/active/` convention and the
ExecPlan writer's self-contained planning requirements and living sections.

## Purpose and observable outcome

The Analytics page will show one comparison chart immediately below its
repository and time filters, before the PR-author table. Selecting any of the
six headline cards changes the chart's metric and highlights the selected card.
Average rounds to approval is the proposed default because it exposes changes
that a median of one can hide.

The current period uses a solid line and the previous period uses a dashed line,
with explicit date ranges in the legend. Align the series by elapsed position
within their windows. A compact tooltip shows period dates, values, and sample
counts. Exact timestamp intervals and durations remain in the expandable data
table. Full ranges and comparison definitions appear in a collapsed "About
this chart" disclosure. Keyboard and touch users can access the same information.

Compare only the same elapsed portion of the previous full period. Calendar
presets use the preceding calendar period: October
month-to-date compares with September from its beginning. Rolling and custom
ranges use the immediately preceding equal-duration window. Keep full requested
ranges and actual compared ranges explicit. Always plot all current-cohort PRs;
if the previous calendar period is shorter, retain the current tail and make
missing or unequal comparison intervals explicit. The first version adds no
whole-period comparison summary or delta; existing cards continue to describe
the current cohort.

The metric choice is bookmarkable through a proposed `analytics_metric` URL
parameter and survives repository, date-range, and tab changes. Choosing a
different metric uses already-loaded data rather than issuing another request.

| Selectable card | Bucket value | Population shown in the tooltip |
| --- | --- | --- |
| PRs reviewed | Unique PRs first sent in the bucket | All bucket PRs |
| Automatically approved | Those PRs with a posted approval from a completed review | Approved PRs and total bucket PRs |
| PR approval rate | Approved PRs divided by all bucket PRs | Numerator and denominator |
| Median rounds to approval | Median completed reviews through first posted approval | Approved bucket PRs |
| Average rounds to approval | Arithmetic mean of those review counts | Approved bucket PRs |
| P95 rounds to approval | Discrete 95th percentile of those review counts | Approved bucket PRs |

## Progress

- [x] Inspect the Analytics cards, page query, time-range helpers, API, SQL, and existing Recharts usage.
- [x] Draft the proposed interaction, comparison contract, delivery sequence, and verification cases.
- [x] Resolve unfinished-period comparison, clock, bucket stability, empty-current card visibility, and selection-history behavior.
- [x] Implement and verify the window/bucket contracts and backend response.
- [x] Implement selectable cards, URL state, and the chart.
- [x] Complete focused tests, integration checks, browser verification, and independent review.
- [ ] Merge and verify the eventual deployment.

## Surprises & Discoveries

- Analytics is a PR cohort report: the date filter selects when a PR was first
  requested, while approvals can come from later reviews outside that period.
  Plotting approval events by their posting date would produce a different metric.
- `GetReviewAnalytics` already computes a reusable per-PR `approval_round` and
  aggregates all six headline metrics in one PostgreSQL statement. It has no
  time-series data today.
- Rolling ranges provide only `created_after`; calendar and custom ranges also
  provide an inclusive `created_before`. Previous-window comparison therefore
  needs an explicit resolved end and careful boundary normalization.
- Membership filters and chart geometry must be separate. Rolling membership
  keeps its existing open end; a geometry-only nominal span controls density
  without hiding recent PRs when browser, app, and database clocks differ.
- All time has no meaningful preceding equal-duration window.
- The current component returns early when the current cohort is empty. That
  would hide useful previous-window data and must change for the comparison view.
- Metric cards already contain an info-tooltip button. Wrapping the whole card
  in another button would create nested interactive elements.
- Recharts is already installed and used by the usage and automation charts.
  There is no shared `ui/chart.tsx` in this checkout; no chart dependency is needed.
- PostgreSQL membership encoding truncates timestamps to microseconds. Geometry
  validation must use the same precision before checking or shifting bounds.
- Historical PostgreSQL timezone offsets can include seconds, which Go's
  RFC3339 decoder cannot read. The query formats trend timestamps directly as
  UTC RFC3339 strings, and returns unavailable geometry for non-finite or
  out-of-range history while preserving the existing report.
- Guard timestamp representability before advancing the latest request by one
  microsecond; PostgreSQL's maximum finite timestamp cannot advance safely.
- Wide All time histories require the final bucket to retain the raw observed
  end. Reconstructing that endpoint from elapsed seconds can lose a microsecond
  and exclude the latest PR.

## Decision Log

These choices define the implementation contract and defaults.

1. **Offer all six metrics, defaulting to Average.** Counts show usage, approval
   rate shows cohort outcomes, and median/average/P95 describe different parts
   of the review-round distribution.
2. **Use one inline chart.** Put it below the filters so its scope is visible and
   the existing author report remains directly accessible. Always render all six
   selectable cards, including zero counts and unavailable round values when the
   current cohort is empty.
3. **Extend the existing response optionally.** Request `include_trend=true` on
   the existing Analytics endpoint. Compute headline cards, current buckets,
   and previous buckets in the same SQL statement and observation snapshot.
4. **Preserve the PR cohort contract.** Assign each PR to its first-request
   bucket and use its completed-review history through first posted approval.
   Evidence-only rechecks remain excluded, as the existing cards explain.
5. **Use calendar periods for calendar presets, equal spans otherwise.** The
   browser-local helper supplies full current/previous month or week boundaries;
   both meet at the current start but their elapsed durations can differ. For
   rolling/custom ranges, previous is the consecutive equal-duration window.
   Plot previous only through the common elapsed extent, even after a period
   finishes; never switch comparison populations at completion. Plot every
   current member and show unmatched bins without a previous value; a retained
   short tail uses the explicitly flagged duration mismatch defined below. All time
   shows one historical series and explains that comparison needs a finite range.
6. **Show sample size and maturity.** For approval and rounds metrics, explain
   that recent PRs may still receive approvals and that previous-window PRs have
   had longer to do so. Do not label the comparison as a measured improvement or
   regression automatically.
7. **Bound chart density and keep both series on the same grid.** Choose the
   smallest standard fixed width (24 hours, seven days, then 30 days) that gives
   at most 90 bins after coalescing a trailing remainder shorter than half a bin
   into its predecessor. This prevents a small clock or DST remainder from
   creating a near-empty point or switching a 90-day report to weekly bins. For
   longer ranges, choose a whole-day width that caps each series at 90 points.
   Return each actual interval: a coalesced final bin is wider than the nominal
   width. Show interval duration and sample counts; retain low-sample values.
   Count any required common-boundary split before enforcing the point cap.
8. **Use one injectable Go clock.** Capture the range-resolution anchor once and
   pass it into the query. SQL supplies one consistent outcome snapshot, which
   can be slightly later than that anchor; this is not historical reconstruction.
   Derive current data extents in that same statement and assign every current
   member to a point, even when its timestamp exceeds the layout clock.
9. **Replace URL history on metric selection.** Match the existing filter
   behavior while keeping the selected metric bookmarkable. Do not add an entry
   for every click. Keep metric selection out of the data cache key.
10. **Bound finite trend ranges at ten years.** Use checked timestamp/seconds
    arithmetic, not an unchecked `time.Duration` subtraction. Excessive or
    unrepresentable trend ranges show a chart-unavailable explanation while the
    legacy report remains usable. All time uses real eligible history and the
    same point cap; never fabricate an earlier start when there is no history.
11. **Keep ordinary chart copy compact.** Show a concise date-range legend and
    one tooltip row per period with its value, date, and sample count. Keep full
    timestamp ranges, durations, and comparison explanations in the data table
    and collapsed "About this chart" disclosure. Inline notices are reserved for
    relevant exceptions.

## Context and orientation

| Existing responsibility | Source |
| --- | --- |
| Analytics route, filter/query state, refresh, and report integration | `frontend/src/app/(dashboard)/code-reviews/page.tsx` |
| Headline cards and author/round/reason reports | `frontend/src/components/code-review-analytics.tsx` |
| Accessible metric definitions | `frontend/src/components/metric-info-tooltip.tsx` |
| Browser-local calendar and rolling range resolution | `frontend/src/lib/time-range.ts` |
| API request construction and response types | `frontend/src/lib/api.ts`, `frontend/src/lib/types.ts` |
| Analytics cache scope | `frontend/src/lib/query-keys.ts` |
| Existing chart implementation to consult | `frontend/src/app/(dashboard)/settings/usage/usage-timeseries-chart.tsx` |
| Analytics HTTP parsing and response | `parseCodeReviewAnalyticsFilters` and `Analytics` in `internal/api/handlers/code_reviews.go` |
| PR cohort, completed rounds, and aggregation | `CodeReviewAnalyticsFilters` and `GetReviewAnalytics` in `internal/db/code_reviews.go` |
| Backend response model | `CodeReviewAnalytics` in `internal/models/code_review.go` |
| Behavioral and API contract tests | `internal/db/code_reviews_postgres_test.go`, `internal/db/code_reviews_test.go`, `internal/api/handlers/code_reviews_test.go` |

Use `docs/design/overall.md` for architecture context and the scoped `AGENTS.md`
instructions for each touched directory. The comparison chart lives in
`frontend/src/components/code-review-analytics-trend.tsx`.

## Data and comparison contract

### One definition for cards and points

Resolve the two windows before the period-specific aggregations. Find each PR's
first-ever request before applying those windows; an in-window rerun must not
admit an older PR. Load completed review history for the union of both cohorts,
including later reviews outside the selected windows. Assign rounds by
`completed_at, id`, count same-revision reruns separately, and stop at the first
completed review with an approved decision and a posted GitHub review ID.

Carry `first_requested_at` into the per-PR facts and tag current/previous
membership. Aggregate the existing author, reason, and operational sections for
the current cohort only. Previous-window work needs the five underlying numeric
metrics used by the six cards, not a second author or finding report. Always
load only the previous cohort's compared elapsed prefix,
not its unobserved-by-comparison remainder. Preserve explicit `org_id` predicates
throughout and repository narrowing on base scans.

Compute each bucket's median, mean, and P95 directly from its approved PR facts.
Compute headline statistics directly from current whole-window PR facts too;
never average bucket percentages or percentiles to reconstruct headline values.
Use exactly the cards' `percentile_cont(0.5)` for median, `AVG` for mean, and
`percentile_disc(0.95)` for P95 in every bucket. An even
sample can therefore have a fractional median. Derive approval percentages
from raw approved and reviewed counts in the UI.

Both periods use outcomes observed by the database statement, not reconstructed
historical outcomes as of each bucket's closing date. Recent cohort results can
change on later refreshes. This is the same eventual-outcome interpretation as
the headline cards and must be visible in chart copy. Do not age-match or cut off
previous PRs' later review histories in this first version. Display sample counts
and explain the remaining cohort-maturity difference.

### Windows, timestamps, and bins

- Capture one `generated_at` range-resolution anchor in Go through an injectable
  clock, pass it as a SQL parameter, and return it. The SQL statement supplies
  one outcome snapshot; do not claim that all outcomes were frozen at the earlier
  Go instant. Preserve the exact legacy current-cohort predicates for cards and
  every existing report section. Chart geometry never adds `created_before` to
  rolling membership or replaces a finite membership bound.
- Rolling trend requests add a geometry-only nominal span (7, 30, or 90 days),
  not a hard membership end. Resolve nominal end from start plus this fixed span.
  Pick density from the nominal span after short-tail coalescing. The injected
  anchor and SQL data extents determine what is observed; neither removes a PR
  already admitted by the legacy membership filters.
- Preserve current filter membership when adapting inclusive upper bounds to
  half-open internal ranges. PostgreSQL timestamps have microsecond precision;
  normalize the existing inclusive end to the immediately following representable
  instant. Test an exact upper-bound timestamp and its next microsecond.
- Full geometry uses half-open ranges meeting at current start. For calendar
  presets, frontend-local geometry expands this_month/this_week to the full
  calendar month/week and chooses the preceding calendar period. It does not
  change their existing membership end-of-today filter. Last-month/week and
  two-week calendar presets also use their preceding calendar period. For
  rolling/custom geometry `[start,end)`, previous remains `[start-duration,start)`.
  Validate previous_start < previous_end = current_start and reject overlapping
  or reversed geometry. Check both full spans against the ten-year limit and
  require any nominal rolling span to be positive and bounded by that limit;
  never infer a calendar preset from timestamps alone.
- Let current observed elapsed length be the bounded length determined by the
  injected anchor and same-statement current data extents. Common extent is the
  minimum of that length and previous full duration. Plot previous only through
  `previous_start + common_extent`, and omit its later remainder. Plot all current
  data through its observed end. If the common boundary falls inside a bucket,
  split only when both pieces are at least half the nominal bucket width: paired
  segments have equal durations, and the remaining current segment has
  previous=null. Otherwise, if the paired piece is shorter than half a nominal
  bucket, make the whole bucket current-only with previous=null. If the paired
  piece is at least half a nominal bucket and the unmatched current piece is
  shorter, retain the whole current bucket paired with previous clipped at the
  common extent and set `unequal_exposure=true`. Show both actual durations and
  explain the mismatch; do not claim equal exposure. Apply this rule before and
  after period completion, preserve
  every current PR, and include any split in the final point-cap calculation.
  Preserve full requested ranges in metadata. Empty future-only periods have no
  points, rather than manufactured zeros.
- Bin finite ranges by fixed elapsed widths anchored to the current start and
  pair previous bins by applying the same elapsed offsets to previous start.
  Use fixed seconds, not timezone-sensitive SQL day intervals. Merge
  a final remainder shorter than half a bucket into the preceding bucket before
  observation clipping; leave a range shorter than one bucket as one bucket.
  Return actual interval durations, including a widened coalesced bin and a
  shortened partially observed bin. Do not re-bin in browser-local dates or
  depend on the database connection's timezone.
- Return UTC instants and format actual dates/times in the browser's local
  timezone. Fixed elapsed buckets can start at 23:00 or 01:00 after a
  daylight-saving change; labels and tooltips must show their actual intervals
  rather than implying local calendar-day boundaries. Elapsed positions and
  period membership remain stable.
- Mark partial current bins and matched previous intervals. Future time is not
  plotted as zero activity. Zero-data observed bins have zero counts and null
  rate/rounds.
- In the same SQL statement, derive minimum and maximum first-request instants
  from the current cohort. Set observed end to the maximum of current start,
  injected clock and latest current request plus one microsecond, capped by full
  geometry end and, when present, normalized finite membership end. This preserves
  the legacy upper-bound microsecond while matching previous exposure to the
  actual admitted period. It gives a nonempty cohort a bucket if the clock precedes
  its start. Assign any current member beyond the geometry end to the final
  observed bucket. Return `overflow_prs`, `latest_included_first_requested_at`,
  and whether data advanced the observed end. Mark the affected final bucket;
  its displayed interval is nominal rather than exact attribution for overflow.
  When overflow occurs, describe the final pair as nominal-period context, not
  matched exposure, and explain that the latest current PRs are included. Never
  drop admitted PRs to manufacture agreement between cards and
  points. If geometry cannot represent a nonempty cohort, show an unavailable
  chart while retaining the cards instead of dropping data.
- For All time, derive the earliest eligible first-request and latest current
  first-request in SQL, then derive its width and bucket boundaries in that same
  statement using fixed seconds. End at the maximum of the injected clock and
  latest first-request plus one microsecond. Do not issue a pre-query. Previous
  window is null. With no history, current window is also
  null, points are empty, and cards keep zero/null summary metrics.
- Reject reversed or end-only trend bounds with a structured 400. Do not change
  legacy request parsing. Also reject a start-only trend request with no nominal
  span or complete explicit geometry with a structured 400; do not silently
  infer its end from the clock. Check finite span and shifted timestamps before
  arithmetic; if a range is too large or cannot be represented, return a typed
  trend-unavailable reason alongside the existing report. If All time history
  cannot be represented, also keep the report usable with no trend.

Worked examples assume October 9, 2026 at 14:00 in America/New_York, no clock
overflow, and the existing Sunday-based week boundaries:

| Selection | Full current geometry | Full previous geometry | Plotted/compared spans |
| --- | --- | --- | --- |
| This month | Oct 1–Nov 1 | Sep 1–Oct 1 | Oct 1–9 14:00 and Sep 1–9 14:00 |
| This week | Oct 4–11 | Sep 27–Oct 4 | Oct 4–9 14:00 and Sep 27–Oct 2 14:00 |
| Last 7 days | Oct 2 14:00–Oct 9 14:00 | Sep 25 14:00–Oct 2 14:00 | Both full seven-day spans; rolling card membership remains open-ended |
| Custom Oct 5–18 inclusive | Oct 5–19 | Sep 21–Oct 5 | Oct 5–9 14:00 and Sep 21–25 14:00 |

For October 31 against September, current observations beyond September's
30-day duration remain visible. Apply the half-bucket split rule at the common
elapsed boundary, including its short-piece exceptions; do not drop current PRs
or extend the previous window into October. Test 31/30-day months, February, DST, and completed
calendar presets with this same prefix rule. Finite observed ends also respect
the existing inclusive membership end normalized by one microsecond.

For completed December 2026 against November in America/New_York, the last
daily bucket has only one paired hour and becomes wholly current-only. For the
completed November 1–8 week against October 25–November 1, the final 25-hour
current bucket stays paired with the 24-hour previous interval and flags unequal
exposure. Neither case creates a separate one-hour point.

### Implemented API addition

Keep `GET /api/v1/code-reviews/analytics`, its authenticated organization context,
and existing read roles (`admin`, `builder`, `member`, `viewer`). Retain repository,
date, and author-sort parameters. Add an optional Boolean `include_trend`,
defaulting to false, with a structured 400 error for malformed input.
Add geometry-only trend parameters alongside it; these never replace the
legacy membership date filters. A rolling request provides its nominal span
in seconds. Calendar/custom requests provide explicit current and previous
geometry starts and exclusive ends from the browser-local range helper. Require
current geometry start to equal the existing created_after and previous end to
equal current start. Validate previous start precedes its end, both geometry
spans are at most ten years, and finite membership end is within current geometry.
Requests with both finite membership bounds and no explicit geometry retain
equal-span inference; do not guess calendar alignment. A start-only trend
request must supply a positive bounded nominal span or complete explicit
geometry, otherwise return a structured 400. All time sends no geometry. The
implemented parameters are `trend_span_seconds`, `trend_current_start`,
`trend_current_end`, `trend_previous_start`, and `trend_previous_end`. Validate
them only when `include_trend=true`.

Optional `data.trend` on `CodeReviewAnalytics` contains:

- `generated_at`: the Go range-resolution anchor.
- `bucket_width_seconds`: the shared fixed elapsed bucket width.
- `current_window`: resolved UTC start, exclusive full end, and observed end.
- `previous_window`: resolved full range and compared end, or null for All time.
- `unavailable_reason`: an optional typed reason for an excessive or
  unrepresentable range; retain the legacy report when present.
- Clock/data extent metadata: `observed_end_advanced_by_data`, `overflow_prs`,
  and `latest_included_first_requested_at`, including the affected point's count.
- `points`: ordered paired bins with an index, actual start/end instants, raw
  reviewed and approved counts, nullable round statistics, and partial/observed
  flags, including `unequal_exposure` for a retained short-tail pair. The previous
  member is null for All time and current-only bins.

`generated_at` is the Go range-resolution anchor, not a historical outcome
cutoff. Existing whole-window metrics remain in `data.summary`; no previous
whole-period summary or delta is added. Use typed
models; any enum-like response fields introduced must have named
constants and validation. No schema migration, persisted rollup, background job,
or new dependency is planned. Legacy requests omit the trend computation and
field. An older backend without trend data must leave the existing report usable
and show an explicit chart-unavailable state.

Use an explicit response discriminator and matching TypeScript union. With
`status=available`, return `mode` (`finite` or `all_time`), generated_at, window
metadata, width and points. A finite future-only
cohort with no PRs has full window metadata and empty points. All time without
history has null windows and empty points. A finite observed range with no PRs
in either period retains observed
zero-count bins and null rate/round values. With `status=unavailable`, return only
generated_at and unavailable_reason (`range_too_large`, `unrepresentable_range`,
or `inconsistent_geometry`); omit windows, width and points.
Legacy requests still omit trend entirely.

Keep these UI states distinct:

- Trend absent: "Trend data is unavailable from this server."
- Range too large: "Choose a range of ten years or less to view trends."
- Unrepresentable/inconsistent geometry: "This range cannot be charted. Choose another range."
- All time: "All time has no previous period to compare."
- Future-only: "This period has not started yet."
- Both cohorts empty: "No PRs were first sent in either period."
- Selected rounds metric with no approvals: "No approved PRs in this period."
- Unmatched current tail: "The previous period is shorter; no comparison is available for this portion."
- Unequal exposure: show a concise interval-length notice and both actual durations in the accessible table.
- Overflow attribution: "This point includes the latest PRs captured after the chart's range anchor."

## Implementation sequence

### 1. Window and backend contract

First add deterministic window/bucket tests and PostgreSQL fixtures that cover
both periods. Extend filters, models, request parsing, and the single analytics
query. Keep period/window resolution outside HTTP business logic; use a small
domain helper if resolution cannot stay declarative in SQL. Do not introduce a
generic query framework or a query per bucket/metric.

Acceptance: current counts equal the sum of current bucket counts; whole-period
rates and round statistics match direct PR-fact calculations; no PR appears in
both periods; existing report sections still describe only the current cohort.
Test legacy requests without `include_trend` alongside the new response.

### 2. Selectable cards and comparison chart

Extend the API client/types and the existing Analytics query to request trend
data. Include request-affecting options in the cache key, while keeping the
selected metric out of it. Continue the existing Analytics-only query enablement,
refresh anchor, and SSE/polling invalidation behavior.

Implement validated `analytics_metric` URL state, default Average, and pass the
selection into the report/chart. Use replacement history, like the other filters.
Keep `Card` as the structural surface. Make a shadcn selection `Button` cover the
card's click area, with visible label/value content inside that control and the
info-tooltip button as a separately layered sibling above it. Never nest the
tooltip control inside the selection button. Use `aria-pressed`, visible focus
and selection treatment, and meaningful accessible names.
When a selection leaves the chart offscreen, bring it into view while keeping
keyboard focus on the selected card and respecting reduced-motion preferences.

Use the existing Recharts dependency inside a semantic `Card`. Render current
and previous values on the same elapsed x-axis with straight segments and clear
legend styles. Show concise local dates and sample counts in tooltips, an appropriate
percentage or rounds y-axis, null gaps without connecting them, and the partial
bucket/maturity explanations in the collapsed chart disclosure. Display actual
exposure durations in the accessible table and a concise unequal-exposure notice
when needed. The chart
header names the selected metric and
the compared ranges; whole-period values remain in the existing cards. Do not
add summary deltas or automatically call a difference an improvement or regression.
Provide an accessible expandable data table for
exact values. Verify touch interaction and use theme tokens in both themes.

Change the current-cohort empty branch so all six selectable cards and a useful
prior-period series remain visible when the current cohort has no PRs. Use zero
counts and dashes for unavailable rate/round values. Preserve a useful empty state
when neither period has data. Preserve cached report/chart data with the existing stale-data
notice on refresh failure; do not clear the entire report on a failed refresh.

Acceptance: all six cards change the chart without a network request, info
buttons preserve their tooltip behavior, reloads and shared links restore
selection, metric clicks add no history entries, and scope changes refresh data
while retaining the chosen metric.

### 3. Verification and delivery

Run focused backend and frontend verification first, then shared-contract checks
at integration. Update the implemented analytics design with the final shipped
contract during implementation; this planning change does not claim it is live.
Capture browser evidence at desktop and mobile widths and in light/dark themes.
Obtain independent review of the final implementation. Fix findings and rerun
the affected checks before presenting the chart as ready to ship.

This feature intentionally replaces design 122's earlier exclusion of a trend
chart. Update that durable product contract when implementation is complete.

## Validation and acceptance cases

Use table-driven Go cases with fresh fixtures and parallel isolated PostgreSQL
schemas. Include:

- Same-head completed reruns, completion order, equal-timestamp ID ties,
  non-completed attempts, unposted approval, and post-approval reviews.
- First requests before the comparison range whose reruns occur inside it.
- Approval after a cohort/bucket end, while retaining original cohort attribution.
- Exact shared and upper boundaries, rolling ranges with no end, custom/calendar
  ranges, daylight-saving changes, future/partial bins, and All time. Inject a
  fixed clock; include small remainders around the 90-point threshold, exact
  half-bucket remainders, and ranges shorter than one bucket.
- Equal exposure for ordinary/split paired segments, flagged unequal exposure
    for retained short-tail pairs, and final partial buckets; future-only ranges;
  reversed/end-only inputs and start-only requests without a span or geometry;
  ten-year limits for both windows and timestamp overflow.
- The worked date examples, calendar months of unequal length, February,
  Sunday-based weeks, DST and the split between paired and unmatched current
  segments. Completed periods must keep the same comparison-prefix rule.
- The December/November 2026 and November 1–8/October 25–November 1 DST cases:
  no one-hour points, all current PRs retained, and exact previous-null or
  unequal-exposure metadata. Test split pieces exactly at half nominal width,
  each short-piece exception, and both pieces shorter than half, where the
  paired-short rule takes precedence. Check the final point cap after splits.
- Other organizations and repository filters in both current and previous scans.
- Current-empty/previous-populated, previous-empty, both-empty, and no approvals.
- Different bucket sizes whose whole-period average, median, P95, or approval
  percentage differs from an unweighted combination of bucket statistics.
- At most 90 points per series, ordered bins, no missing observed zero-count
  bins, null gaps, and correct samples at small population sizes.
- Legacy response compatibility, malformed trend input, and unchanged sort/error
  contracts. Compare exact JSON and values, not just field presence.
- Browser clock ahead/behind, Go clock ahead/behind database-created requests,
  a request after the injected anchor, and a nonempty cohort when the clock
  precedes its start. Rolling counts must match legacy requests exactly, with
  no added upper bound; sums of available current buckets must equal the cards.
- An even-sample fractional median and identical percentile functions across
  headline and bucket calculations.

Test each existing current-cohort report section against previous-period fixture
rows, including author, reason, round distribution, operational summaries,
`comment_requests_total`, and `comment_requests_by_user`.

Frontend tests cover all metric selections, URL restoration, no refetch on card
selection, repository/range changes, keyboard/touch controls, tooltip separation,
both actual date intervals, counts versus missing data, stale/loading/error
states, and rendering a useful prior series with no current PRs. Add a focused
render-isolation check if integration introduces chart rerenders from unrelated
search or table interactions.
Cover every unavailable, future-only, All time, both-empty and no-approval state,
including their exact explanatory copy, the overflow attribution notice, and
the unequal-exposure warning. Verify actual local start/end labels across DST
in the chart, tooltip, and accessible table.

Expected verification commands at the relevant implementation boundaries:

    go test ./internal/db ./internal/api/handlers ./internal/models
    TEST_DATABASE_URL=<isolated-local-database> go test ./internal/db ./internal/api/handlers ./internal/models -run 'CodeReview.*(Analytics|Trend)|ValidateCodeReviewTrend' -count=1
    go vet ./internal/db ./internal/api/handlers ./internal/models
    npm exec -- vitest run src/components/code-review-analytics.test.tsx src/components/code-review-analytics-trend.test.tsx 'src/app/(dashboard)/code-reviews/page.test.tsx'
    npm run typecheck
    npm run lint
    npm run build

Run frontend commands from `frontend/` with Node 24 or newer. Add touched helper
or domain packages to verification if implementation introduces them. Check the
query plan on a representative isolated dataset containing two windows and
repeated reviews; point count bounds alone do not establish query performance.
Measure the cost under the existing 30-second polling cadence, five-second
fallback cadence, and author-sort refetches before accepting the combined query.
Do not run production mutations or manufacture live performance claims.

## Outcomes & Retrospective

The optional trend response, six selectable cards, bookmarkable metric state,
and inline comparison chart are implemented. Headline and point calculations
share one statement; existing current-only report sections retain their original
membership. Calendar comparisons use the previous calendar period's elapsed
prefix, with explicit unmatched and unequal intervals. The chart includes an
expandable data table and exact interval/sample details.

Verification completed so far:

- All touched Go package unit tests and `go vet` passed. Targeted real PostgreSQL
  analytics tests passed, including tenancy, exact boundaries, DST, clock
  overflow, fractional percentiles, 90-point limits, and unrepresentable history.
- 195 focused frontend tests passed. Geometry/chart checks also passed under
  UTC, in addition to the local America/New_York timezone.
- Frontend typecheck, full lint, production build, and tenancy lints passed.
- Desktop and mobile browser checks passed for selection, URL restoration,
  focus retention, scrolling, chart keyboard and actual touch inspection,
  accessible data, and light/dark rendering using synthetic API fixtures.
- An isolated 2,000-PR/6,000-review fixture with six warmed samples measured
  median request times of 12.06 ms without trend, 21.68 ms with trend, and
  21.66 ms with trend plus author sorting. Trend `EXPLAIN ANALYZE` execution was
  22.20 ms. These are single-client local measurements, not production latency
  or fleet capacity evidence.

The broader PostgreSQL package suite on the local server encountered an
unrelated existing credential-versioning migration using unsupported
`NULLS NOT DISTINCT` syntax. Focused analytics integration tests and all package
unit tests passed; the migration was left unchanged.

Merge and deployment verification require separate shipping direction.
