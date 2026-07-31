# Design: PR-Centric Code Review Analytics

> **Status:** Implemented | **Last reviewed:** 2026-07-31
>
> **Depends on:** [112-code-reviewer-bot-auto-approval.md](112-code-reviewer-bot-auto-approval.md), [../overall.md](../overall.md)

## Summary

Change the Code reviews Analytics tab from review-attempt reporting to
pull-request reporting while preserving the useful metrics and sections already
on the page.

The page should answer four questions:

1. How many PRs did 143 review?
2. How many of those PRs received an approval from 143?
3. What percentage of reviewed PRs did 143 approve?
4. How many rounds did approval usually take?

Individual review sessions remain available as evidence, but they are not the
primary analytics unit. This prevents a PR reviewed several times from having
several times the influence of a PR approved immediately.

The existing author, finding, non-approval, and operational metrics remain.
Their labels and calculations change from reviews to unique PRs. First-round
approval and rounds to approval are added alongside them.

Global PR-size, file-count, size-bucket, and policy-limit analytics are
intentionally excluded. The author table retains median additions and deletions
as the addition/deletion distribution design 112 describes for this tab.

## Problem

The current report aggregates `code_review_session_metadata`, so its counts and
rates describe review attempts. A PR with three completed assessments contributes
three observations to approval rate, author usage, findings, and
non-approval reasons.

That is useful for understanding reviewer activity, but it does not match the
main product question: what happened to each PR?

For example, if one PR is approved immediately and another is approved on its
third review, the current report sees four completed reviews and two approvals:
a 50% approval rate. At the PR level, both PRs were approved, one on the first
round and one after iteration: a 100% eventual approval rate and a 50%
first-round approval rate.

## Goals

- Make the Analytics tab understandable in terms of PR outcomes.
- Show how much iteration was required before 143 approved.
- Preserve the useful breadth of the current analytics report.
- Keep the report easy to scan and explain.
- Preserve repository and time-window filters.
- Let users reach the existing review evidence when they need details.
- Reuse the existing PR and code-review session records.

## Non-goals

- Build a complete PR lifecycle funnel.
- Measure time from PR creation to merge.
- Attribute merge speed or engineering productivity to 143.
- Compare 143 approval with human approval.
- Add policy tuning, automatic recommendations, or dispute handling.
- Add new analytics dimensions beyond the existing report and review-round
  metrics defined here.
- Create a new analytics warehouse, event stream, or persisted rollup table.
- Redesign the Reviews or Policy tabs.

## Product definitions

### PR cohort

A PR belongs to the selected time window when its **first 143 review attempt was
requested** during that window.

All later rounds for that PR are used to determine its current outcome, even if
they completed after the end of the window. This keeps the denominator stable:
changing the date range selects PRs, not isolated review attempts.

The UI should describe the filter as:

> PRs first sent to 143 during this period

Recently reviewed PRs may still be awaiting another round. They remain in the
cohort and are shown as not yet approved; the page does not attempt maturity
adjustments in this version.

Using the first request, rather than the first completed round, also keeps PRs
whose attempts only failed or became stale visible in the operational metrics.

### Review round

A review round is a completed 143 assessment of a distinct PR head SHA.

- The first completed assessment is round 1.
- A completed assessment of a new head SHA is the next round.
- Failed, stale, cancelled, or still-running sessions do not count.
- Retries or duplicate completed sessions for the same head SHA count once.
- A completed non-approval counts as a round.
- Rounds after the first posted 143 approval are ignored.

If duplicate completed assessments exist for one head SHA, use the earliest
completed assessment with a posted approval; otherwise use the latest completed
assessment as that head's result. This makes an approval visible without allowing
same-head reruns to inflate the round count.

### 143 approval

A PR is approved by 143 only when a completed review has:

- `decision = 'approved'`; and
- a non-null `github_review_id`.

An internal approval decision that was not posted to GitHub is not a 143 approval.
This matches the existing product distinction.

### Rounds to approval

For an approved PR, rounds to approval is the ordinal number of the first round
that posted a 143 approval.

Example:

| Activity | Counted result |
| --- | --- |
| Assessment becomes stale after the head changes | Not a round |
| New head receives a completed non-approval | Round 1 |
| Infrastructure retry on the same head | No additional round |
| Author pushes fixes; completed non-approval | Round 2 |
| Author pushes again; 143 posts approval | Round 3 |

