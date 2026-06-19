# Design: PR Readiness Checks

> **Status:** Future | **Last reviewed:** 2026-06-19

## Summary

PR Readiness Checks are an automatic pre-PR quality layer for 143 sessions. They answer: "Is this change ready for live review, or will it waste reviewer time?"

The first version should expose a one-click **Run readiness checks** action in session Overview. Builders can be blocked from PR creation when required automatic checks fail or are stale. Engineers and admins see the same checks in advisory mode by default, so the system catches slop without taking over engineering judgment.

This is not a human approval system. Readiness must come from platform-observed evidence: session snapshots, diffs, agent review-loop results, command output, repository metadata, and GitHub/provider state. A person can click a button to trigger checks, but "a person said this is fine" is not readiness evidence.

Detailed intent:

- Help non-engineering builders know when agent-generated code is ready to ask engineers to review.
- Help engineers catch missing tests, stale diffs, risky changes, and agent review findings before opening a PR.
- Give reviewers a compact, trusted summary of what was checked before the PR reached them.
- Keep GitHub CI/CD as the authoritative post-PR validation system.

## Product Principles

1. **Automatic evidence only.** No human signoff, manual attestation, or free-form checklist item should be needed, requested, or treated as evidence to count readiness.
2. **One readiness signal, role-specific enforcement.** Builders can be blocked; engineers are advisory by default.
3. **Freshness matters.** A passing check only counts for the workspace revision it evaluated.
4. **Actionable over punitive.** Every warning or blocker should have a next action: run review, run tests, inspect changed files, fix with agent, or re-run checks.
5. **Review-time usefulness is the bar.** The goal is not a perfect quality score; it is fewer low-effort, under-explained, stale, or obviously broken PRs reaching live review.

## Core UX

Add a compact card near the session Overview PR actions.

Initial state:

```text
PR readiness
Not checked yet

[Run readiness checks]
```

Running state:

```text
PR readiness
Checking...
- Collecting diff
- Running agent review
- Checking test evidence
- Checking risk signals
```

Result state:

```text
PR readiness                                      [Re-run]
Ready with warnings

Passed
- Agent review completed cleanly
- Diff collected from latest snapshot

Warnings
- No test evidence found                         [Run tests]
- Auth-sensitive files changed                   [View files]

Blocked
- None
```

Builder-blocking state:

```text
PR readiness                                      [Re-run]
Blocked

Blocked
- Agent review found unresolved issues           [Fix with agent]
- Readiness is stale after latest file changes   [Re-run]

Create PR is disabled until blockers pass.
```

Engineers keep control. If an engineer clicks `Create PR` with missing, stale, or warning-level readiness:

```text
PR readiness has warnings

- No test evidence found
- Auth-sensitive files changed
- Agent review found 1 concern

[Create PR anyway] [Run readiness checks] [Cancel]
```

Do not show this repeatedly for unchanged results. If the warning set and workspace revision are unchanged, one acknowledgement is enough.

## UI Options

### Option 1: Overview Readiness Card

Recommended for v1. It is visible before shipping, works for builders and engineers, and leaves room for evidence and next actions.

Tradeoff: if the card grows too detailed, evidence needs a secondary view.

### Option 2: Create PR Preflight Dialog

Use as a companion, not the primary surface. It is ideal for engineer advisory warnings and stale checks when someone clicks `Create PR`.

Tradeoff: checks that take time feel worse when they start only after the user tries to publish.

### Option 3: Dedicated Readiness Tab

Defer. It is useful only if evidence becomes too large for Overview.

Tradeoff: lower discoverability for the exact moment the user needs readiness.

### Option 4: Automatic Background Checks

Add later as a setting. Background checks after session completion or diff changes are the right end state, but the first release should be button-driven so users understand cost, latency, and meaning.

Tradeoff: automatic checks can create token/worker spend and noisy stale states while work is still changing.

Recommendation: ship Option 1 first, add Option 2 next, and defer Options 3/4 until the check set is trusted.

## Enforcement Model

Each check produces a factual result. Policy maps that result to role-specific enforcement.

| Mode | Behavior |
|---|---|
| `off` | Not evaluated. |
| `advisory` | Shows a warning, does not block PR creation. |
| `blocking` | Blocks PR creation for roles where the rule is blocking. |

Defaults:

| Role | Default |
|---|---|
| Builder | Blocking for clean agent review and fresh backend-evaluated results; other checks advisory until configured. |
| Engineer/member | Advisory. |
| Admin | Advisory. |
| Viewer | Cannot create PRs. |

V1 should be conservative with blockers. The default builder blocker should be clean automatic agent review plus backend freshness enforcement. Missing tests, large diffs, migrations, and sensitive paths should start advisory but be configurable as blocking per org/repo.

