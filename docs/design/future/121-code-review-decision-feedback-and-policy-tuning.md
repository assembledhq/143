# Design: Code Review Decision Feedback And Policy Tuning

> **Status:** Not Started | **Last reviewed:** 2026-07-30
>
> **Depends on:** [../implemented/112-code-reviewer-bot-auto-approval.md](../implemented/112-code-reviewer-bot-auto-approval.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md), [../future/116-automatic-pr-feedback-follow-through.md](116-automatic-pr-feedback-follow-through.md), [../future/16-ai-agent-evals.md](16-ai-agent-evals.md)

## Summary

Today the Code Reviewer decides and nobody can argue with it. If the bot won't
approve your PR, your only option is to go find a human. That disagreement is the
best data we could possibly collect about whether our policy is right, and we
throw all of it away.

This adds a feedback loop:

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
- **Turn what we learn into policy changes** — edits to the review prompts, each
  showing what it would have changed before anyone clicks approve. That machinery
  waits for a volume trigger; until then admins tune by hand from Insights.

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

The raw material is there. The loop isn't.

### Why people can't tell why

1. **Every blocker looks the same.** "6 files, limit is 5" (a threshold you can
   edit), "possible auth bug" (a model's code judgment), and "needs architectural
   review" (a model saying ask a human) render as identical bullets. You can't
   tell which lever to pull, and "I don't know why" usually means "I don't know
   what to change or who to ask."
2. **No sense of how close you were.** One threshold away and blocked on eleven
   things read exactly alike.
3. **Advisory findings are just a number.** "4 non-blocking observations" without
   showing them reads as the bot hiding something.
4. **Our failures look like your fault.** `context_unavailable` means 143 broke,
   but it sits in the same blocker list as a real policy violation.
5. **Cheap checks run last.** `EvaluateCodeReviewRisk` runs after the full agent
   fan-out, so a PR that trips the file limit waits minutes to be told something
   we knew from one API call.

### Why admins can't fix it either

Doc 112 lists "top non-approval reasons" as a success metric. We can't compute
it. There's no aggregate view — the Code reviews page has only Reviews and
Configurations — and `policy-events` tracks which config sections admins opened,
not how decisions turned out. So an admin asking "is our policy too strict?" has
no data, and a developer who thinks the bot was wrong has nowhere to say so.

### Two traps

**Complaints alone will loosen policy forever.** The person most likely to complain
is the author whose PR was blocked — the most biased judge available — and they're
arguing with a safety control. Build the obvious version and you get a system that
relaxes its own rules whenever someone pushes back, with nobody acting in bad faith.

**Nobody complains about a bad approval.** False approvals are the dangerous error
and they're invisible: the PR merges and everyone moves on. A complaint-driven loop
can therefore only ever find evidence for loosening. That's why disputes go both
directions and why we sample approvals proactively.

A dispute is a claim, not a fact. The evidence rules below keep both traps closed.

## What Counts As Evidence

**One rule: a policy owner read the dispute and agreed with it.** Nothing else
changes policy — not a rerun that flipped, not a clean merge, not a revert three
weeks later, not a complaint that reads convincingly.

That's the highest bar available, and it's set there because nothing sits behind
it: there's no clustering step, so one upheld dispute drafts one proposal.
**Throughput is limited by admin attention on purpose.** The alternative is more
proposals than anyone reads properly, which turns the human approval step into a
rubber stamp. Spot-check verdicts are adjudications too, so the approval direction
feeds the same gate.

### Why "an independent human contradicted the bot" isn't a second rule

It's the tempting one — someone who is neither the author nor the disputer approved a
PR we blocked — and it doesn't hold up. When the bot blocks a PR and a teammate
clicks approve, that approval *is* the escalation path we told them to use. It can't
tell a careful second look from an unblock-my-colleague rubber stamp, and since we
don't track branch protection it can't rule out a self-merge with extra steps. On a
small team, "independent" approvers are the people sitting next to the author. That
objection sinks every automatic signal we could think of.

### Signals rank the queue instead

Admin attention is the bottleneck, so every other signal earns its keep by deciding
what an admin sees first. These order the queue and decide nothing:

| Signal | Why it raises rank |
| --- | --- |
| An independent human approved a PR we blocked, or left a blocking review on one we approved | Weak as proof, decent as a pointer. `ReviewContext.BlockingHumanReviews` already counts the second case |
| The rerun ran and didn't flip | The disputer still disagrees and the cheap fix didn't work. A live standoff is worth a human's time |
| The filer isn't the PR author | Nothing at stake in the complaint, so less of the bias the whole design is guarding against |
| Repeat disputes against the same reason code in the same repo | One person may be wrong; five in a fortnight is a pattern, and it's the closest thing to clustering we need |

Two things stay out entirely, even as ranking: **reverts and incidents** (attribution
is guesswork and it arrives months late) and **a rerun that flipped** — the PR
description can change without the head SHA changing, which is why doc 112 hashes
title and body separately, so someone reading the blocker, fixing their description,
and getting approved is the system working rather than proof the first call was wrong.

### Who gets to influence policy

Capturing a complaint and acting on it are different privileges. Reuse
`evaluatePRFeedbackEligibility` (`internal/services/github/pr_feedback_policy.go`)
rather than writing a new eligibility check.

| Who | Recorded | Answered | Can trigger a rerun | Queued for adjudication |
| --- | --- | --- | --- | --- |
| `OWNER` / `MEMBER` / `COLLABORATOR` | yes | yes | yes | yes |
| Anyone on a private repo | yes | yes | yes | yes |
| Everyone else, including fork contributors | yes | yes | **no** | **not by default** |
| Bots | no | no | no | no |

Outside contributors still get recorded, triaged, and answered — silently ignoring
them is exactly the failure we're fixing — and show up in Insights marked untrusted,
so an admin who notices one is consistently right can pull it into the queue
deliberately. They can't trigger reruns, because a rerun is the expensive action and
on a public repo an unlimited supply of anonymous people triggering unlimited agent
runs is a denial-of-service hole. Spending the org's compute is a form of influence.

**Trust is derived, not frozen.** Store `author_association` as observed and compute
`trusted` at read time from an org setting, with a per-person and per-dispute admin
override. The rule above is a default we fully expect to get wrong — the contractor
with fork-only access who is functionally on the team is a real case — and changing
a derivation is a deploy while changing a stored boolean is a backfill.

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

C's real costs: biggest build; a classifier decides what counts as a dispute, so it
must err toward recording; prompt changes are the hardest kind to review, which is
why replay is mandatory rather than nice-to-have; and agent spend scales with how
much people argue.

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
5. **Run cheap checks first.** Evaluate size, path, author, and fork eligibility
   from the file list *before* the agent fan-out. If the PR can't be approved
   either way, say so in seconds and skip the agent run.

This probably fixes most of "I'm not sure why" on its own, and everything later
depends on the grouping it introduces.

## Phase 1 — Capture, triage, rerun

### Capture

Two ways in, one record, no syntax anywhere. **On GitHub (primary):** reply in the
bot's comment thread, or mention `@143-code-reviewer` on any PR it reviewed —
"this is test-only, why is it blocked?" and "hold on, this shouldn't have been
auto-approved, it touches auth" both work. The `issue_comment` and
`pull_request_review_comment` webhooks are already wired up. **In 143
(secondary):** a "Disagree with this decision" button on the session opening a text
box, with reason codes shown for reference and tickable, though prose alone is
enough.

Capture on the generous side. Eligibility comes from
`evaluatePRFeedbackEligibility`; what a comment *means* is triage's job, not the
webhook handler's.

### Triage

Every captured comment runs `triage_code_review_dispute`:
`deterministicPRFeedbackTriage` first as a free filter for acknowledgements and
empty bodies, then one LLM pass over the comment, the decision it's answering,
that decision's reason codes, the diff summary, and the surrounding thread. It
works out whether this is actually a disagreement, which direction
(`should_have_approved` / `should_not_have_approved`), which reason codes it's
arguing with, whether it contains information the review didn't have, and what to
do about it.

| Route | When | What happens |
| --- | --- | --- |
| `reassess` | Contains new information, or argues with a judgment call. Trusted author, blocked direction only | Rerun the review now |
| `policy_signal_only` | Argues with a threshold or path rule | A rerun would change nothing — the threshold evaluates the same way every time. Say so, name the setting and its value, link to it, record the dispute |
| `answer_only` | It's a question, not a disagreement | Answer from the session evidence. No dispute record |
| `not_a_dispute` | Chatter | Recorded as discarded, with a one-line acknowledgement carrying the override link. No influence |

**A wrong `not_a_dispute` produces exactly the silence this doc exists to fix**, so
it doesn't get to be silent. The acknowledgement is one line — *"noted; if you meant
to challenge this decision, [say so here]"* — and the link opens the in-app form,
which files unconditionally, because an explicit human act never needs a classifier's
permission to count. Triage's worst failure goes from invisible to one click
recoverable.

A `should_not_have_approved` dispute never reruns. Doc 112 makes approval
monotonic and we're not touching that — automation must never walk back an
approval it already gave. That direction is record-and-propose only, which makes
it simpler than the blocked direction, not harder.

### Reruns

A `reassess` goes through the normal review path with a new trigger source
`dispute_reassessment`, keyed by dispute id. This needs no change to doc 112's
idempotency model: a new explicit request after a non-approval already creates a
fresh assessment at the same head SHA, and requests arriving mid-review are
already queued behind a starter job. A dispute is just another explicit request.

The dispute text goes in as **untrusted evidence**, screened the same way PR
descriptions and diffs already are. It's a claim to check against the code, not
an instruction. "Approve this" is prompt injection, not information.

**No rerun limit as a security control.** A limit wouldn't be one: doc 112 already
queues a fresh assessment on every new commit until approval, so `git commit
--allow-empty && git push` is an unlimited rerun path that exists today, and
rationing arguments while that stays open buys nothing. What actually bounds the
risk is that deterministic gates are re-checked from source on every rerun and can
never be waived by a dispute, untrusted authors can't trigger reruns at all, and
approval stays monotonic with each rerun its own immutable session.

**A soft spend budget, though, yes.** The real cost of unlimited reassessment is the
agent bill — a product problem, not a security one, so it gets a product answer:
after N reassessments on one PR, stop rerunning and reply with where the argument
goes next — *"this PR has used its reassessment budget; here's the setting, and
here's how to ask a human."* A per-org monthly ceiling behind it, surfaced in
Insights, both numbers org settings so nobody has to guess right the first time.
Degrading beats blocking because the person still gets an answer, and the outcome to
avoid is discovering any of this on an invoice.

If rerolling until the bot gives in is a worry, the fix isn't here — it's judgment
variance in doc 112, and the empty-commit path is the one to close. Watch flip rate
by attempt number: if attempt 2 flips as often as attempt 1 on unchanged input, fix
the judge rather than ration the reruns.

### Don't let the bots talk to each other

The bot's reply is a PR comment, and doc 116's follow-through reads PR comments —
including from bots, and an installed app on a private repo qualifies under two of
its rules. Reply → ingested → commit pushed → doc 112 queues a review → new reply.
That loop forms with everyone behaving correctly, and with no rerun limit nothing
counts it. Two guards, both already built for doc 116:

- Dispute replies carry the hidden marker doc 116 checks for
  (`prFeedbackHiddenMarker`), so follow-through skips them and the loop never
  starts.
- A cycle budget per epoch, mirroring `feedback_bot_epoch` /
  `feedback_bot_cycles_in_epoch` (`internal/db/pull_request_feedback.go`): any
  human comment resets the epoch and refills the budget; machine-only rounds spend
  it. Humans stay unlimited, machines don't. Markers get dropped in refactors, so
  this is the backstop.

### Keeping the PR quiet

Doc 112 works hard to keep exactly one visible 143 comment per PR, and unlimited
disputes could wreck that. Rerun results **update the rolling comment in place**,
like every other assessment. `policy_signal_only` and `answer_only` get **one
threaded reply** each — these are conversations, and silence is the thing we're
fixing — nested by GitHub under the comment they answer, never a chain. The
rolling comment carries a one-line summary: *"2 objections · 1 rerun ·
[view](link)"*. Everything else lives on the session.

Queue ranking runs as a scheduled job once the rerun settles, the PR closes, or the
window expires. Ship the **adjudication queue**, the **Insights** tab, and a weekly
digest to policy owners in this phase. That's the whole loop end to end at low
volume: developers get answers, admins see the pattern and edit policy by hand. It's
worth having on its own, and it's what tells us whether Phase 2 is warranted.

## Phase 2 — Propose changes

### Start it on a volume trigger, not a date

Phase 2 is a proposal generator, a replay harness, and a guard set with a curation
lifecycle. It's worth building when hand-editing policy is the bottleneck and not
before — at low volume an admin reading Insights and editing the rubric directly is
genuinely competitive, and that path already exists.

**Build it when both hold, sustained over two months:** at least **5 upheld disputes
a month**, and a guard set of **30+ members** with a meaningful share adjudicated
rather than bootstrapped. Below that an admin is handling about one case a week by
hand, and a pipeline would be ceremony around a trickle.

Waiting is cheap because Phase 1 produces the inputs: the wait is spent gathering the
data that says what shape a proposal should take, including the real distribution of
`dispute_kind`, which we're currently guessing. **Spot-checking shouldn't wait** —
it's a scheduled job and a queue, it yields the false-approval rate on its own, and
it's the source of curated guard members. Ship it in Phase 1 if there's room.

That same low volume is why there's **no clustering**: one upheld dispute drafts one
proposal. Split across repos, reason codes, and dispute kinds, most clusters would
have exactly one member, so de-noising would throw away the entire signal — and
per-dispute cuts the loop from a month to days. If proposal volume ever becomes the
complaint, clustering can come later.

### Proposals change prompts, and only prompts

A 143 session reads the dispute, its review, the findings, and the current prompt
text, then proposes a scoped edit to the acceptable-risk rubric or a description
requirement. Edits are limited to a named section and can **add or remove** —
additions-only would mean the rubric can only ever grow and a genuinely wrong rule
could never be deleted. Dispute text enters as untrusted evidence.

**Thresholds are not proposed at all.** An earlier draft added a second,
deterministic generator for limits, path rules, categories, checks, and author
eligibility; it's cut. The Approach section argues thresholds are the lever that
matters least, and a whole second generator for the least important lever — one that
then has to stay consistent with the first — is the wrong trade. Insights already
knows the numbers: *"11 non-approvals this month tripped the 5-file limit; median was
6."* Show that with a deep link to the setting and let the admin type a 7. One
generator, one replay path, and the arithmetic stays a chart rather than a subsystem.

### Replay is what makes review real

Nobody can evaluate a reworded rubric by reading it. So every proposal is replayed
before an admin sees it, against two sets, and both are shown: the **target set**
(the dispute that prompted it — did it fix the problem?) and the **guard set**
(held-out decisions we believe were right, weighted toward approvals near the
frontier).

The guard set is the important half. Replaying only the target set proves nothing:
the prompt was written to fix that case, so it always passes. The guard set is what
catches "fixed 1 complaint, would have flipped 11 good approvals."

Replaying a prompt change **doesn't require rerunning the review**. The editable
prompts feed the orchestrator, `code_review_agent_results` already stores each
reviewer's raw and structured output, and doc 112 requires rendered prompts to be
recoverable. So replay reruns **only the orchestrator step against stored reviewer
output** — no sandboxes, no clones, no fan-out. One cheap call per historical
review, 20–30 per proposal.

**Seeding the guard set, and not trusting it yet.** Day one has no corpus of
known-good decisions, so bootstrap from decisions that merged with no blocking human
review, then curate from spot-check verdicts, which are real judgments.

Those bootstrapped members carry a label this doc rejects everywhere else, and it's
wrong in one specific direction: a bad approval that merged quietly enters the set
marked correct. Replay a proposal that rightly *tightens* policy against it and the
proposal is charged with a regression for fixing the thing we wanted fixed. Early
guard numbers therefore run pessimistic against tightening, and that bias doesn't
average out with more members — only with better-labelled ones.

So **guard regressions are advisory until the set holds 30+ members added by
`spot_check` or `admin`**, with composition shown beside the count — *"3 regressions ·
12 of 40 members adjudicated."* After that they can gate. Gating on a bootstrapped set
would block good tightening on mislabels, admins would learn to override, and an
override people always click is worth less than no gate. The guard set doubles as a
small eval corpus, giving doc 16 something to build on.

### Spot-checking approvals

Since nobody reports a bad approval, go looking. A scheduled job scores each
approval by how close it came to the edge of policy and queues the top few per week
for an admin:

- **Margin** — how near the limits: 5 of 5 files, 296 of 300 lines.
- **Thin judgment** — approved despite P2 findings, low-confidence clean verdicts,
  or reviewer disagreement that didn't quite block.
- **Novelty** — first approval touching a path or category this policy has never
  approved before. Weight this highest: margin means the policy is being used at
  its edge, novelty means it's being used somewhere untested, and untested is
  where a bad rule hides.

Mix in a **blind random sample** the admin can't tell from the frontier picks. If
random spot-checks find bad approvals at the same rate as targeted ones, the score
is worthless and there's no other way to learn that. The random arm also gives an
unbiased false-approval rate — doc 112's headline safety metric, currently
uncomputable.

Keep the queue to a few minutes a week. A "shouldn't have been approved" verdict is
an admin adjudication, so it writes a dispute that's already upheld — spot-checking
needs no evidence rule of its own, it just manufactures cases for the one rule we
have. A "correct" verdict adds the session to the guard set as a curated member,
which is the other reason to start spot-checking early.

### Guardrails

- Proposals never apply themselves.
- They can never touch approval mode, fork eligibility, prompt-injection
  handling, agent roster, or the bot's own policy paths.
- Every activation is attributed to a user, audit-logged, and reversible by
  activating the previous version — policies are insert-only, so revert is one
  click.
- Insights tracks decision mix and false-approval rate per policy version, so a
  bad prompt change shows up in days rather than via an incident.

## Database Schema

Every table below carries `id uuid PRIMARY KEY`, `org_id uuid NOT NULL REFERENCES
organizations(id)`, and `created_at timestamptz NOT NULL DEFAULT now()`. Tenancy
is `org_id` throughout. Only the distinctive columns are listed.

**`code_review_decision_disputes`** — one row per objection.

| Column | Type | Notes |
| --- | --- | --- |
| `session_id`, `pull_request_id`, `repository_id`, `policy_id` | uuid NOT NULL | FKs; session/PR cascade on delete |
| `reviewed_head_sha`, `decision` | text NOT NULL | What was being disputed |
| `direction` | text NOT NULL | `should_have_approved` \| `should_not_have_approved` |
| `filed_by_user_id`, `filed_by_login`, `author_association`, `author_is_pr_author` | uuid / text / bool | Filer identity. `author_association` is stored as observed; trust is derived from it at read time, never stored as a verdict |
| `trust_override` | boolean | Admin escape hatch. NULL means "use the org rule" |
| `source` | text NOT NULL | `github_comment` \| `app_ui` \| `api` \| `spot_check` |
| `github_comment_id`, `github_delivery_id`, `github_thread_root_comment_id`, `reply_comment_id` | bigint / text | Dedupe, threading, and the one reply |
| `body` | text NOT NULL | Verbatim. No syntax is parsed from it |
| `contested_reason_codes` | text[] | Inferred by triage |
| `dispute_kind` | text | **Free text from triage, no constraint.** See below |
| `asserts_new_information` | boolean NOT NULL | Drives the `reassess` route |
| `routing` | text | `reassess` \| `policy_signal_only` \| `answer_only` \| `not_a_dispute` |
| `intake_status`, `intake_confidence` | text | `pending` \| `triaged` \| `discarded` \| `failed` |
| `reassessment_session_id`, `reassessment_decision`, `reassessment_flipped` | uuid / text / bool | Rerun linkage |
| `adjudication_status` | text NOT NULL | `pending` \| `upheld` \| `rejected` \| `expired`. `upheld` is the only thing that drafts a proposal |
| `adjudicated_by_user_id`, `adjudicated_at`, `adjudication_note` | uuid / timestamptz / text | Who decided, and why |
| `queue_signals` | jsonb | The ranking signals observed on this dispute, with their inputs |
| `queue_priority` | numeric | Derived from `queue_signals`; orders the admin queue and decides nothing |

`dispute_kind` is **deliberately unconstrained**. An enum here would be a dozen
guesses about a distribution we haven't observed, frozen into the least flexible part
of the system — categories we invented rather than ones we saw. Triage writes a short
slug, Insights charts the frequencies, and the values that turn out to be real get
promoted to a constraint later. `direction` and `contested_reason_codes` stay
structured; those we actually know.

Indexes: unique on `github_comment_id` and on `github_delivery_id` (both partial,
`WHERE NOT NULL`) so redelivery and edits update in place — deliberately *not*
unique per `(session, filer)`, since one person may raise two genuinely different
objections. Partial index on `intake_status = 'pending'`; partial on `(org_id,
repository_id, queue_priority DESC)` where `adjudication_status = 'pending'` and
triage kept it, to build the admin queue; partial on `(org_id, adjudicated_at DESC)`
where `upheld`, to find disputes awaiting a proposal.

**`code_review_policy_proposals`** — one row per proposal. `repository_id`,
`base_policy_id`, and `source_dispute_id` (uuid; exactly one, no clustering);
`direction` (`loosen` | `tighten`); `origin` (`agent_session` | `manual`) with
`generator_session_id`; `status` (`replaying` | `open` | `activated` | `dismissed` |
`superseded` | `expired`); `proposed_changes jsonb NOT NULL` — a structured validated
prompt-section delta, never a raw config replacement; `rationale text NOT NULL`
citing its dispute and review; `replay_status`, `replay_target_result jsonb`,
`replay_guard_result jsonb`, `guard_regressions int`, and `guard_set_size` /
`guard_set_adjudicated int` so a reader can tell a meaningful regression count from a
provisional one; and the outcome columns `activated_policy_id`, `decided_by_user_id`,
`decided_at`, `decision_note`. There's no `change_kind` — every proposal is a prompt
edit. Unique index on `source_dispute_id` where status is `replaying` or `open`; plus
`(org_id, status, created_at DESC)`.

**`code_review_approval_audits`** — the spot-check queue. `session_id`,
`repository_id`, `pull_request_id`, `policy_id` identify the approval under
review. `selection` (`frontier` | `random`) is **never exposed while queued** —
the control only works blind. `frontier_score numeric` and `frontier_factors
jsonb` record margin, thin judgment, and novelty. `status` (`queued` | `reviewed`
| `expired`) and `verdict` (`correct` | `should_not_have_approved`) carry the
outcome, with `resulting_dispute_id`, `reviewed_by_user_id`, `reviewed_at`, and
`note`. Unique index on `session_id`; partial on `(org_id, status, created_at)`
where queued.

**`code_review_guard_set_members`** — held-out decisions for replay.
`session_id` (unique where active), `repository_id`, `expected_decision`,
`added_by` (`bootstrap` \| `spot_check` \| `admin`), `active boolean`.

**`code_review_decision_outcomes`** — denormalized per-decision facts, keyed by
`session_id` as PK, so Insights and queue ranking don't re-derive PR history on every
read. Carries `decision`, `reason_codes text[]` (GIN indexed), `merged`, `merged_at`,
`independent_approver_login`, `independent_blocking_review_login`,
`human_review_comment_count`, `reverted`, `terminal`, `observed_until`.

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
    ADD COLUMN triggering_dispute_id uuid REFERENCES code_review_decision_disputes(id),
    ADD COLUMN files_changed int,
    ADD COLUMN lines_changed int;

-- Bot-loop budget, mirroring the doc 116 epoch mechanic.
ALTER TABLE pull_requests
    ADD COLUMN code_review_dispute_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN code_review_dispute_cycles_in_epoch integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT chk_pr_code_review_dispute_cycles
        CHECK (code_review_dispute_cycles_in_epoch >= 0);
```

**Jobs**, on the existing queue:

| Job | Queue | Fires when |
| --- | --- | --- |
| `triage_code_review_dispute` | `feedback` | A comment is captured, or a dispute is filed in-app |
| `run_code_review` (existing) | `agent` | Triage routed `reassess`; deduped on dispute id |
| `reply_code_review_dispute` | `feedback` | Triage routed `policy_signal_only` / `answer_only` / `not_a_dispute`, or a rerun finished |
| `rank_code_review_dispute` | `feedback` | Rerun settles, PR closes, or the window expires. Recomputes `queue_signals` and `queue_priority` |
| `generate_code_review_policy_proposal` | `agent` | An admin upholds a dispute |
| `replay_code_review_policy_proposal` | `agent` | Proposal created |
| `sample_code_review_approvals` | `feedback` | Weekly per org |
| `digest_code_review_insights` | `feedback` | Weekly per org |

Reruns reuse the existing `run_code_review` handler unchanged; only request
orchestration learns the new trigger source, the trust gate, and the reassessment
budget. The budget needs no new columns — per-PR spend is a count of sessions with
`trigger_source = 'dispute_reassessment'`, and the org ceiling is the same count over
a month.

**Audit actions:** `code_review_dispute.filed`, `.reassessed`, `.adjudicated`,
`code_review_approval_audit.reviewed`,
`code_review_policy_proposal.activated`, `.dismissed`.

## API Contract

Org-scoped, existing auth conventions. Filing and reading are member-level;
adjudication, spot-check verdicts, and proposal decisions are admin-level.

| Route | Body / query | Returns |
| --- | --- | --- |
| `POST /api/v1/code-reviews/{session_id}/disputes` | `{ body: string, contested_reason_codes?: string[] }` | `201` dispute with `intake_status: "pending"`. `422` if body empty, `404` if no such review. **No 409** — several distinct objections on one review are legitimate; only GitHub-sourced ones dedupe, on comment id. Direction is inferred, never supplied |
| `GET /api/v1/code-reviews/{session_id}/disputes` | — | Disputes with routing, trust, and rerun linkage |
| `GET /api/v1/code-review-disputes` *(admin)* | `adjudication_status?`, `repository_id?`, `direction?` | The adjudication queue, ordered by `queue_priority`, each dispute showing which signals raised it |
| `PATCH /api/v1/code-review-disputes/{id}` *(admin)* | `{ adjudication_status: "upheld"\|"rejected", adjudication_note?, trust_override? }` | Updated dispute. `upheld` enqueues proposal generation. `409` if already adjudicated |
| `GET /api/v1/code-review-approval-audits` *(admin)* | `status?`, `repository_id?` | Spot-check queue. `selection` is **omitted while queued** — the random control only works if the reviewer can't see the arm |
| `POST /api/v1/code-review-approval-audits/{id}/verdict` *(admin)* | `{ verdict: "correct"\|"should_not_have_approved", note? }` | `should_not_have_approved` creates an already-upheld dispute; `correct` adds the session to the guard set |
| `GET /api/v1/code-review-insights` | `repository_id?`, `from?`, `to?`, `decision?`, `reason_code?`, `direction?` | Decisions and dispute rate by reason code, with actual-vs-limit distributions and a deep link to each setting — this is what admins tune from before Phase 2 exists; totals; disputes by direction; **`dispute_kind` frequencies**, the input to eventually constraining that column; flip rate by attempt; reassessment spend against budget; `spot_check` (frontier vs random hit rate, false-approval rate); median decision time; per-policy-version decision mix |
| `GET /api/v1/code-review-policy-proposals` | `status?`, `repository_id?`, `direction?` | Proposals including replay results |
| `POST /api/v1/code-review-policy-proposals/{id}/activate` *(admin)* | `{ proposed_changes? }` (optional edited delta) | New policy + proposal. `409` if not open, replay incomplete, or base policy superseded; `422` invalid delta; `403` touches a locked dimension |
| `POST /api/v1/code-review-policy-proposals/{id}/dismiss` *(admin)* | `{ decision_note }` | Updated proposal |

The GitHub path adds no route and parses no syntax. The existing `issue_comment`
and `pull_request_review_comment` handlers capture replies in the bot's thread and
mentions on reviewed PRs, check eligibility, dedupe on `github_delivery_id`, and
enqueue triage. Whether a comment is a dispute is triage's call, not the
handler's.

Activation runs the same validation as `PUT /api/v1/code-review-policies`, then
writes a new insert-only version. It never patches a policy row in place, so
approvals keep pointing at the version that produced them.

## Success Metrics

- **How often non-approvals get an objection.** The miscalibration signal we don't
  have today. Expect it to jump when intake becomes free-text — that's the feature
  working, not the bot getting worse.
- **Triage accuracy**, hand-sampled. Objections wrongly filed as `not_a_dispute`
  are the silent failure this exists to prevent, and matter far more than the
  opposite error.
- **Frontier hit rate vs random hit rate.** If they're equal the frontier score is
  worthless, and this is the only way to find that out.
- **False approval rate** from the random arm — unbiased, and doc 112's headline
  safety metric.
- **Guard-set regressions per activated proposal**, always read next to the set's
  adjudicated share. How often a change broke decisions that were right; the number
  that says whether one-click activation is safe, and it means little until the set
  is mostly curated.
- **Upheld disputes per month**, the Phase 2 volume trigger. Also the number that
  tells us whether hand-tuning from Insights was enough all along.
- **Reassessment spend per PR and per org.** Hitting the budget is a product signal
  and not just a bill — a PR that exhausts it is an argument the loop couldn't
  settle.
- **Flip rate by rerun attempt.** Flips on new information mean the loop works;
  flips on unchanged input mean judgment variance, a different bug.
- **Time from objection to a real reply**, and to a flipped decision.
- **Activation and dismissal rates by direction.** Lasting asymmetry means drift.

## Open Questions

- How big should the weekly spot-check queue be, and what frontier/random split?
  Too big and nobody opens it; too small and the random arm never says anything.
- What are the starting numbers for the reassessment budget — per PR and per org per
  month? Cheap to change, but the first guess sets the tone.
- How long should a guard-set member stay valid? Codebases move, and a two-year-old
  decision may no longer be the right baseline.
- Should a dispute from someone other than the PR author be `reassess`-eligible on
  more reason codes, given the weaker bias?
- Does the bot-loop budget need to be configurable, or is a constant fine given the
  marker should stop the loop outright?
- Should a `should_not_have_approved` dispute on an already-merged PR notify anyone
  beyond creating the proposal?
