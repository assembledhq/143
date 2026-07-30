# Design: Code Reviewer Bot And Acceptable-Risk Auto-Approval

> **Status:** Implemented | **Last reviewed:** 2026-07-28
>
> **Depends on:** [../overall.md](../overall.md), [78-review-agent-loops.md](78-review-agent-loops.md), [107-pr-readiness-checks.md](107-pr-readiness-checks.md), [61-pr-state-sync-and-repair-actions.md](61-pr-state-sync-and-repair-actions.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md)

## Summary

Create a GitHub-native **Code Reviewer** bot that teams can request as a PR reviewer. It evaluates PR description quality, runs a configurable multi-agent review, classifies risk against org/repo policy, and then either:

- leaves a synthesized review comment only, or
- approves the PR with review evidence when the PR meets the organization's acceptable-risk policy.

The goal is not to replace meaningful human review. It is to move basic acceptable-risk PRs out of the human queue so reviewers can focus on changes where judgment, architecture, ownership, or risk actually matter.

Implemented:

- versioned insert-only code review policies with org defaults and repository overrides
- lossless policy persistence for final review templates
- code review session metadata, agent result, finding, and prompt-artifact tables tied to normal `sessions`
- typed Go models and `pgx` stores for policies, review metadata, agent evidence, and findings
- deterministic acceptable-risk evaluator, starter policy templates, final-review body rendering, and inline finding selection helpers
- GitHub `review_requested` and PR issue-comment mention webhook adapters for configured bot reviewer identities, including authoritative PR snapshot loading and local mirror creation for human-authored PRs
- event-driven reassessment after the initial reviewer request when the PR head changes, plus delivery-keyed explicit rerequests that can reassess the same head after a non-approval; durable follow-up jobs serialize requests behind active work and a terminal stop applies once 143 approves, while PR metadata, readiness, human reviews/comments/threads, checks, and commit statuses continue to synchronize without starting reviewer sessions
- service-layer code review request orchestration that resolves/materializes policy, marks stale older heads, reuses running sessions, creates normal code-review sessions, and enqueues `run_code_review`
- `run_code_review` worker handler that loads the captured policy version, fans out read-only reviewer threads running native `/review`, synthesizes via an orchestrator thread, records agent results, submits a GitHub review when the worker has GitHub credentials, and stores the GitHub review id/url
- live reviewer/orchestrator evidence ingestion harvested from running review threads rather than pre-existing stored result rows
- evidence-gated approval path that evaluates reviewer results, blocking findings, PR health, reviewed head SHA, required check state, changed-file size/path/category context from GitHub, and the captured policy before choosing approval vs comment-only
- coding-agent orchestrator evaluation of PR description requirements, structured findings at every severity, and typed non-finding human-review reasons, with prompt-injection screening; an explicit issue-comment trigger is supplied as bounded, untrusted request context, and the backend derives the decision from the resulting explicit signals
- prompt artifact storage and recovery for rendered reviewer/orchestrator prompts and their structured outputs
- inline-comment posting with marker-based dedupe/update and posted-comment id persistence
- GitHub changed-file fetch support for PR file/line threshold and coarse risk-category evaluation
- one rolling PR conversation comment that links to the active session while review is running and becomes the sole visible summary when it completes; the formal review retains a visible fallback until that rolling update succeeds, then becomes marker-only, without a redundant commit status that could be mistaken for a required CI check
- durable per-PR storage of that rolling comment's GitHub ID, using direct updates normally and an app-authored marker scan only to recover a missing or deleted comment
- in-place rolling-comment refresh for reassessments, with visible latest-assessment commit/time provenance, failure-safe formal-review fallback and marker-only convergence, prior inline findings updated by stable markers, and each assessment retained as a separate auditable 143 session
- stale requested-reviewer cleanup after final review submission for reviewer-login and team-slug triggers carried in the durable job payload
- productized GitHub team-trigger setup that creates or repairs the `143-code-reviewer` org team, grants repository read access, and persists repo-scoped active trigger settings
- final-review template rendering from persisted policy data with safe fallback to the built-in body
- `/api/v1/code-reviews`, `/api/v1/code-reviews/templates`, `/api/v1/code-reviews/{id}/evidence`, `/api/v1/code-review-policies`, and `/api/v1/code-review-github-trigger` API surface
- top-level `Code reviews` dashboard surface with Reviews, Configurations, Insights, repository/decision/risk/status/search filtering, enablement, approval mode, threshold, prerequisite, timeout, cost, path/check/author/agent, prompt, and final-template controls

