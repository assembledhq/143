# 42 - Eval Task Builder

> **Status:** Not Started | **Last reviewed:** 2026-03-30
>
> **Depends on:** [16-ai-agent-evals.md](future/16-ai-agent-evals.md) (grading architecture, release gates), [43-prompt-and-input-versioning.md](43-prompt-and-input-versioning.md) (prompt/document freezing)

## Problem

We have a comprehensive eval *framework* design (doc 16) but no way for users to actually **create eval tasks** from their own codebase. Today, if you want to evaluate whether a new model or prompt change improves agent quality, you have to manually construct test cases. There's no way to:

1. Point at a moment in your repo's history and say "recreate the state before this PR was merged, then see if the agent can produce something equivalent."
2. Define what "good" looks like with multiple weighted scoring criteria.
3. Run those evals against different models, prompts, or PM context documents.
4. Automatically discover good eval candidates from your own PR history.

This doc designs the **Eval Task Builder** — a settings-driven system for creating, managing, and running eval tasks grounded in real codebase history.

---

## Core Concepts

### Eval Task

An eval task is a reproducible challenge: given a codebase at a specific commit, a problem description, and a set of context inputs, can the agent produce a solution that meets the scoring criteria?

```
EvalTask {
  id                  UUID
  org_id              UUID
  repo_id             UUID
  name                string          -- human-readable label
  description         string          -- what this eval tests

  -- Codebase snapshot
  base_commit_sha     string          -- checkout point (before the change)
  solution_commit_sha *string         -- optional: the known-good merged result
  solution_diff       *string         -- cached diff of the known-good solution

  -- Problem definition
  issue_description   string          -- what the agent is told to fix/build
  issue_context       jsonb           -- additional context (Sentry trace, Linear ticket, etc.)

  -- Input configuration (frozen references, see doc 43)
  server_deploy_sha   string          -- pins all prompt templates (they're in the binary)
  pm_document_set_pin_id *UUID        -- pinned PM document set
  org_settings_version_id *UUID       -- pinned org settings version
  memory_snapshot     jsonb           -- pinned learned conventions (memory IDs + content)
  sandbox_image_digest *string        -- pinned sandbox container image
  context_overrides   jsonb           -- any additional context injections (PM guidance, etc.)

  -- Scoring
  scoring_criteria    jsonb           -- array of ScoringCriterion (see below)
  pass_threshold      float           -- minimum weighted score to pass (0.0-1.0)

  -- Metadata
  source              string          -- "manual", "pr_bootstrap", "failure_derived"
  source_pr_number    *int            -- PR this was bootstrapped from (if applicable)
  complexity          string          -- "trivial", "simple", "moderate", "complex"
  tags                text[]          -- freeform tags for filtering/slicing
  created_by          UUID
  created_at          timestamp
  updated_at          timestamp
  archived_at         *timestamp
}
```

### Scoring Criterion

Each eval task has one or more scoring criteria. Criteria are evaluated independently and combined into a weighted final score.

Every platform in the eval ecosystem (Braintrust, Langfuse, LangSmith, DeepEval, Arize Phoenix) converges on the same insight: **there are only two kinds of checks** — things a script can verify deterministically, and things that require judgment. We follow the same pattern.

```
ScoringCriterion {
  name            string    -- e.g. "tests_pass", "minimal_diff", "correct_files_touched"
  notes           string    -- describe what good looks like and what would fail
  grader_type     string    -- "code_check" | "llm_judge"
  grader_config   jsonb     -- type-specific configuration (see below)
  weight          float     -- relative weight in final score (weights are normalized)
  required        bool      -- if true, failing this criterion fails the entire eval
}
```

### Two Grader Primitives

Industry best practice (per Braintrust, LangSmith, Arize): use code-based checks where you can, LLM judges where you must, human review to calibrate both. Start binary (pass/fail) — numeric scales (1-10) produce inconsistent results from LLM judges.

#### `code_check` — Deterministic, fast, cheap