## Recommended Checks

Every built-in check should be independently configurable by an admin as `off`, `advisory`, or `blocking`, with separate defaults for builders and engineers plus repository overrides.

| Check | Evidence | Why it matters |
|---|---|---|
| Agent review clean | 143's first-party Review loop result, or a captured native `/review` result from an existing agent that supports it | Catches obvious agent mistakes before live review while accepting both 143's built-in review loop and agent-native review workflows. |
| Test evidence present | Captured command output after latest revision | Avoids "I forgot to run tests" review waste. |
| Risk flags | Diff stats, sensitive paths, migrations, auth/billing/security path rules | Makes risky changes visible without needing human approval gates. |
| Context complete | Linked issue/attribution or explicit issue-less marker | Helps reviewers understand why the PR exists. |
| Review packet draftable | Session summary, diff summary, linked context, risk flags, and readiness results generated before PR creation | Makes the future PR description and reviewer handoff possible before the PR exists. |

Freshness is still mandatory, but it is a backend invariant rather than a user-facing check. Every readiness result must be tied to the workspace revision or snapshot ID it evaluated, and stale results must not satisfy builder blockers.

Do not count these as checks:

- Engineer approved the PR.
- Builder says tests ran.
- Reviewer looked at the diff.
- Admin accepted the risk.
- Free-form checklist items with no platform evidence.

## Additional Checks to Consider

These should not all ship in v1, but they are high-value candidates because they target common reviewer complaints:

| Check | Evidence | Why it matters |
|---|---|---|
| Scope alignment | Agent review or prompt check compares diff against linked issue/session goal | Catches PRs that solve a different problem than requested. |
| Debug artifact scan | Static scan of changed files for obvious `console.log`, debug flags, temporary comments, broad TODOs, or local-only code | Reduces sloppy cleanup comments in review. |
| Secret and credential scan | Existing secret scanner or lightweight changed-file scan | Prevents high-severity mistakes before a PR exists. |
| Dependency/config risk | Changed lockfiles, package manifests, infra config, runtime config, or generated files | Makes "why did this PR change dependencies?" visible early. |
| Migration pairing | Migration files plus model/store/API changes, or schema changes without expected code updates | Catches incomplete backend changes. |
| Test relevance | Test files changed or captured test command output appears related to touched package/path | Better than only knowing that some command ran. |
| Generated-file churn | Changed generated/build artifacts outside allowed paths | Prevents noisy diffs that waste review time. |
| Error-handling/logging scan | Agent review or prompt check over changed backend code | Catches swallowed errors, noisy logs, missing context, or user-visible failure gaps. |

The highest-leverage additions after v1 are `scope_alignment`, `debug_artifact_scan`, and `secret_and_credential_scan`. They catch visible slop without requiring teams to configure much.

## Reducing Review Slop

The readiness output should be optimized for reviewer trust, not just gatekeeping.

The system should produce a short **review packet**:

- What changed: concise diff summary grounded in files changed.
- Why it changed: linked issue, Sentry/Linear/support context, or explicit issue-less reason.
- What was checked: readiness results with timestamps and workspace revision.
- What is risky: large diff, sensitive paths, migrations, missing tests.
- What remains unknown: explicit warnings instead of implied confidence.

This packet should appear in the session and optionally in the 143 PR footer. Keep the PR footer short so it does not fight the repo's PR template.

Example:

```text
143 readiness:
- Agent review: clean
- Tests: evidence not found
- Diff: 8 files, +220/-41
- Risk: auth paths touched
- Policy: advisory for engineer
```

## Settings

Add:

```text
Settings -> Pull requests -> PR readiness
```

Keep v1 settings narrow:

- Enable readiness checks for builders.
- Enable advisory checks for engineers.
- Configure every built-in check as `off`, `advisory`, or `blocking`.
- Configure separate builder and engineer enforcement per check.
- Require clean agent review for builders by default.
- Treat stale readiness as blocking for builders through backend enforcement.
- Configure test/sensitive-path/migration/large-diff checks without editing code.
- Configure repository overrides.

Avoid a fully generic policy engine in v1. Use typed first-party checks with clear explanations.

## Custom Repo Checks

Admins should be able to add repo-specific checks after the built-in set is stable. Custom checks should be automatic prompt checks, not arbitrary unreviewed code execution.

Two authoring paths should feed the same stored model:

1. **Settings UI prompt checks.** An admin creates a check in `Settings -> Pull requests -> PR readiness`, chooses repositories, writes a short prompt, and sets enforcement defaults. 143 stores the active check definition in the database with version history.
2. **Repo config checks.** A repository can define checks in the existing `.143/config.json` file under a `pr_readiness` key. This keeps readiness policy with the same repo-owned config surface used for previews and sandbox setup instead of introducing a second `.143` config file. 143 reads the config at session start/readiness time, validates it, and materializes the active definitions for that repository. Config-file changes are reviewable in GitHub like any other repo policy.

