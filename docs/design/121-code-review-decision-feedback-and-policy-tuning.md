# Design: Code Review Decision Feedback And Policy Tuning

> **Status:** Partially Implemented | **Last reviewed:** 2026-08-04
>
> **Depends on:** [implemented/112-code-reviewer-bot-auto-approval.md](implemented/112-code-reviewer-bot-auto-approval.md), [backlog/11-review-feedback-loop.md](backlog/11-review-feedback-loop.md), [future/116-automatic-pr-feedback-follow-through.md](future/116-automatic-pr-feedback-follow-through.md), [future/16-ai-agent-evals.md](future/16-ai-agent-evals.md)

## Implementation status

Phase 0 is implemented: blockers are grouped by remedy,
policy-controlled blockers link to settings, a sole blocker is revision-bound,
platform review failures are separated from code risk, and advisory findings are
inspectable in a collapsed non-blocking section. Stable head-bound deterministic
failures publish provisionally before agent fan-out while mutable GitHub-state gates
remain terminal-only. The optional `stop_after_deterministic_failure` setting ends
the first attempt without agent spend; an explicit same-head rerequest after that
early stop forces the full review. Insights reports early stops, avoided agent runs,
and that full-review demand.

The PR 3 Phase 1A runtime is implemented: in-app reconsideration, GitHub mention and inline-thread intake, evidence-grounded answers, durable acknowledgement/timeline states, authorization snapshots, versioned semantic cooldown dedupe, loop guards, latest-revision reassessment, admin promotion of untrusted evidence, the member escalation route, and the flat admin adjudication list. Reassessment workers use an operator kill switch and platform-wide active-work ceiling.

Phase 1B(ii)'s volume-gated ranked queue and expanded Insights are implemented. Completed decisions project into an org-scoped outcome table, durable GitHub events add independent-human and terminal PR facts, daily reconciliation repairs missing or stale projections, and the queue persists explainable signal snapshots while remaining chronological below the sustained-volume trigger. Admin Insights reports objection directions and kinds, per-policy decision mix, reason distributions with actual-versus-limit observations, reassessment flips and spend, resolution time, and projection freshness.

Two cross-cutting contracts remain design gaps rather than Phase 1A implementation claims: the repository code-review-owner identity/delivery model needed for targeted notifications does not exist yet, and the retention section does not specify a retention duration, scanner behavior, or tombstone trigger. The current implementation bounds and isolates untrusted text, preserves immutable source versions, replies on GitHub, and surfaces escalation in the admin queue, but it does not invent notification recipients or destructive retention behavior without those decisions.

Phase 1B(i) remains deferred: approval spot-checks, guard-set curation, and false-approval estimation. Targeted weekly digest delivery remains blocked on the repository-owner identity/channel contract above. Phase 2 policy proposal generation, replay, activation, and guard-set workflows also remain deferred.

## Summary

Today developers cannot challenge a Code Reviewer decision, and admins cannot see
whether policy is calibrated. This design adds a feedback loop:

- **Disagree in plain English.** Reply to the bot like you'd reply to a person —
  no command to remember, no form to fill in.
- **Both directions.** "This should have been approved" and "this should never
  have been approved" are both first-class.
- **Rerun when a rerun would help.** If your objection contains something the
  review didn't know, it runs again immediately. If it doesn't, the bot says why
  rather than going quiet.
- **Spot-check approvals.** Nobody complains about a PR that sailed through, so
  sample the risky ones and ask a human.
- **One rule for changing policy: an admin agreed.** Every other signal ranks the
  admin's queue rather than deciding anything.
- **Propose policy changes when volume warrants it.** Replay prompt edits before
  activation; until then admins tune manually from Insights.

Doc 112 deferred both "automatic policy learning" and "aggregate insights." This
covers both, because insights without a way to disagree only measure the bot's
opinion of itself.

## Problem

### What already works

- Every non-approval has a typed reason code (`CodeReviewRiskReasonCode`), and
  numeric ones carry the actual value and the limit.
- The rolling PR comment shows **Why**, **Policy blockers**, **Blocking
  findings**, and **Next steps**.
- Policies are versioned and insert-only, so every decision points at the exact
  policy that produced it.

### Why people can't tell why

1. Thresholds, model findings, and human-review requirements look identical.
2. The result does not show whether one blocker or many prevented approval.
3. Advisory findings are reduced to a count.
4. Platform failures such as `context_unavailable` look like user risk.
5. Cheap deterministic checks run after the full agent fan-out.

### Why admins can't fix it either

There is no aggregate decision view. `policy-events` records configuration activity,
not outcomes, so admins cannot answer whether policy is too strict or unsafe.

### Two traps

- Complaints skew toward blocked authors and would bias policy toward loosening.
- Bad approvals rarely generate complaints and require proactive sampling.

A dispute is a claim, not evidence.

## What Counts As Evidence

**One rule: a policy owner read the dispute and agreed with it.** Nothing else
changes policy — not a rerun that flipped, not a clean merge, not a revert three
weeks later, not a complaint that reads convincingly.

Once Phase 2 is enabled, one upheld dispute may draft one proposal. Admin attention
intentionally limits throughput; spot-check verdicts use the same evidence gate.

### Why "an independent human contradicted the bot" isn't a second rule

A teammate approval may be a careful escalation or a rubber stamp. Without branch
protection context, it is useful for ranking but not policy evidence.

### Signals rank the queue instead

Admin attention is the bottleneck, so every other signal earns its keep by deciding
what an admin sees first. These order the queue and decide nothing:

| Signal | Why it raises rank |
| --- | --- |
| An independent human approved a PR we blocked, or left a blocking review on one we approved | Weak as proof, decent as a pointer. `ReviewContext.BlockingHumanReviews` already counts the second case |
| The rerun ran and didn't flip | The disputer still disagrees and the cheap fix didn't work. A live standoff is worth a human's time |
| The filer isn't the PR author | Nothing at stake in the complaint, so less of the bias the whole design is guarding against |
| Repeat disputes against the same reason code in the same repo | One person may be wrong; five in a fortnight is a pattern, and it's the closest thing to clustering we need |

Exclude reverts and incidents because attribution is weak. Also exclude a rerun that
flipped: title or body may have changed without a new SHA, making the flip evidence
that reassessment worked, not that the original decision was wrong.

