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
contest a code review decision in plain language, a durable record of that
contest, an automated triage step that can immediately rerun the review when the
objection carries new information, corroboration against what actually happened
to the PR, and an engine that turns recurring corroborated disputes into
concrete, reviewable policy changes.

Disagreeing must cost nothing. There is no command syntax and no form: a
developer replies to the bot the way they would reply to a human reviewer, and
143 works out what they meant.

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

**Reassessment outcome (automatic, immediate).** When triage reruns the review
(below) and the rerun approves the same head SHA under the same policy version,
the original decision was demonstrably wrong on its own terms — the strongest and
fastest corroboration available, and the only one that does not require waiting
for the PR to reach a terminal state. A rerun that reaches the same conclusion is
not by itself a refutation; it is `inconclusive`, because a rerun cannot fix a
threshold that is genuinely miscalibrated.

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

Add a dispute record with the same free-text intake as Option C, but no
reassessment and no agent proposals — the options differ in what happens to a
dispute, not in how one is filed. When N disputes cluster on the same
*deterministic* reason code in the same repo, a rules engine proposes a
mechanical adjustment —
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

Capture disagreement as free-text replies, triage it with one LLM pass that
attributes it to the decision's own reason codes and decides whether a rerun
could change the answer, and rerun the review when it could. Corroborate each
dispute against the reassessment outcome, PR outcome telemetry, and optional
human adjudication. Cluster the survivors. When a cluster crosses a threshold, a
scheduled 143 session reads the cluster, its evidence, and the current policy,
and emits a **proposed policy version** with a concrete diff and a rationale
citing the sessions behind it. An admin reviews and activates it in one click;
activation creates the next insert-only version and an audit event.

**Pros**
- Handles both halves: deterministic dimensions get mechanical proposals, prompt
  and rubric text gets agent-authored edits. Judgment reasons become tunable.
- Two response times. A wrong judgment call is fixed in minutes by a rerun; a
  wrong policy rule is fixed in the next tuning cycle. Options A and B can only
  ever do the slow one, which is why they read as unresponsive to the developer
  who is blocked right now.
- The corroboration gate means only disputes that reality agreed with can move
  policy.
- Free-text intake means the feature has no adoption curve — nobody has to learn
  or remember a command to use it, and objections filed before it shipped are
  still parseable.
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
- Free-text intake means a classifier decides what counts as a dispute. A missed
  dispute is silent, which is the failure mode this whole document exists to fix,
  so triage must bias toward recording. Over-recording is cheap; the routing step
  and the corroboration gate both filter downstream.
- Dispute-triggered reruns cost agent time and create a reroll surface. Bounded
  by the per-head cap and by deterministic gates being non-waivable, but the
  reassessment-flip rate needs watching: if reruns frequently flip the outcome on
  identical inputs, the problem is judgment-layer variance, not policy.

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

### Phase 1 — Capture, triage, and corroborate

Two capture surfaces, one record, no syntax:

- **GitHub-native (primary).** The developer replies in plain language, either
  in the thread rooted on the bot's rolling comment or by mentioning
  `@143-code-reviewer` anywhere on a PR the bot reviewed. No command, no prefix,
  no structured form. "this is test-only, why is it blocked?", "the migration is
  additive so there's no risk here", and "I think this should have been approved"
  are all valid disputes. The `issue_comment` and `pull_request_review_comment`
  webhooks are already routed (`internal/api/handlers/webhooks.go`).
- **143-native (secondary).** A "Disagree with this decision" action on the code
  review session detail opening a free-text box. The reason codes from the actual
  decision are shown for reference and can optionally be ticked, but nothing is
  required beyond prose. The rolling comment links here.

Both write a `code_review_decision_disputes` row attributed to the session, the
policy version, and the reviewed head SHA, with `intake_status = 'pending'`.

