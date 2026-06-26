# Design: Code Reviewer Bot And Acceptable-Risk Auto-Approval

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-26
>
> **Depends on:** [../overall.md](../overall.md), [../implemented/78-review-agent-loops.md](../implemented/78-review-agent-loops.md), [../implemented/107-pr-readiness-checks.md](../implemented/107-pr-readiness-checks.md), [../implemented/61-pr-state-sync-and-repair-actions.md](../implemented/61-pr-state-sync-and-repair-actions.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md)

## Summary

Create a GitHub-native **Code Reviewer** bot that teams can request as a PR reviewer. It evaluates PR description quality, runs a configurable multi-agent review, classifies risk against org/repo policy, and then either:

- leaves a synthesized review comment only, or
- approves the PR with review evidence when the PR meets the organization's acceptable-risk policy.

The goal is not to replace meaningful human review. It is to move basic acceptable-risk PRs out of the human queue so reviewers can focus on changes where judgment, architecture, ownership, or risk actually matter.

Implemented foundation:

- versioned insert-only code review policies with org defaults and repository overrides
- code review session metadata, agent result, and finding tables tied to normal `sessions`
- typed Go models and `pgx` stores for policies, review metadata, agent evidence, and findings
- deterministic acceptable-risk evaluator, starter policy templates, final-review body rendering, and inline finding selection helpers
- GitHub `review_requested` webhook adapter for configured bot reviewer identities, including local PR mirror creation for human-authored PRs
- service-layer code review request orchestration that resolves/materializes policy, marks stale older heads, reuses running sessions, creates normal code-review sessions, and enqueues `run_code_review`
- conservative `run_code_review` worker handler that records an orchestrator result and final comment-only review evidence when live agent/GitHub submitters are not configured
- `/api/v1/code-reviews`, `/api/v1/code-reviews/templates`, `/api/v1/code-reviews/{id}/evidence`, and `/api/v1/code-review-policies` API surface
- top-level `Code reviews` dashboard surface with Reviews, Configurations, Insights, enablement, approval mode, threshold, prerequisite, timeout, and cost controls

Still pending:

- live multi-agent worker orchestration that fans out reviewer tabs and runs native `/review`
- GitHub App review submission, inline-comment retry/update, and stale requested-reviewer cleanup
- full prompt artifact storage and recovery for rendered approval prompts

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
3. **GitHub remains the action surface.** Reviewer assignment triggers the workflow; GitHub reviews carry the result.
4. **Organizations own policy.** Description requirements, agent roster, prompts, risk thresholds, and approval behavior are org/repo configurable.
5. **Every decision is inspectable.** Each approval or non-approval links to the 143 session, policy version, reviewed SHA, and agent evidence.

## Recommendation

Ship **Reviewer Bot With 143 Code Review Sessions**, triggered by explicit GitHub reviewer assignment in v1.

Recommended v1 scope:

- GitHub App-backed bot reviewer identity.
- `review_requested` trigger for selected repositories.
- Normal 143 code review sessions keyed by org, repository, PR, head SHA, and policy version.
- Editable PR-description policy and acceptable-risk starter templates.
- Two reviewer agents plus one orchestrator by default.
- GitHub final review with summary body and a configurable number of inline comments.
- Approval only for acceptable PRs; otherwise comment with escalation reasons.
- Idempotent reruns for duplicate requests, stale heads, and GitHub review retries.
- Top-level `Code reviews` surface for filtered sessions, configuration, and later insights.

Defer:

- Always-on auto-review.
- Code-fixing from the reviewer bot.
- Approval for high-risk directories.
- Arbitrary custom scripts.
- Automatic policy learning from past approvals.

## Core Flow

```text
Developer opens or updates PR
        |
        v
Developer requests "143 Code Reviewer" in GitHub
        |
        v
143 receives review_requested webhook and creates a code review session
        |
        v
Check PR description policy
        |
        v
Run configured review agents against the PR diff and context
        |
        v
Orchestrator agent synthesizes findings and risk decision
        |
        v
If acceptable risk and approval prerequisites pass:
  submit GitHub approval with evidence
Else:
  submit a GitHub review comment with escalation reasons
```

Reviewer assignment should be explicit in v1. Auto-running can come later after teams trust the signal.

## Product Surfaces

### GitHub Reviewer

Primary interaction:

- The installed GitHub App exposes a reviewer identity such as `143-code-reviewer`.
- A user requests the bot as a reviewer on a PR.
- The bot posts one pending/running status, then submits a final GitHub review with a summary body and a configurable number of inline comments on changed lines.

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
- description policy results
- per-agent raw review outputs
- orchestrator synthesis
- risk rubric inputs and final classification
- approval eligibility checklist
- GitHub review submitted by the bot
- audit trail for policy version, agent versions, and prompts

The GitHub review body always links to the session for both approval and non-approval paths.

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
| Reviews | Filtered session list containing code review sessions, with PR, repository, author, risk, decision, status, requested-at, and completed-at columns. |
| Configurations | Org and repository code review policies: enablement, description requirements, risk thresholds, agent roster, orchestrator, and approval mode. |
| Insights | Lightweight reporting on approvals, non-approvals, escalation reasons, false approvals, and review latency. This can be deferred until enough usage exists. |

The Reviews tab reuses the normal session list/detail route. Primary action opens the session; secondary actions open the GitHub PR, policy version, or final GitHub review.

Reviews wireframe:

```text
Code reviews
[Reviews] [Configurations] [Insights]

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
[Reviews] [Configurations] [Insights]

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
- timeout and cost ceilings
- whether disagreement forces human review

Defaults:

- Run two reviewer agents when approving is enabled.
- Use one orchestrator agent that is not one of the reviewer agents when available.
- Treat material disagreement as not acceptable risk by default.

Reviewer agents run in isolated read-only review sandboxes at the PR head SHA. They inspect only; PR repair/revision actions handle fixes.

### Acceptable-Risk Definition

Acceptable risk is fully configurable by org admins with optional repository overrides. 143 ships conservative defaults, but approval always comes from the active org/repo policy.

Risk evaluation combines deterministic signals with synthesized review findings.

Configurable deterministic signals:

- small diff by configured file and line thresholds
- no sensitive paths touched
- no migrations, auth, billing, permissions, crypto, infra, dependency lockfile, or generated artifact surprises
- CI/checks are green or not required by policy
- PR description passes required sections
- branch is mergeable and up to date according to policy
- author is in an eligible role or team
- no unresolved human review threads

Configurable synthesized signals:

- reviewer agents found no blocking correctness, security, or maintainability issues
- orchestrator agrees the change matches the stated intent
- no reviewer-agent disagreement on severity
- no meaningful unknowns remain

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
- No unresolved human blocking review exists.
- No reviewer-agent blocking finding exists.
- Orchestrator classifies the PR as acceptable under the active risk policy.
- Policy allows approval for this repository, author class, and changed paths.
- The bot has not already approved a stale previous head.

The bot should not approve:

- its own policy/config changes unless explicitly allowed
- changes to GitHub workflows, deployment, auth, billing, permissions, secrets, or infrastructure by default
- dependency updates with lockfile changes by default
- PRs with merge conflicts
- PRs from untrusted forks unless explicitly enabled
- PRs where required context cannot be fetched

Every approval stores the policy version and reviewed head SHA. The GitHub review body includes enough evidence to understand the approval without opening 143.

## Rerun And Idempotency Behavior

Code review sessions are keyed to a PR head SHA. Each head is a separate reviewable revision.

Rules:

- If the bot is requested while a review is already running for the same PR head SHA, reuse the existing session and update the pending GitHub status/comment instead of starting another session.
- If the PR receives new commits while review is running, mark the running session stale, stop before approval, and either enqueue a new session for the new head SHA or ask the user to request the bot again according to repository policy.
- If the bot previously approved an older head SHA, that approval must not count as evidence for the new head SHA. The new review should link to the prior session as historical context only.
- If the final GitHub review submission fails after session completion, retry idempotently using a stable review-output key for the session/head SHA/policy version.
- If inline comments were already posted for the same head SHA, update or supersede them where GitHub permits; otherwise avoid posting duplicate line comments.
- If policy changes while a review is running, finish under the policy version captured at session start unless an admin explicitly cancels and reruns.

The Code reviews list should show stale and superseded sessions distinctly from failed sessions. Stale means "reviewed a head that is no longer current," not "the agents failed."

## Review Orchestration

Each code review session has one orchestrator agent that owns review fan-out, synthesis, and the final GitHub review body.

Session shape:

```text
Code review session
  Orchestrator tab
    - reads PR metadata, policy, diff summary, description, CI/check state
    - starts reviewer tabs according to policy
    - waits for reviewer results or timeout
    - compares findings against acceptable-risk policy
    - writes final synthesized review

  Reviewer tab: Codex
    - runs native /review against the PR diff
    - returns findings, severity, confidence, and approval concerns

  Reviewer tab: Claude Code
    - runs native /review against the PR diff
    - returns findings, severity, confidence, and approval concerns
```

Reviewer agents run native `/review` or the closest equivalent. They inspect and explain; they do not edit files or push commits. The orchestrator preserves raw outputs in the session and produces the GitHub review.

The final GitHub review should include:

- decision: approved or comment only
- acceptable-risk result and policy version
- short summary of what changed
- agent findings grouped by severity
- non-blocking comments that are worth surfacing
- reasons approval was withheld, when not approved
- link to the 143 code review session

Inline PR comments are first-class review output. The orchestrator selects the highest-value line-specific findings and submits them with the synthesized review body. The inline comment cap is configurable per policy, defaults to four, and can be raised up to ten. The orchestrator deduplicates overlapping findings and posts only concrete comments tied to changed lines. The bot never requests changes; non-acceptable PRs receive comment-only output.

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

LLM prompts should follow the existing 143 prompt architecture where possible: stable system prompts live in versioned templates, while org/repo editable policy text is stored as policy data and rendered at runtime. Exact rendered prompts used for approval must be recoverable from audit state.

PR descriptions, diffs, comments, file contents, and commit messages are untrusted input. Reviewer and orchestrator prompts must treat that material as evidence, not instructions. PR content cannot override:

- approval policy
- agent roster
- acceptable-risk thresholds
- GitHub posting behavior
- inline-comment cap
- secret handling
- system/developer instructions

Prompt-injection attempts in PR text or code comments should become review findings and make the PR non-acceptable by default unless policy says otherwise.

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

Use insert-only versioning for policies so approvals always point to the policy that produced them. Enforce active policy uniqueness per `(org_id, repository_id)` with partial unique indexes over `active = true`, plus a separate org-default row where `repository_id` is null.

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
- How much of the orchestrator synthesis should be stored as structured JSON versus markdown?
- What is the right reporting metric: approvals issued, human review hours saved, non-approval reasons, or post-approval revert/incident rate?

## Success Metrics

- Percentage of PRs requested from the bot that receive acceptable-risk approval.
- Percentage of bot-approved PRs merged without additional human requested-changes reviews.
- False approval rate, measured by revert, incident, or post-approval human blocker.
- Human review load reduction in repositories where the bot is enabled.
- Top non-approval reasons, used to tune PR templates and readiness checks.
- Median time from reviewer request to review decision.