Deferred:

- always-on auto-review and slash-command triggers (the `slash_command` and `auto_policy` trigger sources are reserved but unwired; only explicit reviewer assignment or an explicit configured-team mention in a PR conversation comment runs the bot)
- structural review-depth behavior (quick/standard/deep is passed to reviewer/orchestrator prompts but does not change fan-out)
- aggregate reporting/insights across reviews

## Problem

Coding agents and faster developer tooling increase PR volume. Teams then face two bad outcomes:

- every PR demands human attention, so reviewers skim and become less effective
- important PRs compete with basic acceptable-risk changes for the same review bandwidth

Existing 143 surfaces help adjacent parts of the flow:

- Review agent loops improve a session's own diff before publishing.
- PR readiness checks decide whether a session looks ready to become a PR.
- PR health surfaces conflicts and failing checks after a PR exists.

This feature fills the post-PR slot: reviewer automation in GitHub, where teams already assign review responsibility.

## Product Principles

1. **Approval requires evidence.** The bot approves only when every prerequisite passes and active policy says the PR is acceptable.
2. **Risky work stays human-centered.** Non-acceptable PRs get review evidence, escalation reasons, and inline comments, not approval.
3. **GitHub remains the action surface.** Reviewer assignment triggers the workflow; one rolling PR comment carries the visible result while formal reviews carry approval state and inline findings.
4. **Organizations own policy.** Description requirements, agent roster, prompts, risk thresholds, and approval behavior are org/repo configurable.
5. **Every decision is inspectable.** Each approval or non-approval links to the 143 session, policy version, reviewed SHA, and agent evidence.

## Recommendation

Ship **Reviewer Bot With 143 Code Review Sessions**, triggered by explicit GitHub reviewer assignment or configured-team mention.

Recommended v1 scope:

- GitHub team trigger (`@org/143-code-reviewer`) backed by GitHub App-authored final reviews.
- `review_requested` trigger for selected repositories.
- `issue_comment` trigger when a PR conversation comment mentions the configured team, with the bounded comment supplied to the orchestrator as untrusted request context.
- Normal 143 code review sessions keyed by org, repository, PR, head SHA, and policy version.
- Editable PR-description policy and acceptable-risk starter templates.
- Two reviewer agents plus one orchestrator by default.
- Each reviewer has a policy-versioned reasoning-effort value aligned by index with `reviewers` and `reviewer_models`; the orchestrator keeps its own `reasoning_effort`. Omitted legacy reviewer values inherit the former roster-wide value, or `high` when that value is also absent.
- One rolling PR comment with the visible summary, plus a formal GitHub review that converges from a visible fallback to marker-only and a configurable number of inline comments.
- Approval only for acceptable PRs; otherwise comment with escalation reasons.
- Delivery-idempotent explicit requests, stale-head reruns, and GitHub review retries.
- Top-level `Code reviews` surface for filtered sessions and configuration.

Defer:

- Always-on auto-review.
- Code-fixing from the reviewer bot.
- Approval for high-risk directories.
- Arbitrary custom scripts.
- Automatic policy learning from past approvals.

## Core Flow

```text
Developer opens PR
        |
        v
Developer requests "143 Code Reviewer" in GitHub, either through the reviewer
picker or by mentioning the configured team in a PR conversation comment
        |
        v
143 receives the explicit-request webhook and creates a code review session
        |
        v
Best-effort async job creates or refreshes one PR comment linked to the session
        |
        v
Run configured review agents against the PR diff and context
        |
        v
Orchestrator agent inspects the change, evaluates description requirements,
and synthesizes structured findings plus explicit human-review reasons
        |
        v
Backend derives the decision from finding severity, typed reasons, and
non-bypassable safeguards
        |
        v
If there are no blocking signals and every safeguard passes:
  submit a marker-only GitHub approval
Else:
  submit a marker-only GitHub review comment
        |
        v
Replace the rolling PR comment with the result
        |
        v
Until approval, later commits automatically rerun the assessment, and a new
explicit reviewer request reruns it even at the same head. A triggering comment
is included only in orchestrator synthesis as untrusted guidance. Both update
the existing formal-review marker and rolling PR comment. Equivalent webhook
deliveries are idempotent; other webhook activity only synchronizes PR state.
Approval is final.
```