### Who gets to influence policy

Capturing a complaint and acting on it are different privileges. Reuse the
**provenance and trust half** of `evaluatePRFeedbackEligibility`
(`internal/services/github/pr_feedback_policy.go`) — self-authored detection, bot
detection, the hidden-marker check, and the `OWNER` / `MEMBER` / `COLLABORATOR`
association tiers — factored out so both callers share one implementation.

Do **not** reuse its mode gates. `human_mode_off` and `mention_required` come from
doc 116's PR-feedback settings, which are a different product surface: an org that
never turned on feedback follow-through would silently drop every dispute, with no
acknowledgement, because of a setting they never associated with code review. Its
`untrusted_human_without_mention` branch also contradicts the table below, which
records and answers outside contributors. **Dispute intake is enabled by the
code-review policy that produced the decision**, not by the feedback-bot settings.

| Who | Recorded | Answered | Can trigger a rerun | Queued for adjudication |
| --- | --- | --- | --- | --- |
| Trusted pull request author (`OWNER` / `MEMBER` / `COLLABORATOR`, or a human who can comment on a private repo) | yes | yes | yes | yes |
| Other trusted humans | yes | yes | **no in Phase 1A** | yes |
| Everyone else, including fork contributors | yes | yes | **no** | **not by default** |
| Bots | no | no | no | no |

Outside contributors are recorded, answered, and shown as untrusted in Insights.
Admins may promote them, but they cannot spend org compute by default. The reply
says that plainly — *"external contributors can't trigger a re-review; ask a
maintainer to re-request one"* — because on public repositories this is the person
most confused by a block, and a clear no is worth more than a vague yes.

Compute current trust from observed facts and current org settings, with a
per-dispute admin override in Phase 1A. A durable per-person override is deferred
until repeated manual promotions prove it is needed; this design has no per-person
schema or API contract. Snapshot every authorization decision and its inputs,
settings version, evaluator version, override, and time so later rule changes do not
rewrite history.

Bots can't file at all. `evaluatePRFeedbackEligibility` already blocks self-authored
comments, and a bot arguing with approval policy has no good use case.

## Approach

Four ways to build this. We're recommending the third.

| Option | What it does | Why not / why |
| --- | --- | --- |
| **A. Insights only** | Aggregate tab, admins tune by hand | Cheap and a prerequisite for the rest, but only measures what the bot already believes and gives the frustrated developer nowhere to go. Ship it *inside* C, not instead of it |
| **B. Disputes + numeric auto-tuner** | Same intake, mechanical threshold changes only | Tunes what matters least. Our deterministic gates are lenient on purpose, so most non-approvals come from judgment reasons living in prompt text, which arithmetic can't fix |
| **C. Disputes + rerun + per-dispute proposals** | Recommended; see below | Tunes the prompts, which drive outcomes. Two speeds: a wrong judgment call fixed in minutes by a rerun, a wrong rule in the next proposal |
| **D. Policy-as-code, proposals as PRs** | Policy in the repo, changes arrive as PRs | Attractive later. Today it forks the source of truth from the versioned DB model, can't express org defaults, and makes prompt iteration need a merge — and that's the part that must be fast |

**Not doing: fully autonomous tuning.** Approval policy is a safety control. A
system that relaxes its own controls under pressure from blocked authors is unsound
however good the rollback is, and "reverted within 7 days" is far too slow when the
failure mode is unreviewed code reaching main.

Option C costs more, depends on conservative classification, requires replay for
prompt edits, and increases agent spend with dispute volume. That spend is
deliberately uncapped per tenant at launch and measured instead; see *Reruns*.

## Phase 0 — Make the decision readable

No new subsystem. Changes to `code_review_output.go`, the worker, and session detail.

1. **Group blockers by what would fix them.** Three labelled groups instead of
   one list: *Policy thresholds* (deep-link to the setting), *Review findings*
   (arguable), *Human judgment needed*. Derived from the existing reason codes.
2. **Say how close it was, without promising anything.** When there's one
   blocker: *"This is the only blocker as of `abc1234`."* Not "fix this and
   you're approved" — fixing it triggers a fresh review that may legitimately
   find something new, and a promise we can't keep is worse than no promise.
3. **Separate our failures from your risk.** `context_unavailable` and
   `orchestrator_synthesis_invalid` get their own rendering and leave the
   blocker list.
4. **Show advisory findings** in a collapsed `<details>` instead of a count.
   Still non-blocking, still no inline comments.
5. **Run cheap checks first without withholding review.** Publish deterministic
   failures immediately, then continue substantive review by default. Repositories
   may enable `stop_after_deterministic_failure`; Insights tracks savings and
   subsequent requests for full review.

   Early publication is a **provisional update to the rolling comment** — not a
   GitHub review event, and not a decision row. Only the terminal session write is
   a decision, so `code_review_decision_outcomes` still carries exactly one row per
   session and Insights does not double-count.

   Publish only gates that are **stable for a given head**:
   `files_limit_exceeded`, `lines_limit_exceeded`, `blocked_path`,
   `path_outside_scope`, `fork_ineligible`, `author_ineligible`. Hold
   `checks_failing`, `required_check_failing`, `branch_out_of_date`, and
   `head_changed` for the terminal decision — they can resolve while the review is
   still running, and a bot that blocks on a red check at 20 seconds and approves at
   60 has contradicted itself in public.

## Phase 1 — Capture, triage, rerun

### Capture

**GitHub (primary):** mention `@143-code-reviewer` in a flat PR comment or reply in
an inline review thread. **143:** use **Ask for reconsideration** on non-approvals or
**Report an unsafe approval** on approvals. Both open a free-text form tied to the
decision, with optional reason-code selection. The form explains that objections
cannot waive deterministic policy, identifies who can change the rule, and only
shows settings links to authorized viewers.

Capture on the generous side. Eligibility comes from the shared provenance/trust
check described above — not the feedback-bot mode gates; what a comment *means* is
triage's job, not the webhook handler's.

### Triage

`triage_code_review_dispute` runs `deterministicPRFeedbackTriage`, then one LLM pass
over the comment, decision, reason codes, diff summary, and surrounding context. It
classifies direction, contested reasons, new information, and route.

