# 41 - PM Agent Project Proposals

> **Status:** Implemented | **Last reviewed:** 2026-04-05

## Why this matters

This is a strategically important feature.

Today the PM agent can react to issues and manage existing projects, but it
cannot propose net-new work. That keeps 143 in a mostly reactive posture. If we
want the PM to feel like a real product/program manager rather than a queue
router, it needs a safe way to say:

- "these recurring issues should become a project"
- "this gap in coverage deserves explicit investment"
- "this repo needs a bounded initiative, not another one-off fix"

The value is real, but only if reviewer trust stays high. A low-signal proposal
queue is worse than no queue at all. For that reason, v1 optimizes for
reviewability and restraint, not proposal volume.

## Product judgment

This is worth building, with a narrow first release.

What makes it useful:

- It gives the PM a way to surface strategic work instead of burying it in free
  text.
- It strengthens the "Projects are the strategic control surface" story from
  design 29.
- It creates a high-signal review loop for whether users actually want the PM
  to originate proactive work.

What would make it fail:

- Proposal spam.
- Ambiguous repo ownership.
- Weak deduplication that creates review fatigue.
- Forcing users to hunt for proposals in the wrong surface.

So the right v1 is:

- repo-scoped proposals only
- human approval required
- canonical review inbox on the Projects page
- lightweight proposal visibility on Autopilot
- strict validation and proposal caps

---

## Problem

The `proposed_projects` concept exists in the PM plan format (design 29), but it
is not a usable end-to-end product capability:

1. **No mutation path from the PM sandbox.** The PM has no CLI tool to create a
   proposal during its run.
2. **Repository ownership is undefined.** A project is repo-scoped in the data
   model, but the current proposal idea does not require a `repository_id`.
3. **No trust controls.** There is no cap, expiry, or hard duplicate rejection,
   so a bad PM run could flood the queue.
4. **No structured dedup metadata.** A warning string is not enough for humans
   to review overlap intelligently.
5. **No review inbox.** The approve/dismiss APIs exist, but there is no
   dedicated operator workflow.
6. **No seeded task preview.** Reviewers cannot see the PM's intended
   decomposition before approval.

---

## Goals

- The PM can create a **repo-scoped** project proposal via an internal CLI tool
  (`143-tools project_propose`).
- Proposals are created with `status = proposed` and require human review before
  any execution begins.
- Proposals include proposal reasoning, motivating issue links, and optional
  seed tasks.
- The system performs prompt-side and server-side deduplication, and stores
  structured overlap metadata for review.
- Operators review proposals in a **canonical inbox on the Projects page**.
- `Autopilot` shows a lightweight PM recommendation card linking to that inbox.
- The server enforces org/repo integrity, proposal caps, and stale proposal
  expiry to keep trust high.

## Non-Goals for v1

- Automatic approval of PM proposals.
- Cross-repository proposals. A single proposal belongs to exactly one repo.
- Slack notifications for new proposals.
- An operator-facing project management CLI.
- New roadmap ingestion or issue-pattern enrichment work beyond what the PM
  already has.
- Changing how active projects execute once approved.

---

## Key Decisions

### 1. Canonical creation path: internal CLI call, not PM plan parsing

The PM should create proposals during its run by calling an internal tool. That
tool call is the source of truth for mutation.

The `proposed_projects` field in the final PM plan can remain as a summary or
audit artifact, but the server should not depend on parsing the final plan to
create proposals. This is more reliable and matches the issue-creation pattern
from design 39.

### 2. Proposals are repo-scoped

The current `projects` model requires `repository_id`, so proposals must too.
If the PM wants to recommend work across multiple repos, it should create
separate proposals or summarize the broader initiative in analysis text for a
human to decompose later.

### 3. Autopilot and Projects should both show proposals, with different roles

This should appear in both places, but not equally.

- `Autopilot` is where users expect to see PM output and recommendations.
  Proposal existence should be visible there.
- `Projects` is where users compare, edit, approve, and dismiss project work.
  It should be the canonical review and action surface.

Recommendation:

- `Autopilot`: summary card, top proposals, clear CTA to review
- `Projects`: full proposal inbox, detail view, approve/dismiss/edit

If we had to choose only one surface, it should be `Projects`. But shipping only
there would undersell the PM's strategic role and make proposals easier to miss.

### 4. Reviewer trust beats proposal throughput

The PM should be conservative. The system should enforce that conservatism in
code, not just in prompt text.

---

## Current State

### What already exists