Reviewer assignment should be explicit in v1. Auto-running can come later after teams trust the signal.

## Product Surfaces

### GitHub Reviewer

Primary interaction:

- A 143 admin creates or repairs the `143-code-reviewer` GitHub team from the Code reviews configuration page.
- 143 grants that team read access to the selected repository and stores the team slug as the repo's active trigger.
- A user requests `@org/143-code-reviewer` as a team reviewer on a PR.
- Alternatively, a repository owner, organization member, or collaborator mentions `@org/143-code-reviewer` in a PR conversation comment; this starts a fresh review and carries that comment into orchestrator synthesis as escaped, untrusted context. External users, bots, and GitHub Apps cannot start reviews through comments.
- 143 asynchronously creates or refreshes one PR conversation comment that links to the running review session.
- The bot submits a formal GitHub review for approval state and a configurable number of inline comments on changed lines. Its body keeps the visible result as a fallback until the rolling comment update succeeds, then becomes marker-only; it does not publish a commit status.
- The rolling conversation comment is replaced with the visible result, and later review passes reuse the same comment.

This does not use CODEOWNERS and does not auto-request reviews on PR open. The team is only the selectable GitHub reviewer trigger; normal review submission still uses the installed GitHub App.

Example final approval:

```text
143 Code Reviewer approved this PR

Risk: acceptable
Description: passed
Review agents: Codex clean, Claude Code clean
Checks considered: CI green, 4 files changed, no sensitive paths, tests updated
Review session: https://143.dev/sessions/sess_abc123

Notes:
- Minor naming suggestion in src/foo.ts, non-blocking.
```

Example non-approval:

```text
143 Code Reviewer did not approve this PR

Risk: needs human review
Reasons:
- Auth-sensitive paths changed
- PR description is missing testing strategy
- Claude Code found one possible authorization edge case

Recommended human reviewers: backend/platform
Review session: https://143.dev/sessions/sess_def456
```

### 143 Code Review Session

Every bot-triggered review creates a normal 143 session so transcript, tabs, agent outputs, runtime state, audit events, GitHub linkage, and future follow-up actions live in the existing execution model.

GitHub stays concise; the session keeps the full detail:

- PR metadata, base/head SHA, author, requested reviewer, run status
- current operational phase, automatic GitHub retry time, and curated failure action
- coding-agent description policy assessments
- per-agent raw review outputs
- orchestrator synthesis
- risk rubric inputs and final classification
- approval eligibility checklist
- GitHub review submitted by the bot
- audit trail for policy version, agent versions, and prompts

The rolling PR comment always links to the session for both approval and non-approval paths. Formal review bodies contain only hidden idempotency markers.

### Code Reviews Navigation

Add `Code reviews` as a top-level navigation item below Automations:

```text
Automations
Code reviews
Sessions
```

This is not a separate execution system; it is an opinionated surface over code review sessions and review policy.

Recommended tabs:

| Tab | Purpose |
| --- | --- |
| Reviews | Filtered session list containing code review sessions, with PR, repository, author, risk, decision, operational phase/status, retry time or failure action, requested-at, and completed-at columns. |
| Configurations | Org and repository code review policies: enablement, description requirements, risk thresholds, agent roster, orchestrator, and approval mode. |

The Reviews tab reuses the normal session list/detail route. Primary action opens the session; secondary actions open the GitHub PR, policy version, or final GitHub review.

Reviews wireframe:

```text
Code reviews
[Reviews] [Configurations]

Repository [All v]  Decision [All v]  Risk [All v]  Search [PR, author, title]

PR                         Repo        Author     Risk        Decision      Status      Completed
#428 Fix invoice rounding   billing     anya       acceptable  approved      complete    4m ago
#427 Add chart tooltip      web         sam        needs human comment only  complete    18m ago
#426 Rotate API key copy    platform    devin      blocked     comment only  reviewing   -

[Open session] [Open PR] [Final review]
```

Configurations wireframe:

```text
Code reviews
[Reviews] [Configurations]

Scope
Organization default [Acme v]          Repository override [All repositories v]

Bot behavior
[x] Enable 143 Code Reviewer
Outcome mode
(*) Comment only
( ) Approve acceptable PRs

PR description requirements
[x] Understandable description          Required for all PRs
    [Prompt ▾] [Edit]
    The PR description should explain what is changing and why in enough
    detail that a reviewer can understand the work without reconstructing
    intent from the diff. It does not need to be long.
[x] Testing evidence                    Required for nontrivial changes
    [Prompt ▸]
[x] Screenshots or preview link         Required for frontend or large changes
    [Prompt ▸]
[+ Add requirement]

Acceptable risk policy
Files changed <= [5] [Edit]    Lines changed <= [300] [Edit]
[x] Require passing GitHub checks        [Configure]
[x] Exclude sensitive paths              [Configure paths]
[x] Exclude migrations/dependencies      [Configure categories]
[+ Add risk rule]

Review agents
Reviewer agents [Codex] [Claude Code] [+ Add]
Orchestrator [Claude Code v]
Inline comments [4] per review (max 10)

[Save policy]
```

Core settings:

- Enable reviewer bot per organization and repository.
- Configure allowed outcomes: comment only or approve acceptable PRs.
- Configure PR description policy.
- Configure acceptable-risk definition.
- Select reviewer agents and orchestrator agent.
- Configure CI/check prerequisites for approval.
- Configure path, size, and author constraints.
- Configure inline comment cap, default 4 and max 10.
- Configure whether human-authored, 143-authored, or all PRs are eligible.

Repository overrides should inherit from org defaults. Policy should be versioned insert-only like other settings where history matters, because approval decisions need later auditability.

## Configurable Policy Areas

### PR Description Policy

Description policy is an editable rubric with optional prompt checks. 143 ships defaults, but admins can adjust requirement text, applicability, thresholds, and enforcement per org or repository.

Default editable requirements:

| Requirement | Example policy |
| --- | --- |
| Understandable description | Required for all PRs. The description should explain what is going on well enough for a reviewer to understand the change; it does not need to be long. |
| Testing evidence | Required for nontrivial changes. Admins can define nontrivial by files changed, lines changed, touched paths, changed test files, or risk categories. |
| Screenshots or preview link | Required for frontend changes, UI-visible changes, or changes above configured file/line thresholds. |

Reuse the PR readiness custom-check pattern where possible: typed built-ins first, prompt-only custom checks second, no arbitrary code execution.

### Multi-Agent Review Roster

Organizations configure:

- reviewer agents: Codex, Claude Code, OpenCode, Amp, Pi, or future providers
- orchestrator agent: the model/provider that reads all outputs and produces the final structured decision
- review depth: quick, standard, deep
- timeout ceiling
- whether disagreement forces human review

Defaults:

- Run two reviewer agents when approving is enabled.
- Use one orchestrator agent that is not one of the reviewer agents when available.
- Treat material disagreement as not acceptable risk by default.

Reviewer agents run in isolated read-only review sandboxes at the PR head SHA. They inspect only; PR repair/revision actions handle fixes.

### Acceptable-Risk Definition

Acceptable risk is fully configurable by org admins with optional repository overrides. 143 ships conservative defaults, but approval always comes from the active org/repo policy.

Risk evaluation combines deterministic safeguards with coding-agent assessments
and synthesized review findings. The coding-agent orchestrator supplies
structured evidence; the backend owns both the approval decision and the final
GitHub action. A P0 or P1 code finding blocks approval. P2 and P3 findings are
advisory. Non-code judgment calls may block only through a typed,
reviewer-visible human-review reason. The model's bare
`approval_recommended` value is supporting output and never acts as an invisible
veto.

Configurable deterministic signals:

- small diff by configured file and line thresholds
- no sensitive paths touched
- no migrations, auth, billing, permissions, crypto, infra, dependency lockfile, or generated artifact surprises
- CI/checks are green or not required by policy
- branch is mergeable and up to date according to policy
- author is in an eligible role or team

Existing human comments, review decisions, and open or resolved review threads are deliberately not risk signals. The bot evaluates the current pull request independently.

Configurable synthesized signals:

- each applicable PR-description requirement is `satisfied`, `not_applicable`,
  or `missing`; both `satisfied` and `not_applicable` pass the requirement