| Route | When | What happens |
| --- | --- | --- |
| `reassess` | Contains new information, or argues with a judgment call. Trusted author, blocked direction only | Rerun the review now |
| `policy_signal_only` | Argues with a threshold or path rule, or triage wasn't confident enough to rerun | A rerun would change nothing — the threshold evaluates the same way every time. Say so, name the setting and its value, link to it, record the dispute, and offer **Send this to a policy owner** |
| `answer_only` | It's a question, not a disagreement | Answer from the session evidence. Keep the captured source/version row for reply reconciliation and audit, but exclude it from policy influence and the adjudication queue |
| `not_a_dispute` | Chatter | Recorded as discarded, with a one-line acknowledgement carrying the override link. No influence |

`not_a_dispute` always receives a one-line acknowledgement with a link to file
explicitly in-app. That explicit action bypasses classification.

**Route to `reassess` only above a confidence floor.** One LLM pass decides whether
to spend agent compute and, on a blocked PR, whether an approval becomes reachable.
When `intake_confidence` falls below the floor, route to `policy_signal_only` with an
honest reply — *"we weren't sure what you were disputing; file explicitly to force a
re-review"* — rather than guessing. The expensive route fails closed, and the
explicit in-app action is always available to override it.

**`policy_signal_only` must not be a dead end.** Telling a developer "this is a
threshold, here's the setting, here's who can change it" and then doing nothing is
the low point of the whole flow: they took the trouble to explain themselves and got
a link they can't click. **Send this to a policy owner** routes the dispute with its
PR context into the owner's queue and digest and notifies them. Phase 1A records the
idempotent escalation demand signal and raises it in the admin queue; targeted
delivery remains blocked on the repository-owner identity and channel contract in
Open Questions. The click still doubles as the cleanest demand signal we have for
Phase 2's volume trigger — a threshold nobody escalates is a threshold nobody
actually minds.

A `should_not_have_approved` dispute never reruns. Doc 112 makes approval
monotonic and we're not touching that — automation must never walk back an
approval it already gave. That direction is record-and-propose only, which makes
it simpler than the blocked direction, not harder.

It does, however, **notify**. When a `should_not_have_approved` dispute or
spot-check verdict lands on a PR that already merged, the PR author and the
repository's code-review owners are told, with links to the decision and the
objection. If the bot approved something unsafe and it shipped, the people who own
that code hearing about it *is* the safety story — filing it silently as Phase 2
input would be the wrong trade.

### Reruns

A `reassess` uses the normal review path with trigger `dispute_reassessment`, keyed
by dispute id. Existing explicit-request idempotency and starter-job behavior apply.

**A reassessment reviews the latest GitHub revision, not `reviewed_head_sha`.** The
reviewed head remains immutable provenance for the decision being disputed, but it
is never an admission guard or rerun target. Admission fetches an authoritative PR
snapshot for semantic dedupe and the starter fetches it again on every attempt. If
commits land while an older assessment keeps the starter deferred, the next attempt
therefore follows them. The normal review worker still detects a head change during
an in-flight review and refuses to publish a stale decision; the ordinary
head-change event then requests a review of the newer revision. This keeps the
approval safety invariant while making reconsideration answer the current code.

The dispute text goes in as **untrusted evidence**, screened the same way PR
descriptions and diffs already are. It's a claim to check against the code, not
an instruction. "Approve this" is prompt injection, not information.

**No user-facing reassessment quotas.** No per-PR budget, no per-user daily rate, no
per-org monthly ceiling. A quota on a feature nobody has used yet is a guess about
demand priced as a restriction, and the first thing a frustrated developer would hit
is a wall saying they've run out of arguing. Insights measures reassessment spend
from day one; if the cost turns out to matter, we'll have real numbers to set a limit
from instead of inventing one now.

What remains is **not** a cost control and stays:

- **Semantic dedupe** over the latest provider head, title/body, dispute evidence,
  and policy, with a
  short cooldown for semantically unchanged input. This is idempotency, not
  rationing: without it, three "please reconsider" comments produce three identical
  reruns and three identical replies on the same PR. Apply the same dedupe to
  empty-commit reviews.
- **Provider refresh at admission and starter execution**, which prevents a stale
  local PR mirror or a long queue delay from selecting an obsolete revision.
- **The bot-loop cycle budget** below, which stops two machines arguing forever.
- **An operator hard ceiling and kill switch**, platform-side and invisible in normal
  operation. This is not a product limit; it's the thing you reach for at 2am when an
  integration goes berserk, and its absence means the only mitigation is a deploy.

A dedupe or cooldown hit still records and answers the dispute — it says we already
answered this, not that you've spent your allowance.

Watch flip rate by attempt number: if attempt 2 flips as often as attempt 1 on
unchanged input, that's a judge problem, and with no quota absorbing it the fix has to
be the judge.

### Don't let the bots talk to each other

Reuse doc 116's two loop guards:

- Dispute replies carry the hidden marker doc 116 checks for
  (`prFeedbackHiddenMarker`), so follow-through skips them and the loop never
  starts.
- A cycle budget per epoch, mirroring `feedback_bot_epoch` /
  `feedback_bot_cycles_in_epoch` (`internal/db/pull_request_feedback.go`): any
  human comment resets the epoch and refills the budget; machine-only rounds spend
  it. Humans stay unlimited, machines don't. Markers get dropped in refactors, so
  this is the backstop.

### Keeping the PR quiet

Reruns update the rolling comment. Other routes get one inline reply or one flat PR
comment referencing the source and carrying the hidden marker. The rolling comment
shows a compact summary; full history lives in 143.

The session shows a durable timeline: received, triaging, reassessment started or
not applicable, decision changed or unchanged, queued for a policy owner, and
adjudicated/expired. GitHub and 143 notify the filer on terminal changes. Triage
delay, triage failure, deduped-as-already-answered, head moved since filing,
reassessment failure, and reply-publication failure each have explicit user-visible
states; reconciliation retries publication without repeating the reassessment.

Rank the queue after rerun completion, PR closure, window expiry, or supersession of
the dispute's base policy. Phase 1 ships the queue, Insights, and a weekly
policy-owner digest; admins still edit policy manually.

The digest leads with **what changed**, not with a to-do list: *"blocks up 14% this
week; three people disagreed with `lines_limit_exceeded` in `payments-api`."* An
admin opening a weekly email wants a diagnosis first and the next item second. All
the ranking work feeds that sentence rather than replacing it.