Capture is deliberately over-inclusive and triage is where meaning gets assigned.
It is much better to record a comment that turns out to be a question than to
drop a real objection because it did not match a pattern.

**Intake and routing.** Every captured comment enqueues
`triage_code_review_dispute`, a single LLM pass that reads the comment, the
decision it is responding to, the reason codes that decision emitted, the diff
summary, and the thread it sits in. It produces:

| Field | Meaning |
| --- | --- |
| `is_dispute` | Does this actually contest the decision? Questions, thanks, unrelated chatter, and comments aimed at other bots are not disputes |
| `contested_reason_codes` | Which of the decision's actual reason codes the objection targets, inferred from prose. Empty when the objection is general |
| `dispute_kind` | `threshold_too_strict`, `path_rule_too_broad`, `description_requirement_wrong`, `finding_incorrect`, `judgment_overreach`, `bot_correct`, `other` |
| `asserts_new_information` | Does the comment supply a fact the review did not have — "that path isn't auth-sensitive, it's a fixture", "the generated file is checked in but not executed" |
| `routing` | `reassess`, `policy_signal_only`, `answer_only`, or `not_a_dispute` |

Routing is the useful part, because the two kinds of objection need opposite
responses:

- **`reassess`** — the objection asserts new information or contests an
  agent-judgment reason (`blocking_findings`, `scope_mismatch`,
  `unresolved_uncertainty`, `architecture`, and the other typed human-review
  reasons). A rerun can genuinely reach a different conclusion, so triage
  enqueues one immediately.
- **`policy_signal_only`** — the objection contests a deterministic gate
  (`files_limit_exceeded`, `sensitive_path`, `required_check_failing`). Rerunning
  is pointless: the threshold will evaluate identically. The bot replies saying
  exactly that, names the policy setting and its current value, links to it, and
  records the dispute for clustering. This is the honest answer to "why won't you
  approve this," and it is the case that most needs Phase 2.
- **`answer_only`** — a question rather than a disagreement. The bot answers from
  the existing session evidence. No dispute record survives triage.
- **`not_a_dispute`** — recorded as `discarded`, no reply, no clustering weight.

**Dispute-triggered reassessment.** A `reassess` routing enqueues the normal code
review request path with a new trigger source `dispute_reassessment`, keyed by
dispute id. This fits doc 112's existing idempotency model without changing it:
doc 112 already specifies that a genuinely new explicit request after a
non-approval creates a distinct assessment even at the same head SHA, and that
requests arriving while a review is running are held behind a durable starter job.
A dispute is one more kind of explicit request.

The dispute text enters the reassessment as **untrusted evidence**, under exactly
the screening doc 112 applies to PR descriptions and diffs. It is a claim by the
author to be verified against the code, never an instruction. "Approve this" and
"ignore your policy" are prompt injection, not information, and are handled as
doc 112 already handles them.

Bounds, so that disputing is cheap but rerolling is not a strategy:

- Deterministic gates are re-evaluated from source on every reassessment and can
  never be waived by a dispute. Only the agent-judgment half of the decision can
  move.
- A configurable cap on dispute-triggered reassessments per PR head SHA
  (default 2). Beyond it, further disputes are recorded and clustered but do not
  rerun; the bot says so and points at human review.
- Approval remains monotonic, and each reassessment remains an immutable session,
  so the full history of "blocked, disputed, reassessed, approved" is auditable.
- Reassessments triggered by a dispute are labelled as such in Insights, so a
  repo where they routinely flip the outcome is visibly a repo with an unreliable
  judgment layer.

**Corroboration.** A scheduled `corroborate_code_review_dispute` job resolves
each dispute per the corroboration table once the reassessment settles, the PR
reaches a terminal state, or the window expires.