- The `projects` table already supports PM-originated proposals via
  `status = proposed`, `proposed_by_pm`, `source_issue_ids`, and
  `proposal_reasoning`.
- `project_tasks` can already exist before project activation.
- `POST /api/v1/projects/{id}/approve` and
  `POST /api/v1/projects/{id}/dismiss` already exist.
- The frontend API client already exposes `api.projects.approve(id)` and
  `api.projects.dismiss(id)`.
- The projects task PATCH endpoint already exists and can be extended rather
  than added from scratch.

### What is missing

- No `project_propose` internal tool.
- No repo requirement for proposals.
- No structured dedup persistence.
- No proposal review inbox UI.
- No product-level trust controls.
- No explicit API for proposal badge/count summaries.

---

## V1 Design

### Part 1: PM proposal flow

#### New CLI command: `143-tools project_propose`

File: `internal/services/mcp/tools.go`

```
Name:        project_propose
Description: Propose a repo-scoped project for human review
Flags:
  --repository-id       (required) Target repository UUID
  --title               (required) Project title
  --goal                (required) What success looks like
  --scope               (optional) What's in and out of bounds
  --completion-criteria (optional) How to know when done
  --reasoning           (required) Why this project should exist
  --source-issue-ids    (optional) Comma-separated motivating issue UUIDs
  --priority            (optional) 0-100, default 50
  --tasks               (optional) JSON array of seed task specs
  --similar-project-ids (optional) Existing same-repo project UUIDs considered
                         by the PM and judged non-duplicate
```

The `--tasks` payload remains a JSON array of seed task specs:

```json
[
  {
    "title": "Normalize payment handler error types",
    "description": "Introduce shared payment error types and update handlers...",
    "approach": "Start in internal/api/handlers/payments.go and related tests...",
    "complexity": "moderate",
    "confidence": "high"
  }
]
```

#### New internal API endpoint: `POST /api/v1/internal/projects/propose`

File: `internal/api/handlers/internal_projects.go`

```go
type ProposeProjectRequest struct {
    RepositoryID       uuid.UUID        `json:"repository_id"`
    Title              string           `json:"title"`
    Goal               string           `json:"goal"`
    Scope              *string          `json:"scope,omitempty"`
    CompletionCriteria *string          `json:"completion_criteria,omitempty"`
    Reasoning          string           `json:"reasoning"`
    SourceIssueIDs     []uuid.UUID      `json:"source_issue_ids,omitempty"`
    Priority           int              `json:"priority"`
    Tasks              []ProposedTask   `json:"tasks,omitempty"`
    SimilarProjectIDs  []uuid.UUID      `json:"similar_project_ids,omitempty"`
}

type ProposedTask struct {
    Title       string  `json:"title"`
    Description *string `json:"description,omitempty"`
    Approach    *string `json:"approach,omitempty"`
    Complexity  *string `json:"complexity,omitempty"`
    Confidence  *string `json:"confidence,omitempty"`
}

type ProposalOverlap struct {
    ProjectID    uuid.UUID `json:"project_id"`
    Title        string    `json:"title"`
    OverlapScore float64   `json:"overlap_score"`
    OverlapType  string    `json:"overlap_type"`
    Explanation  string    `json:"explanation"`
}

type ProposeProjectResponse struct {
    ID               uuid.UUID         `json:"id"`
    DuplicateWarning *string           `json:"duplicate_warning,omitempty"`
    SimilarProjects  []ProposalOverlap `json:"similar_projects,omitempty"`
}
```

#### Handler rules

The handler must:

1. Validate the short-lived internal token, same mechanism as design 39.
2. Verify `repository_id` belongs to the authenticated org.
3. Verify every `source_issue_id` belongs to the same org and repository.
4. Verify every `similar_project_id` belongs to the same org and repository.
5. Enforce proposal policy:
   - max `1` new PM-created proposal per repo per PM run
   - max `3` open `proposed` projects per repo
   - reject creation if a hard duplicate already exists
6. Run deduplication.
7. Create the `projects` row with:
   - `repository_id = request.RepositoryID`
   - `status = proposed`
   - `proposed_by_pm = true`
   - `proposal_reasoning = request.Reasoning`
   - `source_issue_ids = request.SourceIssueIDs`
   - `similar_projects = dedupResult.SimilarProjects`
8. Create seed `project_tasks` with:
   - `status = pending`
   - `batch_number = 0`
9. Return project ID plus structured overlap metadata.

#### PM behavior when creation is rejected

If the internal endpoint rejects creation because of caps or hard duplication,
the PM should not retry in a loop. It should instead record the opportunity in
its analysis text and move on.