Runs a command in the sandbox against the agent's output. Exit code 0 = pass, non-zero = fail. Optional JSON on stdout for a numeric score.

```json
{ "command": "make test", "timeout_seconds": 300 }
```

This single primitive covers everything that was previously split across multiple grader types:
- **CI/tests**: `{ "command": "make test" }`
- **File scope**: `{ "command": "git diff --name-only | grep -q 'src/auth.go' && ! git diff --name-only | grep -q 'go.mod'" }`
- **Regression tests**: `{ "command": "go test -run TestAuth ./..." }`
- **Custom scripts**: `{ "command": ".143/eval-scripts/check_auth.sh" }`
- **Lint/format**: `{ "command": "golangci-lint run ./..." }`

The `notes` field on the criterion documents what the check verifies, for human understanding.

#### `llm_judge` — For anything requiring judgment

The user's `notes` field IS the rubric. The system wraps it in a judge prompt, passes the agent's diff + context, and gets back pass/fail + reasoning.

```json
{ "model": "claude-sonnet-4-6" }
```

The `notes` describe what to evaluate:
> "The fix should be minimal — only touch files directly related to the auth token refresh bug. No unrelated refactoring. The diff should be under 100 lines. A new test should be added that would have caught the original bug."

Best practices baked in (from Braintrust/DeepEval research):
- **One dimension per criterion.** Don't ask a single judge to evaluate "correctness AND code quality AND test coverage." Create separate criteria.
- **Binary by default.** The judge returns pass/fail + reasoning. If you need a numeric score, set `"output": "score"` in config to get 0.0-1.0.
- **Reasoning is always returned.** Every LLM judge call includes chain-of-thought explaining the judgment. This is non-negotiable for debuggability.
- **Solution diff comparison.** If the eval task has a `solution_diff`, it's automatically included in the judge context so it can compare approaches. No separate "diff_similarity" grader needed — just mention it in the notes.

### Why only two types?

The eval framework research across Braintrust, LangSmith, Langfuse, DeepEval, Arize Phoenix, and Patronus AI shows clear convergence:

1. **Every platform offers code + LLM + human.** No platform has dropped either code or LLM graders.
2. **The trend is toward simpler authoring, not fewer grader types.** Braintrust's "Loop" lets users describe failures in plain language and auto-generates scorers. DeepEval's G-Eval takes criteria in everyday language. The platforms are making it easier to create criteria, not adding more grader type taxonomies.
3. **Six grader types is authoring friction, not capability.** `file_scope`, `regression_test`, `diff_similarity` are all just specific instances of either a shell command or an LLM rubric instruction. Having them as separate types means more dropdowns, more type-specific config forms, and more docs to maintain — with no additional capability.

---

## Codebase Snapshot Mechanism

The core capability: pull out the repo at the state it was in *before* a change landed.

### How it works

1. **At task creation time**, the user specifies (or the bootstrapper identifies) the `base_commit_sha` — the parent commit of the PR's merge commit.
2. When an eval run starts, the sandbox clones the repo and checks out `base_commit_sha`. This gives the agent the exact codebase state that existed when the original work began.
3. The `solution_commit_sha` (merge commit) and `solution_diff` are stored for comparison grading but never shown to the agent.

### Snapshot storage

We do NOT store full repo snapshots. Git is the snapshot mechanism:

- `base_commit_sha` is immutable — it's a content-addressed hash
- The sandbox `git clone --branch main <repo> && git checkout <base_commit_sha>` at eval start
- For orgs with large repos, we shallow-clone to the relevant commit range

### Ensuring snapshot integrity

- On task creation, verify the commit exists and is reachable
- Store the repo's remote URL at creation time to detect repo renames/deletions
- If a repo is force-pushed and the commit becomes unreachable, mark the eval task as `snapshot_broken` and surface it in the UI

### Config overlay: testing repo configuration changes

Eval tasks pin the codebase at `base_commit_sha`, which includes the config files (AGENTS.md, CLAUDE.md, .claude/, .143/) that existed at that point. But a common need is: "I've been iterating on my AGENTS.md on a branch — does it actually make the agent better?" You want the source code at the historical state but with your new config overlaid.