### Delivery sequence

Keep the first release narrower than the full machinery:

1. **Phase 0A:** readable decisions, advisory details, platform-failure separation,
   and immediate deterministic outcomes while substantive review continues.
2. **Phase 0B:** Insights with reason distributions and actual-versus-limit data,
   plus the **baseline objection rate** — how often a blocked author escalates by any
   means today (re-request, slash command, comment at the bot, or going to find a
   human). Phase 1 is justified by that number rather than assumed. Much of "I
   disagree" is really "I don't understand," and Phase 0A may retire most of it; if
   the rate collapses after 0A, Phase 1A is a smaller feature than this doc describes.
3. **Phase 1A:** in-app reconsideration, GitHub mention/inline intake, durable
   acknowledgement and timeline, semantic dedupe and loop guards, and reassessment.
   Adjudication-eligible disputes land in a flat, unranked list a human reads.
4. **Phase 1B(i) — spot-checks:** blinded approval sampling and the false-approval
   estimator. **Ships on approval volume, not dispute volume** — an org averaging
   **50+ auto-approvals a month** has a population worth sampling. Gating this on
   disputes would be a category error: the whole reason spot-checks exist is that bad
   approvals don't generate complaints, so waiting for complaints to justify them
   inverts the argument.
5. **Phase 1B(ii) — the ranked queue and digest.** Build when **10+
   adjudication-eligible disputes a month in a single org, sustained over two
   months**, prove the flat list has stopped working. Ranking exists to decide what a
   busy admin sees first; below that volume they see everything anyway, and the four
   signals in *Signals rank the queue instead* are guesses that a hundred real
   disputes will improve far more than argument will. Phase 1A already records
   `queue_signals` for every dispute, so the ranking arrives with history to
   calibrate against rather than starting cold.
6. **Phase 2:** proposal generation only after volume, relevant curated coverage,
   and repeated costly manual edits prove generation is the bottleneck.

Starting operating defaults are deliberately small and changeable: a 15-minute
cooldown for semantically unchanged inputs and six approval audits per org per week,
split evenly between frontier and uniform random sampling. The machine-only bot-loop
budget is a constant two cycles per human-reset epoch. Guard members expire from
gating after 180 days unless re-adjudicated, though expired members remain historical
evidence. Insights shows when these defaults bind; policy owners tune them from
observed demand and cost.

## Phase 2 — Propose changes

### Start it on a volume trigger, not a date

**Build it when all hold, sustained over two months:** at least **5 upheld disputes
a month**, a guard set of **30+ members** with a meaningful share adjudicated rather
than bootstrapped, and evidence that policy owners repeatedly make similar manual
prompt edits whose preparation and validation consume meaningful time. Phase 1
already collects the needed data and curated guard members. Do not cluster initially:
one upheld dispute produces one proposal; add clustering only if proposal volume
becomes a problem.

### Proposals change prompts, and only prompts

A session proposes a scoped add/remove edit to one named acceptable-risk or
description-prompt section. Dispute text is untrusted evidence. Thresholds, paths,
categories, checks, and author eligibility are not generated; Insights shows their
distributions and links admins to manual settings.

### Replay is what makes review real

Replay every proposal against the triggering **target** and a **guard set** of
held-out decisions, weighted toward approvals near the frontier.

Replay cost depends on what the edited section influenced. Every editable section
is classified in a registry as one of:

- **Orchestrator-replayable** — rerun only orchestration against stored reviewer
  output.
- **Description-reassessment required** — rerun description assessment and
  orchestration.
- **Reviewer-rerun required** — cached reviewer output was conditioned on the old
  text, so a full reviewer replay is required.
- **Not replayable** — show the edit for human review without a causal replay claim.

The acceptable-risk rubric is normally orchestrator-replayable from stored reviewer
results. Never use cached output as a counterfactual when edited text influenced the
reviewers.

**Unregistered sections default to *not replayable*, and a test enumerates every
renderable section against the registry.** The classification is a causality claim
that nothing else enforces: add a prompt section, forget to register it, and a
proposal will claim orchestrator-replayable evidence for an edit that actually
conditioned the reviewers. Fail closed and let the test catch the omission.

Persist immutable replay provenance: model/settings, rendered prompt, parser and
implementation versions, policy, input hashes, reviewer records/outputs, and
guard-member versions.

**Replay measures candidate effect against a replayed baseline, not only history.**
Run the unchanged prompt beside the candidate. Mark non-reproduced baseline cases
unstable and exclude them from headline flips. Use deterministic settings where
possible; otherwise repeat runs and show distributions.

Bootstrap from approvals merged without blocking human review, then replace them
with spot-check/admin judgments. Bootstrapped labels may hide bad approvals, so
regressions remain advisory until there are 30+ curated members. Gating also
requires relevant repository/language, risk/path, direction, decision balance,
policy-family, and recency coverage. Missing coverage requires explicit heightened
review. The set also seeds doc 16's eval corpus.

**Crossing 30 is not permanent.** Members expire from gating after 180 days, while
curated members accrue only from spot-check `correct` verdicts and admin
adjudications — six audits per org per week, of which only some verdict `correct`,
and only some of those are *relevant* to a given proposal's coverage dimensions. A
set that crossed the threshold can decay back below it. When it does, regressions
revert to advisory and open proposals are re-flagged rather than silently
grandfathered on the coverage they had at creation. If Insights shows relevant
coverage sitting under threshold for a quarter, the audit rate is too low for the
guarantee we're claiming — raise sampling rather than lower the bar.

### Spot-checking approvals

A scheduled job queues frontier approvals using:

- **Margin** — how near the limits: 5 of 5 files, 296 of 300 lines.
- **Thin judgment** — approved despite P2 findings, low-confidence clean verdicts,
  or reviewer disagreement that didn't quite block.
- **Novelty** — first approval touching a path/category not previously approved;
  weight this highest.

Mix in a blinded uniform random sample. Hide arm, score, and factors while queued;
persist population, inclusion probability, stratum, and run, including skipped and
expired audits. Report **estimated false-approval rate among adjudicated random
samples** with sample size, response rate, and confidence interval. Compare random
and frontier hit rates to validate ranking.

