# Design: Agent Run Capabilities

> **Status:** Not Started | **Last reviewed:** 2026-06-16

## Summary

Sessions and automations should share one **capabilities** model. A capability
is a product-safe bundle of access that controls what an agent run can inspect
or do: repository context, PR history, session history, logs, docs, issue
sources, eval authoring, publishing, and related tool groups.

Manual sessions inherit org defaults from **Settings -> Coding Agents**.
Automations can override those defaults per automation because they run
unattended and repeatedly. Every run resolves to a `capability_snapshot` before
the agent starts, and manual sessions can append explicit, user-approved
capability grants later through the existing human-input request flow.

`143-tools` remains the delivery surface for sandbox agents. Capabilities
control which existing `143-tools` namespaces/actions are visible, plus a small
always-available capability request command for explicit escalation.

```text
code-owned catalog
  -> org default policy
  -> launch/template defaults
  -> automation override, when present
  -> initial session/run capability_snapshot
  -> optional user-approved in-session grant
```

## Product Specification

### Core Concepts

| Concept | User-facing meaning | Implementation meaning |
| --- | --- | --- |
| Goal | What the agent should accomplish. | Prompt/task input. |
| Capability | Context or action class the agent may use. | Catalog entry plus grant. |
| Policy | Saved set of enabled capabilities. | Org default or automation override. |
| Snapshot | What the run actually received. | JSON stored on the run/session; initial grants are fixed at launch and later grants require explicit approval. |

Use the word **capabilities** in product surfaces. Keep raw "tools" as an
implementation detail, except in developer/admin diagnostics.

### Principles

- One capability system should serve manual sessions, automations, event runs,
  and future eval/bootstrap flows.
- Manual sessions should stay low-friction through Settings defaults.
- Automations should expose per-automation overrides and template defaults.
- High-risk capabilities must require deliberate confirmation.
- Disabled capabilities must be absent from prompts, env injection, and
  `143-tools` registration.
- Agents can request more capability during a manual session, but only through
  an explicit user approval request in the session UI.
- All retrieved history, comments, docs, messages, and logs are untrusted
  context, never instructions.

### Initial Capability Catalog

The v1 catalog is code-owned. User-authored custom capabilities are out of
scope.

| ID | Display | Access | Risk | Unlocks |
| --- | --- | --- | --- | --- |
| `repo_context` | Repository context | Read | Low | Code, docs, local repo facts. |
| `pr_history` | PR history | Read | Low | Recent PRs, reviews, conventions. |
| `session_history` | Session history | Read | Medium | Prior 143 sessions for this org/repo. |
| `review_feedback` | Review feedback | Read | Medium | Review comments and learned patterns. |
| `ci_history` | CI/test history | Read | Medium | Test failures and flaky-test evidence. |
| `issue_sources` | Issue sources | Read | Medium | Linear, Sentry, support-derived bugs. |
| `team_docs` | Team docs/messages | Read | Medium | Notion, Slack, architecture/product context. |
| `production_diagnostics` | Production diagnostics | Read | High | Logs and error-tracker reads. |
| `external_comments` | External comments | Write | Medium | Linear/Slack comments or status updates. |
| `project_proposals` | Project proposals | Write | Medium | Planning/project proposal creation. |
| `eval_authoring` | Eval authoring | Write | High | Eval candidate creation. |
| `publishing` | Branch and PR publishing | Publish | High | Branch/PR publication through 143 workflows. |

Future candidates: `external_advisories`, `preview_control`, `merge_control`.

### Non-Goals

- Do not rebuild the `143-tools` command model or duplicate tool definitions.
- Do not expose user-authored custom tools/capabilities in v1.
- Do not silently escalate unattended automations.
- Do not inject new raw provider credentials into a running sandbox without a
  continuation/restart path.

### Settings -> Coding Agents Defaults

Add a **Default capabilities** section to Settings -> Coding Agents.

Recommended initial defaults:

| Capability | Default | Reason |
| --- | --- | --- |
| `repo_context` | On | Baseline for repository-scoped work. |
| `pr_history` | On | Low-risk convention discovery. |
| `issue_sources` | Contextual | On when launched from an issue source. |
| `publishing` | On, permission-bound | Existing branch/PR permissions still apply. |
| `team_docs` | Off | Useful, but can add noise and broader data access. |
| `session_history` | Off | Valuable for metaprogramming, noisy for normal work. |
| `production_diagnostics` | Off | High-risk production context. |

Behavior:

- Viewers can read the effective policy; admins can edit.
- Changes affect future runs only.
- High-risk toggles show confirmation copy.
- Disconnected integrations render as unavailable or degraded.