#### End-to-end user experience

**Scenario: You've rewritten AGENTS.md on a branch called `better-agents-md`.**

1. Go to **Settings > Evals**. You see your eval tasks with their baseline scores.

2. Select the tasks you want to test (or "Select all"), click **Run Batch**.

3. The batch run panel appears:

```
┌──────────────────────────────────────────────────────────────┐
│  Run Batch                                                   │
│                                                              │
│  Selected: 8 eval tasks                                      │
│                                                              │
│  Configurations to compare:                                  │
│                                                              │
│  ┌─ Config A (baseline) ──────────────────────────────────┐  │
│  │  Model:   [claude-sonnet-4-6    ▼]                     │  │
│  │  Config:  [base_commit (default) ▼]  ← no overlay     │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌─ Config B ─────────────────────────────────────────────┐  │
│  │  Model:   [claude-sonnet-4-6    ▼]                     │  │
│  │  Config:  [better-agents-md     ▼]  ← branch picker   │  │
│  │                                                        │  │
│  │  Preview:                                              │  │
│  │   AGENTS.md           changed (±47 lines)              │  │
│  │   CLAUDE.md           unchanged                        │  │
│  │   .claude/            unchanged                        │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
│  [+ Add another configuration]                               │
│                                                              │
│  [Run 16 evals]  (8 tasks × 2 configs)                       │
└──────────────────────────────────────────────────────────────┘
```

4. The **Config** dropdown is a branch/tag picker (same component used elsewhere for branch selection in the app). It lists repo branches and shows a search field. Selecting a branch means: "use the config files from this branch instead of the ones at `base_commit_sha`."

5. When you select a branch, the panel shows a **preview** of which config files differ between the branch and the eval task's `base_commit_sha`. This gives you immediate feedback — you can see that AGENTS.md changed but CLAUDE.md didn't.

6. After runs complete, the results page shows a **comparison matrix**:

```
┌──────────────────────────────────────────────────────────────┐
│  Batch Results                                               │
│                                                              │
│  Task                      │ Baseline │ better-agents-md     │
│  ─────────────────────────-┼──────────┼─────────────────     │
│  Auth token refresh        │ 0.72     │ 0.89  ▲ +0.17       │
│  Pagination fix            │ 0.91     │ 0.88  ▼ -0.03       │
│  Rate limiter bug          │ 0.65     │ 0.81  ▲ +0.16       │
│  Webhook retry logic       │ 0.78     │ 0.85  ▲ +0.07       │
│  ...                       │          │                      │
│  ─────────────────────────-┼──────────┼─────────────────     │
│  Average                   │ 0.77     │ 0.86  ▲ +0.09       │
│  Pass rate (≥0.7)          │ 6/8      │ 8/8                  │
└──────────────────────────────────────────────────────────────┘
```

7. You see that the new AGENTS.md improved average scores by +0.09 and brought all tasks above the pass threshold. You merge the branch.

**Scenario: Comparing two different AGENTS.md approaches.**

Same flow, but you add a third configuration pointing to a different branch. The matrix gets a third column. You can compare as many configs as you want.

**Scenario: Testing a model change alongside a config change.**

Config A: sonnet + current AGENTS.md. Config B: opus + current AGENTS.md. Config C: sonnet + new AGENTS.md. This tells you whether the AGENTS.md improvement is worth more or less than upgrading the model.

#### What gets overlaid

The overlay replaces a fixed set of **repo config paths**:

| Path | What it controls |
|------|-----------------|
| `AGENTS.md` | Agent behavioral conventions, code patterns, testing instructions |
| `CLAUDE.md` | Claude-specific context and instructions |
| `.claude/` | Claude Code configuration directory (settings, commands, skills) |
| `.143/` | 143-specific configuration (eval scripts, learned-conventions.md, etc.) |