**Budget 20–30 minutes per policy owner per week and design against that number.**
Six audits plus queued disputes, each needing a diff and a policy version read, is
not a five-minute chore, and understating the cost is how a feature like this quietly
dies in month three. The queue is capped rather than unbounded: owners see the top N
by `queue_priority`, the remainder expire, and Insights reports what expired so a
growing tail is visible instead of silent. Policy-owner minutes per resolution is
already half the north-star pair — this is the budget it's measured against.

Cards show the decision/policy, disputed text, evidence, rerun changes, and ranking
signals, with **Uphold**, **Reject**, and **Need more context** plus assignment,
snooze, expiry, age, and ownership. A card whose base policy is no longer active is
marked **policy changed since filing** and de-ranked: upholding it drafts a proposal
against a superseded base, which activation rejects with `409` anyway, so the admin
shouldn't spend attention there before re-confirming the dispute still applies.
Digests open the next item. Label/audit self-adjudication; high-risk loosening may
require a second owner.

A “shouldn't have been approved” verdict creates an upheld dispute; “correct” adds
a curated guard member.

### Guardrails

- Proposals never apply themselves.
- They can never touch approval mode, fork eligibility, prompt-injection
  handling, agent roster, or the bot's own policy paths.
- Every activation is attributed to a user, audit-logged, and reversible by
  activating the previous version — policies are insert-only, so revert is one
  click.
- Insights tracks decision mix and the estimated false-approval rate per policy
  version, with sample size and uncertainty, so a bad prompt change can surface
  through leading evidence rather than waiting for an incident.

## Database Schema

Unless a table explicitly names another primary key, every table below carries
`id uuid PRIMARY KEY`, `org_id uuid NOT NULL REFERENCES organizations(id)`, and
`created_at timestamptz NOT NULL DEFAULT now()`. Tenancy is `org_id` throughout.
Independent foreign keys do not by themselves prove that a session, PR,
repository, policy, dispute, and organization belong together: store methods take
`orgID`, every query filters by it, and create/update transactions validate all
linked ownership under the same organization. Use composite ownership constraints
where the existing schema makes them practical. Only distinctive columns are
listed.

**`code_review_decision_disputes`** — one row per objection.

| Column | Type | Notes |
| --- | --- | --- |
| `session_id`, `pull_request_id`, `repository_id`, `policy_id` | uuid NOT NULL | FKs; deletion follows the explicit retention/tombstone policy below rather than blind cascade |
| `reviewed_head_sha`, `decision` | text NOT NULL | Immutable provenance for what was disputed; the SHA is not the reassessment target |
| `direction` | text | Nullable until triage; then `should_have_approved` \| `should_not_have_approved` |
| `filed_by_user_id`, `filed_by_login`, `author_association`, `author_is_pr_author` | uuid / text / bool | Filer identity and association observed at intake |
| `repository_visibility`, `membership_evidence` | text / jsonb | Raw authorization inputs observed at intake |
| `trust_override` | boolean | Admin escape hatch. NULL means "use the org rule" |
| `source` | text NOT NULL | `github_comment` \| `app_ui` \| `api` \| `spot_check` |
| `github_comment_id`, `github_thread_root_comment_id`, `reply_comment_id` | bigint | Source identity, inline threading where available, and the one reply |
| `source_body_hash`, `source_version` | text / integer | Immutable source version that triage consumed |
| `body` | text NOT NULL | Verbatim. No syntax is parsed from it |
| `contested_reason_codes` | text[] | Inferred by triage |
| `dispute_kind` | text | **Free text from triage, no constraint.** See below |
| `asserts_new_information` | boolean | Nullable until triage; drives the `reassess` route |
| `routing` | text | `reassess` \| `policy_signal_only` \| `answer_only` \| `not_a_dispute` |
| `intake_status`, `intake_confidence` | text | `pending` \| `triaged` \| `discarded` \| `failed`; confidence is nullable until triage |
| `reassessment_session_id`, `reassessment_decision`, `reassessment_flipped` | uuid / text / bool | Rerun linkage |
| `semantic_input_hash_at_filing`, `semantic_input_hash_at_rerun` | text | Both hashes, so a reader can tell a flip caused by changed input from an unstable judge. Makes the "exclude flips as evidence" rule auditable rather than asserted |
| `adjudication_status` | text | NULL when not adjudication-eligible; otherwise `pending` \| `upheld` \| `rejected` \| `expired` \| `needs_context`. `upheld` is the only status eligible to draft a proposal when Phase 2 is enabled |
| `adjudicated_by_user_id`, `adjudicated_at`, `adjudication_note` | uuid / timestamptz / text | Who decided, and why |
| `policy_owner_active_seconds` | integer | Optional client-measured active queue interaction for a completed adjudication, bounded to one hour |
| `escalated_at`, `escalated_by_user_id` | timestamptz / uuid | Set by **Send this to a policy owner**. Also a ranking signal: a filer who bothered to escalate cares more than one who didn't |
| `queue_signals` | jsonb | The ranking signals observed on this dispute, with their inputs |
| `queue_priority` | numeric | Derived from `queue_signals`; orders the admin queue and decides nothing |

`dispute_kind` is **deliberately unconstrained**. An enum here would be a dozen
guesses about a distribution we haven't observed, frozen into the least flexible part
of the system — categories we invented rather than ones we saw. Triage writes a short
slug, Insights charts the frequencies, and the values that turn out to be real get
promoted to a constraint later. `direction` and `contested_reason_codes` stay
structured; those we actually know.

Unconstrained is not unbounded. Normalize on write — lowercase, trim, collapse
whitespace and separators — and give triage the slugs already seen in this org so it
reuses a close match instead of minting a synonym. Without that, Insights charts four
hundred near-duplicates and the promotion step never gets a signal to act on.

State-dependent checks require classified fields after `triaged`, require routing
and direction for an adjudication-eligible dispute, and require adjudicator/time for
terminal adjudication. `answer_only`, `not_a_dispute`, failed intake, and untrusted
items not explicitly promoted have NULL adjudication status and cannot enter the
queue.

**`code_review_dispute_authorizations`** — immutable action-level authorization
decisions. `dispute_id`, `action` (`rerun` | `queue_influence` | `admin_promotion`),
`trusted`, observed-input snapshot, settings/policy version, evaluator version,
override provenance, decision reason, and `decided_at`. A dispute may encounter more
than one gate; current trust remains derived separately and never rewrites these
events.

