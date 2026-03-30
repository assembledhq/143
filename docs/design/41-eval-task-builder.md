# 41 - Eval Task Builder

> **Status:** Not Started | **Last reviewed:** 2026-03-30
>
> **Depends on:** [16-ai-agent-evals.md](future/16-ai-agent-evals.md) (grading architecture, release gates), [42-prompt-and-input-versioning.md](42-prompt-and-input-versioning.md) (prompt/document freezing)

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

  -- Input configuration (frozen references)
  prompt_version_id   *UUID           -- pinned prompt version (see doc 42)
  pm_document_set_id  *UUID           -- pinned PM document snapshot (see doc 42)
  context_overrides   jsonb           -- any additional context injections

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

```
ScoringCriterion {
  name            string    -- e.g. "tests_pass", "minimal_diff", "correct_files_touched"
  description     string    -- human-readable explanation of what this measures
  grader_type     string    -- "deterministic" | "llm_judge" | "diff_similarity" | "custom_script"
  grader_config   jsonb     -- grader-specific configuration (see Grader Types below)
  weight          float     -- relative weight in final score (weights are normalized)
  required        bool      -- if true, failing this criterion fails the entire eval
  fail_reasons    text[]    -- example failure modes to watch for
}
```

### Grader Types

| Type | Config | What it checks |
|------|--------|----------------|
| `deterministic` | `{ "command": "make test", "timeout_seconds": 300 }` | Exit code 0 = pass. Runs in sandbox against agent output. |
| `llm_judge` | `{ "rubric": "...", "model": "claude-sonnet-4-6", "dimensions": [...] }` | LLM scores output against rubric. Returns per-dimension scores. |
| `diff_similarity` | `{ "max_files_changed": 5, "max_lines_added": 200, "similarity_threshold": 0.6 }` | Compares agent diff against known-good solution diff. Penalizes unnecessary changes. |
| `custom_script` | `{ "script_path": ".143/eval-scripts/check_auth.sh", "args": [...] }` | User-provided script. Receives agent diff on stdin, exits 0/1, optional JSON score on stdout. |
| `file_scope` | `{ "expected_files": ["src/auth.go", "src/auth_test.go"], "forbidden_files": ["go.mod"] }` | Checks that the right files were touched and wrong ones weren't. |
| `regression_test` | `{ "test_pattern": "TestAuth.*", "must_add_test": true }` | Verifies specific tests pass and optionally that new tests were added. |

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

---

## Input Freezing

An eval is only reproducible if the inputs are frozen. Three categories of inputs need versioning:

### 1. Prompts (system prompts, task preambles, validation prompts)

**Current state:** Prompts are embedded Go templates (`internal/prompts/templates/`). They change with code deploys. There is no versioning.

**Required:** Full prompt versioning with immutable snapshots. **This is not implemented and is spun out into [42-prompt-and-input-versioning.md](42-prompt-and-input-versioning.md).**

Each eval task pins a `prompt_version_id`. When the eval runs, it uses that exact prompt text regardless of what the current production prompt is.

### 2. PM Documents (product context, roadmap, philosophy)

**Current state:** `PMDocument` records store current content. Updates overwrite in place. No history.

**Required:** Immutable document snapshots. **Also covered in [42-prompt-and-input-versioning.md](42-prompt-and-input-versioning.md).**

Each eval task pins a `pm_document_set_id` — a snapshot of all PM documents as they existed at a point in time.

### 3. Codebase context (CLAUDE.md, AGENTS.md, learned-conventions.md)

These are already versioned by git — they're files in the repo. Checking out `base_commit_sha` automatically gives you the correct versions. No additional work needed.

---

## Settings UI

The eval task builder lives in **Settings > Evals** (new section alongside the existing Agent, Prioritization, and Integrations settings).

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
- Name, description
- Grader type (dropdown) → type-specific config fields
- Weight slider
- Required checkbox
- Example failure reasons (optional, helps LLM judges)

**Step 4: Pin inputs**

- Prompt version: "Current" or pick from version history (requires doc 42)
- PM documents: "Current" or pick from snapshot history (requires doc 42)
- Additional context overrides (JSON editor)

**Step 5: Review and save**

---

## Running Evals

### Single task run

