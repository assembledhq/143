# 42 - Prompt and Input Versioning

> **Status:** Not Started | **Last reviewed:** 2026-04-01
>
> **Required by:** [41-eval-task-builder.md](41-eval-task-builder.md) (input freezing for reproducible evals), [16-ai-agent-evals.md](future/16-ai-agent-evals.md) (prompt lifecycle and release gates)

## Problem

The eval system (docs 16 and 41) requires pinning exact inputs to specific eval runs so results are reproducible and comparable. Today, most agent run inputs have no version history:

- **Prompts** are embedded Go templates (`internal/prompts/templates/*.template`). They are identical across all orgs — they change only on server deploy. But the deploy version is not recorded on any run, so there's no way to know which prompt text was used.
- **PM Documents** (`pm_documents` table) store current content. Updates overwrite in place. There is no history of what the PM agent was reading at any given point.
- **PMPlan** snapshots `product_context_snapshot` (the org settings context), but does NOT snapshot PM document content or other inputs.
- **Memory context**, **sandbox image**, and **integration skills** all change over time with no record.

This means:
1. You cannot reproduce a past agent run with the same inputs.
2. Eval tasks cannot freeze their inputs, making eval scores non-comparable across time.

---

## Design Principle: Version Everything, Separate Concerns

The versioning system serves two distinct purposes that must not be conflated:

1. **Audit trail** ("who changed what, when, and why") — answered by `audit_logs`, which is append-only with retention-based expiry.
2. **Content history** ("what was the exact state at time X") — answered by version tables, which are permanent and content-addressed.

These are complementary. Audit logs can age out per retention policy without losing version history. Version rows never expire — they're referenced by eval tasks and agent runs indefinitely.

---

## Design

### 1. Prompts: Server Deploy SHA (No Per-Prompt Versioning Needed)

Prompt templates in `internal/prompts/templates/` are **identical across all orgs**. They are embedded in the Go binary via `//go:embed` and contain no org-specific logic — org context (product direction, focus areas, etc.) is injected as *data* through template variables, not through different template text.

This means prompts only change when the server code is deployed. **The `server_deploy_sha` is sufficient to pin all prompt content.** Given a deploy SHA, you can check out that commit and read the exact template files. No separate `prompt_versions` table is needed.

```go
// Set at build time via ldflags: -X main.buildSHA=abc123
var buildSHA string
```

This value is:
- Included in the input manifest on every agent run
- Exposed via a `/healthz` or `/version` endpoint for operational visibility
- Used by the eval system to reconstruct exact prompt text: `git show <deploy_sha>:internal/prompts/templates/<name>.template`

**If per-org prompt overrides become a feature in the future**, a `prompt_versions` table would be needed at that point. But today prompts are global and immutable between deploys, so the SHA is the right abstraction.

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

### 3. Org Settings Version Tracking

Org settings already follow the insert-only versioned pattern (deactivate old row, insert new active row in a transaction). Each active row implicitly has a version identity (its row ID).

**What's needed:** Record the active org settings row ID in the input manifest. No schema changes needed — the version history already exists. We just need to capture which version was active at run time.

Key org settings fields that affect agent behavior:
- `ContextLimits` (token budgets, issue/PR counts for context gathering)
- `ConfidenceThresholds` (auto-proceed vs. human-review gates)
- `MaxConcurrentRuns`
- `LLMModel`, `LLMReasoningEffort`
- `AgentConfig` (per-agent env var overrides)
- `ProductContext` (philosophy, direction, focus/avoid areas)

---

### 4. Memory Context Snapshots

The memory system (`internal/services/memory/`) maintains learned conventions from review feedback. These are injected as a "Learned Conventions" markdown section into the agent's context. Memories evolve over time — they're reinforced, decay, and can be deleted.

**What's needed:** When building an agent run's context, snapshot the selected memory IDs and their content into the input manifest. This is a lightweight capture — typically under 2K tokens of memories are selected per run.

```json
"memory_snapshot": {
  "selected_memory_ids": ["uuid-1", "uuid-2", "uuid-3"],
  "content_hash": "sha256-of-formatted-memory-section",
  "token_budget_used": 1847
}
```