Ship the `Insights` tab here (Option A's content, plus dispute rate,
corroboration rate, and reassessment-flip rate by reason code), and a weekly
digest to policy owners over the existing notification path — this is the "store
the data and send the results" half of the ask, and it is independently valuable.

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
    github_thread_root_comment_id bigint,
    -- Verbatim natural-language objection. No command syntax is parsed from it.
    body                   text NOT NULL DEFAULT '',
    -- Everything below is inferred by triage, not supplied by the filer.
    -- The app UI may pre-tick reason codes, but never requires them.
    contested_reason_codes text[] NOT NULL DEFAULT '{}',
    dispute_kind           text CHECK (dispute_kind IS NULL OR dispute_kind IN (
                               'threshold_too_strict','path_rule_too_broad',
                               'description_requirement_wrong','finding_incorrect',
                               'judgment_overreach','bot_correct','other')),
    asserts_new_information boolean NOT NULL DEFAULT false,
    routing                text CHECK (routing IS NULL OR routing IN (
                               'reassess','policy_signal_only','answer_only','not_a_dispute')),
    intake_status          text NOT NULL DEFAULT 'pending'
                               CHECK (intake_status IN ('pending','triaged','discarded','failed')),
    intake_confidence      text CHECK (intake_confidence IS NULL OR intake_confidence IN ('low','medium','high')),
    -- Reassessment kicked off by this dispute, when routing = 'reassess'.
    reassessment_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    reassessment_decision   text,
    reassessment_flipped    boolean NOT NULL DEFAULT false,
    reply_comment_id        bigint,
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

-- Each GitHub comment yields at most one dispute; webhook redelivery and comment
-- edits update in place rather than filing a second objection. Deliberately not
-- unique per (session, filer): one person may raise two distinct objections, and
-- collapsing them would lose a signal.
CREATE UNIQUE INDEX idx_cr_disputes_github_comment
    ON code_review_decision_disputes (github_comment_id) WHERE github_comment_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cr_disputes_delivery
    ON code_review_decision_disputes (github_delivery_id) WHERE github_delivery_id IS NOT NULL;
CREATE INDEX idx_cr_disputes_cluster
    ON code_review_decision_disputes (org_id, repository_id, cluster_key, created_at DESC)
    WHERE corroboration_status IN ('corroborated','upheld');
CREATE INDEX idx_cr_disputes_intake
    ON code_review_decision_disputes (org_id, intake_status, created_at)
    WHERE intake_status = 'pending';
CREATE INDEX idx_cr_disputes_pending
    ON code_review_decision_disputes (org_id, corroboration_status, created_at)
    WHERE corroboration_status = 'pending' AND intake_status = 'triaged';
-- Enforces the per-head reassessment cap without a table scan.
CREATE INDEX idx_cr_disputes_reassessments
    ON code_review_decision_disputes (pull_request_id, reviewed_head_sha)
    WHERE reassessment_session_id IS NOT NULL;

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

The reassessment path needs a new trigger source on existing tables:

```sql
-- internal/models/code_review.go gains CodeReviewTriggerSourceDisputeReassessment.
ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT chk_code_review_session_metadata_trigger_source,
    ADD CONSTRAINT chk_code_review_session_metadata_trigger_source
        CHECK (trigger_source IN ('app_reviewer','alias_reviewer','team_reviewer',
                                  'slash_command','auto_policy','dispute_reassessment'));

-- Links a reassessment session back to the objection that caused it.
ALTER TABLE code_review_session_metadata
    ADD COLUMN triggering_dispute_id uuid REFERENCES code_review_decision_disputes(id);
```

New job types on the existing queue:

| Job type | Queue | Trigger |
| --- | --- | --- |
| `triage_code_review_dispute` | `feedback` | Comment captured on a reviewed PR, or a dispute filed in the app |
| `run_code_review` (existing) | `agent` | Triage routed `reassess`; enqueued with `trigger_source = 'dispute_reassessment'`, deduped on dispute id |
| `reply_code_review_dispute` | `feedback` | Triage routed `policy_signal_only` or `answer_only`, or a reassessment completed |
| `corroborate_code_review_dispute` | `feedback` | Reassessment settles, PR reaches terminal state, or corroboration window expires |
| `cluster_code_review_disputes` | `feedback` | Scheduled, hourly per org with new corroborated disputes |
| `generate_code_review_policy_proposal` | `agent` | Cluster crosses threshold |
| `digest_code_review_insights` | `feedback` | Scheduled, weekly per org |

