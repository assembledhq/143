# Design: Code Review Decision Feedback And Policy Tuning

> **Status:** Not Started | **Last reviewed:** 2026-07-30
>
> **Depends on:** [../implemented/112-code-reviewer-bot-auto-approval.md](../implemented/112-code-reviewer-bot-auto-approval.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md), [../future/18-fix-quality-feedback.md](18-fix-quality-feedback.md), [../future/116-automatic-pr-feedback-follow-through.md](116-automatic-pr-feedback-follow-through.md), [../future/16-ai-agent-evals.md](16-ai-agent-evals.md)

## Summary

The 143 Code Reviewer decides; nobody can argue with it. When the bot withholds
approval, the PR author's only recourse is "ask a human." That disagreement —
the highest-signal data the system could collect about its own policy — is
discarded, and the org never learns which of its rules are actually miscalibrated.

This document proposes a **decision feedback loop**:

- a first-class way to contest a code review decision in plain language, in
  either direction — "this should have been approved" and "this should not have
  been approved"
- automated triage that reruns the review immediately when the objection carries
  information a rerun could act on, and answers plainly when it does not
- proactive sampling of approvals near the policy frontier, because false
  approvals are silent and nobody files a complaint about them
- a deliberately narrow evidence standard for which objections may influence
  policy at all
- per-dispute policy proposals, centred on the editable rubric and description
  prompts, each carrying a replay of what it would have changed

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

2. **No counterfactual.** The comment lists what blocked. It never says whether a
   single blocker was the only one. "One threshold away" and "blocked on eleven
   separate things" read the same.

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

**A dispute is a claim, not a fact.** The evidence standard below is what
separates this design from that one.

### The asymmetry that makes complaints insufficient

Blocked authors complain. Nobody complains about a PR that sailed through.

False approvals — the dangerous error, and the one doc 112 tracks as its headline
safety metric — are **silent by construction**. A purely complaint-driven loop can
therefore only ever discover evidence for loosening policy, and over any time
horizon becomes a one-way ratchet no matter how careful its evidence standard is.

This design answers that in two ways: disputes are **bidirectional**, so
"this should not have been approved" is a first-class input; and approvals near
the policy frontier are **proactively sampled** for human spot-check, so the
tightening direction has a source of evidence that does not depend on anyone
noticing a problem.

## Evidence Standard

Only two signals may generate a policy proposal. Everything else is visible in
Insights and inert.

**Proposal-generating:**

| Signal | Direction | Why it qualifies |
| --- | --- | --- |
| Admin adjudicates `upheld` | Both | Authoritative. A policy owner has looked and agreed. Overrides everything |
| **Independent human contradiction** — a human who is neither the PR author nor the disputer approved a PR 143 blocked, or left a blocking review / requested changes on a PR 143 approved | Both | Genuine independent judgment, contradicting the bot, from someone with no stake in the complaint. `ReviewContext.BlockingHumanReviews` already counts the second case |

**Insights-only — never generates a proposal:**

| Signal | Why it is excluded |
| --- | --- |
| A reassessment flipped the decision | The PR description is a mutable input that neither the head SHA nor the policy version pins — doc 112 hashes title/body separately for exactly this reason. An author who reads the blocker, improves the description, and gets approved is the system working correctly, not evidence the first call was wrong |
| PR merged with no blocking human review | Frequently circular: after a non-approval, a human approving is the normal escalation path, so treating it as evidence reads the escalation as proof escalation was unnecessary. It also cannot distinguish a careful review from a rubber stamp, and without branch-protection data — which 143 does not track — it cannot exclude self-merge |
| Reverted, or linked to an incident | Attribution is guesswork and the lag is months. Too weak to draft a change to a security control |
| Any signal, where the disputer is not an org member | See the trust model below |

This standard is deliberately strict, because there is no clustering step behind
it: one qualifying dispute drafts a proposal. In the steady state most proposals
will therefore originate from admin adjudication, including adjudications
produced by frontier spot-checks. **Throughput is bounded by admin attention by
design** — the alternative is volume of proposals nobody reads carefully, which
converts the human activation gate into a rubber stamp and defeats its purpose.

### Trust model

Capture and influence are separate privileges. Reuse
`evaluatePRFeedbackEligibility` (`internal/services/github/pr_feedback_policy.go`)
for capture rather than reinventing eligibility.

| Author | Captured | Answered | Can trigger a rerun | Can influence policy |
| --- | --- | --- | --- | --- |
| `OWNER` / `MEMBER` / `COLLABORATOR` | yes | yes | yes | yes |
| Any human on a private repository | yes | yes | yes | yes |
| Other humans (incl. fork contributors on public repos) | yes | yes | **no** | **no** |
| Bots | no | no | no | no |

Untrusted objections are still recorded, triaged, and answered — silently
dropping a fork contributor's point is the exact failure this document exists to
fix — and they appear in Insights flagged `untrusted`, so if outside
contributors are consistently right about a rule, a human can still notice and
adjudicate it into the loop deliberately.