---

### Part 2: Deduplication and overlap review

Deduplication happens at two levels.

#### Level 1: Prompt-side reasoning

Update the PM prompt so that before proposing a project it must check same-repo
projects in states `proposed`, `draft`, `planning`, `active`, `paused`, and
`completed`.

Prompt guidance:

```text
## Before Proposing a New Project

You may only propose a project for the current repository.

Before proposing, review existing same-repo projects and ask:

1. Goal overlap: does another project already subsume this outcome?
2. Scope overlap: would the same code areas be touched?
3. Issue overlap: are the motivating issues already covered by project tasks?

If overlap is strong:
- do not propose a new project
- or explicitly explain why the new project is distinct

Be conservative. Proposal quality matters more than proposal volume.
```

#### Level 2: Server-side dedup

File: `internal/services/pm/dedup.go`

```go
type DedupResult struct {
    HardDuplicate   bool
    Warning         *string
    SimilarProjects []ProposalOverlap
}
```

Heuristics, all constrained to the same org and repository:

1. **Issue overlap**: if more than 50% of `source_issue_ids` already map to the
   same existing project, mark as similar.
2. **Title similarity**: trigram or Levenshtein similarity above 0.7 marks as
   similar.
3. **Scope overlap**: shared path/module prefixes above 0.6 mark as similar.

Hard duplicate rejection:

- reject if overlap score is `>= 0.85` with an open `proposed`, `draft`,
  `planning`, `active`, or `paused` project in the same repo
- return a typed error such as `DUPLICATE_PROJECT_PROPOSAL`

Soft duplicate warning:

- create the proposal
- store structured overlap metadata in `projects.similar_projects`
- surface the warning in the response and UI

#### Structured persistence

A string warning is not enough for the reviewer UI. Persist overlap metadata in
structured form so humans can see *which* project is similar and *why*.

---

### Part 3: Product surfaces

#### Recommendation: both `Autopilot` and `Projects`

This should appear in both places, but with a clear split of responsibility.

| Surface | Purpose | Why |
|--------|---------|-----|
| `Autopilot` | PM output summary and discovery | Users already look here for PM recommendations |
| `Projects` | Canonical proposal inbox and review workflow | Proposals become projects and need project-side comparison |

#### `Autopilot` behavior

`Autopilot` should not become a second full proposal-management screen.

It should show a compact PM card such as:

```text
PM found 2 strategic opportunities in Payments repo.
Top proposal: Standardize payment error handling
[Review proposals]
```

The card should:

- show proposal count
- show the highest-priority proposal title
- link directly to the Projects proposal inbox

#### `Projects` page behavior

The Projects page is the canonical review surface.

Add a proposal inbox above the normal project list when proposals exist:

```text
PM Proposals (2)

- Standardize payment error handling
  Priority 30 · 3 seed tasks · 3 issues
  Similar to: Payment module cleanup
  [View details] [Approve] [Dismiss]

- Harden webhook delivery retries
  Priority 45 · 2 seed tasks · 1 issue
  [View details] [Approve] [Dismiss]
```

The detail view can be a slide-over panel. It should show:

- full proposal reasoning
- motivating issues
- seed tasks
- similar projects with overlap explanations
- edit, approve, dismiss actions

If we need to reduce scope further, keep the Projects inbox and drop the richer
Autopilot card before doing the reverse.

#### New summary endpoint for badges/cards

The list API only returns `{data, meta.next_cursor}`, so it should not be used
as a count API.

Add a small summary endpoint:

`GET /api/v1/projects/proposals/summary`

Response:

```json
{
  "data": {
    "count": 2
  }
}
```

Use this for:

- sidebar badge/dot
- `Autopilot` proposal summary card

Use `GET /api/v1/projects?status=proposed` for the actual inbox list.

---

### Part 4: Seed tasks

Seed tasks are useful because they show the PM's intended decomposition, but
they are still only suggestions.

#### Behavior

- Before approval: visible, editable, not executable
- After approval: retained as starting context for the project
- Before activation: still not executable
- After activation: the normal project execution engine decides what to do with
  them

#### API behavior

The existing task endpoints are enough, with small changes:

- `POST /api/v1/projects/{id}/tasks` can create additional seed tasks while the
  project is `proposed` or `draft`
- `PATCH /api/v1/projects/{id}/tasks/{taskId}` should be extended to support
  `complexity` and `confidence`
- task edits should only be allowed while the parent project is `proposed` or
  `draft`