This is a file-level replacement, not a merge — the branch version completely replaces the `base_commit_sha` version for each file that exists on the branch. Source code files outside these paths are untouched.

#### How it works under the hood

When `config_ref` is set on an EvalRun, the sandbox setup becomes:

```
1. git clone <repo> && git checkout <base_commit_sha>     # source code at historical state
2. git show <config_ref>:AGENTS.md > AGENTS.md             # overlay config from branch
   git show <config_ref>:CLAUDE.md > CLAUDE.md             # (skipped if file doesn't exist on branch)
   # same for .claude/ and .143/ directories
```

#### What this does NOT cover

Platform-level changes (model, agent type, org settings, PM documents) are not repo files — they're selected directly in the run configuration. The config overlay is specifically for **files that live in the git repo**.

---

## Input Freezing

An eval is only reproducible if the inputs are frozen. A thorough audit of `orchestrator.RunAgent()` and the agent adapter layer identified **every input** that flows into an agent run. Here's what needs freezing and how:

### Inputs versioned by doc 43

These are covered in detail in [43-prompt-and-input-versioning.md](43-prompt-and-input-versioning.md):

| Input | Current state | Doc 42 solution |
|-------|--------------|-----------------|
| **Prompt templates** (19 templates in `internal/prompts/templates/`) | Embedded in Go binary, identical across all orgs, change only on deploy | `server_deploy_sha` — prompts are code, not config. The deploy SHA pins them all. |
| **PM documents** (roadmap, philosophy, context) | Overwritten in place, no history | Insert-only versioning on `pm_documents` with `active` flag |
| **Org settings** (token limits, context limits, autonomy) | Already insert-only versioned | Record active version ID in manifest |
| **Memory context** (learned conventions from review feedback) | Changes over time, not snapshotted | Snapshot selected memory IDs + content in manifest |
| **Sandbox image version** | Uses mutable `"143-sandbox:latest"` | Pin to image digest, record in manifest |
| **Integration skills doc** (auto-generated CLI tool docs) | Changes when integrations change | Content hash in manifest |

### Inputs already versioned

| Input | Why it's already covered |
|-------|------------------------|
| **Source code** | Git — checking out `base_commit_sha` gives exact state |
| **Repo config** (AGENTS.md, CLAUDE.md, .claude/, .143/) | Also pinned by `base_commit_sha` by default. But can be **overlaid** from a different branch/commit via `config_ref` on the eval run — see "Config overlay" above. This is how you test repo configuration changes against your eval suite. |
| **Product context** (org settings philosophy/direction/focus) | Already captured as `product_context_snapshot` on PMPlan |

### Inputs that are per-task (not versioned, captured directly)

| Input | Where it lives |
|-------|---------------|
| **Issue description + context** | Stored directly on the EvalTask (`issue_description`, `issue_context`) |
| **PM task guidance** (approach, risk, reasoning) | Stored on session as `PMApproach`/`PMReasoning` — captured in eval task's `context_overrides` |
| **Model + model config** | Selected per eval run, stored on EvalRun |
| **Complexity tier** | Stored on eval task as `complexity` |

### Inputs intentionally NOT versioned

| Input | Why |
|-------|-----|
| **Credentials** (API keys, tokens) | Secrets — never stored in version history. Manifest records credential *source type* (user/team/org) since resolution order affects which endpoint is hit |
| **Revision context** (previous diff + reviewer feedback) | Only applies to retry runs, not relevant for evals which always start fresh |

---

## Settings UI

The eval task builder lives in **Settings > Evals** (new section alongside the existing Agent, Prioritization, and Integrations settings).

**Component conventions:** All UI in this section should use the existing shadcn component library and follow the same table/list/card patterns used on the Sessions and Projects pages. Specifically:

