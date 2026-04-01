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

  -- Input configuration (frozen references, see doc 42)
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

---

## Input Freezing

An eval is only reproducible if the inputs are frozen. A thorough audit of `orchestrator.RunAgent()` and the agent adapter layer identified **every input** that flows into an agent run. Here's what needs freezing and how:

### Inputs versioned by doc 42

These are covered in detail in [42-prompt-and-input-versioning.md](42-prompt-and-input-versioning.md):

| Input | Current state | Doc 42 solution |
|-------|--------------|-----------------|
| **Prompt templates** (19 templates in `internal/prompts/templates/`) | Embedded in Go binary, identical across all orgs, change only on deploy | `server_deploy_sha` — prompts are code, not config. The deploy SHA pins them all. |
| **PM documents** (roadmap, philosophy, context) | Overwritten in place, no history | Insert-only versioning on `pm_documents` with `active` flag |
| **Org settings** (token limits, confidence thresholds, context limits, autonomy) | Already insert-only versioned | Record active version ID in manifest |
| **Memory context** (learned conventions from review feedback) | Changes over time, not snapshotted | Snapshot selected memory IDs + content in manifest |
| **Sandbox image version** | Uses mutable `"143-sandbox:latest"` | Pin to image digest, record in manifest |
| **Integration skills doc** (auto-generated CLI tool docs) | Changes when integrations change | Content hash in manifest |

### Inputs already versioned

| Input | Why it's already covered |
|-------|------------------------|
| **Codebase** (source code, CLAUDE.md, AGENTS.md, learned-conventions.md) | Git — checking out `base_commit_sha` gives exact state |
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
- PM documents: "Current" or pick from snapshot history (requires doc 42)
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

  -- Configuration used (full input manifest per doc 42)
  input_manifest      jsonb           -- complete frozen inputs (see doc 42 §7)
  model               string
  server_deploy_sha   string          -- pins prompt templates
  pm_document_set_pin_id UUID
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
     - Always → `code_check` with `make test` or equivalent CI command
     - Always → `llm_judge` with notes comparing agent diff against solution diff for approach similarity
     - If tests were added → `code_check` running the specific test suite
     - If the change is stylistically interesting → `llm_judge` with notes about code quality expectations

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
3. **Code check grader** — run commands in sandbox, capture exit code + output
4. **LLM judge grader** — wrap user notes as rubric, call judge model, return pass/fail + reasoning
5. **Settings UI** — task list, create flow, run trigger, results display
6. **PR history bootstrapper** — scan, rank, propose candidates
7. **Batch runs and comparison view** — multi-model/prompt matrices
8. **Input freezing integration** — depends on doc 42 shipping first
9. **Continuous bootstrap scheduling** — weekly candidate suggestions