### Automation Overrides

Automation create/detail should show a compact Advanced summary:

```text
Capabilities: Repository context, Session history, Review feedback, Draft PRs
```

Opening the summary shows grouped toggles: Context, Diagnostics, Planning,
Actions.

Behavior:

- If `capabilities` is absent, use org defaults plus template/launch defaults.
- If `capabilities` is present, replace the automation override policy.
- Template defaults materialize as an automation override, so the user can
  inspect and edit them.
- High-risk capabilities are never silently enabled outside explicit templates.
- Run history shows the snapshot used for that run.
- Unattended automations should not interactively escalate by default. If a
  run needs a missing capability, it should return a clear configuration
  recommendation unless the automation is explicitly configured to pause for
  approval.

### In-Session Capability Requests

Manual coding agents should be able to ask for a missing capability without
making the user preconfigure every possible tool. The request must be explicit:
the agent states the capability, why it needs it, and the minimum access level.

Primary path:

```bash
143-tools capability request \
  --capability session_history \
  --access read \
  --reason "Compare this AGENTS.md update against prior failed automation runs."
```

Product behavior:

- The command creates a durable `session_human_input_requests` approval using
  the existing session request UI.
- The session moves to `awaiting_input`; approving resumes the agent with an
  updated snapshot and regenerated `143-tools` help.
- Denying resumes or fails with a clear `CAPABILITY_DENIED` result.
- Approval is scoped to the current session/thread by default, not to org
  settings or the automation's saved policy.
- High-risk capabilities show stronger copy and may require an admin answer.
- Automations only use this path when configured to pause for approval.

### Use Case Defaults

| Use case | Suggested capabilities | Expected output |
| --- | --- | --- |
| Default coding session | `repo_context`, `pr_history`, contextual `issue_sources`, `publishing` | Normal code/docs PR. |
| Agent instruction maintenance | `repo_context`, `session_history`, `review_feedback`, `pr_history`, draft `publishing` | Small PR to `AGENTS.md`, learned conventions, or design docs. |
| Flaky-test maintenance | `repo_context`, `ci_history`, `pr_history`, `publishing`; optional `session_history` | Fix PR or ranked no-op evidence. |
| Production bug investigation | `issue_sources`, `production_diagnostics`, `repo_context`; optional `team_docs`, `publishing` | Focused bug-fix PR with bounded evidence. |
| Backlog/planning triage | `issue_sources`, `team_docs`, `project_proposals`; optional `external_comments` | Proposed project, issue updates, or summary. |
| Eval bootstrap | `session_history`, `pr_history`, `review_feedback`, `eval_authoring` | Eval candidates with source evidence. |

### Special Guardrails

| Capability | Guardrail |
| --- | --- |
| `session_history` | V1 is same org and repository only; search is summary-first; raw messages require explicit session/thread selection; tool internals are hidden by default. |
| `production_diagnostics` | Read-only in v1; require time-bounded queries and low default limits; cite IDs/windows/signatures instead of copying large logs. |
| `publishing` | Allows branch/PR publication, not merge; draft PR should be default for high-risk templates. |
| `eval_authoring` | Creates candidates only; known solution diffs must not leak into eval-run sessions. |

## Engineering Specification

### Runtime Flow

1. Manual session, automation trigger, event trigger, or eval launch requests a
   run.
2. Capability service loads the org `session_default` policy.
3. It applies launch/template defaults.
4. For automation runs, it applies the automation override policy.
5. It validates availability for the org/repository.
6. It persists `sessions.capability_snapshot`.
7. Automation runs copy the same snapshot to
   `automation_runs.capability_snapshot`.
8. Orchestration injects only allowed provider env vars and generated
   `143-tools` docs.
9. Internal capability-backed APIs validate the current session snapshot.
10. If the agent calls `143-tools capability request`, the backend creates a
    `session_human_input_requests` approval and, on approval, appends the grant
    before resuming. If the newly approved capability needs provider env vars
    that were not already present, resume through a continuation/restart path
    that can refresh the sandbox env.

### Database Schema

Use shared policy/grant tables and run snapshots. Policy tables are
settings/config data, so they should follow the repo's insert-only versioning
pattern: deactivate the current policy, insert a new active policy, and insert
its grants in the same transaction. Launch-time snapshot entries are fixed;
user-approved in-session grants append new entries instead of mutating prior
entries.