- **Eval task list** — Use the same table structure as the Sessions list: shadcn `Table` with sortable column headers, status badges (`Badge`), and row-click navigation to detail view. Filter tabs (All | Manual | PR-Bootstrapped | Archived) use the same `Tabs` component as Sessions page filters.
- **Create flow** — Multi-step form using shadcn `Card`, `Input`, `Textarea`, `Select`, `Switch`, `Slider`, and `Button`. Same side-sheet or full-page pattern used for project creation.
- **Batch run panel** — `Dialog` or `Sheet` (consistent with how the Sessions page handles "New Session" creation). Configuration rows use `Card` with nested form fields.
- **Results matrix** — shadcn `Table` with score cells. Delta indicators use the same color tokens (green/red) and `Badge` variants used elsewhere for status changes.
- **Branch picker** — Reuse the same branch selector component used in the PR creation flow.

### Evals Settings Page

```
┌─────────────────────────────────────────────────────────────┐
│  Settings > Evals                                           │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  [+ Create Eval Task]  [Bootstrap from PR History]          │
│                                                             │
│  ┌─ Filter: All | Manual | PR-Bootstrapped | Archived ─┐   │
│  │                                                       │  │
│  │  ┌──────────────────────────────────────────────────┐ │  │
│  │  │ Auth token refresh regression          complex   │ │  │
│  │  │ PR #247 · base: a1b2c3d · 4 criteria · 12 runs │ │  │
│  │  │ Last run: 3h ago · claude-opus-4-6: 0.87       │ │  │
│  │  │                     claude-sonnet-4-6: 0.72      │ │  │
│  │  └──────────────────────────────────────────────────┘ │  │
│  │                                                       │  │
│  │  ┌──────────────────────────────────────────────────┐ │  │
│  │  │ Pagination fix on issues list           simple   │ │  │
│  │  │ PR #189 · base: d4e5f6a · 3 criteria · 8 runs  │ │  │
│  │  │ Last run: 1d ago · codex: 0.91                  │ │  │
│  │  └──────────────────────────────────────────────────┘ │  │
│  │                                                       │  │
│  └───────────────────────────────────────────────────────┘  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Create Eval Task Flow

**Step 1: Choose source**

- **From PR** — Pick a merged PR. System auto-fills base commit, solution diff, and proposes a problem description from the PR body/linked issue.
- **From commit range** — Specify base and optional solution commits manually.
- **From scratch** — No codebase anchor. Define a greenfield task.

**Step 2: Define the problem**

- Issue description (what the agent should be told)
- Additional context (paste Sentry trace, Linear ticket body, etc.)
- Complexity tag

**Step 3: Configure scoring criteria**

Repeatable form section. Each criterion:
- Name
- Notes — describe what good looks like and what would fail (free-text, natural language)
- Type toggle: **Code check** (can a script verify this?) or **LLM judge** (needs judgment?)
  - Code check → command field + optional timeout
  - LLM judge → model selector (defaults to org's configured model)
- Weight slider
- Required checkbox

**Step 4: Pin inputs**

- Server deploy SHA: defaults to current deploy (pins all prompt templates)
- PM documents: "Current" or pick from snapshot history (requires doc 43)
- Additional context overrides (JSON editor, for PM guidance, memory, etc.)

**Step 5: Review and save**

---

## Running Evals

### Single task run

From the eval task detail page, click **Run** and choose:
- **Model**: dropdown of available models (claude-opus-4-6, claude-sonnet-4-6, codex, gemini-cli, etc.)
- **PM documents**: override the pinned document set for this run

The run:
1. Spins up a sandbox container
2. Clones the repo at `base_commit_sha`
3. Injects the selected prompts, PM documents, and issue description
4. Runs the coding agent
5. Collects the agent's diff output
6. Runs each scoring criterion against the diff
7. Computes weighted final score
8. Stores the full result (diff, per-criterion scores, trace, token usage)

### Batch runs (compare models/prompts)

From the evals overview, select multiple tasks and click **Run Batch**:
- Choose N model/prompt combinations to compare
- System runs all combinations (parallelized across sandbox pool)
- Results displayed in a comparison matrix

### Eval Run Result

```
EvalRun {
  id                  UUID
  task_id             UUID
  org_id              UUID

  -- Configuration used (full input manifest per doc 43)
  input_manifest      jsonb           -- complete frozen inputs (see doc 43 §7)
  model               string
  server_deploy_sha   string          -- pins prompt templates
  pm_document_set_pin_id UUID
  config_ref          *string         -- optional: branch/SHA for repo config overlay
  context_overrides   jsonb

  -- Output
  agent_diff          text
  agent_trace         jsonb           -- full execution trace
  token_usage         jsonb

  -- Scoring
  criterion_results   jsonb           -- per-criterion: { name, score, pass, details }
  final_score         float
  passed              bool

  -- Metadata
  duration_seconds    int
  sandbox_id          string
  started_at          timestamp
  completed_at        timestamp
  error               *string         -- if the run itself failed
}
```

---

## Self-Bootstrapping from PR History

The most powerful feature: automatically discover good eval candidates from your repo's merged PRs.

### Why a coding agent session

The bootstrapper runs as a **coding agent session** — the same sandbox + agent infrastructure used for regular coding tasks. This is intentional: analyzing PR history, reading diffs, understanding linked issues, and generating good scoring criteria is judgment-heavy work that benefits from the agent having full repo context and tool access. A deterministic script can't judge whether a PR body constitutes a clear problem description or whether a diff represents a "single logical change."

The bootstrapper session gets:
- The repo cloned at HEAD (it needs access to full git history)
- `143-tools github` CLI access (to read PR metadata, linked issues, review comments)
- A system prompt explaining the bootstrap task and fitness criteria
- The output schema for eval task candidates

### How it works

1. User clicks **Bootstrap from PR History** in the evals settings.
2. System launches a coding agent session with the bootstrap prompt. The agent:
   - Uses `git log` and `143-tools github` to scan recent merged PRs (configurable window, default: last 90 days)
   - Reads each PR's diff, body, linked issues, and review history
   - Evaluates **bootstrap fitness** using its judgment:

```
Bootstrap Fitness Criteria (agent evaluates these):
  - Human-authored (not from a bot or 143 itself)
  - Reasonably scoped (10-500 lines changed, 1-15 files touched)
  - Has a clear problem description (PR body or linked issue)
  - Has associated tests (either existing tests that cover the change, or new tests added)
  - Clean merge (no excessive conflict resolution)
  - Single logical change (not a mega-PR with unrelated changes)