Untrusted authors cannot trigger reassessments. Reassessment is the expensive
action, and on a public repository an unbounded supply of anonymous authors
triggering unbounded agent runs is a denial-of-service surface. Spending an
organization's compute is a form of influence.

Bots cannot file disputes at all. The `self_authored` and hidden-marker checks in
`evaluatePRFeedbackEligibility` already prevent the obvious self-loop, and there
is no case where a bot should be arguing with approval policy.

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

### Option B — Disputes plus a deterministic auto-tuner

Same free-text intake as Option C, but no reassessment and no prompt proposals —
the options differ in what happens to a dispute, not in how one is filed. A rules
engine proposes mechanical adjustments to numeric and enumerable dimensions from
the `Actual`/`Limit` values the reason codes already carry.

**Pros**
- Fully explainable and unit-testable; the tuner is arithmetic.
- No LLM anywhere in the policy-authoring path.
- Maps cleanly onto insert-only policy versioning.

**Cons**
- **Tunes the dimensions that matter least here.** 143's deterministic gates are
  deliberately set lenient, so relatively few non-approvals are caused by them.
  The volume arrives through judgment reasons — `blocking_findings`,
  `scope_mismatch`, `unresolved_uncertainty`, `architecture` — which are governed
  by rubric and description prompt text and are untouchable by arithmetic.
- Threshold ratcheting: repeated disputes monotonically loosen limits, and each
  individual step looks reasonable.
- Deterministic thresholds are also the rules teams are most able to set
  correctly by hand, so this automates the easy half and leaves the hard half
  untouched.

### Option C — Disputes, triage-driven reassessment, and per-dispute proposals (recommended)

Capture disagreement as free-text replies in either direction. Triage with one
LLM pass that attributes the objection to the decision's own reason codes and
decides whether a rerun could change the answer; rerun when it could. Apply the
evidence standard above. Each qualifying dispute produces a **proposed policy
version** — most often an edit to the rubric or description prompts — carrying a
replay of what it would have changed. An admin reviews and activates in one
click; activation creates the next insert-only version and an audit event.
Separately, sample approvals near the policy frontier for spot-check so the
tightening direction has evidence that does not require a complaint.

**Pros**
- Tunes the dimension that actually drives outcomes: the prompts.
- Two response times. A wrong judgment call is fixed in minutes by a rerun; a
  wrong rule is fixed in the next proposal. Options A and B can only do the slow
  one, which is why they read as unresponsive to the developer blocked right now.
- Bidirectional, with frontier sampling, so policy can tighten as well as loosen.
- Human activation keeps the security boundary intact — the system proposes,
  a person disposes — while removing essentially all of the analysis toil.
- Free-text intake has no adoption curve: nobody has to learn a command, and
  objections filed before it shipped are still parseable.
- Reuses infrastructure wholesale: 143 sessions, the job queue, insert-only
  policies, the audit log, notifications, and the existing PR-comment
  eligibility model.

**Cons**
- The largest build.
- A classifier decides what counts as a dispute. A missed dispute is silent —
  the failure mode this document exists to fix — so triage must bias toward
  recording. Over-recording is cheap; routing and the evidence standard filter
  downstream.
- Prompt proposals are the least legible kind of change: an admin cannot
  evaluate a reworded rubric by reading it. Replay against a held-out guard set
  is what makes the review real rather than nominal, and it is load-bearing
  rather than optional.
- Unbounded reruns for trusted humans means agent spend scales with argument
  volume. Accepted deliberately; see the reassessment section.

### Option D — Policy-as-code with proposal PRs

Same capture and evidence standard as C, but policy lives in the repository
(`.143/code-review-policy.yml`) and proposals arrive as ordinary pull requests,
following the `.143/learned-conventions.md` pattern from doc 11.

**Pros**
- Policy changes get the team's real review process, CODEOWNERS, and git history
  for free — a strong fit for a control that governs approvals.
- Fully transparent and diffable.
- No new approval UI to build.

**Cons**
- Forks the source of truth. Policy is DB-owned, versioned, and resolvable with
  org-default plus repo-override inheritance; a repo file needs sync, conflict,
  and precedence rules against that model, and doc 112's requirement that
  approvals point at the exact policy version that produced them becomes harder
  to guarantee.
- Repo-scoped files cannot express org-level defaults inherited by repositories
  with no override.
- A PR proposing to loosen the reviewer bot's own rules is exactly the category
  doc 112 says the bot must not approve — workable, but needs a carve-out.
- Slower loop: prompt tuning now requires a merge, and prompt tuning is the part
  that needs the fastest iteration.

Worth revisiting once policy stabilizes. Not the right first move.

### Rejected — Fully autonomous closed-loop adaptation

C or D with automatic activation and metric-triggered rollback.

Rejected on principle rather than effort. Approval policy is a security control;
an automated system that relaxes its own controls in response to pressure from
blocked authors is unsound regardless of how good the rollback is, and "reverts
within 7 days" is far too slow a signal for a gate whose failure mode is
unreviewed code reaching main. Doc 112's product principle — *approval requires
evidence* — applies to changes in what counts as evidence.

