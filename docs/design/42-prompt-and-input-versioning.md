# 42 - Prompt and Input Versioning

> **Status:** Not Started | **Last reviewed:** 2026-03-30
>
> **Required by:** [41-eval-task-builder.md](41-eval-task-builder.md) (input freezing for reproducible evals), [16-ai-agent-evals.md](future/16-ai-agent-evals.md) (prompt lifecycle and release gates)

## Problem

The eval system (docs 16 and 41) requires pinning exact prompt text and PM document content to specific eval runs. Today, neither prompts nor PM documents have version history:

- **Prompts** are embedded Go templates (`internal/prompts/templates/*.template`). They change with code deploys. There is no record of what prompt text was used for any given agent run.
- **PM Documents** (`pm_documents` table) store current content. Updates overwrite in place. There is no history of what the PM agent was reading at any given point.
- **PMPlan** snapshots `product_context_snapshot` (the org settings context), but does NOT snapshot the actual prompt templates or PM document content used during the run.
- **Server deploy version** is not recorded on any run. Since prompts are embedded in the Go binary, knowing which binary ran is equivalent to knowing which prompts were used — but this is not tracked.

This means:
1. You cannot reproduce a past agent run with the same inputs.
2. You cannot A/B test prompt changes — there's no way to run "old prompt vs. new prompt" on the same task.
3. Eval tasks cannot freeze their inputs, making eval scores non-comparable across time.

---

## Design Principle: Version Everything, Separate Concerns

The versioning system serves two distinct purposes that must not be conflated:

1. **Audit trail** ("who changed what, when, and why") — answered by `audit_logs`, which is append-only with retention-based expiry.
2. **Content history** ("what was the exact state at time X") — answered by version tables, which are permanent and content-addressed.

These are complementary. When a PM document is updated:
- An `audit_logs` entry records the actor, timestamp, IP, and a reference to the new version ID in `details`.
- A version row preserves the immutable content for future replay and eval pinning.

Audit logs can age out per retention policy without losing version history. Version rows never expire — they're referenced by eval tasks and agent runs indefinitely.

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
    content_hash    TEXT NOT NULL,          -- SHA-256 of template text
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

**On deploy (automatic):** A startup hook compares each embedded template's content hash against the latest `active` version in the database. If the hash differs, a new `prompt_version` row is inserted with `source = 'deploy'`, `state = 'active'`, and `deploy_sha` set to the current build's git SHA. The previous active version is transitioned to `archived`. This means every deploy that changes a prompt automatically creates a version record.

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

#### Audit log integration

Prompt version changes emit audit log entries:

| Event | Action | Details |
|-------|--------|---------|
| Deploy creates new version | `prompt.version_created` | `{ "template_name": "...", "version_id": "...", "source": "deploy", "deploy_sha": "..." }` |
| Org override created | `prompt.override_created` | `{ "template_name": "...", "version_id": "...", "previous_version_id": "..." }` |
| Version promoted to active | `prompt.version_promoted` | `{ "template_name": "...", "version_id": "...", "from_state": "candidate" }` |
| Version archived | `prompt.version_archived` | `{ "template_name": "...", "version_id": "..." }` |

New audit enums:
```go
AuditActionPromptVersionCreated  AuditAction = "prompt.version_created"
AuditActionPromptOverrideCreated AuditAction = "prompt.override_created"
AuditActionPromptVersionPromoted AuditAction = "prompt.version_promoted"
AuditActionPromptVersionArchived AuditAction = "prompt.version_archived"

AuditResourcePromptVersion AuditResourceType = "prompt_version"
```

---

### 2. PM Document Versioning

#### Current state

`pm_documents` stores the current content of each document. The `Update` method overwrites `title`, `content`, `doc_type`, etc. in place. Previous content is lost.

#### Approach: Insert-only versioning on `pm_documents` itself

Rather than adding a separate snapshot table, we apply the **insert-only versioned settings pattern** already established in the codebase (see AGENTS.md). This is the same pattern used for org settings: deactivate the old row, insert a new active row, all in a transaction.