- reviewer agents found no blocking correctness, security, or maintainability issues
- orchestrator agrees the change matches the stated intent
- no reviewer-agent disagreement on severity
- no meaningful unknowns remain
- no explicit architecture, ownership, operational-risk, sensitive-change, or policy-requirement judgment remains for a human

Conservative default:

```text
Acceptable risk means:
- <= 5 changed files [adjustable]
- <= 300 changed lines [adjustable]
- no configured sensitive paths [adjustable path set]
- no dependency, migration, permission, auth, billing, or infra changes [adjustable categories]
- PR description passes [adjustable requirements]
- required GitHub checks pass [adjustable check set]
- at least two configured reviewer agents report no blocking issues [adjustable agent/quorum rule]
- orchestrator finds no scope mismatch or unresolved uncertainty [editable prompt/rubric]
```

Admins can tune this over time based on false positives, false negatives, team trust, and repository-specific risk.

### Acceptable-Risk Templates

The configuration UI should offer starter templates so admins do not start from a blank page.

Recommended templates:

| Template | Default behavior |
| --- | --- |
| Docs and comments only | Eligible for approval when only docs, comments, or markdown paths change, PR description passes, and no generated/security/config paths are touched. |
| Tests only | Eligible for approval when changes are limited to test files and fixtures, no snapshots/golden files exceed configured churn, and required checks pass. |
| Small frontend change | Eligible for approval when file/line thresholds are low, screenshots or preview link are present when required, and no auth/billing/data-fetching paths are touched. |
| Small backend change | Eligible for approval only outside sensitive packages, with test evidence, passing checks, and no schema, permissions, auth, billing, dependency, or infra changes. |
| Small combined feature | Eligible for approval when a limited-scope feature touches both frontend and backend within tighter file/line thresholds, includes test evidence, includes screenshot or preview evidence when UI-visible, and avoids sensitive paths, schema changes, permissions, auth, billing, dependency, and infra changes. |

Each template expands into editable rules rather than hidden presets: thresholds, path categories, PR description prompts, required checks, reviewer quorum, and orchestrator rubric.

## Bot Identity

Recommended long-term shape: a **GitHub App-backed bot identity** named something like `143 Code Reviewer`, with optional repository/team routing layered on top in 143.

Rationale:

- It matches the existing 143 GitHub App setup, permission model, webhook flow, audit trail, and installation lifecycle.
- Reviews, approvals, inline comments, and status updates are clearly authored by 143 instead of by a shared human or ambiguous team account.
- App installation scope gives admins a natural place to control which repositories can use automated approval.
- The same identity can work for human-authored and 143-authored PRs without requiring every org to manage a real GitHub user seat.
- Team-based routing can still recommend humans or map policies to CODEOWNERS/team labels without making a GitHub team the approval actor.

If GitHub's reviewer picker cannot expose the app identity in every org configuration, use a 143-managed reviewer alias or team as the trigger. The final review should still be authored by the GitHub App-backed bot.

Implementation requirements:

- Verify GitHub reviewer-request behavior for GitHub App bot users, organization-owned repositories, private repositories, and fork PRs before finalizing the v1 trigger.
- Treat the reviewer request trigger and the review author as separate concepts. A team or alias may trigger the workflow, but the submitted review should be authored by the app-backed bot.
- Store the trigger source on the session, such as app reviewer, alias reviewer, team reviewer, slash command, or future auto-run policy.
- If a team alias is used, the bot should remove or resolve its own pending request after posting the final review where GitHub permits it, so teams do not see a stale requested-reviewer state.

## Trust And Safety Controls

Approval requires all of these by default:

- PR head SHA still matches the reviewed SHA at submission time.
- No blocking GitHub checks are failing.
- No P0 or P1 finding exists.
- No explicit typed non-finding human-review reason exists.
- Policy allows approval for this repository, author class, and changed paths.
- The bot has not already approved a stale previous head.

Existing human comments, review decisions, and unresolved review threads are excluded from the bot's approval decision. GitHub branch protection and merge rules remain authoritative independently.

The bot should not approve:

- its own policy/config changes unless explicitly allowed
- changes to GitHub workflows, deployment, auth, billing, permissions, secrets, or infrastructure by default
- dependency updates with lockfile changes by default
- PRs with merge conflicts
- PRs from untrusted forks unless explicitly enabled
- PRs where required context cannot be fetched