```sql
CREATE TABLE agent_capability_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    policy_type text NOT NULL,
    automation_id uuid REFERENCES automations(id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_agent_capability_policy_type
        CHECK (policy_type IN ('session_default', 'automation')),
    CONSTRAINT chk_agent_capability_policy_owner
        CHECK (
            (policy_type = 'session_default' AND automation_id IS NULL)
            OR (policy_type = 'automation' AND automation_id IS NOT NULL)
        ),
    UNIQUE (org_id, id)
);

CREATE UNIQUE INDEX idx_agent_capability_policies_session_default
    ON agent_capability_policies (org_id)
    WHERE policy_type = 'session_default' AND active = true;

CREATE UNIQUE INDEX idx_agent_capability_policies_automation
    ON agent_capability_policies (org_id, automation_id)
    WHERE policy_type = 'automation' AND active = true;

CREATE TABLE agent_capability_policy_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    policy_id uuid NOT NULL,
    capability_id text NOT NULL,
    access_level text NOT NULL DEFAULT 'read',
    enabled boolean NOT NULL DEFAULT true,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_agent_capability_grant_access_level
        CHECK (access_level IN ('read', 'write', 'publish')),
    CONSTRAINT chk_agent_capability_grant_config_object
        CHECK (jsonb_typeof(config) = 'object'),
    CONSTRAINT fk_agent_capability_grants_policy
        FOREIGN KEY (org_id, policy_id)
        REFERENCES agent_capability_policies (org_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_agent_capability_policy_grants_unique
    ON agent_capability_policy_grants (org_id, policy_id, capability_id);

ALTER TABLE sessions
    ADD COLUMN capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_sessions_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array');

ALTER TABLE automation_runs
    ADD COLUMN capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_automation_runs_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array');
```

Store requirements:

- Every query filters by `org_id`.
- Automation policy reads join `automations` on `org_id` and `automation_id`.
- Policy writes run in a transaction and use insert-only replacement.
- Required store methods:
  `GetSessionDefaultPolicy`, `GetAutomationPolicy`,
  `UpdateSessionDefaultPolicy`, `ReplaceAutomationPolicy`, `ListGrantsByPolicy`,
  `AppendApprovedSessionGrant`.
- `AppendApprovedSessionGrant` locks the session row, rejects duplicate active
  grants, appends a `source = "user_approved"` snapshot item, and records the
  `human_input_request_id`. If the session belongs to an automation run, update
  the run snapshot in the same transaction.

Snapshot item fields: `id`, `display_name`, `access_level`, `risk`, `scope`,
`config`, `source`, `granted_at`, and optional `human_input_request_id`.

### Go Models And Service

Add typed string enums in `internal/models` with table-driven validation tests.
The enum values mirror the catalog IDs above.

```go
type AgentCapabilityID string
type AgentCapabilityAccessLevel string // read, write, publish
type AgentCapabilityRisk string        // low, medium, high
type AgentCapabilityScope string       // repository, org, integration
type AgentCapabilityPolicyType string  // session_default, automation
type AgentCapabilityGrantSource string // session_default, automation, launch_default, user_approved

type AgentCapabilityGrant struct {
    ID           uuid.UUID                  `db:"id" json:"id"`
    OrgID        uuid.UUID                  `db:"org_id" json:"org_id"`
    PolicyID     uuid.UUID                  `db:"policy_id" json:"policy_id"`
    CapabilityID AgentCapabilityID          `db:"capability_id" json:"capability_id"`
    AccessLevel  AgentCapabilityAccessLevel `db:"access_level" json:"access_level"`
    Enabled      bool                       `db:"enabled" json:"enabled"`
    Config       json.RawMessage            `db:"config" json:"config"`
}

type AgentCapabilitySnapshotItem struct {
    ID                  AgentCapabilityID          `json:"id"`
    DisplayName         string                     `json:"display_name"`
    AccessLevel         AgentCapabilityAccessLevel `json:"access_level"`
    Risk                AgentCapabilityRisk        `json:"risk"`
    Scope               AgentCapabilityScope       `json:"scope"`
    Config              json.RawMessage            `json:"config"`
    Source              AgentCapabilityGrantSource `json:"source"`
    GrantedAt           time.Time                  `json:"granted_at"`
    HumanInputRequestID *uuid.UUID                 `json:"human_input_request_id,omitempty"`
}
```

Service package: `internal/services/agentcapabilities`.

