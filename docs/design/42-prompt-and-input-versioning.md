# 42 - Prompt and Input Versioning

> **Status:** Not Started | **Last reviewed:** 2026-03-30
>
> **Required by:** [41-eval-task-builder.md](41-eval-task-builder.md) (input freezing for reproducible evals), [16-ai-agent-evals.md](future/16-ai-agent-evals.md) (prompt lifecycle and release gates)

## Problem

The eval system (docs 16 and 41) requires pinning exact prompt text and PM document content to specific eval runs. Today, neither prompts nor PM documents have version history:

- **Prompts** are embedded Go templates (`internal/prompts/templates/*.template`). They change with code deploys. There is no record of what prompt text was used for any given agent run.
- **PM Documents** (`pm_documents` table) store current content. Updates overwrite in place. There is no history of what the PM agent was reading at any given point.
- **PMPlan** snapshots `product_context_snapshot` (the org settings context), but does NOT snapshot the actual prompt templates or PM document content used during the run.

This means:
1. You cannot reproduce a past agent run with the same inputs.
2. You cannot A/B test prompt changes — there's no way to run "old prompt vs. new prompt" on the same task.
3. Eval tasks cannot freeze their inputs, making eval scores non-comparable across time.

---

## Design

### 1. Prompt Versioning

#### What gets versioned

Every template in `internal/prompts/templates/` that is rendered and sent to an LLM. Currently 19 templates:

| Template | Used by |
|----------|---------|
| `pm_system_prompt` | PM agent planning |
| `pm_bootstrap` | PM context bootstrap |
| `pm_context_refresh` | PM context refresh |
| `coding_task_preamble` | Coding agent task injection |
| `direction_check_prompt` | Validation: direction |
| `correctness_check_prompt` | Validation: correctness |
| `regression_check_prompt` | Validation: regression tests |
| `direction_alignment_prompt` | Prioritization: alignment scoring |
| `complexity_estimate_prompt` | Prioritization: complexity scoring |
| `review_comment_prompt` | Feedback: comment classification |
| `slack_summarizer_prompt` | Slack thread analysis |
| `project_generate_prompt` | Project generation |
| `project_cycle_system_prompt` | Project cycle planning |
| + 6 corresponding `*_user_prompt` templates | User-turn content for the above |

#### Data model

```sql
CREATE TABLE prompt_versions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_name   TEXT NOT NULL,          -- e.g. "pm_system_prompt"
    content_hash    TEXT NOT NULL,          -- SHA-256 of rendered content
    content         TEXT NOT NULL,          -- full template text
    -- For org-specific overrides (NULL = global default)
    org_id          UUID REFERENCES organizations(id),
    -- Lifecycle
    state           TEXT NOT NULL DEFAULT 'active',  -- draft, candidate, active, archived
    source          TEXT NOT NULL DEFAULT 'deploy',  -- deploy, manual_override, eval_override
    -- Metadata
    deploy_sha      TEXT,                  -- git commit that introduced this version (for deploy-sourced)
    change_summary  TEXT,                  -- human-readable description of what changed
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (template_name, content_hash, org_id)
);

CREATE INDEX idx_prompt_versions_lookup
    ON prompt_versions (template_name, org_id, state, created_at DESC);
```

#### How versions are created

**On deploy (automatic):** A startup hook compares each embedded template's content hash against the latest `active` version in the database. If the hash differs, a new `prompt_version` row is inserted with `source = 'deploy'` and `state = 'active'`. The previous active version is transitioned to `archived`. This means every deploy that changes a prompt automatically creates a version record.