```

3. The agent ranks candidates by fitness and outputs structured JSON with the top N (default: 20).
4. For each candidate, the agent generates:
   - `base_commit_sha` from the PR's merge base
   - `solution_commit_sha` and `solution_diff` from the merge
   - `issue_description` synthesized from the PR body + linked issue (the agent rewrites this as a clear task description, since PR bodies are often written for reviewers, not for agents)
   - Proposed scoring criteria based on what the PR touched and the agent's understanding of the change:
     - Always → `code_check` with `make test` or equivalent CI command
     - Always → `llm_judge` with notes comparing agent diff against solution diff for approach similarity
     - If tests were added → `code_check` running the specific test suite
     - If the change is stylistically interesting → `llm_judge` with notes about code quality expectations

5. Candidates are presented to the user in the evals settings UI. User reviews, adjusts criteria/weights, and accepts to create eval tasks.

### Bootstrap quality signals

The agent prefers PRs that:
- Fix a bug with a clear reproduction (Sentry link, failing test)
- Add a focused feature with clear acceptance criteria
- Have reviewer approval with minimal back-and-forth (indicates clear scope)
- Touch well-tested areas of the codebase (more deterministic grading possible)

The agent avoids PRs that:
- Are dependency bumps or auto-generated
- Touch only config/docs
- Have extensive merge conflicts
- Were reverted

### Continuous bootstrap

Optionally, the system can run a bootstrap session on a schedule (e.g., weekly via PM cadence) and surface new candidates as suggestions in the evals settings. This keeps the eval suite growing organically as the team ships features. The scheduled session runs the same agent prompt but filters to PRs merged since the last bootstrap run.

---

## Running Evals with Changes

Three categories of changes can be tested against the eval suite, all through the same batch run panel (see "Config overlay" above for the full UX):

| Change type | How to test it |
|------------|---------------|
| **Repo config** (AGENTS.md, CLAUDE.md, skills, .claude/) | Select the branch with your changes in the Config picker |
| **Platform config** (model, agent type, PM documents) | Select different model or PM doc set in the configuration row |
| **Org settings** (token limits, reasoning effort, context limits) | Change the setting, then run — the current active settings version is captured in the manifest |

You can mix these: one configuration row might be "sonnet + current config" and another might be "opus + new AGENTS.md branch." The batch results matrix shows scores for every combination, making it easy to isolate which change actually moved the needle.

---

## API Endpoints

```
POST   /api/v1/evals/tasks                  -- create eval task
GET    /api/v1/evals/tasks                  -- list eval tasks (with filters)
GET    /api/v1/evals/tasks/:id              -- get eval task detail
PATCH  /api/v1/evals/tasks/:id              -- update eval task
DELETE /api/v1/evals/tasks/:id              -- archive eval task