```go
type ResolveInput struct {
    OrgID           uuid.UUID
    RepositoryID    *uuid.UUID
    SessionOrigin   models.SessionOrigin
    AutomationID    *uuid.UUID
    AutomationRunID *uuid.UUID
    TemplateID      string
    ExistingSnapshot []models.AgentCapabilitySnapshotItem
}

func (s *Service) Definitions() []models.AgentCapabilityDefinition
func (s *Service) ValidateGrant(models.AgentCapabilityGrant) error
func (s *Service) ResolveAvailability(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) ([]Availability, error)
func (s *Service) ResolveForSession(ctx context.Context, in ResolveInput) ([]models.AgentCapabilitySnapshotItem, error)
func (s *Service) RequestGrant(ctx context.Context, in GrantRequestInput) (models.HumanInputRequest, error)
func (s *Service) ApplyApprovedGrant(ctx context.Context, in ApprovedGrantInput) ([]models.AgentCapabilitySnapshotItem, error)
```

Responsibilities:

- own the code-defined catalog,
- validate grant IDs, access levels, and config,
- resolve provider/integration availability,
- merge defaults, launch defaults, template defaults, and automation overrides,
- map snapshots to allowed `143-tools` namespaces and env vars,
- append approved session-scoped grants after human-input approval.

### API Contract

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /api/v1/agent-capabilities?repository_id=<uuid>` | Viewer+ | Catalog plus availability. |
| `GET /api/v1/settings/agent/capabilities` | Viewer+ | Current org default policy. |
| `PATCH /api/v1/settings/agent/capabilities` | Admin | Replace org default policy. |
| `GET /api/v1/automations/{id}/capabilities` | Viewer+ | Automation effective/override policy. |
| `POST /api/v1/automations` | Existing automation auth | Optional `capabilities` on create. |
| `PATCH /api/v1/automations/{id}` | Existing automation auth | Optional full policy replacement. |
| `GET /api/v1/internal/agent-capabilities/effective` | Internal session token | Effective snapshot for `143-tools capability list`. |
| `POST /api/v1/internal/agent-capabilities/requests` | Internal session token | Create a human-input capability approval request. |

Policy request shape:

```json
{
  "capabilities": [
    {
      "capability_id": "session_history",
      "access_level": "read",
      "enabled": true,
      "config": {
        "max_age_days": 30,
        "raw_messages": false
      }
    }
  ]
}
```

Catalog response items include `id`, `display_name`, `description`,
`category`, `max_access_level`, `risk`, `scope`, `requirements`,
`default_config`, and `availability`.

Expose `capability_snapshot` on session detail and automation run detail.
Session lists may omit it.

Primary errors:

- `INVALID_CAPABILITY`
- `INVALID_CAPABILITY_ACCESS`
- `INVALID_CAPABILITY_CONFIG`
- `CAPABILITY_UNAVAILABLE`
- `CAPABILITY_APPROVAL_REQUIRED`
- `CAPABILITY_DENIED`
- `FORBIDDEN`
- `UPDATE_CAPABILITIES_FAILED`

Internal capability request body:

```json
{
  "capability_id": "session_history",
  "access_level": "read",
  "reason": "Compare this AGENTS.md update against prior failed automation runs.",
  "thread_id": "uuid"
}
```

Implementation should reuse `session_human_input_requests`, not create a new
approval table. Use `request_kind = "action_choice"` for context capability
requests and `request_kind = "tool_approval"` when the requested capability
enables an external write/publish action.

### Internal Session-History Tool

First new capability-backed tool: `143-tools session-history`.

```http
GET /api/v1/internal/session-history/search
GET /api/v1/internal/session-history/{session_id}
GET /api/v1/internal/session-history/{session_id}/threads/{thread_id}/messages
```

Authorization:

- session-scoped internal token,
- current session snapshot includes `session_history`,
- returned sessions match token org and repository.

Search params: `q`, `status`, `created_after`, `created_before`,
`changed_path`, `failure_category`, `limit` default 10 max 50, `cursor`.

Search returns summaries first: `id`, `title`, `status`, `origin`,
`result_summary`, `failure_category`, `changed_paths`, and `meta.next_cursor`.

### 143-Tools And Registry Policy

Capability snapshots control `143-tools` registration and env injection. Do
not replace the existing CLI or duplicate tool definitions:

- Keep `BuildRegistryFromEnv` as the source of provider defaults and credential
  registration.
- Keep `BuildRegistryFromOrg` for server-proxied local-agent tools.
- Keep `ToolRegistry.ListTools`, `ToolRegistry.CallTool`, and `cliPathForTool`
  as the shared CLI/MCP command contract.
- Add a capability-aware `ToolSource` wrapper that filters `ListTools()` and
  blocks `CallTool()` for disallowed tool names.
- Preserve existing per-tool flag defaults from `ToolSchema.Default`; capability
  config may bound limits but should not redefine normal command defaults.
- `143-tools --help` remains the runtime source of truth for agents.

```go
type ToolCapabilityPolicy struct {
    Capabilities []models.AgentCapabilitySnapshotItem
}