**Schema changes to `pm_documents`:**

```sql
ALTER TABLE pm_documents
    ADD COLUMN active       BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN version      INT NOT NULL DEFAULT 1,
    ADD COLUMN content_hash TEXT NOT NULL DEFAULT '',
    ADD COLUMN parent_id    UUID REFERENCES pm_documents(id);

-- Ensure only one active version per logical document
CREATE UNIQUE INDEX idx_pm_documents_active_parent
    ON pm_documents (org_id, parent_id) WHERE active = true AND parent_id IS NOT NULL;

-- For the first version (no parent), ensure uniqueness differently
-- The first version of a document has parent_id = NULL; subsequent versions point to the first version
```

**How it works:**

1. The first version of a document is inserted with `active = true`, `version = 1`, `parent_id = NULL`.
2. On update, within a transaction:
   - Set `active = false` on the current active row (returns the row for value merging).
   - Insert a new row with `active = true`, `version = previous + 1`, `parent_id = first version's ID`, and the new content.
3. All existing queries add `WHERE active = true` (enforced by tenancy-style test).
4. Version history = `SELECT * FROM pm_documents WHERE parent_id = :first_version_id ORDER BY version DESC`.

**Why this over a separate table:**

- Follows the established codebase pattern — developers already understand it.
- No new tables or join complexity for the common case.
- Version history is a simple query on the same table.
- The `active` filter is already a tested pattern (tenancy test can enforce it).
- Content dedup isn't necessary — PM documents change infrequently and are not large enough to warrant content-addressed storage.

#### Document set pinning for evals

For eval tasks that need to freeze the full set of PM documents at a point in time, we store a lightweight reference:

```sql
CREATE TABLE pm_document_set_pins (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    name            TEXT,                   -- optional label
    source          TEXT NOT NULL,          -- "pm_cycle", "eval_pin", "manual"
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pm_document_set_pin_members (
    pin_id          UUID NOT NULL REFERENCES pm_document_set_pins(id),
    document_id     UUID NOT NULL REFERENCES pm_documents(id),  -- points to specific version row
    PRIMARY KEY (pin_id, document_id)
);
```

- Before every PM planning cycle, the system creates a pin capturing the current active document IDs. The `pm_plan` stores `document_set_pin_id`.
- Eval tasks store `document_set_pin_id` to freeze their input documents.
- Since `pm_documents` rows are never deleted (insert-only), the pin references are stable forever.

#### Audit log integration

PM document version changes emit audit entries:

| Event | Action | Details |
|-------|--------|---------|
| Document created | `pm_document.created` (existing) | `{ "document_id": "...", "version": 1 }` |
| Document updated | `pm_document.updated` | `{ "document_id": "...", "version": 5, "previous_version_id": "..." }` |
| Document restored to old version | `pm_document.restored` | `{ "document_id": "...", "restored_from_version": 3, "new_version": 6 }` |
| Document set pinned | `pm_document_set.pinned` | `{ "pin_id": "...", "document_count": 4, "source": "pm_cycle" }` |

New audit enums:
```go
AuditActionPMDocumentUpdated     AuditAction = "pm_document.updated"
AuditActionPMDocumentRestored    AuditAction = "pm_document.restored"
AuditActionPMDocumentSetPinned   AuditAction = "pm_document_set.pinned"

AuditResourcePMDocumentSet AuditResourceType = "pm_document_set"
```

---

### 3. Server Deploy SHA Tracking

Since prompts are embedded in the Go binary, every run should record which build produced it. This is the cheapest way to know exactly what code (and therefore what prompt templates, validation logic, routing logic) was in play.

```go
// Set at build time via ldflags: -X main.buildSHA=abc123
var buildSHA string
```

This value is included in the input manifest (below) and also exposed via a `/healthz` or `/version` endpoint for operational visibility.

---

### 4. Agent Run Input Manifest