The defensible bounded form, if a team later wants it: admin pre-authorizes
auto-application for named low-risk deterministic dimensions only, within an
admin-set ceiling, never for prompts, path rules, sensitive categories, required
checks, or agent roster. That is a later configuration option on top of C.

## Recommendation

Ship **Option C** in three phases. Each phase stands alone and is useful if the
next never ships.

### Phase 0 — Make the decision legible (no new subsystem)

Changes to `internal/models/code_review_output.go`, the worker, and the session
detail view:

1. **Group blockers by lever.** Render three labelled groups instead of one list:
   *Policy thresholds* (deterministic, tunable in Configurations — deep-link to
   the exact setting), *Review findings* (agent code judgment, contestable),
   *Human judgment required* (typed non-finding reasons). Derive grouping from
   the existing `CodeReviewRiskReasonCode` — no new taxonomy.
2. **State the scope of the blocker, not a promise.** When exactly one blocker
   exists, say so as of a commit: *"This is the only blocker as of `abc1234`."*
   Not "resolving it would get this approved" — fixing it triggers a fresh review
   that may legitimately surface something new, and a promise the system cannot
   keep is worse than no promise.
3. **Separate system faults from risk.** Give `context_unavailable` and
   `orchestrator_synthesis_invalid` a distinct rendering — 143 could not complete
   the review — and keep them out of the policy-blocker list entirely.
4. **Expose advisory findings.** Put P2/P3 observations in a collapsed
   `<details>` block instead of a count. They remain non-blocking and still
   generate no inline comments.
5. **Fast-fail deterministic gates.** Evaluate size, path, author, and fork
   eligibility from the PR file list *before* reviewer fan-out. If policy makes
   the PR categorically ineligible, post the result immediately and skip the
   agent run. Cuts cost and turns a multi-minute wait into seconds.

Phase 0 alone likely resolves a large share of "I'm not sure why," and every
later phase depends on the reason-code grouping it introduces.

### Phase 1 — Capture, triage, reassess, corroborate

**Capture, with no syntax.** Two surfaces, one record:

- **GitHub-native (primary).** The developer replies in plain language, either in
  the thread rooted on the bot's rolling comment or by mentioning
  `@143-code-reviewer` anywhere on a PR it reviewed. "this is test-only, why is it
  blocked?", "the migration is additive so there's no risk here", and "hold on,
  this shouldn't have been auto-approved, it touches auth" are all valid. The
  `issue_comment` and `pull_request_review_comment` webhooks are already routed.
- **143-native (secondary).** A "Disagree with this decision" action on the code
  review session detail, opening a free-text box. Reason codes from the actual
  decision are shown for reference and may optionally be ticked; nothing beyond
  prose is required.

Capture is deliberately over-inclusive. Eligibility comes from
`evaluatePRFeedbackEligibility`; meaning is assigned by triage, not by the
webhook handler.

**Triage.** Every captured comment enqueues `triage_code_review_dispute`, which
runs `deterministicPRFeedbackTriage` first as a free pre-filter for
acknowledgements and empty bodies, then a single LLM pass over the comment, the
decision it responds to, that decision's reason codes, the diff summary, and the
surrounding thread. It produces:

| Field | Meaning |
| --- | --- |
| `is_dispute` | Does this contest the decision? Questions, thanks, and chatter are not disputes |
| `direction` | `should_have_approved` or `should_not_have_approved`, inferred from the decision being responded to |
| `contested_reason_codes` | Which of the decision's actual reason codes the objection targets, inferred from prose |
| `dispute_kind` | See the enum in the schema; covers both directions |
| `asserts_new_information` | Does the comment supply a fact the review did not have — "that path isn't auth-sensitive, it's a fixture" |
| `routing` | `reassess`, `policy_signal_only`, `answer_only`, `not_a_dispute` |

Routing matters because the two kinds of objection need opposite responses:

- **`reassess`** — the objection asserts new information or contests an
  agent-judgment reason. A rerun can genuinely reach a different conclusion.
  Requires a trusted author and the `should_have_approved` direction.
- **`policy_signal_only`** — the objection contests a deterministic gate. A rerun
  is pointless: the threshold evaluates identically. The bot says exactly that,
  names the policy setting and its current value, links to it, and records the
  dispute.
- **`answer_only`** — a question, not a disagreement. Answered from existing
  session evidence; no dispute record survives.
- **`not_a_dispute`** — recorded as `discarded`. No reply, no influence.

A `should_not_have_approved` dispute never reassesses. Doc 112 makes approval
monotonic and this design does not touch that: automation must never dismiss or
contradict an approval that has already occurred. That direction is
record-and-propose only, which makes it simpler than the blocked direction.

**Reassessment.** A `reassess` routing enqueues the normal code review request
path with a new trigger source `dispute_reassessment`, keyed by dispute id. This
fits doc 112's existing idempotency model unchanged: a genuinely new explicit
request after a non-approval already creates a distinct assessment at the same
head SHA, and requests arriving mid-review are already held behind a durable
starter job. A dispute is one more kind of explicit request.