func NewCapabilityFilteredToolSource(base ToolSource, policy ToolCapabilityPolicy) ToolSource
```

| Capability | Tool namespace examples |
| --- | --- |
| `session_history` | `session-history` |
| `pr_history` | read-only GitHub PR/review tools |
| `ci_history` | CI/test insight tools |
| `issue_sources` | read-only Sentry/Linear tools |
| `team_docs` | Notion/Slack read tools |
| `production_diagnostics` | logs and error-tracker read tools |
| `external_comments` | Linear/Slack write/comment tools |
| `project_proposals` | `project propose` |
| `eval_authoring` | `eval add` |
| `publishing` | `pr create`, branch publish |

If a provider has read and write methods in one namespace, register only the
methods allowed by the snapshot.

Always register a 143-owned `capability` namespace when `INTERNAL_API_TOKEN`
and `INTERNAL_API_URL` are available:

| Command | Purpose |
| --- | --- |
| `143-tools capability list` | Show the current snapshot and available requestable capabilities. |
| `143-tools capability request` | Create the human-input approval request described above. |

The capability namespace is a meta-tool. It should not grant access itself; it
only asks the backend to create a session approval request.

Runtime constraint: direct env-backed sandbox tools cannot gain provider
credentials that were omitted at launch. Approved grants that need new env vars
must resume through a continuation/restart that refreshes env injection, or use
a server-backed internal API path that keeps credentials server-side.

### Validation Rules

- Unknown capability IDs are rejected.
- Access level cannot exceed the catalog definition.
- Disabled grants are omitted from snapshots.
- Repository-scoped capabilities require a repository in v1.
- In-session capability requests require an active internal session token and
  cannot update org defaults or automation policies.
- `production_diagnostics` requires connected providers and bounded defaults.
- `eval_authoring` requires an eval-bootstrap launch or admin-approved eval
  automation template.
- `publishing` never implies merge permission.
- Backend validation must not rely only on UI confirmation.

### Frontend Types

Add shared types in `frontend/src/lib/types.ts` that mirror the Go grant and
snapshot structs. Extend `Session`, `Automation`, and `AutomationRun` with the
relevant capability grant/snapshot fields.

### Audit And Tests

Audit events:

- `agent.capabilities.default_updated`
- `automation.capabilities.updated`
- `agent.capability.used`
- `agent.capability.denied`
- `agent.capability.grant_requested`
- `agent.capability.grant_approved`

Audit details include capability IDs, access level, automation ID, run ID,
session ID, and counts. Never include raw transcripts, logs, comments, docs, or
secrets.

Required tests:

- enum validation tests,
- store tests proving `org_id` filters,
- settings handler tests for default policy read/update,
- automation create/update tests for override policies,
- snapshot persistence tests for sessions and automation runs,
- internal session-history authorization tests,
- registry policy tests proving disabled capabilities hide tools,
- direct-call tests proving filtered `CallTool()` rejects hidden tools,
- `143-tools capability request` tests proving approval is required before new
  namespaces/actions appear,
- Settings -> Coding Agents UI tests,
- automation capability-sheet and high-risk confirmation tests.

Implementation verification:

```bash
go vet ./...
go build ./...
go test ./...

cd frontend
npm run typecheck
npm run lint
npm run build
```

### Implementation Order

1. Add model enums, catalog service, and validation tests.
2. Add policy/grant tables and snapshot columns.
3. Add policy/grant store and tenancy tests.
4. Wire Settings -> Coding Agents default policy.
5. Resolve/persist snapshots for manual session creation.
6. Wire automation create/update and automation run snapshots.
7. Add catalog API and response fields.
8. Add settings UI, automation capability sheet, and template defaults.
9. Add capability-filtered env/tool registry policy around the existing
   `143-tools` registry.
10. Add `143-tools capability list/request` backed by existing human-input
    approvals.
11. Add `session-history` internal API and CLI namespace.
12. Add session/run detail snapshot rendering and audit events.

### Open Questions

- Is `repo_context` user-toggleable, or implied for repository-scoped runs?
- Should high-risk capabilities be admin-only by default?
- Should v1 allow per-session overrides, or only Settings defaults?
- Should capability config use JSON Schema in the catalog response?
- Should session-history search include transcript full-text search in v1?
- Should capability usage counts use audit/session logs or a dedicated table?