To close the reproducibility loop, every agent run records a complete input manifest:

```sql
ALTER TABLE agent_runs ADD COLUMN input_manifest JSONB;
```

The manifest captures everything needed to reconstruct "what was happening when this ran":

```json
{
  "server_deploy_sha": "abc123def",
  "prompt_versions": {
    "coding_task_preamble": "uuid-of-prompt-version",
    "direction_check_prompt": "uuid-of-prompt-version"
  },
  "pm_document_set_pin_id": "uuid-of-document-set-pin",
  "product_context_hash": "sha256-of-org-settings-context",
  "repo_base_commit_sha": "def456abc",
  "model": "claude-opus-4-6",
  "model_config": {
    "reasoning_effort": "high",
    "temperature": 1.0
  }
}
```

For the eval system, this manifest is what enables:
- **"Replay this run"** — check out the repo at `repo_base_commit_sha`, load the exact prompts by version ID, inject the pinned PM documents, use the same model config.
- **"Compare against baseline"** — diff two manifests to see exactly what changed between runs.
- **"What was happening"** — `server_deploy_sha` tells you the exact 143 server code, `repo_base_commit_sha` tells you the exact customer repo state, and the version IDs give you exact prompt/document content.

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

The existing PM documents UI gains:

- **Version indicator** on each document card ("v3 · edited 2d ago")
- **History drawer** showing all versions with diffs (query: `WHERE parent_id = :first_id ORDER BY version DESC`)
- **Restore** button to revert to any previous version (creates a new version with the old content)
- **Document set timeline** showing auto-pins aligned with PM planning cycles

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

-- PM document versions
GET    /api/v1/pm/documents/:id/versions        -- version history for a document
POST   /api/v1/pm/documents/:id/restore         -- restore to a specific version

-- PM document set pins
GET    /api/v1/pm/document-set-pins             -- list pins
GET    /api/v1/pm/document-set-pins/:id         -- get pin with member contents
POST   /api/v1/pm/document-set-pins             -- create manual pin
```

---

## Migration Path

This is additive — no existing behavior changes until the new columns/tables are populated.

1. **Alter `pm_documents`** — Add `active`, `version`, `content_hash`, `parent_id` columns. Backfill: set all existing rows to `active = true`, `version = 1`, `parent_id = NULL`, compute `content_hash`.
2. **Add `pm_document_set_pins` and `pm_document_set_pin_members`** tables.
3. **Add `prompt_versions` table** — Seed with one row per embedded template from the current deploy.
4. **Add deploy startup hook** — Compare embedded templates vs. DB, auto-insert on hash change. Record `deploy_sha` from build ldflags.
5. **Update `PMDocumentStore.Update`** — Replace in-place UPDATE with insert-only transaction pattern (deactivate + insert).
6. **Add `input_manifest` to agent runs** — Start recording. Old runs will have NULL (acceptable).
7. **Add audit log emissions** — For prompt version and PM document version changes.
8. **Add `WHERE active = true`** to all existing PM document queries. Add tenancy-style test to enforce this.
9. **Build Settings > Prompts UI** — Version history, override editor.
10. **Build document history UI** — Version timeline, restore, pin management.
11. **Wire into eval system** — Enable pinning prompt versions and document set pins on eval tasks.

---

## Connection to Existing Patterns

**Insert-only versioning** (from AGENTS.md): PM documents now follow the exact same pattern as org settings — deactivate old row, insert new active row in a transaction. Developers already understand this pattern. The `active` boolean, transactional update, and `WHERE active = true` discipline are all established.

**Audit logs**: The existing `AuditEmitter` and `AuditResourcePMDocument` resource type are extended with new actions for version tracking. Audit logs reference version IDs in their `details` JSONB but do not store content — that lives in the version rows, which have no retention expiry.

**Content hashing**: Used for both prompts and PM documents to detect actual changes (vs. no-op saves) and for dedup on the prompt side (`UNIQUE (template_name, content_hash, org_id)`).