The dispute text enters as **untrusted evidence** under exactly the screening
doc 112 applies to PR descriptions and diffs. It is a claim to verify against the
code, never an instruction. "Approve this" and "ignore your policy" are prompt
injection, not information.

There is **no cap** on dispute-triggered reassessments per PR for trusted
authors. A cap would not be a security boundary anyway: doc 112 already
auto-enqueues a fresh assessment on every new commit until approval, so
`git commit --allow-empty && git push` is an existing unlimited-rerun path that
predates this feature. Rationing arguments while leaving that open would buy
nothing and be hostile to people with a real problem. What actually bounds risk:

- Deterministic gates are re-evaluated from source on every reassessment and can
  never be waived by a dispute. This is the real security boundary and it does
  not depend on any cap.
- Untrusted authors cannot trigger reruns at all (trust model above).
- Approval remains monotonic; each reassessment remains an immutable session.

If rerolling a stochastic judge until it approves is a concern, the fix is not in
this document — it is judgment-layer variance in doc 112, and the empty-commit
path is the one to close. Track reassessment-flip rate by attempt number: if
attempt 2 flips as often as attempt 1 on unchanged inputs, fix the judge rather
than ration the reruns.

**Loop safety between bot subsystems.** The reviewer's dispute reply is a comment
on the PR, and doc 116's feedback follow-through ingests PR comments including
bot-authored ones — an installed app on a private repo is eligible by two of its
provenance tiers. Reply → ingested → commit pushed → doc 112 auto-enqueues a
review → new reply is a loop that forms without anyone behaving incorrectly, and
with no cap there is no counter in it.

Two guards, reusing what doc 116 already built:

- Dispute replies carry the hidden marker doc 116 already checks
  (`prFeedbackHiddenMarker` → `IgnoreReason: "hidden_response_marker"`), so
  follow-through skips them and the loop never forms.
- A reviewer-side **cycle budget per epoch**, mirroring
  `feedback_bot_epoch` / `feedback_bot_cycles_in_epoch`
  (`internal/db/pull_request_feedback.go`): any human comment on the PR resets
  the epoch and refills the budget; consecutive machine-only rounds decrement it.
  Human-driven rounds stay unlimited; machine-only rounds are bounded.

Markers get dropped in refactors. The epoch budget is the backstop.

**Corroboration.** A scheduled `corroborate_code_review_dispute` job applies the
evidence standard once the reassessment settles, the PR reaches a terminal state,
or the window expires.

**Surfacing.** Ship the `Insights` tab here, and a weekly digest to policy owners
over the existing notification path. This is the "store the data and send the
results" half of the ask, and it is independently valuable.

**GitHub surface discipline.** Doc 112 fights to keep exactly one visible 143
comment per PR, and unlimited disputes could easily undo that:

- Reassessment results update the **rolling comment in place**, as every other
  assessment already does. No new comment.
- `policy_signal_only` and `answer_only` get **one threaded reply** each — these
  are conversational turns and silence is the failure being fixed. GitHub threads
  them under the comment they answer. Never a follow-up chain.
- The rolling comment carries a compact state line: *"2 objections · 1
  reassessment · [view](link)"*.
- Full dispute history, triage reasoning, and reassessment lineage live on the
  143 session, which is already the detail surface.

### Phase 2 — Propose

**Per-dispute, not clustered.** One dispute that meets the evidence standard
produces one proposal. Clustering is a de-noising mechanism, and at realistic
review volumes split across repositories, reason codes, and dispute kinds the
modal cluster size is one — de-noising would discard the entire signal. It also
tightens the loop from a month to days. If proposal volume ever becomes the
complaint, clustering is a later optimization.

**Proposal kinds.** Two, with the emphasis inverted from the obvious ordering:

- **Prompt proposals (primary).** A 143 session reads the dispute, its review
  session, findings, reviewer evidence, and current prompt text, and proposes a
  scoped edit to the acceptable-risk rubric or a PR-description requirement.
  Edits are scoped to a named section and may add **or remove** — an
  additions-only rule would mean the rubric can only ever grow and a genuinely
  wrong rule could never be deleted, which is its own ratchet. Dispute text
  enters the session as untrusted evidence.
- **Deterministic proposals (secondary).** A rules engine computes threshold,
  path, category, check, and author-eligibility changes directly from
  `Actual`/`Limit`. No LLM. Bounded by admin-set ceilings, and may widen a path
  or category set by at most one named entry at a time.

**Replay is what makes review real.** An admin cannot evaluate a reworded rubric
by reading it. Every proposal is replayed before an admin sees it, against two
sets, and both are shown:

- the **target set** — the dispute(s) that motivated it. Did it fix them?
- a **guard set** — held-out decisions believed correct, weighted toward
  approvals near the frontier. Replaying only the target set is worthless: the
  prompt was written to fix those cases, so it always passes. The guard set is
  what catches "fixed 1 dispute, would have flipped 11 correct approvals."