For eval runs, the memory snapshot from the manifest can be replayed directly instead of re-querying the memory store (which would return different memories since they've evolved).

---

### 5. Sandbox Image Pinning

The sandbox currently uses `"143-sandbox:latest"` — a mutable tag. This means the runtime environment changes without any record. Agent CLI tools, system packages, and language runtimes all live in this image.

**What's needed:**
- Pin sandbox images to content-addressed digests (`sha256:abc123...`) rather than mutable tags.
- Record the digest in the input manifest.
- For eval runs, use the exact pinned digest.

```json
"sandbox_image_digest": "sha256:abc123def456..."
```

---

### 6. Integration Skills Doc Hashing

The integration skills doc is auto-generated by `mcp.GenerateSkillsDoc()` based on which integrations are connected (Sentry, Linear, Notion, GitHub). It tells the agent what CLI tools are available. When integrations change, the available tools change.

**What's needed:** Compute a content hash of the generated skills doc and include it in the manifest. For eval runs, the skills doc can be regenerated from the integration state, or stored directly if integrations have changed.

```json
"integration_skills_hash": "sha256:def789..."
```

---

### 7. Agent Run Input Manifest

To close the reproducibility loop, every agent run records a complete input manifest:

```sql
ALTER TABLE agent_runs ADD COLUMN input_manifest JSONB;
```

The manifest captures **everything** needed to reconstruct "what was happening when this ran":

```json
{
  "server_deploy_sha": "abc123def",
  "pm_document_set_pin_id": "uuid-of-document-set-pin",
  "org_settings_version_id": "uuid-of-active-org-settings-row",
  "product_context_hash": "sha256-of-org-settings-context",
  "repo_base_commit_sha": "def456abc",
  "model": "claude-opus-4-6",
  "model_config": {
    "reasoning_effort": "high",
    "temperature": 1.0
  },
  "sandbox_image_digest": "sha256:abc123def456...",
  "memory_snapshot": {
    "selected_memory_ids": ["uuid-1", "uuid-2"],
    "content_hash": "sha256-of-memory-section",
    "token_budget_used": 1847
  },
  "integration_skills_hash": "sha256:def789...",
  "credential_sources": {
    "anthropic": "org_credential",
    "github": "installation_token"
  }
}
```

Notes:
- `server_deploy_sha` pins all prompt templates (since they're embedded in the binary and identical across orgs). To get the exact text: `git show <sha>:internal/prompts/templates/<name>.template`.
- `credential_sources` records which credential resolution path was used (user personal → team default → org credential → installation token) without storing secrets. This matters because different sources can point to different API endpoints or model access tiers.

For the eval system, this manifest enables:
- **"Replay this run"** — check out the repo at `repo_base_commit_sha`, build the server at `server_deploy_sha` (or just read its templates), inject pinned PM documents + memory snapshot, use the same model config and sandbox image.
- **"Compare against baseline"** — diff two manifests to see exactly what changed between runs.
- **"What was happening"** — `server_deploy_sha` gives exact 143 server code + prompts, `repo_base_commit_sha` gives exact customer repo state, version IDs give exact PM document/settings content.
- **"What else could have affected this"** — `sandbox_image_digest`, `memory_snapshot`, and `integration_skills_hash` capture the less obvious inputs that can cause eval score drift.

---

## PM Document History UI

The existing PM documents UI gains:

- **Version indicator** on each document card ("v3 · edited 2d ago")
- **History drawer** showing all versions with diffs (query: `WHERE parent_id = :first_id ORDER BY version DESC`)
- **Restore** button to revert to any previous version (creates a new version with the old content)
- **Document set timeline** showing auto-pins aligned with PM planning cycles

---

## API Endpoints

```
-- PM document versions
GET    /api/v1/pm/documents/:id/versions        -- version history for a document
POST   /api/v1/pm/documents/:id/restore         -- restore to a specific version

-- PM document set pins
GET    /api/v1/pm/document-set-pins             -- list pins
GET    /api/v1/pm/document-set-pins/:id         -- get pin with member contents
POST   /api/v1/pm/document-set-pins             -- create manual pin

-- Server version
GET    /api/v1/version                          -- current deploy SHA and build metadata
```

---

## Migration Path

This is additive — no existing behavior changes until the new columns/tables are populated.

1. **Alter `pm_documents`** — Add `active`, `version`, `content_hash`, `parent_id` columns. Backfill: set all existing rows to `active = true`, `version = 1`, `parent_id = NULL`, compute `content_hash`.
2. **Add `pm_document_set_pins` and `pm_document_set_pin_members`** tables.
3. **Add `server_deploy_sha` via build ldflags** — Set `-X main.buildSHA=$(git rev-parse HEAD)` in Makefile/CI. Expose via `/api/v1/version`.
4. **Update `PMDocumentStore.Update`** — Replace in-place UPDATE with insert-only transaction pattern (deactivate + insert).
5. **Pin sandbox image digests** — Update `DefaultSandboxConfig()` to use content-addressed digests instead of `"143-sandbox:latest"`. Update CI/CD to tag images with digest.
6. **Add `input_manifest` to agent runs** — Start recording all inputs (deploy SHA, PM doc pin, org settings version, memory snapshot, sandbox digest, integration skills hash, credential sources). Old runs will have NULL (acceptable).
7. **Add audit log emissions** — For PM document version changes.
8. **Add `WHERE active = true`** to all existing PM document queries. Add tenancy-style test to enforce this.
9. **Build document history UI** — Version timeline, restore, pin management.
10. **Wire into eval system** — Enable pinning deploy SHA, document set pins, org settings versions, memory snapshots, and sandbox digests on eval tasks.

---

## Connection to Existing Patterns

**Insert-only versioning** (from AGENTS.md): PM documents now follow the exact same pattern as org settings — deactivate old row, insert new active row in a transaction. Developers already understand this pattern. The `active` boolean, transactional update, and `WHERE active = true` discipline are all established.

**Audit logs**: The existing `AuditEmitter` and `AuditResourcePMDocument` resource type are extended with new actions for version tracking. Audit logs reference version IDs in their `details` JSONB but do not store content — that lives in the version rows, which have no retention expiry.

**Prompts are code, not config**: Prompt templates are embedded in the Go binary and identical across all orgs. They are versioned by git commits, not by a database table. The `server_deploy_sha` is the natural version identifier — no separate prompt versioning infrastructure needed unless per-org prompt overrides become a feature.