Every approval stores the policy version and reviewed head SHA. The rolling PR comment includes enough evidence to understand the approval without opening 143.

## Rerun And Idempotency Behavior

Automatic commit reassessments use the PR head SHA and captured policy as their base identity. Explicit requests instead reserve a PR-and-GitHub-delivery identity before resolving mutable head or policy state, while each resulting session still captures the head and policy it actually assesses.

Rules:

- Redelivery of the same GitHub `review_requested` event reuses the assessment keyed to that delivery ID even if head or policy state changed afterward. A genuinely new explicit request after a non-approval creates a distinct assessment even for the same PR head SHA. If the original attempt never reached the durable worker queue, redelivery creates one immutable replacement attempt under the same delivery identity.
- A created PR issue comment that mentions the configured reviewer team follows the same explicit-request lifecycle. The comment ID provides a stable fallback identity when delivery metadata is unavailable, and the request context is bounded before it enters durable session/job state.
- If a new explicit request arrives while a review is already running for the same PR head SHA, retain a durable starter job until the active assessment finishes, then create the requested assessment. The request carries reviewer/team identity through the worker so GitHub assignment cleanup still occurs.
- After the first explicit reviewer request, new commits automatically enqueue a fresh assessment until 143 approves. Human review submissions/edits/dismissals, ordinary issue and inline review comment changes, review thread changes, PR title/description edits, readiness changes, completed checks, and commit-status updates do not enqueue reviewer sessions; the exception is a newly created PR conversation comment that explicitly mentions the configured reviewer team.
- If the PR receives new commits while review is running, mark the running session stale, stop before approval, and enqueue a new session for the new head SHA.
- If new commits arrive while agents are running, retain a durable starter job until the older assessment finishes. Re-read mutable PR metadata and check gates immediately before every final recommendation, but do not queue a new session solely because metadata, human review activity, or CI changed. A newer head coalesces duplicate webhook deliveries for that commit.
- A submitted 143 approval is monotonic for the PR. Later webhook changes and explicit reviewer rerequests are ignored so automation never dismisses or contradicts an approval that has already occurred.
- Reassessments update the rolling PR comment in place so the PR has one current visible 143 recommendation; the body identifies the latest assessed commit and UTC assessment time, and the backing 143 sessions remain immutable audit history.
- The original formal review carries the latest visible result until each reassessment updates the rolling comment, then returns to marker-only. When a previously non-approved result becomes acceptable, update the rolling comment and submit a separate, marker-only formal GitHub approval for the current head. Editing a submitted review body alone never represents a review-state transition.
- New-commit webhook deliveries are keyed by the PR head SHA, while explicit requests are keyed by GitHub delivery ID, so equivalent deliveries reuse the same assessment without collapsing distinct user requests.
- If the final GitHub review submission fails after session completion, retry idempotently using the assessment's stable review-output key; automatic keys capture head/policy state and explicit-request keys remain rooted in delivery identity.
- If inline comments were already posted for the same head SHA, update or supersede them where GitHub permits; otherwise avoid posting duplicate line comments.
- If policy changes while a review is running, finish under the policy version captured at session start unless an admin explicitly cancels and reruns.
- GitHub rate limits keep the attempt running in `waiting_for_github` with the worker's scheduled `retry_at`; the UI shows automatic recovery and does not offer a competing manual retry.
- When a retryable failure becomes terminal, an admin or member may call `POST /api/v1/code-reviews/{session_id}/retry`. The API validates the live PR head and monotonic-approval guard, creates a new immutable session/job under the current policy, and compare-and-set links the failed attempt through `superseded_by_session_id`. Completed, non-retryable, stale, cancelled, superseded, non-latest, closed-PR, and changed-head attempts return `409 Conflict`.
- If job dispatch fails after the replacement session is created, the service terminalizes that replacement as retryable, links it to the original attempt, records a failed-dispatch audit event, and returns an error. The UI refreshes to expose the replacement's retry action; no terminal attempt is reopened or deleted.

The Code reviews dashboard treats stale or explicitly replaced attempts as
superseded audit history, not failed or current review work. Its default
`activity_status=current` scope excludes rows whose status is `stale` or whose
`superseded_by_session_id` is set from both activity totals and headline
metrics. The activity status control can narrow current work to completed,
in-progress, failed, or cancelled attempts; **Superseded history** includes
stale attempts and failed attempts that already have replacements, while
**All attempts** restores the complete immutable history. Superseded rows use
neutral presentation and remain linked to their replacement rather than being
rewritten or deleted.

