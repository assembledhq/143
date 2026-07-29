# Design: Code Review Decision Feedback And Policy Tuning

> **Status:** Not Started | **Last reviewed:** 2026-07-29
>
> **Depends on:** [../implemented/112-code-reviewer-bot-auto-approval.md](../implemented/112-code-reviewer-bot-auto-approval.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md), [../future/18-fix-quality-feedback.md](18-fix-quality-feedback.md), [../future/116-automatic-pr-feedback-follow-through.md](116-automatic-pr-feedback-follow-through.md)

## Summary

The 143 Code Reviewer decides; nobody can argue with it. When the bot withholds
approval, the PR author's only recourse is "ask a human." That disagreement —
the highest-signal data the system could collect about its own policy — is
discarded, and the org never learns which of its rules are actually miscalibrated.

This document proposes a **decision feedback loop**: a first-class way to
contest a code review decision, a durable record of that contest, corroboration
against what actually happened to the PR, and an automated engine that turns
recurring corroborated disputes into concrete, reviewable policy changes.

Doc 112 explicitly deferred "automatic policy learning from past approvals" and
"aggregate reporting/insights across reviews." This document fills both gaps and
treats them as one system, because insights without a dispute channel measure
only the bot's own opinion of itself.

## Problem

### What works today

The decision path is already better instrumented than most review bots:

- Every non-approval is attributable to typed `CodeReviewRiskReasonCode` values
  (`internal/models/code_review.go`), and numeric reasons carry `Actual` and
  `Limit` alongside the code.
- The rolling PR comment renders `**Why:**`, `**Policy blockers:**`,
  `**Blocking findings:**`, and `**Next steps:**`
  (`internal/models/code_review_output.go`).
- Policies are insert-only and versioned, and every session captures the policy
  id, rendered prompts, and reviewed head SHA. Decisions are auditable after
  the fact.

The raw material for a feedback loop already exists. What is missing is the loop.

### Why "people aren't sure why"

Five concrete gaps, each independently fixable:

1. **Blockers of different kinds are rendered identically.** `files_limit_exceeded`
   (an objective policy threshold), `blocking_findings` (a model's code judgment),
   and `architecture` (a model's judgment that a human should weigh in) all appear
   as bullets under `**Policy blockers:**`. The author cannot tell which lever
   would change the outcome: edit the policy, argue with the finding, or accept
   that a human must look. "I don't know why" is usually "I don't know who to
   ask or what to change."

2. **No counterfactual.** The comment lists what blocked. It never says what
   *unblocking* would take, or that a single blocker was the only one. "Would
   have been approved except for one threshold" and "blocked on eleven separate
   things" read the same.

3. **Advisory findings are a bare count.** P2/P3 observations appear as
   "N non-blocking observations available in the full review." A reader who
   cannot see them cannot confirm they were correctly classified as non-blocking,
   which reads as withholding rather than as concision.

4. **System faults look like the PR's fault.** `context_unavailable` and
   `orchestrator_synthesis_invalid` mean 143 failed, not that the change is
   risky. There is partial special-casing for the orchestrator case, but a
   context-fetch failure still lands in the same blocker list as a genuine
   policy violation.

5. **Deterministic gates run last.** `EvaluateCodeReviewRisk` is called after
   reviewer fan-out and orchestrator synthesis
   (`internal/worker/code_review_handler.go`). A PR that trips `max_files_changed`
   waits for a full multi-agent review to be told a fact that was knowable from
   the PR file list in one API call.

### Why the org can't fix it either

Doc 112 lists "Top non-approval reasons, used to tune PR templates and readiness
checks" as a success metric. That metric is not computable today: there is no
aggregate surface, and the `Code reviews` page ships only `Reviews` and
`Configurations` tabs. `POST /api/v1/code-reviews/policy-events` records *policy
editing* telemetry (which config sections admins opened), not decision outcomes.

So an admin asking "is our policy too strict?" has no data, and a developer who
believes the bot was wrong has nowhere to put that belief. The result is the
reported failure mode: silent frustration, no correction, eroding trust in the
signal.

### The trap to avoid

The naive version of this feature — "let people flag bad decisions, then loosen
the policy where flags pile up" — is worse than nothing. The person most likely
to file a dispute is the author whose PR was blocked, i.e. the single most biased
available judge, and the thing they are disputing is a *security and quality
control*. A system that automatically relaxes its approval criteria in response
to complaints from the people it blocked is gameable by construction, and the
gaming does not even have to be deliberate.

**A dispute is a claim, not a fact.** Every option below is evaluated on how it
establishes ground truth before acting.

## Corroboration: establishing ground truth

Two independent signals, both already available, decide whether a dispute was
right:

**Outcome telemetry (automatic, unbiased).** 143 already syncs PR reviews,
comments, threads, checks, and merge state. For a disputed non-approval:

| Observed outcome | Reading |
| --- | --- |
| Merged with no human requested-changes and no substantive human review comments | **Corroborates** — a human reviewer saw nothing the bot should have blocked on |
| A human subsequently requested changes, or blocking comments landed | **Refutes** — the bot's caution was warranted |
| Closed without merge | **Refutes** (weakly) |
| Reverted, or linked to an incident within N days of merge | **Strongly refutes** — feeds the false-approval metric in doc 112 |
| Still open past the window | **Inconclusive** — expires, never counted |

**Explicit adjudication (human, authoritative).** A policy owner — not the PR
author — marks a dispute `upheld` or `rejected`. Adjudication always overrides
telemetry.

Only `upheld` disputes and `corroborated` telemetry feed the tuning engine.
Disputes from the PR author corroborated by nothing are visible in Insights and
inert everywhere else. This single rule is what makes the loop safe.

## Options

### Option A — Insights surface only, manual tuning

Add the deferred `Insights` tab. Aggregate existing decision data by repository,
reason code, decision, and time. No dispute object; admins infer miscalibration
from reason-code frequency and tune policy by hand.

**Pros**
- Smallest change: read-only queries over `code_review_session_metadata` and
  existing reason details, one new tab, no schema beyond indexes.
- Makes doc 112's stated success metrics computable for the first time.
- No new trust surface, no LLM in the loop, nothing to game.
- Ships in days and is a strict prerequisite for every other option.

**Cons**
- Measures only what the bot already believes. Frequency is not error: the top
  reason code is probably `blocking_findings`, which may be entirely correct.
- Does not give the frustrated developer anywhere to put their disagreement, so
  it does not address the reported problem.
- Manual tuning depends on an admin noticing, caring, and acting.

### Option B — Structured disputes plus a deterministic auto-tuner

Add a dispute record. When N disputes cluster on the same *deterministic* reason
code in the same repo, a rules engine proposes a mechanical adjustment —
`files_limit_exceeded` with disputed `Actual` values consistently at 7 against a
`Limit` of 5 proposes raising the limit to 8.

**Pros**
- Fully explainable and unit-testable; the tuner is arithmetic over data the
  reason codes already carry in `Actual`/`Limit`.
- No LLM anywhere in the approval-policy path.
- Maps cleanly onto insert-only policy versioning: each adjustment is a new version.

**Cons**
- Covers only the mechanical dimensions. The judgment reasons — `scope_mismatch`,
  `unresolved_uncertainty`, `architecture`, `blocking_findings` — are where
  disagreement actually concentrates, and none of them are tunable by arithmetic.
  They need prompt and rubric changes.
- Threshold ratcheting is the exact failure mode described above: repeated
  disputes monotonically loosen limits, and each individual step looks reasonable.
- Deterministic thresholds are the rules teams are *most* able to set correctly
  by hand, so this automates the easy half and leaves the hard half untouched.

### Option C — Disputes, corroboration, and agent-authored proposals (recommended)

Capture disputes as first-class records attributed to specific reason codes.
Corroborate each against PR outcome telemetry and optional human adjudication.
Cluster the survivors. When a cluster crosses a threshold, a scheduled 143
session reads the cluster, its evidence, and the current policy, and emits a
**proposed policy version** with a concrete diff and a rationale citing the
sessions behind it. An admin reviews and activates it in one click; activation
creates the next insert-only version and an audit event.

**Pros**
- Handles both halves: deterministic dimensions get mechanical proposals, prompt
  and rubric text gets agent-authored edits. Judgment reasons become tunable.
- The corroboration gate means only disputes that reality agreed with can move
  policy.
- Human activation keeps the security boundary intact — the system proposes,
  a person disposes — while removing essentially all of the analysis toil.
- Reuses infrastructure wholesale: 143 sessions for the proposal agent, the job
  queue for clustering, insert-only policies for versioning, the audit log for
  attribution, notifications for the digest.
- Proposals carry evidence, so the review is genuinely reviewable rather than a
  rubber stamp.

**Cons**
- The largest build: capture surfaces, schema, corroboration job, clustering,
  proposal agent, review UI, digest.
- Proposal quality depends on cluster quality; thin clusters produce noisy
  proposals and admins learn to ignore the queue. Thresholds must start
  conservative.
- Introduces an agent that writes approval policy. Even gated behind human
  activation, this needs the same untrusted-input discipline doc 112 applies to
  PR content: dispute text is written by the people the bot blocked and must be
  treated as evidence, never as instructions.

### Option D — Policy-as-code with proposal PRs

Same capture and corroboration as C, but the policy lives in the repository
(`.143/code-review-policy.yml`) and proposals arrive as ordinary pull requests,
following the `.143/learned-conventions.md` pattern from doc 11.

**Pros**
- Policy changes get the team's real review process, CODEOWNERS, and git history
  for free — a strong fit for a control that governs approvals.
- Fully transparent and diffable; the same reviewers who care about the rules
  are the ones who see changes to them.
- No new approval UI to build.

**Cons**
- Forks the source of truth. Today policy is DB-owned, versioned, and resolvable
  with org-default plus repo-override inheritance; a repo file needs sync,
  conflict, and precedence rules against that model, and doc 112's requirement
  that approvals point at the exact policy version that produced them becomes
  harder to guarantee.
- Repo-scoped files cannot express org-level defaults inherited by repositories
  that have no override.
- A PR proposing to loosen the reviewer bot's own approval rules is exactly the
  category doc 112 says the bot must not approve — workable, but it needs an
  explicit carve-out.
- Slower loop: policy edits now require a merge.

Worth revisiting once policy stabilizes, and a reasonable later export path. Not
the right first move.

### Rejected — Fully autonomous closed-loop adaptation

C or D with automatic activation and metric-triggered rollback.

Rejected on principle rather than on effort. Approval policy is a security
control; an automated system that relaxes its own controls in response to
pressure from blocked authors is unsound regardless of how good the rollback is,
and "reverts within 7 days" is far too slow a feedback signal for a gate whose
failure mode is unreviewed code reaching main. Doc 112's product principle —
*approval requires evidence* — applies to changes in what counts as evidence.

If a team eventually wants some of this, the defensible bounded form is: admin
pre-authorizes auto-application for named low-risk dimensions only (e.g. line
thresholds, within an admin-set ceiling, on repos with no sensitive paths), never
for path rules, sensitive categories, required checks, agent roster, or prompt
text. That is a later configuration option on top of C, not a different design.

## Recommendation

Ship **Option C**, in three phases, and do the transparency fixes in Phase 0
because they reduce disputes at the source rather than processing them better.

The phases stand alone. Each is useful if the next never ships.

### Phase 0 — Make the decision legible (no new subsystem)

Changes to `internal/models/code_review_output.go`, the worker, and the session
detail view:

1. **Group blockers by lever.** Render three labelled groups instead of one list:
   *Policy thresholds* (deterministic, tunable in Configurations — deep-link to
   the exact setting), *Review findings* (agent code judgment, contestable),
   *Human judgment required* (typed non-finding reasons). Derive grouping from
   the existing `CodeReviewRiskReasonCode` — no new taxonomy.
2. **State the counterfactual.** When exactly one blocker exists, say so:
   "This is the only blocker. Resolving it would make this PR eligible for
   automated approval under policy vN." Requires no new computation; the
   evaluator already has the full reason list.
3. **Separate system faults from risk.** Give `context_unavailable` and
   `orchestrator_synthesis_invalid` a distinct rendering — 143 could not complete
   the review — and keep them out of the policy-blocker list entirely.
4. **Expose advisory findings.** Put P2/P3 observations in a collapsed
   `<details>` block on the rolling comment instead of a count. They remain
   non-blocking and still generate no inline comments.
5. **Fast-fail deterministic gates.** Evaluate size, path, author, and fork
   eligibility from the PR file list *before* reviewer fan-out. If policy makes
   the PR categorically ineligible, post the result immediately and skip the
   agent run. Cuts cost and turns a multi-minute wait into seconds.

Phase 0 alone likely resolves a large share of "I'm not sure why," and every
later phase depends on the reason-code grouping it introduces.

### Phase 1 — Capture and corroborate

Two capture surfaces, one record:

- **GitHub-native (primary).** Reply to the rolling comment with
  `@143-code-reviewer disagree: <reason>`. The `issue_comment` and
  `pull_request_review_comment` webhooks are already routed
  (`internal/api/handlers/webhooks.go`); this adds a command parser. Disputes are
  filed where the frustration happens, with no context switch.
- **143-native (secondary).** A "Disagree with this decision" action on the code
  review session detail, with a reason-code multi-select prefilled from the
  decision's actual blockers. The rolling comment links to it.

Both write `code_review_decision_disputes`, attributed to the session, the policy
version, the reviewed head SHA, and the specific contested reason codes.

A light LLM classification pass maps free text to a `dispute_kind`
(`threshold_too_strict`, `path_rule_too_broad`, `description_requirement_wrong`,
`finding_incorrect`, `judgment_overreach`, `bot_correct`, `other`). Classification
is advisory metadata for clustering; it never decides anything on its own.

A scheduled `corroborate_code_review_dispute` job resolves telemetry per the
table above, once the PR reaches a terminal state or the window expires.

Ship the `Insights` tab here (Option A's content, plus dispute rate and
corroboration rate by reason code), and a weekly digest to policy owners over the
existing notification path — this is the "store the data and send the results"
half of the ask, and it is independently valuable.

### Phase 2 — Propose

A scheduled clustering job groups `upheld` and `corroborated` disputes by
`(org, repository, reason_code, dispute_kind)`. Clusters crossing a configurable
threshold (default: 3 corroborated disputes within 30 days) create a
`code_review_policy_proposals` row.

Proposal generation is split by reason kind:

- **Deterministic reasons** — a rules engine computes the change directly from
  `Actual`/`Limit` values across the cluster. No LLM. Proposals are bounded by
  admin-set ceilings and can never widen path/category/check rules by more than
  one named entry at a time.
- **Judgment reasons** — a 143 session reads the cluster's sessions, findings,
  disputes, and current prompt text, and proposes edited rubric or description
  prompt text. Dispute bodies enter that prompt as untrusted evidence under the
  same screening doc 112 applies to PR content.

Every proposal carries a structured diff, a rationale, and links to the sessions
that produced it. Admins activate, edit-then-activate, or dismiss with a reason.
Activation writes a new insert-only policy version and an audit event. Dismissals
are training data for the threshold, and a dismissed cluster does not re-propose
until materially new evidence arrives.

**Guardrails, non-negotiable:**

- Proposals never auto-apply in v1.
- Proposals can never touch approval mode, fork eligibility, prompt-injection
  handling, or the bot's own policy paths.
- Every activation is attributable to a user, in the audit log, and revertible by
  activating the prior version.
- Insights tracks post-activation false-approval rate per policy version, so a
  loosening that goes wrong is visible against the version that caused it.

## Database Schema

Three new tables, all tenant-scoped by `org_id`, following the insert-only and
partial-unique-index conventions in doc 112.

```sql
CREATE TABLE code_review_decision_disputes (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                 uuid NOT NULL REFERENCES organizations(id),
    session_id             uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pull_request_id        uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    repository_id          uuid NOT NULL REFERENCES repositories(id),
    policy_id              uuid NOT NULL REFERENCES code_review_policies(id),
    reviewed_head_sha      text NOT NULL,
    decision               text NOT NULL,
    -- Who filed it. author_is_pr_author drives the bias weighting in clustering.
    filed_by_user_id       uuid REFERENCES users(id),
    filed_by_login         text NOT NULL DEFAULT '',
    author_is_pr_author    boolean NOT NULL DEFAULT false,
    source                 text NOT NULL CHECK (source IN ('github_comment','app_ui','api')),
    github_comment_id      bigint,
    github_delivery_id     text,
    body                   text NOT NULL DEFAULT '',
    -- Reason codes from the disputed decision that this dispute contests.
    contested_reason_codes text[] NOT NULL DEFAULT '{}',
    dispute_kind           text CHECK (dispute_kind IS NULL OR dispute_kind IN (
                               'threshold_too_strict','path_rule_too_broad',
                               'description_requirement_wrong','finding_incorrect',
                               'judgment_overreach','bot_correct','other')),
    classification_status  text NOT NULL DEFAULT 'pending'
                               CHECK (classification_status IN ('pending','classified','failed','skipped')),
    -- Ground truth. Only 'upheld' or 'corroborated' feed the tuning engine.
    corroboration_status   text NOT NULL DEFAULT 'pending'
                               CHECK (corroboration_status IN (
                                   'pending','corroborated','refuted','inconclusive','upheld','rejected')),
    corroboration_detail   jsonb NOT NULL DEFAULT '{}',
    corroborated_at        timestamptz,
    adjudicated_by_user_id uuid REFERENCES users(id),
    adjudicated_at         timestamptz,
    adjudication_note      text,
    cluster_key            text,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

-- One dispute per filer per review session; re-filing updates in place.
CREATE UNIQUE INDEX idx_cr_disputes_session_filer
    ON code_review_decision_disputes (session_id, COALESCE(filed_by_user_id::text, filed_by_login));
CREATE UNIQUE INDEX idx_cr_disputes_delivery
    ON code_review_decision_disputes (github_delivery_id) WHERE github_delivery_id IS NOT NULL;
CREATE INDEX idx_cr_disputes_cluster
    ON code_review_decision_disputes (org_id, repository_id, cluster_key, created_at DESC)
    WHERE corroboration_status IN ('corroborated','upheld');
CREATE INDEX idx_cr_disputes_pending
    ON code_review_decision_disputes (org_id, corroboration_status, created_at)
    WHERE corroboration_status = 'pending';

CREATE TABLE code_review_policy_proposals (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               uuid NOT NULL REFERENCES organizations(id),
    repository_id        uuid REFERENCES repositories(id),
    base_policy_id       uuid NOT NULL REFERENCES code_review_policies(id),
    cluster_key          text NOT NULL,
    origin               text NOT NULL CHECK (origin IN ('deterministic_rule','agent_session','manual')),
    generator_session_id uuid REFERENCES sessions(id),
    status               text NOT NULL DEFAULT 'open'
                             CHECK (status IN ('open','activated','dismissed','superseded','expired')),
    -- Structured, validated policy delta. Never raw config replacement.
    proposed_changes     jsonb NOT NULL,
    rationale            text NOT NULL,
    evidence_session_ids uuid[] NOT NULL DEFAULT '{}',
    dispute_count        int NOT NULL DEFAULT 0,
    activated_policy_id  uuid REFERENCES code_review_policies(id),
    decided_by_user_id   uuid REFERENCES users(id),
    decided_at           timestamptz,
    decision_note        text,
    created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_cr_proposals_open_cluster
    ON code_review_policy_proposals (org_id, COALESCE(repository_id, '00000000-0000-0000-0000-000000000000'::uuid), cluster_key)
    WHERE status = 'open';
CREATE INDEX idx_cr_proposals_org_status
    ON code_review_policy_proposals (org_id, status, created_at DESC);

-- Denormalized per-decision outcome facts, so Insights and corroboration do not
-- re-derive PR history on every read.
CREATE TABLE code_review_decision_outcomes (
    session_id                uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id                    uuid NOT NULL REFERENCES organizations(id),
    repository_id             uuid NOT NULL REFERENCES repositories(id),
    pull_request_id           uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    policy_id                 uuid NOT NULL REFERENCES code_review_policies(id),
    decision                  text NOT NULL,
    reason_codes              text[] NOT NULL DEFAULT '{}',
    merged                    boolean NOT NULL DEFAULT false,
    merged_at                 timestamptz,
    human_requested_changes   boolean NOT NULL DEFAULT false,
    human_review_comment_count int NOT NULL DEFAULT 0,
    reverted                  boolean NOT NULL DEFAULT false,
    reverted_at               timestamptz,
    terminal                  boolean NOT NULL DEFAULT false,
    observed_until            timestamptz,
    updated_at                timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_cr_outcomes_org_repo ON code_review_decision_outcomes (org_id, repository_id, updated_at DESC);
CREATE INDEX idx_cr_outcomes_reason_codes ON code_review_decision_outcomes USING gin (reason_codes);
```

New job types on the existing queue:

| Job type | Queue | Trigger |
| --- | --- | --- |
| `classify_code_review_dispute` | `feedback` | Dispute created |
| `corroborate_code_review_dispute` | `feedback` | PR reaches terminal state, or corroboration window expires |
| `cluster_code_review_disputes` | `feedback` | Scheduled, hourly per org with new corroborated disputes |
| `generate_code_review_policy_proposal` | `agent` | Cluster crosses threshold |
| `digest_code_review_insights` | `feedback` | Scheduled, weekly per org |

New audit actions: `code_review_dispute.filed`, `code_review_dispute.adjudicated`,
`code_review_policy_proposal.activated`, `code_review_policy_proposal.dismissed`.

## API Contract

All routes are org-scoped and follow existing auth conventions. Filing and
reading disputes is member-level; adjudication and proposal decisions are
admin-level.

```
POST   /api/v1/code-reviews/{session_id}/disputes
       body:    { "body": string, "contested_reason_codes": string[] }
       201 ->   CodeReviewDispute
       errors:  404 review not found · 409 already filed by this user
                422 reason code not present on the disputed decision

GET    /api/v1/code-reviews/{session_id}/disputes
       200 ->   { "data": CodeReviewDispute[] }

PATCH  /api/v1/code-review-disputes/{id}            (admin)
       body:    { "corroboration_status": "upheld" | "rejected", "adjudication_note": string? }
       200 ->   CodeReviewDispute
       errors:  403 not an admin · 409 already adjudicated

GET    /api/v1/code-review-insights
       query:   repository_id?, from?, to?, decision?, reason_code?
       200 ->   { "data": {
                    "decisions_by_reason": [{ "reason_code", "count", "dispute_count",
                                              "corroborated_count", "dispute_rate" }],
                    "decision_totals":     { "approved", "comment_only", "needs_human_review", "blocked" },
                    "outcomes":            { "merged_clean", "human_requested_changes", "reverted" },
                    "median_decision_seconds": number,
                    "false_approval_rate_by_policy_version": [{ "policy_id", "version", "rate" }]
                 } }

GET    /api/v1/code-review-policy-proposals
       query:   status?, repository_id?
       200 ->   { "data": CodeReviewPolicyProposal[] }

POST   /api/v1/code-review-policy-proposals/{id}/activate      (admin)
       body:    { "proposed_changes": object? }   // optional edited delta
       200 ->   { "policy": CodeReviewPolicy, "proposal": CodeReviewPolicyProposal }
       errors:  409 not open / base policy superseded · 422 delta fails policy validation
                403 delta touches a locked dimension

POST   /api/v1/code-review-policy-proposals/{id}/dismiss       (admin)
       body:    { "decision_note": string }
       200 ->   CodeReviewPolicyProposal
```

The GitHub command path adds no route: `@143-code-reviewer disagree: <reason>`
is parsed in the existing `issue_comment` and `pull_request_review_comment`
webhook handlers and creates the same record with `source = 'github_comment'`,
deduplicated on `github_delivery_id`.

Activation validates the delta against the same policy validation used by
`PUT /api/v1/code-review-policies`, then writes a new insert-only version. It
never patches a policy row in place, so approvals continue to point at the exact
version that produced them.

## Success Metrics

- Share of non-approvals where the author files a dispute — the miscalibration
  signal that does not exist today.
- Corroboration rate of disputes, by reason code. A reason code with a high
  dispute rate *and* high corroboration is a genuinely miscalibrated rule; high
  dispute rate with low corroboration means the explanation is unclear, not the
  policy wrong. Phase 0 should move the second number without moving the first.
- Proposal activation rate, and dismissal reasons.
- False-approval rate per policy version after activation — the guardrail metric.
- Median time from non-approval to policy correction, versus today's unbounded.

## Open Questions

- Should a dispute filed by the PR author carry less clustering weight than one
  filed by a third party, or is corroboration sufficient to handle bias alone?
- What is the right corroboration window? 7 days captures most merges; revert and
  incident signal needs 30.
- Should filing a dispute trigger an immediate re-review under the same policy
  (cheap, sometimes resolves flaky judgment reasons), or is that an invitation to
  reroll until the bot approves? Leaning: no automatic re-review, since approval
  is monotonic and re-rolling a stochastic judge is exactly the gaming path.
- Should Insights be org-wide only, or per-repository with its own tuning
  thresholds?
- Do dismissed proposals suppress the cluster permanently, or decay back after a
  policy change makes the evidence newly relevant?