**`code_review_dispute_escalations`** — one immutable demand-signal row per
`(org_id, dispute_id, user_id)`, with the bounded member note and timestamp. The
first escalation is also projected onto `escalated_at` / `escalated_by_user_id` on
the dispute for queue reads; the ledger makes API retries idempotent per member
without discarding escalation interest from additional members.

GitHub deliveries use the existing immutable webhook-ingress ledger: one delivery
record per delivery id, idempotent processing per delivery, and one source object
identity per GitHub comment id. Comment edits create a new source version/body hash
and never overwrite which version produced an earlier triage or rerun. Redelivery
of an old version is a no-op. An edit after reassessment does not trigger another
reassessment without a new explicit mention/action, which is a new semantic input and
therefore a new admission row.

Indexes: unique on `(org_id, github_comment_id, source_version)` where the comment
id is non-NULL — deliberately *not* unique per `(session, filer)`, since one person
may raise two genuinely different objections. Partial index on `intake_status =
'pending'`; partial on `(org_id, repository_id, queue_priority DESC)` where
`adjudication_status = 'pending'`, to build the admin queue; partial on `(org_id,
adjudicated_at DESC)` where `adjudication_status = 'upheld'`, to find disputes
awaiting a proposal.

**`code_review_dispute_queue_snapshots`** — short-lived, org-scoped materialized
queue orderings used only for pagination. The first page stores dispute identities
at monotonically increasing positions ordered by `(queue_priority, created_at, id)`;
the opaque cursor carries the snapshot identity and last returned position. Later
rank recomputation therefore cannot skip or duplicate a different dispute across a
cursor boundary. Snapshots expire after one hour and stale rows are cleaned when a
new paging session starts and by the daily per-organization retention job.

**`code_review_policy_proposals`** — one row per proposal. `repository_id`,
`base_policy_id`, and `source_dispute_id` (uuid; exactly one, no clustering);
`direction` (`loosen` | `tighten`); `origin` (`agent_session` | `manual`) with
`generator_session_id`; `status` (`replaying` | `open` | `activated` | `dismissed` |
`superseded` | `expired`); `current_revision_id` points to an immutable structured,
validated prompt-section delta; `rationale` cites the dispute and review;
`replay_status`, `replay_target_result jsonb`,
`replay_guard_result jsonb`, `replay_output_id`, `baseline_stable`,
`guard_regressions int`, and `guard_set_size` /
`guard_set_adjudicated int` so a reader can tell a meaningful regression count from a
provisional one; and the outcome columns `activated_policy_id`, `decided_by_user_id`,
`decided_at`, `decision_note`. There's no `change_kind` — every proposal is a prompt
edit. Unique index on `source_dispute_id` so retries cannot draft a second proposal
for the same upheld dispute; plus `(org_id, status, created_at DESC)`.

**`code_review_policy_proposal_revisions`** — immutable edits to a proposal.
`proposal_id`, `revision`, `proposed_changes jsonb NOT NULL`, `changes_hash`,
`created_by_user_id`, replay status/output linkage, and supersession metadata.
Unique `(proposal_id, revision)` and `changes_hash`; revising a proposal inserts a
new row and advances `current_revision_id` rather than overwriting the delta that an
earlier replay evaluated.

**`code_review_policy_replay_outputs`** — immutable replay provenance.
`proposal_id`, `replay_kind`, model/version and inference settings, rendered prompt
or output reference, parser/schema and implementation versions, policy id,
title/body/head/diff hashes, reviewer-output record references, guard-member
versions, baseline/candidate repetitions, and raw structured results. Large prompt
and output bodies use the existing bounded output path.

**`code_review_approval_audits`** — the spot-check queue. `session_id`,
`repository_id`, `pull_request_id`, and `policy_id` identify the approval.
`selection` (`frontier` | `random`), `frontier_score`, and `frontier_factors` stay
hidden while queued.
`sampling_run_id`, `eligible_population`, `inclusion_probability`, and
`sampling_stratum` make the estimator auditable. `status` (`queued` | `reviewed` |
`expired` | `skipped`) and `verdict` (`correct` |
`should_not_have_approved`) carry the outcome, with `resulting_dispute_id`,
`reviewed_by_user_id`, `reviewed_at`, and `note`. Unique index on
`(org_id, session_id)` — composite so tenancy is structural rather than a query
convention; partial on `(org_id, status, created_at)` where queued.

**`code_review_guard_set_members`** — held-out decisions for replay.
`session_id` (unique where active), `repository_id`, `expected_decision`,
`added_by` (`bootstrap` \| `spot_check` \| `admin`), `active boolean`,
`version`, `adjudicated_at`, `expires_at`, language/risk/path coverage tags, and
the policy-family/version against which the judgment was made.

**`code_review_decision_outcomes`** — denormalized per-decision facts, keyed by
`(org_id, session_id)` as a composite PK so Insights and queue ranking don't
re-derive PR history on every read, and so the tenancy invariant is enforced by the
key rather than by every query remembering to filter. Carries `decision`, `reason_codes text[]` (GIN
indexed), `merged`, `merged_at`, `independent_approver_login`,
`independent_blocking_review_login`, `human_review_comment_count`, `terminal`,
`lifecycle_observed_at`, `observed_until`, and `projection_updated_at`.
`lifecycle_observed_at` orders provider close/reopen facts independently from the
aggregate `observed_until` projection-freshness watermark. Reverts and incidents
remain outside this projection because the design does not use them even for
ranking.

**`code_review_pull_request_lifecycle_observations`** — the latest durable
provider lifecycle fact per `(org_id, pull_request_id)`. Close, merge, and reopen
events upsert this row before projecting any completed review sessions, so an
event remains available when it arrives before its decision outcome exists.

Durable GitHub events update the projection idempotently; dismissal and late reviews
may revise non-terminal facts. Periodic reconciliation repairs recent/non-terminal
PRs and the same projector handles backfill. Insights shows freshness, ranking
ignores stale absence, and independence uses membership observed at event time.

