# Design: PR Readiness Checks

> **Status:** Implemented | **Last reviewed:** 2026-06-20

## Summary

PR Readiness Checks are an automatic pre-PR quality layer for 143 sessions. They answer: "Is this change ready for live review, or will it waste reviewer time?"

The shipped implementation exposes a one-click **Run readiness checks** action in session Overview, persists runs/checks/bypasses/policies, and gates builder PR creation on current, unbypassed blocking checks. Engineers and admins see the same checks in advisory mode by default and get a one-time Create PR preflight for missing, stale, or warning readiness with the concrete findings listed.

This is not a human approval system. Readiness must come from platform-observed evidence: session snapshots, diffs, agent review-loop results, command output, repository metadata, and GitHub/provider state. A person can click a button to trigger checks, but "a person said this is fine" is not readiness evidence.

Implemented core behavior:

- `pr_readiness_runs`, `pr_readiness_checks`, `pr_readiness_policies`, `pr_readiness_custom_checks`, `pr_readiness_bypasses`, and `pr_readiness_contexts` persist readiness state with `org_id` tenancy.
- Policies resolve as repository override -> org policy -> default policy. Legacy builder review compatibility settings are no longer honored; absent explicit policy means the default PR readiness policy applies.
- Checks store factual status separately from role-specific enforcement (`builder`, `engineer`, `admin`) and expose effective enforcement to the UI.
- Builders are blocked only by current-revision, non-bypassed blocking `failed`/`error` checks. Queued/running, missing, and stale readiness cannot be bypassed.
- The Overview card groups checks, shows per-check next actions and expandable evidence, and derives a visible stale blocker when the stored run no longer matches the session revision/snapshot.
- Built-ins include freshness, agent review, diff collection, test evidence after the current workspace revision timestamp, expanded risk flags, dependency/config risk, generated-file churn outside configured allowed paths, context completeness, and review-packet draftability.
- The review packet captures changed files, diff stats, context, risk flags, unknowns, timestamps, revision, checks, and bypass state. Newly created PR bodies get a short `143 readiness` footer when readiness data is available.
- Admins can manage prompt-based custom checks in settings. `.143/config.json` can declare `pr_readiness.checks`; the agent repo-prep path validates and materializes those definitions for the repository, including deactivating stale repo-config checks when the list becomes empty.
- Custom prompt checks use bounded context, Go-template prompt rendering, a central system prompt template, and structured JSON responses. Execution failures surface as `error` checks and inherit the check's configured enforcement.

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

## Readiness Card UI

Use the Overview Readiness Card for v1. Do not add a dedicated Readiness tab or alternate primary UI.

The card should live near the session Overview PR actions and use the same component family, density, borders, status treatment, and action hierarchy as the existing PR details/PR health card. It should feel like a sibling status surface: compact header, concise state line, grouped checks, and one clear primary action.

The two cards should mesh without competing:

- Before a PR exists, Readiness is the pre-PR status surface and should sit where PR details will later become relevant.
- After a PR exists, PR details/health becomes the primary shipping status, while Readiness becomes historical context or a collapsed summary.
- Do not stack two loud cards with competing warning treatments. If both cards are visible, use one shared visual language: status icon, muted metadata, compact action buttons, and quiet secondary links.
- Keep detailed evidence behind per-check expansion or a secondary detail view so the Overview stays scannable.

Design recommendation:

- Ship a button-driven Overview card first.
- If someone clicks `Create PR` with missing/stale/advisory readiness, use a lightweight confirmation that points back to the card; do not make preflight the primary readiness surface.
- Keep auto-runs optional and default off. The shipped policy can auto-run after session completion with a diff, or when Create PR is clicked with missing/stale/running readiness; manual `Run` / `Re-run` remains the primary control.

## Enforcement Model

Each check produces a factual result. Policy maps that result to role-specific enforcement.

| Mode | Behavior |
|---|---|
| `off` | Not evaluated. |
| `advisory` | Shows a warning, does not block PR creation. |
| `blocking` | Blocks PR creation for roles where the rule is blocking. |

## Bypass / Escape Hatch

Blocking checks will be imperfect, especially while custom checks and repo policies are new. The product should include a controlled break-glass bypass rather than forcing users into worse workarounds.