**Manual override (org-specific):** An org admin edits a prompt in Settings > Prompts. This creates a version with `org_id` set and `source = 'manual_override'`. Org overrides take precedence over global defaults (per doc 16's layering: org_override > global_default).

**Eval override (ephemeral):** When running an eval with a modified prompt, a version is created with `source = 'eval_override'` and `state = 'draft'`. These are never served in production but can be referenced by eval runs.

#### Resolution at runtime

When rendering a prompt for a production run:

```
1. Check for org-specific active version (org_id = current org, state = active)
2. Fall back to global active version (org_id IS NULL, state = active)
3. Fall back to embedded template (defensive — should never happen after first deploy)
```

When rendering a prompt for an eval run with a pinned version:

```
1. Load the exact prompt_version row by ID
2. Use its content directly (ignore resolution chain)
```

#### Recording what was used

Every agent run and validation call records the `prompt_version_id` that was resolved and used. This is added as a column to:

- `agent_runs` — which coding task preamble + any system prompt was used
- `pm_plans` — which PM system prompt version was used
- `validations` — which validation prompt version was used

This makes any past run fully reproducible: you know the exact prompt text, model, and codebase state.

---

### 2. PM Document Versioning

#### Current state

`pm_documents` stores the current content of each document. When a document is edited, the previous content is lost.

#### Design: Immutable content-addressed snapshots

```sql
CREATE TABLE pm_document_snapshots (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     UUID NOT NULL REFERENCES pm_documents(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    content_hash    TEXT NOT NULL,          -- SHA-256 of content
    content         TEXT NOT NULL,          -- full document text at this point
    title           TEXT NOT NULL,          -- title at this point
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (document_id, content_hash)
);

CREATE INDEX idx_pm_doc_snapshots_doc
    ON pm_document_snapshots (document_id, created_at DESC);
```

**When snapshots are created:** Every time a `pm_document` is updated (via API or sync), the system checks if the content hash changed. If it did, a new snapshot row is inserted. This is cheap (content-addressed dedup) and automatic.

#### Document Set Snapshots

For eval pinning, we need to freeze the *entire set* of PM documents as they existed at a point in time — not just one document.

```sql
CREATE TABLE pm_document_set_snapshots (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    name            TEXT,                   -- optional label (e.g. "Pre-Q2 roadmap update")
    source          TEXT NOT NULL,          -- "auto", "manual", "eval_pin"
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pm_document_set_members (
    set_id          UUID NOT NULL REFERENCES pm_document_set_snapshots(id),
    snapshot_id     UUID NOT NULL REFERENCES pm_document_snapshots(id),
    PRIMARY KEY (set_id, snapshot_id)
);
```

**Auto-snapshots:** Before every PM planning cycle, the system creates a document set snapshot. This means every `pm_plan` can reference the exact document set it was working with. Cheap because it's just UUID references to existing content-addressed snapshots.

**Eval pinning:** When creating an eval task, the user selects a document set snapshot (or "current" which takes a snapshot at that moment). The eval task stores `pm_document_set_id`.

---

### 3. Agent Run Input Recording

To close the reproducibility loop, every agent run records a complete input manifest:

```sql
ALTER TABLE agent_runs ADD COLUMN input_manifest JSONB;
```

The manifest captures:

```json
{
  "prompt_versions": {
    "coding_task_preamble": "uuid-of-prompt-version",
    "direction_check_prompt": "uuid-of-prompt-version"
  },
  "pm_document_set_id": "uuid-of-document-set-snapshot",
  "product_context_hash": "sha256-of-org-settings-context",
  "base_commit_sha": "abc123",
  "model": "claude-opus-4-6",
  "model_config": {
    "reasoning_effort": "high",
    "temperature": 1.0
  }
}
```

This manifest is what makes "replay this run" and "compare against this baseline" possible.

---

## Settings UI: Prompts

New section in **Settings > Prompts** (alongside Agent, Prioritization, etc.).

### Prompt List

Shows all prompt templates with their current active version:

```
┌─────────────────────────────────────────────────────────────┐
│  Settings > Prompts                                         │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  PM System Prompt                                           │
│  Active: v12 (deploy 3d ago) · No org override              │
│  [View History] [Create Override]                            │
│                                                             │
│  Coding Task Preamble                                       │
│  Active: v8 (deploy 3d ago) · Org override: v2 (manual)    │
│  [View History] [Edit Override]                              │
│                                                             │
│  Direction Check Prompt                                     │
│  Active: v5 (deploy 1w ago) · No org override               │
│  [View History] [Create Override]                            │
│                                                             │
│  ...                                                        │
└─────────────────────────────────────────────────────────────┘
```

### Version History

For each template, shows the full timeline of versions with diffs between adjacent versions. Users can:
- View any past version's full text
- Diff any two versions
- Pin a specific version to an eval task
- Create an org override from any version as a starting point

### Override Editor

Monaco-style editor with:
- Template variable highlighting (shows `{{.AvailableSlots}}` etc.)
- Live preview with sample data
- Diff against the global default
- Save as draft → promote to active (with optional eval gate from doc 16)

---

## PM Document History

The existing PM documents UI (`Settings > Prioritization > Documents` or wherever it lives) gains:

- **Version indicator** on each document card ("v3 · edited 2d ago")
- **History drawer** showing all snapshots with diffs
- **Restore** button to revert to any previous version
- **Document set timeline** showing auto-snapshots aligned with PM planning cycles

---

## API Endpoints

```
-- Prompt versions
GET    /api/v1/prompts                          -- list all templates with active versions
GET    /api/v1/prompts/:template_name/versions  -- version history for a template
GET    /api/v1/prompts/versions/:id             -- get specific version content
POST   /api/v1/prompts/:template_name/override  -- create org override
PATCH  /api/v1/prompts/versions/:id/promote     -- promote draft/candidate to active
POST   /api/v1/prompts/versions/:id/archive     -- archive a version

-- PM document snapshots
GET    /api/v1/pm/documents/:id/snapshots       -- snapshot history for a document
GET    /api/v1/pm/document-sets                  -- list document set snapshots
GET    /api/v1/pm/document-sets/:id             -- get set with member contents
POST   /api/v1/pm/document-sets                  -- create manual set snapshot
```

---

## Migration Path

This is additive — no existing behavior changes until the new tables are populated.

1. **Add tables** — `prompt_versions`, `pm_document_snapshots`, `pm_document_set_snapshots`, `pm_document_set_members`
2. **Seed from current state** — On first deploy, insert one `prompt_version` per template from the embedded content. Insert one `pm_document_snapshot` per existing PM document.
3. **Add deploy hook** — Startup comparison of embedded templates vs. DB, auto-insert on change.
4. **Add PM document snapshot trigger** — On every PM document update, auto-insert if hash changed.
5. **Add `input_manifest` to `agent_runs`** — Start recording. Old runs will have NULL (acceptable).
6. **Add `prompt_version_id` columns** — To `agent_runs`, `pm_plans`, and validation records.
7. **Build Settings > Prompts UI** — Version history, override editor.
8. **Build document history UI** — Snapshot timeline, restore, set management.
9. **Wire into eval system** — Enable pinning prompt versions and document sets on eval tasks.

---

## Connection to Existing Patterns

The insert-only versioned settings pattern already exists in the codebase (per AGENTS.md: "deactivate old row, insert new active row in a transaction"). Prompt versioning follows the same pattern — `state` transitions from `active` to `archived` when a new version takes over.

PM document snapshots use content-addressed storage (deduplicate by hash), which is the standard approach for immutable content versioning without unbounded storage growth.