Replaying a prompt change does **not** require rerunning the review. The editable
rubric and description prompts feed the *orchestrator*, and
`code_review_agent_results` stores per-role `raw_output` and `structured_result`
while doc 112 requires rendered prompts to be recoverable from audit state. So
replay reruns the **orchestrator step alone against stored reviewer outputs** —
no sandboxes, no repo clones, no reviewer fan-out. One cheap agent call per
historical review, 20–30 per proposal. Deterministic proposals replay exactly and
offline by re-evaluating `EvaluateCodeReviewRisk` against stored inputs.

**Seeding the guard set.** On day one there is no corpus of agreed-correct
decisions. Bootstrap from decisions that merged with no blocking human review —
weak evidence individually, but adequate as a regression baseline, which is a
lower bar than proposal-generating evidence. Curate it thereafter from
spot-check verdicts, which are real human judgments. The guard set doubles as a
small approval-decision eval corpus, giving doc 16 a dataset to build on rather
than something to compete with.

**Frontier sampling.** Because false approvals are silent, sample them instead of
waiting. A scheduled job scores each approval by proximity to the policy
boundary and queues the top-K per week for admin spot-check:

| Component | Signal |
| --- | --- |
| **Margin** | How close to the deterministic limits — 5/5 files, 296/300 lines |
| **Thin judgment** | Approved despite P2 findings, `low` confidence on clean verdicts, or reviewer disagreement that did not reach blocking |
| **Novelty** | First approval touching a path or risk category this policy has never approved before |

Novelty carries the highest weight: margin says the policy is being exercised at
its edge, novelty says it is being applied somewhere it has never been tested,
and untested is where an unexamined rule turns out to be wrong.

Alongside the frontier picks, include a **blind random sample** the reviewer
cannot distinguish. If random spot-checks surface bad approvals at the same rate
as frontier picks, the frontier score is worthless and there is no other way to
find that out. The random arm also yields an unbiased false-approval rate —
doc 112's success metric, currently uncomputable.

Queue size should be a few minutes of work per week. A verdict of "should not
have been approved" writes a dispute with `direction = should_not_have_approved`
and `corroboration_status = upheld`, which is an admin adjudication and therefore
already proposal-generating. Frontier sampling needs no new evidence tier; it is
a discovery mechanism feeding a signal that already qualifies.

**Guardrails, non-negotiable:**

- Proposals never auto-apply.
- Proposals can never touch approval mode, fork eligibility, prompt-injection
  handling, agent roster, or the bot's own policy paths.
- Every activation is attributable to a user, in the audit log, and revertible by
  activating the prior version — policies are already insert-only, so revert is
  one click.
- Insights tracks decision-mix shift and false-approval rate per policy version,
  so a bad prompt change is visible in days rather than discovered by incident.

## Database Schema

Four new tables plus alterations, all tenant-scoped by `org_id`, following the
insert-only and partial-unique-index conventions in doc 112.

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
    direction              text NOT NULL DEFAULT 'should_have_approved'
                               CHECK (direction IN ('should_have_approved','should_not_have_approved')),
    -- Filer identity and trust. `trusted` gates both reruns and policy influence.
    filed_by_user_id       uuid REFERENCES users(id),
    filed_by_login         text NOT NULL DEFAULT '',
    author_association     text NOT NULL DEFAULT '',
    author_is_pr_author    boolean NOT NULL DEFAULT false,
    trusted                boolean NOT NULL DEFAULT false,
    source                 text NOT NULL CHECK (source IN ('github_comment','app_ui','api','spot_check')),
    github_comment_id      bigint,
    github_delivery_id     text,
    github_thread_root_comment_id bigint,
    -- Verbatim natural-language objection. No command syntax is parsed from it.
    body                   text NOT NULL DEFAULT '',
    -- Everything below is inferred by triage, not supplied by the filer.
    contested_reason_codes text[] NOT NULL DEFAULT '{}',
    dispute_kind           text CHECK (dispute_kind IS NULL OR dispute_kind IN (
                               -- should_have_approved
                               'threshold_too_strict','path_rule_too_broad',
                               'description_requirement_wrong','finding_incorrect',
                               'judgment_overreach',
                               -- should_not_have_approved
                               'threshold_too_lenient','path_rule_too_narrow',
                               'missed_finding','judgment_underreach',
                               -- either
                               'bot_correct','other')),
    asserts_new_information boolean NOT NULL DEFAULT false,
    routing                text CHECK (routing IS NULL OR routing IN (
                               'reassess','policy_signal_only','answer_only','not_a_dispute')),
    intake_status          text NOT NULL DEFAULT 'pending'
                               CHECK (intake_status IN ('pending','triaged','discarded','failed')),
    intake_confidence      text CHECK (intake_confidence IS NULL OR intake_confidence IN ('low','medium','high')),
    reassessment_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    reassessment_decision   text,
    reassessment_flipped    boolean NOT NULL DEFAULT false,
    reply_comment_id        bigint,
    -- Evidence standard. Only 'independent_contradiction' and 'upheld' may
    -- generate a proposal, and only when `trusted` is true.
    corroboration_status   text NOT NULL DEFAULT 'pending'
                               CHECK (corroboration_status IN (
                                   'pending','independent_contradiction','upheld',
                                   'rejected','weak','inconclusive')),
    corroboration_detail   jsonb NOT NULL DEFAULT '{}',
    corroborated_at        timestamptz,
    adjudicated_by_user_id uuid REFERENCES users(id),
    adjudicated_at         timestamptz,
    adjudication_note      text,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