Reassessment reuses the existing `run_code_review` handler unchanged; only the
request-orchestration path in `internal/services/codereview` learns the new
trigger source and the per-head cap.

Two new policy knobs, versioned with the rest of `code_review_policies`:

| Field | Default | Meaning |
| --- | --- | --- |
| `dispute_reassessment_enabled` | `true` | Whether a dispute may rerun the review at all |
| `max_dispute_reassessments_per_head` | `2` | Cap per PR head SHA before disputes become record-only |

New audit actions: `code_review_dispute.filed`, `code_review_dispute.reassessed`,
`code_review_dispute.adjudicated`, `code_review_policy_proposal.activated`,
`code_review_policy_proposal.dismissed`.

## API Contract

All routes are org-scoped and follow existing auth conventions. Filing and
reading disputes is member-level; adjudication and proposal decisions are
admin-level.

```
POST   /api/v1/code-reviews/{session_id}/disputes
       body:    { "body": string,                        // required, free text
                  "contested_reason_codes": string[]? }  // optional hint; triage may override
       201 ->   CodeReviewDispute   // intake_status "pending"; triage runs async
       errors:  404 review not found · 422 empty body
       note:    No 409. Multiple distinct objections on one review are legitimate;
                only GitHub-sourced disputes dedupe, on comment id.

GET    /api/v1/code-reviews/{session_id}/disputes
       200 ->   { "data": CodeReviewDispute[] }   // includes routing, reassessment linkage

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

The GitHub path adds no route and parses no syntax. The existing `issue_comment`
and `pull_request_review_comment` webhook handlers capture replies in the bot's
rolling-comment thread and comments mentioning the bot on any PR it has reviewed,
create the record with `source = 'github_comment'` deduplicated on
`github_delivery_id`, and enqueue triage. Whether a captured comment is a dispute
at all is decided by triage, not by the handler.

Activation validates the delta against the same policy validation used by
`PUT /api/v1/code-review-policies`, then writes a new insert-only version. It
never patches a policy row in place, so approvals continue to point at the exact
version that produced them.

## Success Metrics

- Share of non-approvals where someone objects — the miscalibration signal that
  does not exist today. Expect this to rise when intake becomes free-text, which
  is the feature working, not the bot getting worse.
- Triage accuracy, sampled by hand: objections recorded as `not_a_dispute` are
  the silent failure this document exists to prevent, and matter far more than
  the reverse error.
- Reassessment flip rate, split by whether the dispute asserted new information.
  Flips on genuinely new facts are the loop working. Flips on identical inputs
  mean the judgment layer is non-deterministic, which is a different bug and
  should be fixed rather than tuned around.
- Median time from objection to a substantive reply, and to a flipped decision
  where one occurs — the developer-facing latency this feature is judged on.
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
- Is 2 the right per-head reassessment cap? Too low and a legitimate two-round
  clarification hits the wall; too high and rerolling becomes viable. Instrument
  the flip rate by attempt number before settling it.
- Should a reassessment that flips to approval require the new information to be
  verifiable in the diff, rather than merely asserted by the author? Stricter and
  safer, but it narrows the cases a rerun can resolve.
- Should triage treat a dispute from someone other than the PR author as
  `reassess`-eligible on a wider set of reason codes, given the weaker bias?
- Should Insights be org-wide only, or per-repository with its own tuning
  thresholds?
- Do dismissed proposals suppress the cluster permanently, or decay back after a
  policy change makes the evidence newly relevant?