We do **not** need a brand-new task PATCH endpoint; we need to tighten the
existing one.

---

### Part 5: Reviewer trust controls

These are part of v1, not optional polish.

#### Caps

- max `1` PM-created proposal per repo per PM run
- max `3` open proposals per repo

#### Ageing / expiry

Stale proposals should not accumulate forever.

- auto-cancel `proposed` projects older than `14` days with a system-generated
  dismissal reason such as `"expired_without_review"`
- emit an audit/decision log entry when this happens

This uses existing project status (`cancelled`) and does not require a new
status enum.

#### Human gate

No project with `status = proposed` is eligible for execution. Approval remains
the hard boundary.

---

## Data Model Changes

### Add structured overlap metadata

Add a JSONB column to `projects`:

```sql
ALTER TABLE projects
ADD COLUMN similar_projects JSONB NOT NULL DEFAULT '[]'::jsonb;
```

Example stored value:

```json
[
  {
    "project_id": "8f9c...",
    "title": "Payment module cleanup",
    "overlap_score": 0.81,
    "overlap_type": "scope",
    "explanation": "Both proposals target payment handlers and shared error paths"
  }
]
```

No additional schema is required for expiry in v1; `created_at` is enough.

---

## API and Handler Changes

### Backend

| File | Change |
|------|--------|
| `internal/api/handlers/internal_projects.go` | New internal proposal endpoint |
| `internal/api/router.go` | Register internal proposal endpoint and proposal summary endpoint |
| `internal/services/integration/project_proposer.go` | New internal tool client |
| `internal/services/mcp/tools.go` | Register `project_propose` |
| `internal/services/mcp/registry_builder.go` | Wire tool dependencies |
| `internal/services/pm/dedup.go` | New dedup logic |
| `internal/services/pm/service.go` | Provide internal tool env and stop treating final plan parsing as the canonical mutation path |
| `internal/api/handlers/projects.go` | Extend dismiss to accept optional reason body; restrict seed-task editing to `proposed`/`draft`; extend task PATCH fields |
| `internal/prompts/templates/pm_system_prompt.template` | Add conservative proposal guidance |

### Frontend

| File | Change |
|------|--------|
| `frontend/src/app/(dashboard)/projects/page.tsx` | Add proposal inbox above the normal project list |
| `frontend/src/app/(dashboard)/autopilot/...` | Add compact proposal summary card linking to Projects |
| `frontend/src/components/proposal-card.tsx` | Proposal card with overlap, issue, and task summary |
| `frontend/src/components/proposal-detail.tsx` | Slide-over detail/review view |
| `frontend/src/components/sidebar.tsx` | Proposal badge or dot using the summary endpoint |
| `frontend/src/lib/types.ts` | Add `similar_projects` metadata to the `Project` type |

### Migration

| File | Change |
|------|--------|
| `migrations/000XXX_projects_similar_projects.up.sql` | Add `similar_projects JSONB` |
| `migrations/000XXX_projects_similar_projects.down.sql` | Drop `similar_projects` |

---

## Rollout

### Phase 1: Core proposal loop

- internal `project_propose` tool
- internal proposal API
- repo/org validation
- dedup + structured overlap metadata
- proposal caps
- Projects page inbox with approve/dismiss/detail

### Phase 2: PM visibility

- `Autopilot` proposal summary card
- sidebar badge/dot
- proposal summary endpoint

### Phase 3: Hardening

- proposal expiry job
- improved overlap heuristics
- richer review copy and audit instrumentation

Anything beyond this should be treated as follow-on work, not part of the
initial feature.

---

## Follow-on Work, Explicitly Deferred

These may be valuable, but they are not necessary to validate the product:

- operator-facing `project_manage` CLI
- Slack notifications for new proposals
- proposal learning from dismissal reasons
- richer PM context enrichment (roadmaps, issue patterns)
- bulk proposal actions

---

## Security and correctness requirements

- Every lookup and write must filter by `org_id`.
- `repository_id` must be validated against the org before proposal creation.
- `source_issue_ids` and `similar_project_ids` must be validated against the
  same org and repository as the proposal.
- The execution engine must continue to skip `status = proposed`.
- All state changes must use existing authenticated project endpoints or the
  new internal PM endpoint.

---

## Open Questions

1. Should the PM ever be allowed to suggest "expand this existing project"
   through a first-class UI action instead of only via free text?
2. Should seed tasks be editable directly in the list card, or only in the
   detail panel?
3. Once we have enough usage data, should we add repo-specific proposal quality
   metrics such as approve rate and stale rate?