The PR was approved in three rounds.

### Representative assessment

Metrics that need one assessment snapshot per PR use its **representative
assessment**:

- for an approved PR, the round that first posted the 143 approval;
- for a PR without approval, its latest completed round;
- if a PR has no completed round, no representative assessment.

This snapshot supplies author change-distribution, decision subtype, and finding
metrics.
Using one snapshot prevents a multi-round PR from being weighted several times
while still describing the final observed state of the PR.

## Analytics page

Keep the existing page shell, repository selector, and time-window selector.
Replace the current review-based report with the sections below.

### 1. Headline cards

Show four cards:

| Card | Definition | Supporting text |
| --- | --- | --- |
| **PRs reviewed** | Unique PRs in the cohort | “First sent to 143 in this period” |
| **Approved by 143** | Cohort PRs with a posted 143 approval | Percentage of PRs reviewed |
| **Approval rate** | Approved by 143 / PRs reviewed | Approved PR count |
| **Median rounds to approval** | Median among approved cohort PRs | Approved PRs only |

If no PRs are in the cohort, show the existing analytics empty state with PR-based
copy. If PRs exist but none are approved, show `—` for median rounds.

### 2. Approval by round

Show one compact distribution with these mutually exclusive outcomes:

- Approved in round 1
- Approved in round 2
- Approved in round 3
- Approved in round 4+
- Not yet approved

Each item shows a PR count and percentage of PRs reviewed. A simple row or set of
cards is sufficient; this does not require a charting library.

### 3. Usage by PR author

Keep the current author table and convert every count to unique PRs:

| Column | Definition |
| --- | --- |
| PR author | Captured author from the first available assessment |
| PRs | Unique cohort PRs |
| Approved | PRs with a posted 143 approval |
| Not approved | PRs with a completed round and no posted approval |
| Approval rate | Approved / PRs |
| First-round approval | PRs approved in round 1 |
| Median rounds | Approved PRs only |
| Median additions | Representative assessments with captured additions |
| Median deletions | Representative assessments with captured deletions |

Keep counts linked to the Reviews tab with repository, time range, author, and
outcome filters where the existing list supports them. The Reviews tab may still
show individual sessions after navigation; grouping that tab by PR is outside
this design.

### 4. Why PRs were not approved right away

Show the existing structured non-approval reason labels, but count each reason at
most once per PR.

Include distinct reason codes from every completed non-approval round up to the
first approval. A repeated reason contributes once for that PR, even if it
appeared in several rounds. One PR can still contribute to several different
reasons.

This preserves evidence about friction encountered during the PR journey without
allowing long-running PRs to dominate the report.

### 5. PR findings and operational outcomes

Keep the current findings and decision-outcome section, using the representative
assessment:

- PRs with findings;
- PRs with blocking findings;
- findings per PR;
- total findings;
- PRs needing human review;
- comment-only PRs;
- blocked PRs;
- PRs with an approval decision that was not posted.

“Findings per PR” is the total findings on representative assessments divided by
PRs with a completed round.

Also retain failed and stale visibility, but label the metrics **PRs with a failed
attempt** and **PRs with a stale attempt**. These are unique PR counts and may
overlap other outcomes: a PR can have a stale attempt and later be approved. The
copy must make this clear rather than presenting them as mutually exclusive final
outcomes.

## Suggested layout

```text
Analytics
[Repository] [PRs first sent to 143: Last 30 days]

[PRs reviewed] [Approved by 143] [Approval rate] [Median rounds]

Approval by round
[Round 1] [Round 2] [Round 3] [Round 4+] [Not yet approved]

Usage by PR author
Author       PRs   Approved   Not approved   First-round approval   Median rounds

Why PRs were not approved right away
Reason                                      PRs
Blocking issue                               12
Required checks were not passing              8
PR description requirements were not met      5

PR findings and outcomes
[PRs with findings] [Blocking findings] [Findings per PR]
Needs human review · Comment only · Blocked · Approval not posted
```

Do not add a funnel, trend chart, maturity model, or separate review-activity
panel in the first version. Use the current section styling and avoid introducing
a charting library.

## API and query shape