From the eval task detail page, click **Run** and choose:
- **Model**: dropdown of available models (claude-opus-4-6, claude-sonnet-4-6, codex, gemini-cli, etc.)
- **Prompt version**: override the pinned version for this run
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

  -- Configuration used
  model               string
  prompt_version_id   UUID
  pm_document_set_id  UUID
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

### How it works

1. User clicks **Bootstrap from PR History** in the evals settings.
2. System scans recent merged PRs (configurable window, default: last 90 days).
3. For each PR, it evaluates **bootstrap fitness**:

```
Bootstrap Fitness Criteria:
  - Human-authored (not from a bot or 143 itself)
  - Reasonably scoped (10-500 lines changed, 1-15 files touched)
  - Has a clear problem description (PR body or linked issue)
  - Has associated tests (either existing tests that cover the change, or new tests added)
  - Clean merge (no excessive conflict resolution)
  - Single logical change (not a mega-PR with unrelated changes)
```

4. The bootstrapper ranks candidates by fitness and presents the top N (default: 20) to the user.
5. For each candidate, it auto-generates:
   - `base_commit_sha` from the PR's merge base
   - `solution_commit_sha` and `solution_diff` from the merge
   - `issue_description` from the PR body + linked issue
   - Proposed scoring criteria based on what the PR touched:
     - If tests were added → `regression_test` criterion
     - If few files changed → `file_scope` criterion
     - Always → `deterministic` (CI must pass) + `diff_similarity` (solution comparison)
     - If the change is stylistically interesting → `llm_judge` for code quality

6. User reviews, adjusts criteria/weights, and saves as eval tasks.

### Bootstrap quality signals

The system prefers PRs that:
- Fix a bug with a clear reproduction (Sentry link, failing test)
- Add a focused feature with clear acceptance criteria
- Have reviewer approval with minimal back-and-forth (indicates clear scope)
- Touch well-tested areas of the codebase (more deterministic grading possible)

The system avoids PRs that:
- Are dependency bumps or auto-generated
- Touch only config/docs
- Have extensive merge conflicts
- Were reverted

### Continuous bootstrap

Optionally, the system can run the bootstrapper on a schedule (e.g., weekly) and surface new candidates as suggestions in the evals settings. This keeps the eval suite growing organically as the team ships features.

---

## Running Evals with New Prompts and Documents

A key use case: you've edited a prompt or added a new PM document and want to see the impact before deploying.

### Workflow

1. Go to **Settings > Evals**
2. Select one or more eval tasks
3. Click **Run with overrides**
4. In the override panel:
   - **Prompt**: paste modified prompt text or select a draft version (doc 42)
   - **PM Documents**: add/remove/edit documents for this run
   - **Model**: optionally change the model
5. Run executes with overrides, results show alongside baseline runs for comparison

### Diff view

The results page shows a side-by-side:
- Left: baseline run (current production prompts/docs)
- Right: override run (your changes)
- Delta: per-criterion score changes, highlighted green/red

This lets you answer: "If I change the PM context to emphasize security, do the auth-related evals improve without regressing the feature evals?"

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
│  scoring_    │     │  prompt_version  │
│    criteria  │     │  final_score     │
│  prompt_     │     │  agent_diff      │
│    version   │     │  agent_trace     │
└──────┬───────┘     └──────────────────┘
       │
       │ source_pr_number
       ▼
┌──────────────┐
│  PR History  │  (GitHub API / local git)
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
| Prompt override resolution | Input freezing via prompt/document versioning (doc 42) |

Eval tasks created here feed into doc 16's dataset buckets. PR-bootstrapped tasks are natural candidates for the golden set. Failure-derived tasks (from doc 16's data flywheel) can also be managed through this UI.

---

## Implementation Order

1. **EvalTask and EvalRun schema** — migrations, models, store layer
2. **Codebase snapshot mechanism** — sandbox checkout at arbitrary commits
3. **Scoring criteria engine** — deterministic + diff_similarity graders first
4. **Settings UI** — task list, create flow, run trigger, results display
5. **LLM judge grader** — rubric-based scoring with calibration
6. **PR history bootstrapper** — scan, rank, propose candidates
7. **Batch runs and comparison view** — multi-model/prompt matrices
8. **Input freezing integration** — depends on doc 42 shipping first
9. **Continuous bootstrap scheduling** — weekly candidate suggestions