**`code_review_reassessment_admissions`** — one immutable row per dispute
admission decision, for dedupe and observability rather than tenant rationing. Carries
`dispute_id`, `pull_request_id`, `repository_id`, `user_id`, `semantic_input_hash`,
`status` (`admitted` | `deduped` | `denied`), denial reason, and timestamps. It is
unique on `(org_id, dispute_id)` so retries reuse the row. A rolling index on
`(org_id, pull_request_id, semantic_input_hash, created_at DESC)` supports the
15-minute semantic cooldown. Admission serializes the cooldown check and the
operator-wide active-work ceiling under one platform advisory lock, then records the
admission and enqueues through the same transaction/outbox boundary.

There are no usage counters, no reservation/release lifecycle, and no per-tenant
budget rows, because there are no quotas to enforce. Reassessment spend is a
**reporting fact** in Insights; the operator kill switch and hard ceiling live in
platform configuration and are checked here without a per-tenant ledger.

**Changes to existing tables:**

```sql
-- New trigger source; models gains CodeReviewTriggerSourceDisputeReassessment.
ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT chk_code_review_session_metadata_trigger_source,
    ADD CONSTRAINT chk_code_review_session_metadata_trigger_source
        CHECK (trigger_source IN ('app_reviewer','alias_reviewer','team_reviewer',
                                  'slash_command','auto_policy','dispute_reassessment'));

-- Risk reasons are only recorded when a gate FAILS, so an approval leaves no
-- record of how close it came. Frontier margin scoring needs these.
ALTER TABLE code_review_session_metadata
    ADD COLUMN triggering_dispute_id uuid,
    ADD COLUMN files_changed int,
    ADD COLUMN lines_changed int,
    ADD CONSTRAINT fk_code_review_metadata_triggering_dispute_org
        FOREIGN KEY (org_id, triggering_dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id)
        ON DELETE SET NULL (triggering_dispute_id);

-- Bot-loop cycle budget, mirroring the doc 116 epoch mechanic. This is a loop
-- guard, not a cost cap: it bounds machine-only rounds, and any human comment
-- resets it. There is no per-PR reassessment quota.
ALTER TABLE pull_requests
    ADD COLUMN code_review_dispute_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN code_review_dispute_cycles_in_epoch integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT chk_pr_code_review_dispute_cycles
        CHECK (code_review_dispute_cycles_in_epoch >= 0);
```

The production rollout installs the final `pull_requests` check constraint as
`NOT VALID` in migration `000281`, then validates it in migration `000282`.
This keeps the `ACCESS EXCLUSIVE` portion metadata-only and performs the table
scan under PostgreSQL's weaker validation lock. The first `000281` production
attempt timed out waiting for the hot table and rolled its transaction back;
deploys therefore allowlist exactly dirty version 281 for rewind-and-retry via
`migrate repair-known-dirty`.

Versioned org/repository policy settings gain `stop_after_deterministic_failure` and
the semantic-dedupe cooldown. They gain **no reassessment quotas** — no per-PR
budget, no per-user rate, no per-org ceiling. The operator-wide hard ceiling and kill
switch remain platform configuration, not tenant-editable policy, and being
platform-side they never surface to a user as a quota.

**Jobs**, on the existing queue:

| Job | Queue | Fires when |
| --- | --- | --- |
| `triage_code_review_dispute` | `feedback` | A comment is captured, or a dispute is filed in-app |
| `start_code_review_reassessment` | `agent` | Triage routed `reassess`; creates a linked session and dispatches the existing review path |
| `reply_code_review_dispute` | `feedback` | Triage routed `policy_signal_only` / `answer_only` / `not_a_dispute`, or a rerun finished |
| `rank_code_review_dispute` | `feedback` | Rerun settles, PR closes, the window expires, or the dispute's base policy is superseded. Recomputes `queue_signals` and `queue_priority` |
| `generate_code_review_policy_proposal` | `agent` | An admin upholds a dispute |
| `replay_code_review_policy_proposal` | `agent` | Proposal created |
| `sample_code_review_approvals` | `feedback` | Weekly per org |
| `digest_code_review_insights` | `feedback` | Weekly per org |
| `reconcile_code_review_decision_outcomes` | `feedback` | Periodically repairs recent/non-terminal GitHub outcome projections |

Reruns reuse the existing `run_code_review` handler unchanged; request orchestration
learns the new trigger source, authorization snapshot, head guard, and semantic
dedupe with its cooldown. Session and reassessment counts remain reporting facts, not
admission controls. Proposal-generation jobs are feature-gated until the Phase 2
volume, curated-coverage, and repeated-manual-edit conditions hold; upholding a
dispute before then leaves it visible for manual tuning without enqueueing a
generator.

**Audit actions:** `code_review_dispute.filed`, `.reassessed`, `.adjudicated`,
`code_review_approval_audit.reviewed`,
`code_review_policy_proposal.activated`, `.dismissed`.

## API Contract

Org-scoped, existing auth conventions. Filing and reading a session's own disputes
are member-level; aggregate Insights, the cross-session queue, adjudication,
spot-check verdicts, trust overrides, and proposal decisions are admin-level. Every
list uses the standard `{data, meta: {next_cursor}}` cursor contract. Bodies are
trimmed, non-empty, valid UTF-8, and bounded by the normal request limit plus a
smaller dispute-body limit.