Example config:

```json
{
  "pr_readiness": {
    "checks": [
      {
        "id": "no_analytics_schema_drift",
        "name": "Analytics schema compatibility",
        "type": "prompt",
        "enforcement": {
          "builder": "blocking",
          "engineer": "advisory"
        },
        "paths": {
          "include": ["analytics/**", "frontend/src/events/**"]
        },
        "prompt": "Review the diff for analytics event schema drift. Fail only if an event payload changes without a matching migration, compatibility note, or test evidence."
      }
    ]
  }
}
```

Prompt checks should receive a bounded readiness context: diff summary, changed file list, relevant hunks, linked issue context, existing readiness results, and repo conventions. They should return structured output:

```json
{
  "status": "passed | warning | blocked",
  "message": "short reviewer-facing explanation",
  "evidence": ["path/to/file.ts:42"],
  "confidence": "low | medium | high"
}
```

Safety and product constraints:

- Only admins can create or enable settings-defined prompt checks.
- Repo config checks are accepted only from the checked-out repository and should be validated against a schema.
- Custom checks can be turned `off`, `advisory`, or `blocking` like built-in checks.
- The UI should show whether a check came from org settings, repo settings, or `.143/config.json`.
- Prompt checks should be bounded and evidence-seeking; they should not become broad "review the whole PR again" prompts.
- Failed custom-check execution should surface as `error`; admins decide whether check errors block builders or degrade to advisory.

## Execution Roadmap

### Phase 1: Button-driven readiness

- Add `Run readiness checks`.
- Persist readiness runs against session ID and workspace revision.
- Gate builder PR creation on required non-stale results.
- Show advisory warnings for engineers.

### Phase 2: PR preflight

- When `Create PR` is clicked, detect missing/stale readiness.
- Builders are routed to `Run readiness checks`.
- Engineers get a one-time advisory dialog for the current revision.

### Phase 3: Automatic run options

- Admin setting to auto-run after a session reaches idle/completed with a diff.
- Optional auto-run when a user clicks `Create PR`.
- Manual `Re-run` remains available because users need a clear recovery path.

### Phase 4: Custom checks

- Add settings-defined prompt checks.
- Add `.143/config.json` `pr_readiness` ingestion.
- Show custom check provenance and enforcement in the readiness card.
- Keep custom checks behind admin-controlled enablement until latency and false-positive behavior are understood.

## Backend Shape

Suggested tables:

```text
pr_readiness_policies
- id, org_id, repository_id nullable, active, config jsonb
- created_by_user_id, created_at

pr_readiness_runs
- id, org_id, session_id, repository_id
- workspace_revision, snapshot_id nullable
- status, triggered_by, triggered_by_user_id nullable
- started_at, completed_at nullable, summary jsonb

pr_readiness_check_results
- id, org_id, readiness_run_id
- check_type, status, enforcement, evidence jsonb, message

pr_readiness_custom_checks
- id, org_id, repository_id nullable, active
- source, check_key, name, prompt, path_filters jsonb, enforcement jsonb
- created_by_user_id nullable, created_at
```

Suggested API:

```text
POST /api/v1/sessions/{id}/pr-readiness-runs
GET  /api/v1/sessions/{id}/pr-readiness-runs/latest
GET  /api/v1/pr-readiness-policies
PUT  /api/v1/pr-readiness-policies
GET  /api/v1/pr-readiness-custom-checks
POST /api/v1/pr-readiness-custom-checks
PATCH /api/v1/pr-readiness-custom-checks/{id}
```

Use SSE/polling to update the readiness card while a run is queued/running. If a check requires sandbox work, enqueue it as a durable job rather than blocking the request.

## Open Questions

- Should `Run readiness checks` always run agent review, or should expensive checks be selectable later?
- How should expected test commands be inferred when a repo has no `.143` config?
- Should branch-only publish be allowed when PR readiness is blocked?
- Which risk flags belong in the PR footer versus only in the session?
- If org settings and `.143/config.json` define the same custom check key, should repo config override settings, merge with it, or be rejected as ambiguous?

## Recommendation

Build one PR Readiness system with role-specific enforcement:

- Builders: blocking by default for clean agent review, with backend freshness enforcement.
- Engineers/admins: advisory by default.
- Checks: automatic evidence only.
- UX: start with an Overview card and `Run readiness checks`, then add PR preflight, then add optional automatic runs.

This gives non-engineering builders a clear path to live-review-ready PRs and gives engineers useful friction without making 143 feel like a process tax.