-- Each GitHub comment yields at most one dispute; redelivery and comment edits
-- update in place. Deliberately not unique per (session, filer): one person may
-- raise two distinct objections and collapsing them would lose a signal.
CREATE UNIQUE INDEX idx_cr_disputes_github_comment
    ON code_review_decision_disputes (github_comment_id) WHERE github_comment_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cr_disputes_delivery
    ON code_review_decision_disputes (github_delivery_id) WHERE github_delivery_id IS NOT NULL;
CREATE INDEX idx_cr_disputes_intake
    ON code_review_decision_disputes (org_id, intake_status, created_at)
    WHERE intake_status = 'pending';
CREATE INDEX idx_cr_disputes_pending
    ON code_review_decision_disputes (org_id, corroboration_status, created_at)
    WHERE corroboration_status = 'pending' AND intake_status = 'triaged';
-- Proposal-eligible disputes awaiting a proposal.
CREATE INDEX idx_cr_disputes_eligible
    ON code_review_decision_disputes (org_id, repository_id, direction, corroborated_at DESC)
    WHERE trusted = true AND corroboration_status IN ('independent_contradiction','upheld');
CREATE INDEX idx_cr_disputes_session
    ON code_review_decision_disputes (session_id, created_at);

CREATE TABLE code_review_policy_proposals (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               uuid NOT NULL REFERENCES organizations(id),
    repository_id        uuid REFERENCES repositories(id),
    base_policy_id       uuid NOT NULL REFERENCES code_review_policies(id),
    source_dispute_id    uuid NOT NULL REFERENCES code_review_decision_disputes(id),
    direction            text NOT NULL CHECK (direction IN ('loosen','tighten')),
    change_kind          text NOT NULL CHECK (change_kind IN ('prompt','deterministic')),
    origin               text NOT NULL CHECK (origin IN ('deterministic_rule','agent_session','manual')),
    generator_session_id uuid REFERENCES sessions(id),
    status               text NOT NULL DEFAULT 'replaying'
                             CHECK (status IN ('replaying','open','activated','dismissed','superseded','expired')),
    -- Structured, validated policy delta. Never raw config replacement.
    proposed_changes     jsonb NOT NULL,
    rationale            text NOT NULL,
    -- Replay outcomes. Target = the motivating dispute; guard = held-out set.
    replay_status        text NOT NULL DEFAULT 'pending'
                             CHECK (replay_status IN ('pending','running','complete','failed')),
    replay_target_result jsonb NOT NULL DEFAULT '{}',
    replay_guard_result  jsonb NOT NULL DEFAULT '{}',
    guard_regressions    int NOT NULL DEFAULT 0,
    activated_policy_id  uuid REFERENCES code_review_policies(id),
    decided_by_user_id   uuid REFERENCES users(id),
    decided_at           timestamptz,
    decision_note        text,
    created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_cr_proposals_open_dispute
    ON code_review_policy_proposals (source_dispute_id) WHERE status IN ('replaying','open');
CREATE INDEX idx_cr_proposals_org_status
    ON code_review_policy_proposals (org_id, status, created_at DESC);

-- Frontier + random sampling of approvals for human spot-check.
CREATE TABLE code_review_approval_audits (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES organizations(id),
    session_id         uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository_id      uuid NOT NULL REFERENCES repositories(id),
    pull_request_id    uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    policy_id          uuid NOT NULL REFERENCES code_review_policies(id),
    -- Never exposed to the reviewing admin: the random arm is the control.
    selection          text NOT NULL CHECK (selection IN ('frontier','random')),
    frontier_score     numeric(6,4) NOT NULL DEFAULT 0,
    frontier_factors   jsonb NOT NULL DEFAULT '{}',
    status             text NOT NULL DEFAULT 'queued'
                           CHECK (status IN ('queued','reviewed','expired')),
    verdict            text CHECK (verdict IS NULL OR verdict IN ('correct','should_not_have_approved')),
    resulting_dispute_id uuid REFERENCES code_review_decision_disputes(id),
    reviewed_by_user_id uuid REFERENCES users(id),
    reviewed_at        timestamptz,
    note               text,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_cr_audits_session ON code_review_approval_audits (session_id);
CREATE INDEX idx_cr_audits_queue ON code_review_approval_audits (org_id, status, created_at)
    WHERE status = 'queued';

-- Held-out decisions used as the replay regression baseline.
CREATE TABLE code_review_guard_set_members (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    repository_id     uuid NOT NULL REFERENCES repositories(id),
    session_id        uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    expected_decision text NOT NULL,
    added_by          text NOT NULL CHECK (added_by IN ('bootstrap','spot_check','admin')),
    active            boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_cr_guard_session ON code_review_guard_set_members (session_id) WHERE active = true;
CREATE INDEX idx_cr_guard_repo ON code_review_guard_set_members (org_id, repository_id) WHERE active = true;

-- Denormalized per-decision outcome facts for Insights and corroboration.
CREATE TABLE code_review_decision_outcomes (
    session_id                 uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id                     uuid NOT NULL REFERENCES organizations(id),
    repository_id              uuid NOT NULL REFERENCES repositories(id),
    pull_request_id            uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    policy_id                  uuid NOT NULL REFERENCES code_review_policies(id),
    decision                   text NOT NULL,
    reason_codes               text[] NOT NULL DEFAULT '{}',
    merged                     boolean NOT NULL DEFAULT false,
    merged_at                  timestamptz,
    -- Independent human contradiction inputs. Author and disputer are excluded
    -- when evaluating these.
    independent_approver_login text,
    independent_blocking_review_login text,
    human_review_comment_count int NOT NULL DEFAULT 0,
    reverted                   boolean NOT NULL DEFAULT false,
    reverted_at                timestamptz,
    terminal                   boolean NOT NULL DEFAULT false,
    observed_until             timestamptz,
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_cr_outcomes_org_repo ON code_review_decision_outcomes (org_id, repository_id, updated_at DESC);
CREATE INDEX idx_cr_outcomes_reason_codes ON code_review_decision_outcomes USING gin (reason_codes);
```

Alterations to existing tables:

```sql
-- internal/models/code_review.go gains CodeReviewTriggerSourceDisputeReassessment.
ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT chk_code_review_session_metadata_trigger_source,
    ADD CONSTRAINT chk_code_review_session_metadata_trigger_source
        CHECK (trigger_source IN ('app_reviewer','alias_reviewer','team_reviewer',
                                  'slash_command','auto_policy','dispute_reassessment'));

ALTER TABLE code_review_session_metadata
    ADD COLUMN triggering_dispute_id uuid REFERENCES code_review_decision_disputes(id),
    -- Risk reasons are only recorded when a gate FAILS, so an approval currently
    -- leaves no record of how close it came. Frontier margin scoring needs these.
    ADD COLUMN files_changed int,
    ADD COLUMN lines_changed int;

-- Reviewer-side bot-loop budget, mirroring the doc 116 epoch mechanic.
ALTER TABLE pull_requests
    ADD COLUMN code_review_dispute_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN code_review_dispute_cycles_in_epoch integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT chk_pr_code_review_dispute_cycles
        CHECK (code_review_dispute_cycles_in_epoch >= 0);
```

New job types on the existing queue:

| Job type | Queue | Trigger |
| --- | --- | --- |
| `triage_code_review_dispute` | `feedback` | Comment captured on a reviewed PR, or dispute filed in the app |
| `run_code_review` (existing) | `agent` | Triage routed `reassess`; `trigger_source = 'dispute_reassessment'`, deduped on dispute id |
| `reply_code_review_dispute` | `feedback` | Triage routed `policy_signal_only` / `answer_only`, or a reassessment completed |
| `corroborate_code_review_dispute` | `feedback` | Reassessment settles, PR reaches terminal state, or window expires |
| `generate_code_review_policy_proposal` | `agent` | A dispute becomes proposal-eligible |
| `replay_code_review_policy_proposal` | `agent` | Proposal created; orchestrator-only replay over target + guard sets |
| `sample_code_review_approvals` | `feedback` | Scheduled weekly per org; frontier + random selection |
| `digest_code_review_insights` | `feedback` | Scheduled weekly per org |

Reassessment reuses the `run_code_review` handler unchanged; only request
orchestration in `internal/services/codereview` learns the new trigger source and
the trust gate.

New audit actions: `code_review_dispute.filed`, `code_review_dispute.reassessed`,
`code_review_dispute.adjudicated`, `code_review_approval_audit.reviewed`,
`code_review_policy_proposal.activated`, `code_review_policy_proposal.dismissed`.

## API Contract

All routes are org-scoped and follow existing auth conventions. Filing and
reading disputes is member-level; adjudication, spot-check verdicts, and proposal
decisions are admin-level.

```
POST   /api/v1/code-reviews/{session_id}/disputes
       body:    { "body": string,                        // required, free text
                  "contested_reason_codes": string[]? }  // optional hint; triage may override
       201 ->   CodeReviewDispute   // intake_status "pending"; triage runs async
       errors:  404 review not found · 422 empty body
       note:    No 409. Multiple distinct objections on one review are legitimate;
                only GitHub-sourced disputes dedupe, on comment id. Direction is
                inferred by triage, never supplied by the caller.

GET    /api/v1/code-reviews/{session_id}/disputes
       200 ->   { "data": CodeReviewDispute[] }   // routing, trust, reassessment linkage

PATCH  /api/v1/code-review-disputes/{id}            (admin)
       body:    { "corroboration_status": "upheld" | "rejected", "adjudication_note": string? }
       200 ->   CodeReviewDispute
       errors:  403 not an admin · 409 already adjudicated

GET    /api/v1/code-review-approval-audits          (admin)
       query:   status?, repository_id?
       200 ->   { "data": CodeReviewApprovalAudit[] }
       note:    `selection` is omitted from the response while status = "queued";
                the random control only works if the reviewer cannot see the arm.

POST   /api/v1/code-review-approval-audits/{id}/verdict   (admin)
       body:    { "verdict": "correct" | "should_not_have_approved", "note": string? }
       200 ->   { "audit": CodeReviewApprovalAudit, "dispute": CodeReviewDispute? }
       note:    A "should_not_have_approved" verdict creates an upheld dispute in
                the should_not_have_approved direction. A "correct" verdict adds
                the session to the guard set.

GET    /api/v1/code-review-insights
       query:   repository_id?, from?, to?, decision?, reason_code?, direction?
       200 ->   { "data": {
                    "decisions_by_reason":  [{ "reason_code","count","dispute_count",
                                               "qualifying_count","dispute_rate" }],
                    "decision_totals":      { "approved","comment_only","needs_human_review","blocked" },
                    "disputes_by_direction":{ "should_have_approved","should_not_have_approved" },
                    "reassessment_flip_rate_by_attempt": [{ "attempt","flips","runs" }],
                    "spot_check":           { "frontier_hit_rate","random_hit_rate","false_approval_rate" },
                    "median_decision_seconds": number,
                    "policy_versions":      [{ "policy_id","version","decision_mix","false_approval_rate" }]
                 } }

GET    /api/v1/code-review-policy-proposals
       query:   status?, repository_id?, direction?
       200 ->   { "data": CodeReviewPolicyProposal[] }   // includes replay results

POST   /api/v1/code-review-policy-proposals/{id}/activate      (admin)
       body:    { "proposed_changes": object? }   // optional edited delta
       200 ->   { "policy": CodeReviewPolicy, "proposal": CodeReviewPolicyProposal }
       errors:  409 not open / replay incomplete / base policy superseded
                422 delta fails policy validation
                403 delta touches a locked dimension

POST   /api/v1/code-review-policy-proposals/{id}/dismiss       (admin)
       body:    { "decision_note": string }
       200 ->   CodeReviewPolicyProposal
```

The GitHub path adds no route and parses no syntax. The existing `issue_comment`
and `pull_request_review_comment` handlers capture replies in the bot's
rolling-comment thread and comments mentioning the bot on any PR it reviewed,
evaluate eligibility via `evaluatePRFeedbackEligibility`, create the record with
`source = 'github_comment'` deduplicated on `github_delivery_id`, and enqueue
triage. Whether a captured comment is a dispute at all is decided by triage, not
by the handler.

Activation validates the delta against the same validation used by
`PUT /api/v1/code-review-policies`, then writes a new insert-only version. It
never patches a policy row in place, so approvals continue to point at the exact
version that produced them.

## Success Metrics

- **Share of non-approvals where someone objects.** The miscalibration signal
  that does not exist today. Expect it to rise when intake becomes free-text —
  that is the feature working, not the bot getting worse.
- **Triage accuracy**, sampled by hand. Objections wrongly recorded as
  `not_a_dispute` are the silent failure this document exists to prevent, and
  matter far more than the reverse error.
- **Frontier hit rate vs random hit rate.** If the two are equal, the frontier
  score is worthless. This is the only check on the sampler.
- **False approval rate**, from the random control arm — unbiased, and doc 112's
  headline safety metric, currently uncomputable.
- **Guard-set regression rate per activated proposal.** How often an activated
  change flipped decisions that were correct. The metric that says whether
  one-click activation is safe.
- **Reassessment flip rate by attempt number.** Flips on new information are the
  loop working; flips on unchanged inputs mean judgment-layer variance, which is
  a different bug and should be fixed rather than tuned around.
- **Median time from objection to a substantive reply**, and to a flipped
  decision where one occurs. The developer-facing latency this is judged on.
- **Proposal activation and dismissal rates**, split by `direction`. Persistent
  asymmetry means the loop is drifting one way.

## Open Questions

- What is the right weekly spot-check queue size, and what frontier/random split?
  Too large and nobody opens it; too small and the random arm never reaches
  significance.
- Should a proposal with any guard-set regression be blocked from one-click
  activation and require an explicit override, or is showing the number enough?
  Leaning toward blocking above a threshold, since the number is easy to skim past.
- How long should the guard set retain a member before it becomes stale? Codebases
  move, and a two-year-old decision may no longer be the right baseline.
- Should triage treat a dispute from someone other than the PR author as
  `reassess`-eligible on a wider set of reason codes, given the weaker bias?
- Does the reviewer-side epoch budget need to be policy-configurable, or is a
  fixed constant adequate given the marker should prevent the loop outright?
- Should `should_not_have_approved` disputes on already-merged PRs trigger any
  notification beyond the proposal, given the code is already in main?