| Route | Body / query | Returns |
| --- | --- | --- |
| `POST /api/v1/code-reviews/{session_id}/disputes` | `{ body: string, contested_reason_codes?: string[] }` | `201` dispute with `intake_status: "pending"`. `422` if body empty, `404` if no such review. **No 409** — several distinct objections on one review are legitimate; GitHub source versions dedupe through the ingress ledger. Direction is inferred, never supplied |
| `GET /api/v1/code-reviews/{session_id}/disputes` | `cursor?` | Disputes with routing, trust, and rerun linkage |
| `POST /api/v1/code-review-disputes/{id}/escalate` | `{ note?: string }` | Member-level. Records an idempotent escalation per `(dispute, user)` and raises the `policy_signal_only` dispute, with PR context, in the admin queue. Targeted owner notification/digest delivery starts only after the repository-owner identity/channel contract is settled; `409` if the dispute isn't in a route where escalation is meaningful |
| `GET /api/v1/code-review-disputes` *(admin)* | `adjudication_status?`, `repository_id?`, `direction?`, `cursor?` | The adjudication queue, materialized in stable `(queue_priority, created_at, id)` order for each paging session, each dispute showing which signals raised it |
| `PATCH /api/v1/code-review-disputes/{id}` *(admin)* | `{ expected_version: int, adjudication_status?: "upheld"\|"rejected"\|"needs_context", adjudication_note?, policy_owner_active_seconds?, trust_override? }` | CAS update; active seconds are the bounded client-measured queue interaction used for the owner-minutes metric, and trust override can promote an untrusted item without adjudicating it. `upheld` enqueues proposal generation only when Phase 2 is enabled. `409` if the supplied version lost a race |
| `GET /api/v1/code-review-approval-audits` *(admin)* | `status?`, `repository_id?`, `cursor?` | Spot-check queue. `selection`, `frontier_score`, and `frontier_factors` are **omitted while queued** — the random control only works if the reviewer cannot infer the arm |
| `POST /api/v1/code-review-approval-audits/{id}/verdict` *(admin)* | `{ expected_version: int, verdict: "correct"\|"should_not_have_approved", note? }` | CAS verdict; `should_not_have_approved` creates an already-upheld dispute; `correct` adds the session to the guard set |
| `GET /api/v1/code-review-insights` *(admin)* | `repository_id?`, `from?`, `to?`, `decision?`, `reason_code?`, `direction?` | Decisions and dispute rate by reason code, with actual-vs-limit distributions and an authorization-aware deep link to each setting; totals; disputes by direction; **`dispute_kind` frequencies**; flip rate by attempt; reassessment spend; estimated false-approval rate with sample size/response rate/confidence interval; median decision time; projection freshness; policy-owner minutes; per-policy-version decision mix |
| `GET /api/v1/code-review-policy-proposals` *(admin)* | `status?`, `repository_id?`, `direction?`, `cursor?` | Proposals including baseline stability, replay results, and relevant guard coverage |
| `POST /api/v1/code-review-policy-proposals/{id}/activate` *(admin)* | `{ expected_version: int, replayed_changes_hash: string, confirm_insufficient_guard_coverage?: boolean }` | New policy + proposal. Activation accepts only the exact replayed delta; `409` if not open, replay incomplete/unstable beyond policy, hash differs, required coverage confirmation or second high-risk-loosening confirmation is absent, or base policy is superseded; `422` invalid delta; `403` touches a locked dimension |
| `POST /api/v1/code-review-policy-proposals/{id}/revise` *(admin)* | `{ proposed_changes: object }` | Validates and stores a new proposal revision, invalidates prior replay, and enqueues replay. Editing can never activate an unreplayed delta |
| `POST /api/v1/code-review-policy-proposals/{id}/dismiss` *(admin)* | `{ expected_version: int, decision_note: string }` | CAS-updated proposal |

Existing comment handlers capture flat-comment mentions and inline-thread replies.
The durable ingress ledger handles delivery/source-version dedupe before triage;
handlers do not classify meaning or parse syntax. Once a comment is captured as a
code-review dispute, its normalized PR-feedback item is retained as `ignored` with
the dispute-specific reason and does not enter the separate automatic PR-feedback
follow-through worker; ordinary GitHub automation triggers still observe the event.

Activation atomically locks/CASes the proposal and replayed revision, validates the
active base policy and exact delta, deactivates the old policy, inserts the new
version, marks activation, and writes audit/outbox state. Concurrent activation
returns `409`; retries are idempotent. Adjudication and audit verdicts use the same
CAS/outbox pattern.

### Retention and sensitive text

Bound dispute/context inputs. Retain source text for the audit window, then allow
redaction to a tombstone while preserving provenance. Edits/deletions never rewrite
the version acted on; the UI and retention/deletion workflows reflect current
source state. Session/PR deletion follows explicit tombstone rules.

Treat all user/repository text as delimited untrusted data. Secret-scan before
long-term storage; bound, protect, export/delete, and never log raw outputs.

## Success Metrics

**North-star:** disputes reaching useful resolution without ad hoc escalation,
provided estimated false-approval rate does not worsen. Pair it with policy-owner
minutes per resolution.

- Objection and upheld-dispute rates; the latter gates Phase 2.
- Objection rate before and after Phase 0, to separate confusion from disagreement.
  If readable decisions retire most of it, Phase 1 is smaller than planned.
- Hand-sampled triage accuracy, weighted against false `not_a_dispute`.
- Frontier versus random hit rate.
- Estimated false-approval rate with inclusion probability, sample/response size,
  and confidence interval.
- Guard regressions with curated share and relevant coverage.
- Reassessment spend and flip rate by attempt/input change. With no quota in the
  way, spend per org and per dispute is the number that would justify introducing
  one later — and the distribution's tail matters more than its mean.
- Time to acknowledgement, reply, reassessment, and terminal state. Initial targets:
  acknowledgement within one minute and healthy-provider triage p95 under five.
- Queue age, expiry/non-response, digest conversion, and admin minutes.
- Repeat objections, full-review requests after early termination, and proposal
  activation/dismissal by direction.

## Open Questions

- What durable repository-level role defines a "code-review owner," and which in-app/email/Slack delivery channel is authoritative for escalation and merged-unsafe-approval notifications?
- What audit retention duration, secret-scanner disposition (reject, redact, or quarantine), and source-deletion/tombstone trigger apply to dispute text?
- After observing Phase 1 traffic, should trusted non-authors become
  `reassess`-eligible for additional judgment reason codes? They pass through the
  same latest-revision refresh and dedupe; the initial release keeps the narrower
  rule until demand is measured.
- Does uncapped reassessment spend concentrate in a small number of orgs or PRs? If
  the tail is thin, it stays uncapped indefinitely. If one org is generating most of
  it, the answer is more likely a conversation with that org than a global quota.

### Settled

- **Phase 1B carries a volume trigger, and splits in two.** Spot-checks ship on
  approval volume; the ranked queue and digest wait for sustained dispute volume.
  Ranking is a solution to a scarcity of admin attention, and until that scarcity is
  observed a flat list is both cheaper and more informative. Recorded in *Delivery
  sequence*.
- **Reassessment limits were cost caps, so they're gone.** Rationing a feature before
  anyone has used it prices a guess about demand as a restriction on the user, and
  the first person to hit it would be someone already frustrated enough to argue with
  a bot. Spend is measured rather than capped; dedupe, the head guard, the bot-loop
  budget, and an operator kill switch remain, because none of those are about cost.
  Recorded in *Reruns*.