POST   /api/v1/evals/tasks/:id/runs         -- start an eval run
GET    /api/v1/evals/tasks/:id/runs         -- list runs for a task
GET    /api/v1/evals/runs/:id               -- get run detail + scores

POST   /api/v1/evals/batch                  -- start batch run (multiple tasks x configs)
GET    /api/v1/evals/batch/:id              -- get batch results

POST   /api/v1/evals/bootstrap              -- trigger PR history scan
GET    /api/v1/evals/bootstrap/candidates   -- get bootstrap candidates
POST   /api/v1/evals/bootstrap/accept       -- accept candidates as eval tasks
```

---

## Data Model Relationships

```
┌──────────────┐     ┌──────────────────┐     ┌──────────────┐
│  EvalTask    │────▶│  EvalRun         │────▶│  CriterionResult │
│              │     │                  │     │  (per criterion)  │
│  base_commit │     │  model           │     └──────────────┘
│  scoring_    │     │  config_ref      │
│    criteria  │     │  final_score     │
│  deploy_sha  │     │  agent_diff      │
│              │     │  input_manifest  │
└──────┬───────┘     └──────────────────┘
       │
       │ source_pr_number (bootstrapped from)
       ▼
┌──────────────┐
│  PR History  │  (coding agent session scans via git + 143-tools)
└──────────────┘
```

---

## Connection to Doc 16

This design is the **user-facing task creation and management layer** that sits on top of doc 16's eval framework:

| Doc 16 provides | Doc 41 adds |
|-----------------|-------------|
| Grading architecture (deterministic + LLM judge) | Task builder UI for creating graded challenges |
| Dataset strategy (golden/shadow/adversarial) | PR-history bootstrapping to populate datasets |
| Release gates and rollout | Per-task run comparison with prompt/model overrides |
| Trace-centric evaluation | Codebase snapshot mechanism (git-based time travel) |
| Statistical rigor requirements | Batch run execution with comparison matrices |
| Prompt override resolution | Input freezing via prompt/document versioning (doc 43) |

Eval tasks created here feed into doc 16's dataset buckets. PR-bootstrapped tasks are natural candidates for the golden set. Failure-derived tasks (from doc 16's data flywheel) can also be managed through this UI.

---

## Implementation Order

1. **EvalTask and EvalRun schema** — migrations, models, store layer
2. **Codebase snapshot mechanism** — sandbox checkout at arbitrary commits
3. **Code check grader** — run commands in sandbox, capture exit code + output
4. **LLM judge grader** — wrap user notes as rubric, call judge model, return pass/fail + reasoning
5. **Settings UI** — task list, create flow, run trigger, results display
6. **PR history bootstrapper** — scan, rank, propose candidates
7. **Batch runs and comparison view** — multi-model/prompt matrices
8. **Input freezing integration** — depends on doc 43 shipping first
9. **Continuous bootstrap scheduling** — weekly candidate suggestions