## Review Orchestration

Each code review session has one orchestrator agent that owns review fan-out, synthesis, and the final assessment rendered into the rolling PR comment.

Session shape:

```text
Code review session
  Orchestrator tab
    - reads PR metadata, policy, diff summary, description, CI/check state
    - starts reviewer tabs according to policy
    - waits for reviewer results or timeout
    - inspects the actual diff to determine whether description evidence is
      satisfied, missing, or not applicable
    - emits every finding as structured evidence with severity and confidence
    - emits typed human-review reasons only for non-code judgment such as
      architecture, ownership, operational risk, sensitive changes, or an
      explicit policy requirement
    - writes supporting review prose; the backend derives approval from the
      structured evidence and hard safeguards

  Reviewer tab: Codex
    - runs native /review against the PR diff
    - returns findings, severity, confidence, and approval concerns

  Reviewer tab: Claude Code
    - runs native /review against the PR diff
    - returns findings, severity, confidence, and approval concerns
```

Reviewer agents run native `/review` or the closest equivalent. They inspect and explain; they do not edit files or push commits. The orchestrator preserves raw outputs in the session and produces the GitHub review.

The worker validates that the orchestrator returned exactly one assessment for
every applicable structured description requirement, plus explicit `findings`
and `human_review_reasons` arrays. Unknown, duplicate, or omitted requirement
keys, invalid finding coordinates or enums, and unknown human-review reason
codes make the synthesis unusable for approval. It also captures a hash of the
PR title and body supplied to the orchestrator; if either changes before the
final decision, the assessment is stale and approval is withheld until a new
review runs. These checks prevent malformed or out-of-date agent output from
becoming approval authority.

The final rolling PR comment should include:

- decision: approved or comment only
- acceptable-risk result and policy version
- short summary of what changed
- P0 and P1 blocking findings with actionable details
- a count of P2 and P3 advisory observations, with their details retained in the linked 143 evidence rather than duplicated on GitHub
- reasons approval was withheld, when not approved
- link to the 143 code review session

Inline PR comments are first-class review output. The orchestrator selects the highest-value line-specific P0 and P1 findings and submits them with the formal review while the synthesized assessment converges onto the rolling PR comment and the formal body is reduced to its marker. P2 and P3 findings are persisted in 143 as advisory evidence, never affect the approval decision, never create inline GitHub comments, and appear on GitHub only as an advisory count. Any code issue important enough to block must therefore be represented as P0 or P1. The inline comment cap is configurable per policy, defaults to four, and can be raised up to ten. The orchestrator deduplicates overlapping findings and posts only concrete comments tied to changed lines. The bot never requests changes; non-acceptable PRs receive comment-only output.

Example inline comment selection:

```text
Inline comments to post
1. src/auth/session.go:88     Authorization edge case
2. frontend/src/Chart.tsx:44  Missing empty-state rendering
3. internal/db/users.go:121   Query should keep org_id filter in subquery

Suppressed
- 4 duplicate comments about the same auth branch
- 2 broad style suggestions with no specific line
- 1 low-confidence concern
- all P2 and P3 findings
```

## Prompt Versioning And Untrusted PR Content

Editable prompts are approval policy and are versioned with that policy. A review session captures:

- active policy id and version
- rendered orchestrator prompt version
- rendered reviewer prompt versions
- editable PR-description requirement prompt text
- editable acceptable-risk rubric prompt text
- reviewer agent/provider/model versions
- PR base SHA and head SHA
- PR title/body input hash used for the orchestrator's description assessment

LLM prompts should follow the existing 143 prompt architecture where possible: stable system prompts live in versioned templates, while org/repo editable policy text is stored as policy data and rendered at runtime. Exact rendered prompts used for approval must be recoverable from audit state.

PR descriptions, diffs, comments, file contents, and commit messages are untrusted input. Reviewer and orchestrator prompts must treat that material as evidence, not instructions. PR content cannot override:

- approval policy
- agent roster
- acceptable-risk thresholds
- GitHub posting behavior
- inline-comment cap
- secret handling
- system/developer instructions