Keep `GET /api/v1/code-reviews/analytics` and its existing repository and date
filters. Change the response contract to PR-oriented fields:

```json
{
  "data": {
    "summary": {
      "prs_reviewed": 42,
      "prs_with_completed_round": 39,
      "approved_by_143": 31,
      "not_approved": 8,
      "approved_first_round": 20,
      "median_rounds_to_approval": 2,
      "prs_with_failed_attempt": 2,
      "prs_with_stale_attempt": 5,
      "prs_with_change_breakdown": 36,
      "median_additions": 84,
      "median_deletions": 25,
      "prs_with_findings": 21,
      "prs_with_blocking_findings": 9,
      "total_findings": 48,
      "needs_human_review": 4,
      "comment_only": 2,
      "blocked": 2,
      "approval_not_posted": 1
    },
    "approval_rounds": [
      {"bucket": "round_1", "prs": 20},
      {"bucket": "round_2", "prs": 8},
      {"bucket": "round_3", "prs": 2},
      {"bucket": "round_4_plus", "prs": 1},
      {"bucket": "not_yet_approved", "prs": 11}
    ],
    "authors": [
      {
        "author": "octocat",
        "prs_reviewed": 10,
        "approved_by_143": 8,
        "not_approved": 2,
        "approved_first_round": 5,
        "median_rounds_to_approval": 2,
        "median_additions": 75,
        "median_deletions": 20
      }
    ],
    "non_approval_reasons": [
      {"code": "blocking_findings", "prs": 12}
    ]
  }
}
```

The implementation should derive this in one org-scoped PostgreSQL query:

1. identify PRs by their first review-attempt creation time and apply the cohort
   filters;
2. load all attempts for those PRs, including attempts after the cohort window;
3. collapse duplicate completed sessions to one result per PR and head SHA;
4. order distinct completed heads by completion time to assign round numbers;
5. select one representative assessment per PR;
6. aggregate PR outcomes, rounds, authors, representative change-distribution
   and finding metrics, and distinct per-PR reason codes.

Continue filtering every source by `org_id`. No migration is expected unless
focused query testing shows an additional index is required.

The date parameters retain their current names. They apply to the first attempt
created for each PR rather than to every matching session. This contract change
must be reflected in handler, store, and frontend tests.

## Edge cases

- **No completed review:** the PR is included if its first attempt is in the
  window, but it has no representative assessment.
- **Only failed or stale attempts:** the PR is included and contributes to the
  corresponding operational PR metric.
- **Approval decision was not posted:** the PR remains not yet approved.
- **Same SHA reviewed more than once:** it is one round.
- **PR receives approval after the selected window:** it is approved in its
  original cohort.
- **PR remains open or is closed without 143 approval:** it is not yet approved.
  This version does not introduce abandoned or merged-without-approval outcomes.
- **Missing author:** use `Unknown`, matching the existing report.
- **Policy changes between rounds:** preserve the observed results. This report
  describes the PR journey, not one policy version's performance.

## Verification

Add focused store tests covering:

- immediate approval;
- approval after multiple distinct heads;
- repeated sessions on one head SHA;
- failed and stale sessions excluded from round count;
- internal approval without `github_review_id`;
- approval after the cohort window;
- repository, time, and organization isolation;
- per-PR reason deduplication across rounds;
- representative author change-distribution, decision, and finding metrics;
- unique PR counts for failed and stale attempts;
- author aggregation and missing authors.

Update handler contract tests and frontend component tests for:

- the four headline cards;
- all approval-round buckets;
- no-PR and no-approval empty values;
- non-approval reasons;
- finding and outcome sections with PR-based copy;
- PR-author rows and navigation filters.

## Rollout

Replace the current Analytics response and UI together. The endpoint is consumed
by the first-party page, so a parallel version is unnecessary.

Use the existing analytics time-window default. No backfill is required because
the report is derived from current durable review and PR records.

## Success criteria

- A user can explain the four headline metrics without knowing what a review
  session is.
- Each PR contributes once to PR counts and outcome rates.
- Failed, stale, and same-head retries do not inflate rounds.
- The page directly answers how many rounds approval took.
- Existing author, finding, non-approval, and operational
  insights remain available with PR-based denominators.
- No PR contributes more than once to an author row, outcome count,
  or individual non-approval reason.