Recommendation:

- Bypass is enabled by default for builders, but always requires a reason and audit record.
- Admins can configure bypass scope for any role: disabled, admins only, admins plus engineers, builders only, or all PR-capable roles.
- Builders can bypass their own blockers when the builder bypass setting is enabled.
- Some checks can be marked non-bypassable by policy, especially future security/secret checks.
- Bypass requires a short reason and shows the exact blockers being bypassed.
- Bypass creates an audit event, increments org/repo/user/check bypass counters, and is attached to the readiness run and PR record.
- The PR footer/session review packet should state that readiness was bypassed and include the reason summary.
- Settings should expose bypass counts by repo, user, and check so repeated bypassing becomes visible.

This is a good idea if the product treats bypasses as operational debt, not as a normal workflow. It is a bad idea if bypass becomes the easiest path; then checks lose trust. The UI should make `Run checks` / `Fix with agent` / `Re-run` easier than bypass, and bypass should sit behind a secondary action such as `More -> Bypass readiness`.

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
| Dependency/config risk | Changed lockfiles, package manifests, infra config, runtime config, or generated files | Makes "why did this PR change dependencies?" visible early. |
| Generated-file churn | Changed generated/build artifacts outside allowed paths | Prevents noisy diffs that waste review time. |
| Context complete | Linked issue/attribution or explicit issue-less marker | Helps reviewers understand why the PR exists. |
| Review packet draftable | Session summary, diff summary, linked context, risk flags, and readiness results generated before PR creation | Makes the future PR description and reviewer handoff possible before the PR exists. |

Freshness is both a visible built-in check and a backend invariant. Every readiness result is tied to the workspace revision and snapshot key it evaluated; stale, queued/running, or missing results cannot satisfy builder blockers and cannot be bypassed.

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
| Migration pairing | Migration files plus model/store/API changes, or schema changes without expected code updates | Catches incomplete backend changes. |
| Test relevance | Test files changed or captured test command output appears related to touched package/path | Better than only knowing that some command ran. |
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

Implemented:

```text
Settings -> Pull requests -> PR readiness
```

The Pull requests settings surface includes:

- Enable readiness checks for builders.
- Enable advisory checks for engineers.
- Configure every built-in check as `off`, `advisory`, or `blocking`.
- Configure separate builder and engineer enforcement per check.
- Configure whether bypass is disabled, limited to specific roles, or enabled for all PR-capable roles.
- Show configured custom checks and their provenance (`org settings`, `repo settings`, or `.143/config.json`). Bypass rows are persisted for audit/reporting, and settings expose aggregate bypass counts by repository, check, and user for the selected org/repository scope.
- Require clean agent review for builders by default.
- Treat stale readiness as blocking for builders through backend enforcement.
- Configure test/sensitive-path/migration/large-diff checks and generated-file allowed paths without editing code.
- Configure repository overrides.

The implementation intentionally avoids arbitrary code execution. Built-ins are typed first-party checks; custom checks are prompt-only.

## Custom Repo Checks

Admins can add repo-specific checks. Custom checks are automatic prompt checks, not arbitrary unreviewed code execution.

Two authoring paths should feed the same stored model:

1. **Settings UI prompt checks.** An admin creates a check in `Settings -> Pull requests`, writes a prompt, and sets enforcement defaults. 143 stores the active check definition in the database with insert-only history.
2. **Repo config checks.** A repository can define checks in the existing `.143/config.json` file under a `pr_readiness` key. The agent repository-prep path reads the config after clone/auth setup, validates it, and materializes active definitions for that repository. Config-file changes are reviewable in GitHub like any other repo policy.

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

Prompt checks receive bounded readiness context: changed file list, diff stats, workspace revision, and recent logs. They return structured output:

```json
{
  "status": "passed | warning | failed",
  "summary": "short reviewer-facing explanation",
  "details": {},
  "action": "optional next action"
}
```

Safety and product constraints:

- Only admins can create or enable settings-defined prompt checks.
- Repo config checks are accepted only from the checked-out repository and are validated before materialization. An empty `pr_readiness.checks` list deactivates previously materialized repo-config checks for that repository.
- Custom checks can be turned `off`, `advisory`, or `blocking` like built-in checks. Checks configured `off` for every role are not evaluated.
- The UI should show whether a check came from org settings, repo settings, or `.143/config.json`.
- Prompt checks should be bounded and evidence-seeking; they should not become broad "review the whole PR again" prompts.
- Failed custom-check execution should surface as `error`; admins decide whether check errors block builders or degrade to advisory.
- Built-in generated-file churn uses policy-level `generated_file_allowed_paths` as exceptions for generated/build artifacts that are intentionally committed.

## Execution Roadmap

### Phase 1: Button-driven readiness

- Implemented.

### Phase 2: PR preflight

- Implemented for the web UI. Builders are blocked by backend enforcement. Engineers/admins can create PRs directly; advisory readiness findings remain visible in the readiness card instead of opening a pre-creation modal.

### Phase 3: Automatic run options

- Implemented. Policy settings default off. When enabled, readiness can run after session completion with a diff, or when Create PR is clicked with missing/stale/running readiness. Manual `Re-run` remains the primary recovery path.

### Phase 4: Custom checks

- Implemented as prompt-only checks with admin-managed settings definitions and materialized repo-config definitions. The readiness card shows provenance.

## Backend Shape

Implemented tables:

```text
pr_readiness_policies
- id, org_id, repository_id nullable, active, config jsonb
- created_by_user_id, created_at

pr_readiness_runs
- id, org_id, session_id, repository_id
- evaluated_workspace_revision, evaluated_snapshot_key nullable
- status, triggered_by_user_id nullable
- started_at, completed_at nullable, summary, review_packet jsonb

pr_readiness_checks
- id, org_id, run_id, session_id
- check_key, check_type, status
- enforcement plus enforcement_builder/engineer/admin
- provenance, source, title, summary, details jsonb, action

pr_readiness_custom_checks
- id, org_id, repository_id nullable, active
- source, check_key, name, prompt, path_filters jsonb, enforcement jsonb
- created_by_user_id nullable, created_at

pr_readiness_bypasses
- id, org_id, readiness_run_id, session_id, repository_id, pull_request_id nullable
- bypassed_by_user_id, reason, bypassed_checks jsonb
- created_at

pr_readiness_contexts
- org_id, session_id
- issue_less_reason, created_by_user_id, updated_by_user_id
- created_at, updated_at
```

Implemented API:

```text
POST /api/v1/sessions/{id}/pr-readiness-runs
GET  /api/v1/sessions/{id}/pr-readiness-runs/latest
POST /api/v1/sessions/{id}/pr-readiness-bypasses
GET  /api/v1/sessions/{id}/pr-readiness-context
POST /api/v1/sessions/{id}/pr-readiness-context
GET  /api/v1/pr-readiness-policies
PUT  /api/v1/pr-readiness-policies
GET  /api/v1/pr-readiness-custom-checks
POST   /api/v1/pr-readiness-custom-checks
PUT    /api/v1/pr-readiness-custom-checks/{id}
DELETE /api/v1/pr-readiness-custom-checks/{id}

Compatibility aliases remain:

```text
GET  /api/v1/sessions/{id}/readiness
POST /api/v1/sessions/{id}/readiness/run
```

Use SSE/polling to update the readiness card while a run is queued/running. If a check requires sandbox work, enqueue it as a durable job rather than blocking the request.

## Remaining Future Work

- Should `Run readiness checks` always run agent review, or should expensive checks be selectable later?
- How should expected test commands be inferred when a repo has no `.143` config?
- Should branch-only publish be allowed when PR readiness is blocked?
- Which risk flags belong in the PR footer versus only in the session?
- If org settings and `.143/config.json` define the same custom check key, should repo config override settings, merge with it, or be rejected as ambiguous?
- Richer reporting dashboards for bypass trends by repo/user/check type beyond the settings counters.

## Recommendation

Build one PR Readiness system with role-specific enforcement:

- Builders: blocking by default for clean agent review, with backend freshness enforcement.
- Engineers/admins: advisory by default.
- Checks: automatic evidence only.
- UX: start with an Overview card and `Run readiness checks`, then add PR preflight, then add optional automatic runs.

This gives non-engineering builders a clear path to live-review-ready PRs and gives engineers useful friction without making 143 feel like a process tax.