Prompt-injection attempts in PR text or code comments are a separate typed hard-risk signal and make the PR non-acceptable by default unless policy says otherwise.

## Data Model Sketch

Potential tables:

```sql
code_review_policies (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    repository_id uuid references repositories(id),
    active boolean not null default true,
    version int not null,
    approval_mode text not null,
    description_policy jsonb not null,
    risk_policy jsonb not null,
    agent_roster jsonb not null,
    inline_comment_limit int not null default 4,
    created_at timestamptz not null default now()
);

-- Reviewer tabs persist their policy-captured override here so the generic
-- thread worker can execute each reviewer independently of the parent
-- orchestrator session's reasoning level.
ALTER TABLE session_threads
    ADD COLUMN reasoning_effort text;

code_review_session_metadata (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    session_id uuid not null references sessions(id),
    repository_id uuid not null references repositories(id),
    pull_request_id uuid not null references pull_requests(id),
    policy_id uuid not null references code_review_policies(id),
    base_sha text not null,
    head_sha text not null,
    trigger_source text not null,
    status text not null,
    phase text,
    status_code text,
    status_message text,
    retry_at timestamptz,
    last_error_at timestamptz,
    retryable_failure boolean not null default false,
    decision text,
    acceptable boolean,
    stale boolean not null default false,
    superseded_by_session_id uuid references sessions(id),
    review_output_key text not null,
    prompt_artifact_key text,
    github_review_id bigint,
    completed_at timestamptz,
    created_at timestamptz not null default now()
);

code_review_github_trigger_settings (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    repository_id uuid not null references repositories(id),
    installation_id bigint not null,
    active boolean not null default true,
    version int not null,
    team_slug text not null,
    team_name text not null,
    team_id bigint not null,
    repo_permission text not null,
    created_by_user_id uuid references users(id),
    created_at timestamptz not null default now()
);

code_review_agent_results (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    session_id uuid not null references sessions(id),
    agent_provider text not null,
    agent_model text,
    role text not null,
    status text not null,
    raw_output text,
    structured_result jsonb,
    created_at timestamptz not null default now()
);

code_review_findings (
    id uuid primary key,
    org_id uuid not null references organizations(id),
    session_id uuid not null references sessions(id),
    agent_result_id uuid references code_review_agent_results(id),
    dedupe_key text not null,
    severity text not null,
    confidence text not null,
    path text,
    start_line int,
    end_line int,
    summary text not null,
    body text not null,
    selected_for_inline boolean not null default false,
    github_comment_id bigint,
    created_at timestamptz not null default now()
);
```

Use insert-only versioning for policies so approvals always point to the policy that produced them. Enforce active policy uniqueness per `(org_id, repository_id)` with partial unique indexes over `active = true`, plus a separate org-default row where `repository_id` is null. GitHub trigger settings are also insert-only and repo-scoped; only one active trigger setting may exist per `(org_id, repository_id)`.

Code review execution state hangs off normal `sessions` through a dedicated session kind plus companion metadata keyed by `session_id`. Do not create a separate detail/run hierarchy.

Implementation notes:

- Store large raw agent transcripts in the existing session transcript/object-storage path when possible; keep `raw_output` bounded or replace it with an artifact pointer if output size becomes a concern.
- Model `approval_mode`, `decision`, `severity`, `confidence`, and `status` as typed string enums in Go models with validation tests.
- Validate `inline_comment_limit` as `1..10`; default new policies to `4`.
- Every table and query is tenant-scoped by `org_id`.
- Inline comments should be posted from `code_review_findings.selected_for_inline = true` and idempotently tied to `github_comment_id`.

## Open Questions

- Should approval require two clean agents, or should one clean agent be enough for very small docs/test-only PRs?
- Should 143-authored PRs have stricter defaults than human-authored PRs, or the reverse?
- What is the right reporting metric: approvals issued, human review hours saved, non-approval reasons, or post-approval revert/incident rate?

## Success Metrics

- Percentage of PRs requested from the bot that receive acceptable-risk approval.
- Percentage of bot-approved PRs merged without additional human requested-changes reviews.
- False approval rate, measured by revert, incident, or post-approval human blocker.
- Human review load reduction in repositories where the bot is enabled.
- Top non-approval reasons, used to tune PR templates and readiness checks.
- Median time from reviewer request to review decision.
