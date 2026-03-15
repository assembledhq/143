# Design: Audit Logs

**Status**: Proposal
**Depends on**: [01-database-schema.md](01-database-schema.md), [02-api-server.md](02-api-server.md)

---

## 1. Problem Statement

Today, 143 has a basic `audit_log` table (created in migration `000001`) with minimal structure:

```sql
CREATE TABLE audit_log (
    id            bigserial   PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES organizations(id),
    actor_type    text        NOT NULL,
    actor_id      text        NOT NULL,
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   uuid,
    details       jsonb,
    created_at    timestamptz NOT NULL DEFAULT now()
);
```

This table exists but is **not wired into the application** — no Go model, no store, no handler, and no code paths emit audit entries. Additionally, it lacks:

- **Optional user association**: `actor_id` is an opaque `text` field with no foreign key, making it impossible to join with `users` to answer "who did what"
- **Structured action taxonomy**: No defined vocabulary for actions, making queries unreliable
- **IP/user-agent tracking**: No request context for security forensics
- **Correlation**: No way to tie an audit entry to the session, project, or job that triggered it
- **Retention policy**: No TTL or partitioning strategy for high-volume tables
- **Query patterns**: Missing indexes for common access patterns (by actor, by action, time-range scans)

We need a production-grade audit log that captures **all significant state changes** — whether initiated by a user, an agent, the PM system, a webhook, or a scheduled job — and makes them queryable for compliance, debugging, and operational visibility.

---

## 2. Design Principles

1. **Append-only and immutable** — Audit logs must never be updated or deleted through the application. The existing immutability trigger is preserved and extended.
2. **Organization-scoped** — Every entry belongs to exactly one organization. All queries filter by `org_id`.
3. **Actor-agnostic** — The system must attribute actions to users, agents, system processes, and external webhooks uniformly.
4. **Optional user linkage** — When a human user is the actor, `user_id` provides a typed foreign key for joins. When the actor is a system process, `user_id` is NULL and `actor_type` + `actor_id` provide identification.
5. **Structured but extensible** — Core fields use constrained vocabularies (enums for `actor_type`, `action`, `resource_type`). The `details` JSONB field captures action-specific context without schema changes.
6. **Low-friction instrumentation** — Emitting audit entries should be a single function call. The audit store should accept context and extract org/user automatically where possible.
7. **Query-first indexing** — Indexes are designed around the access patterns defined in section 6, not speculative.

---

## 3. Schema

### 3.1 Migration: `000019_audit_logs.up.sql`

The new `audit_logs` table replaces the existing `audit_log` table. A migration renames the old table for data preservation, then creates the new one.

```sql
-- =============================================================================
-- Rename legacy audit_log table (preserve existing data)
-- =============================================================================
ALTER TABLE audit_log RENAME TO audit_log_legacy;
DROP TRIGGER IF EXISTS audit_log_immutable ON audit_log_legacy;

-- =============================================================================
-- audit_logs (new)
-- =============================================================================
CREATE TABLE audit_logs (
    id              bigserial       PRIMARY KEY,
    org_id          uuid            NOT NULL REFERENCES organizations(id),

    -- Actor identification
    actor_type      text            NOT NULL,  -- 'user', 'agent', 'system', 'webhook'
    actor_id        text            NOT NULL,  -- user UUID, agent run UUID, 'pm_agent', 'scheduler', provider name, etc.
    user_id         uuid            REFERENCES users(id),  -- nullable; set when actor is a user

    -- What happened
    action          text            NOT NULL,  -- e.g. 'session.created', 'project.started', 'settings.updated'
    resource_type   text            NOT NULL,  -- e.g. 'session', 'project', 'issue', 'settings', 'team_member'
    resource_id     text,                      -- text (not uuid) to support non-uuid identifiers (e.g. provider names)

    -- Context
    details         jsonb,                     -- action-specific payload (before/after values, parameters, etc.)
    request_id      text,                      -- correlates with chi middleware.RequestID for HTTP-initiated actions
    ip_address      inet,                      -- source IP for user-initiated actions
    user_agent      text,                      -- browser/client user-agent for user-initiated actions

    -- Correlation (no FK intentionally — audit entries must survive if the
    -- referenced session or project is deleted)
    session_id      uuid,                      -- links to sessions table when action is session-related
    project_id      uuid,                      -- links to projects table when action is project-related

    created_at      timestamptz     NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------
-- Indexes (designed for the query patterns in section 6)
-- -----------------------------------------------------------------------

-- Primary listing: "show me the audit trail for this org, newest first"
CREATE INDEX idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC);

-- Resource drill-down: "show me all actions on this specific resource"
CREATE INDEX idx_audit_logs_resource ON audit_logs (org_id, resource_type, resource_id, created_at DESC);

-- Actor drill-down: "show me everything this user did"
CREATE INDEX idx_audit_logs_user ON audit_logs (org_id, user_id, created_at DESC) WHERE user_id IS NOT NULL;

-- Action filtering: "show me all session.created events"
CREATE INDEX idx_audit_logs_action ON audit_logs (org_id, action, created_at DESC);

-- Correlation: "show me all audit entries for this session/project"
CREATE INDEX idx_audit_logs_session ON audit_logs (session_id, created_at DESC) WHERE session_id IS NOT NULL;
CREATE INDEX idx_audit_logs_project ON audit_logs (project_id, created_at DESC) WHERE project_id IS NOT NULL;

-- -----------------------------------------------------------------------
-- Immutability trigger (same pattern as legacy table)
-- -----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION prevent_audit_logs_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_logs_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_logs_modification();
```

### 3.2 Down migration: `000019_audit_logs.down.sql`

```sql
DROP TRIGGER IF EXISTS audit_logs_immutable ON audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_logs_modification();
DROP TABLE IF EXISTS audit_logs;
ALTER TABLE IF EXISTS audit_log_legacy RENAME TO audit_log;

-- Re-create the immutability function in case it was dropped by a later migration.
-- Using CREATE OR REPLACE so this is safe even if it still exists from 000001.
CREATE OR REPLACE FUNCTION prevent_audit_log_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_modification();
```

---

## 4. Action Taxonomy

Actions follow a `resource.verb` naming convention. This keeps them grep-friendly and naturally groups by resource type.

### 4.1 Session actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `session.created` | user | User triggered a fix or created a manual session |
| `session.started` | system | Agent execution began |
| `session.completed` | agent | Agent finished successfully |
| `session.failed` | agent | Agent run failed |
| `session.cancelled` | user | User cancelled a running session |
| `session.status_changed` | system | Any status transition not covered above |
| `session.question.created` | agent | Agent asked a clarifying question |
| `session.question.answered` | user | User answered a question |
| `session.resumed_locally` | user | User took over a session via CLI |

### 4.2 Project actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `project.created` | user | User created a project |
| `project.updated` | user | User modified project fields |
| `project.deleted` | user | User deleted a project |
| `project.started` | user | User transitioned project to active |
| `project.paused` | user | User paused a project |
| `project.resumed` | user | User resumed a paused project |
| `project.approved` | user | User approved a proposed project |
| `project.dismissed` | user | User dismissed/cancelled a project |
| `project.run_triggered` | user | User triggered an immediate PM cycle |
| `project.cycle_completed` | system | PM cycle completed for project |
| `project.task.created` | user/system | Task added to project |
| `project.task.updated` | user | User modified a task |
| `project.task.deleted` | user | User removed a task |
| `project.task.retried` | user | User retried a failed task |

### 4.3 Issue actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `issue.created` | webhook | Issue ingested from external source |
| `issue.reprioritized` | user | Admin triggered re-prioritization |

### 4.4 PM actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `pm.analysis_triggered` | user | Admin triggered PM analysis |
| `pm.plan_created` | system | PM agent produced a plan |
| `pm.decision_made` | system | PM made a delegate/skip/cluster decision |

### 4.5 Team & settings actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `settings.updated` | user | Admin changed org settings |
| `team.member_invited` | user | Admin invited a team member |
| `team.member_role_changed` | user | Admin changed a member's role |
| `team.member_removed` | user | Admin removed a team member |
| `team.invitation_revoked` | user | Admin revoked an invitation |
| `team.invitation_accepted` | user | New member accepted an invitation |

### 4.6 Integration & credential actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `integration.connected` | user | User connected an integration (Linear, Sentry, GitHub, Slack) |
| `credential.updated` | user | Admin updated agent credentials |
| `credential.deleted` | user | Admin deleted agent credentials |

### 4.7 Auth actions

| Action | Actor Type | Description |
|--------|-----------|-------------|
| `auth.login` | user | User logged in |
| `auth.logout` | user | User logged out |
| `auth.register` | user | New user registered |

This taxonomy is **not enforced at the database level** (it's `text`, not an enum). The vocabulary is enforced in Go constants and documented here. This allows new actions to be added without migrations.

---

## 5. Go Model & Store

### 5.1 Enums (`internal/models/audit_enums.go`)

Following the codebase convention (see `session_enums.go`, `pm_enums.go`), all categorical fields use typed strings with validation.

```go
package models

import "fmt"

// AuditActorType identifies who or what performed an audited action.
type AuditActorType string

const (
    AuditActorUser    AuditActorType = "user"
    AuditActorAgent   AuditActorType = "agent"
    AuditActorSystem  AuditActorType = "system"
    AuditActorWebhook AuditActorType = "webhook"
)

func (t AuditActorType) Validate() error {
    switch t {
    case AuditActorUser, AuditActorAgent, AuditActorSystem, AuditActorWebhook:
        return nil
    default:
        return fmt.Errorf("invalid AuditActorType: %q", t)
    }
}

// AuditAction identifies the specific action that was performed.
// Follows a resource.verb naming convention.
type AuditAction string

const (
    // Session actions
    AuditActionSessionCreated          AuditAction = "session.created"
    AuditActionSessionStarted          AuditAction = "session.started"
    AuditActionSessionCompleted        AuditAction = "session.completed"
    AuditActionSessionFailed           AuditAction = "session.failed"
    AuditActionSessionCancelled        AuditAction = "session.cancelled"
    AuditActionSessionStatusChanged    AuditAction = "session.status_changed"
    AuditActionSessionQuestionCreated  AuditAction = "session.question.created"
    AuditActionSessionQuestionAnswered AuditAction = "session.question.answered"
    AuditActionSessionResumedLocally   AuditAction = "session.resumed_locally"

    // Project actions
    AuditActionProjectCreated        AuditAction = "project.created"
    AuditActionProjectUpdated        AuditAction = "project.updated"
    AuditActionProjectDeleted        AuditAction = "project.deleted"
    AuditActionProjectStarted        AuditAction = "project.started"
    AuditActionProjectPaused         AuditAction = "project.paused"
    AuditActionProjectResumed        AuditAction = "project.resumed"
    AuditActionProjectApproved       AuditAction = "project.approved"
    AuditActionProjectDismissed      AuditAction = "project.dismissed"
    AuditActionProjectRunTriggered   AuditAction = "project.run_triggered"
    AuditActionProjectCycleCompleted AuditAction = "project.cycle_completed"
    AuditActionProjectTaskCreated    AuditAction = "project.task.created"
    AuditActionProjectTaskUpdated    AuditAction = "project.task.updated"
    AuditActionProjectTaskDeleted    AuditAction = "project.task.deleted"
    AuditActionProjectTaskRetried    AuditAction = "project.task.retried"

    // Issue actions
    AuditActionIssueCreated       AuditAction = "issue.created"
    AuditActionIssueReprioritized AuditAction = "issue.reprioritized"

    // PM actions
    AuditActionPMAnalysisTriggered AuditAction = "pm.analysis_triggered"
    AuditActionPMPlanCreated       AuditAction = "pm.plan_created"
    AuditActionPMDecisionMade      AuditAction = "pm.decision_made"

    // Team & settings actions
    AuditActionSettingsUpdated        AuditAction = "settings.updated"
    AuditActionTeamMemberInvited      AuditAction = "team.member_invited"
    AuditActionTeamMemberRoleChanged  AuditAction = "team.member_role_changed"
    AuditActionTeamMemberRemoved      AuditAction = "team.member_removed"
    AuditActionTeamInvitationRevoked  AuditAction = "team.invitation_revoked"
    AuditActionTeamInvitationAccepted AuditAction = "team.invitation_accepted"

    // Integration & credential actions
    AuditActionIntegrationConnected AuditAction = "integration.connected"
    AuditActionCredentialUpdated    AuditAction = "credential.updated"
    AuditActionCredentialDeleted    AuditAction = "credential.deleted"

    // Auth actions
    AuditActionAuthLogin    AuditAction = "auth.login"
    AuditActionAuthLogout   AuditAction = "auth.logout"
    AuditActionAuthRegister AuditAction = "auth.register"
)

// AuditAction.Validate checks that the action is a known value.
func (a AuditAction) Validate() error {
    switch a {
    case AuditActionSessionCreated, AuditActionSessionStarted, AuditActionSessionCompleted,
        AuditActionSessionFailed, AuditActionSessionCancelled, AuditActionSessionStatusChanged,
        AuditActionSessionQuestionCreated, AuditActionSessionQuestionAnswered, AuditActionSessionResumedLocally,
        AuditActionProjectCreated, AuditActionProjectUpdated, AuditActionProjectDeleted,
        AuditActionProjectStarted, AuditActionProjectPaused, AuditActionProjectResumed,
        AuditActionProjectApproved, AuditActionProjectDismissed, AuditActionProjectRunTriggered,
        AuditActionProjectCycleCompleted, AuditActionProjectTaskCreated, AuditActionProjectTaskUpdated,
        AuditActionProjectTaskDeleted, AuditActionProjectTaskRetried,
        AuditActionIssueCreated, AuditActionIssueReprioritized,
        AuditActionPMAnalysisTriggered, AuditActionPMPlanCreated, AuditActionPMDecisionMade,
        AuditActionSettingsUpdated, AuditActionTeamMemberInvited, AuditActionTeamMemberRoleChanged,
        AuditActionTeamMemberRemoved, AuditActionTeamInvitationRevoked, AuditActionTeamInvitationAccepted,
        AuditActionIntegrationConnected, AuditActionCredentialUpdated, AuditActionCredentialDeleted,
        AuditActionAuthLogin, AuditActionAuthLogout, AuditActionAuthRegister:
        return nil
    default:
        return fmt.Errorf("invalid AuditAction: %q", a)
    }
}

// AuditResourceType identifies the type of resource an action targets.
type AuditResourceType string

const (
    AuditResourceSession     AuditResourceType = "session"
    AuditResourceProject     AuditResourceType = "project"
    AuditResourceProjectTask AuditResourceType = "project_task"
    AuditResourceIssue       AuditResourceType = "issue"
    AuditResourcePMPlan      AuditResourceType = "pm_plan"
    AuditResourcePMDecision  AuditResourceType = "pm_decision"
    AuditResourceSettings    AuditResourceType = "settings"
    AuditResourceTeamMember  AuditResourceType = "team_member"
    AuditResourceInvitation  AuditResourceType = "invitation"
    AuditResourceIntegration AuditResourceType = "integration"
    AuditResourceCredential  AuditResourceType = "credential"
    AuditResourceUser        AuditResourceType = "user"
)

func (t AuditResourceType) Validate() error {
    switch t {
    case AuditResourceSession, AuditResourceProject, AuditResourceProjectTask,
        AuditResourceIssue, AuditResourcePMPlan, AuditResourcePMDecision,
        AuditResourceSettings, AuditResourceTeamMember, AuditResourceInvitation,
        AuditResourceIntegration, AuditResourceCredential, AuditResourceUser:
        return nil
    default:
        return fmt.Errorf("invalid AuditResourceType: %q", t)
    }
}
```

### 5.2 Model (`internal/models/audit.go`)

```go
package models

import (
    "encoding/json"
    "net/netip"
    "time"

    "github.com/google/uuid"
)

// AuditLog represents an immutable audit trail entry.
type AuditLog struct {
    ID           int64             `db:"id" json:"id"`
    OrgID        uuid.UUID         `db:"org_id" json:"org_id"`
    ActorType    AuditActorType    `db:"actor_type" json:"actor_type"`
    ActorID      string            `db:"actor_id" json:"actor_id"`
    UserID       *uuid.UUID        `db:"user_id" json:"user_id,omitempty"`
    Action       AuditAction       `db:"action" json:"action"`
    ResourceType AuditResourceType `db:"resource_type" json:"resource_type"`
    ResourceID   *string           `db:"resource_id" json:"resource_id,omitempty"`
    Details      json.RawMessage   `db:"details" json:"details,omitempty"`
    RequestID    *string           `db:"request_id" json:"request_id,omitempty"`
    IPAddress    *netip.Addr       `db:"ip_address" json:"ip_address,omitempty"`
    UserAgent    *string           `db:"user_agent" json:"user_agent,omitempty"`
    SessionID    *uuid.UUID        `db:"session_id" json:"session_id,omitempty"`
    ProjectID    *uuid.UUID        `db:"project_id" json:"project_id,omitempty"`
    CreatedAt    time.Time         `db:"created_at" json:"created_at"`
}
```

### 5.3 Store (`internal/db/audit_log_store.go`)

> **Note**: The `whereClause` helper below is defined in a shared file
> `internal/db/where_clause.go` so that other stores (`SessionStore`,
> `IssueStore`, `ProjectStore`, etc.) can adopt it to replace their ad-hoc
> `query += " AND ..."` concatenation. The audit log store is the first
> consumer.

```go
package db

import (
    "context"
    "fmt"
    "strconv"
    "strings"

    "github.com/assembledhq/143/internal/models"
    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
)

// escapeLike escapes SQL LIKE meta-characters (%, _) so that user-supplied
// values are matched literally.
func escapeLike(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `%`, `\%`)
    s = strings.ReplaceAll(s, `_`, `\_`)
    return s
}

type AuditLogStore struct {
    db DBTX
}

func NewAuditLogStore(db DBTX) *AuditLogStore {
    return &AuditLogStore{db: db}
}

// Create inserts a new audit log entry. This is the only write operation —
// the table is append-only (enforced by DB trigger).
func (s *AuditLogStore) Create(ctx context.Context, entry *models.AuditLog) error {
    query := `
        INSERT INTO audit_logs (
            org_id, actor_type, actor_id, user_id,
            action, resource_type, resource_id,
            details, request_id, ip_address, user_agent,
            session_id, project_id
        ) VALUES (
            @org_id, @actor_type, @actor_id, @user_id,
            @action, @resource_type, @resource_id,
            @details, @request_id, @ip_address, @user_agent,
            @session_id, @project_id
        )
        RETURNING id, created_at`

    row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
        "org_id":        entry.OrgID,
        "actor_type":    entry.ActorType,
        "actor_id":      entry.ActorID,
        "user_id":       entry.UserID,
        "action":        entry.Action,
        "resource_type": entry.ResourceType,
        "resource_id":   entry.ResourceID,
        "details":       entry.Details,
        "request_id":    entry.RequestID,
        "ip_address":    entry.IPAddress,
        "user_agent":    entry.UserAgent,
        "session_id":    entry.SessionID,
        "project_id":    entry.ProjectID,
    })
    return row.Scan(&entry.ID, &entry.CreatedAt)
}

// AuditLogFilters controls listing/search behavior.
type AuditLogFilters struct {
    ActorType    models.AuditActorType    // filter by actor type
    UserID       *uuid.UUID               // filter by specific user
    Action       models.AuditAction       // filter by action (exact match)
    ActionPrefix string                   // filter by action prefix (e.g. "session." matches all session actions)
    ResourceType models.AuditResourceType // filter by resource type
    ResourceID   string                   // filter by specific resource
    SessionID    *uuid.UUID               // filter by correlated session
    ProjectID    *uuid.UUID               // filter by correlated project
    Since        *string                  // ISO 8601 timestamp lower bound
    Until        *string                  // ISO 8601 timestamp upper bound
    Limit        int
    Cursor       string                   // cursor for keyset pagination (bigserial id)
}

// whereClause is a shared helper (internal/db/where_clause.go) that accumulates
// WHERE conditions and named args. It eliminates the error-prone pattern of
// manually concatenating " AND ..." fragments and ensures every condition is
// joined consistently. Other stores should adopt this instead of ad-hoc concatenation.
type whereClause struct {
    conditions []string
    args       pgx.NamedArgs
}

func newWhereClause() *whereClause {
    return &whereClause{args: pgx.NamedArgs{}}
}

func (w *whereClause) add(condition string, name string, value interface{}) {
    w.conditions = append(w.conditions, condition)
    w.args[name] = value
}

func (w *whereClause) build() (string, pgx.NamedArgs) {
    if len(w.conditions) == 0 {
        return "", w.args
    }
    return " WHERE " + strings.Join(w.conditions, " AND "), w.args
}

// List returns audit log entries for an organization, filtered and paginated.
func (s *AuditLogStore) List(ctx context.Context, orgID uuid.UUID, filters AuditLogFilters) ([]models.AuditLog, error) {
    w := newWhereClause()
    w.add("org_id = @org_id", "org_id", orgID)

    if filters.ActorType != "" {
        w.add("actor_type = @actor_type", "actor_type", filters.ActorType)
    }
    if filters.UserID != nil {
        w.add("user_id = @user_id", "user_id", *filters.UserID)
    }
    if filters.Action != "" {
        w.add("action = @action", "action", filters.Action)
    }
    if filters.ActionPrefix != "" {
        w.add("action LIKE @action_prefix ESCAPE '\\'", "action_prefix", escapeLike(filters.ActionPrefix)+"%")
    }
    if filters.ResourceType != "" {
        w.add("resource_type = @resource_type", "resource_type", filters.ResourceType)
    }
    if filters.ResourceID != "" {
        w.add("resource_id = @resource_id", "resource_id", filters.ResourceID)
    }
    if filters.SessionID != nil {
        w.add("session_id = @session_id", "session_id", *filters.SessionID)
    }
    if filters.ProjectID != nil {
        w.add("project_id = @project_id", "project_id", *filters.ProjectID)
    }
    if filters.Since != nil {
        w.add("created_at >= @since", "since", *filters.Since)
    }
    if filters.Until != nil {
        w.add("created_at <= @until", "until", *filters.Until)
    }
    if filters.Cursor != "" {
        cursorID, err := strconv.ParseInt(filters.Cursor, 10, 64)
        if err != nil {
            return nil, fmt.Errorf("invalid cursor %q: %w", filters.Cursor, err)
        }
        w.add("id < @cursor", "cursor", cursorID)
    }

    where, args := w.build()

    query := `
        SELECT id, org_id, actor_type, actor_id, user_id,
               action, resource_type, resource_id,
               details, request_id, ip_address, user_agent,
               session_id, project_id, created_at
        FROM audit_logs` + where + ` ORDER BY id DESC`

    limit := filters.Limit
    if limit <= 0 || limit > 200 {
        limit = 50
    }
    query += fmt.Sprintf(` LIMIT %d`, limit)

    rows, err := s.db.Query(ctx, query, args)
    if err != nil {
        return nil, fmt.Errorf("query audit logs: %w", err)
    }
    return pgx.CollectRows(rows, pgx.RowToStructByName[models.AuditLog])
}
```

### 5.4 Emitter helper (`internal/db/audit_emitter.go`)

To reduce boilerplate at call sites, provide a convenience layer:

```go
package db

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/netip"

    "github.com/assembledhq/143/internal/models"
    "github.com/google/uuid"
)

// AuditEmitter provides convenience methods for emitting audit log entries.
// All Emit* methods log errors internally and never return them, so callers
// can treat emission as fire-and-forget without discarding errors silently.
type AuditEmitter struct {
    store  *AuditLogStore
    logger *slog.Logger
}

func NewAuditEmitter(store *AuditLogStore, logger *slog.Logger) *AuditEmitter {
    return &AuditEmitter{store: store, logger: logger}
}

// EmitUserAction logs an action performed by an authenticated user.
func (e *AuditEmitter) EmitUserAction(ctx context.Context, params UserActionParams) {
    entry := &models.AuditLog{
        OrgID:        params.OrgID,
        ActorType:    models.AuditActorUser,
        ActorID:      params.UserID.String(),
        UserID:       &params.UserID,
        Action:       params.Action,
        ResourceType: params.ResourceType,
        ResourceID:   params.ResourceID,
        Details:      params.Details,
        RequestID:    params.RequestID,
        IPAddress:    params.IPAddress,
        UserAgent:    params.UserAgent,
        SessionID:    params.SessionID,
        ProjectID:    params.ProjectID,
    }
    if err := e.store.Create(ctx, entry); err != nil {
        e.logger.ErrorContext(ctx, "failed to emit audit log",
            "action", params.Action,
            "actor_id", params.UserID,
            "error", err,
        )
    }
}

type UserActionParams struct {
    OrgID        uuid.UUID
    UserID       uuid.UUID
    Action       models.AuditAction
    ResourceType models.AuditResourceType
    ResourceID   *string
    Details      json.RawMessage
    RequestID    *string
    IPAddress    *netip.Addr
    UserAgent    *string
    SessionID    *uuid.UUID
    ProjectID    *uuid.UUID
}

// EmitSystemAction logs an action performed by a system process (PM agent, scheduler, etc.).
func (e *AuditEmitter) EmitSystemAction(ctx context.Context, params SystemActionParams) {
    entry := &models.AuditLog{
        OrgID:        params.OrgID,
        ActorType:    models.AuditActorSystem,
        ActorID:      params.ActorID,
        Action:       params.Action,
        ResourceType: params.ResourceType,
        ResourceID:   params.ResourceID,
        Details:      params.Details,
        SessionID:    params.SessionID,
        ProjectID:    params.ProjectID,
    }
    if err := e.store.Create(ctx, entry); err != nil {
        e.logger.ErrorContext(ctx, "failed to emit audit log",
            "action", params.Action,
            "actor_id", params.ActorID,
            "error", err,
        )
    }
}

type SystemActionParams struct {
    OrgID        uuid.UUID
    ActorID      string // e.g. "pm_agent", "scheduler", "validator"
    Action       models.AuditAction
    ResourceType models.AuditResourceType
    ResourceID   *string
    Details      json.RawMessage
    SessionID    *uuid.UUID
    ProjectID    *uuid.UUID
}
```

---

## 6. API

### 6.1 Endpoints

All audit log endpoints are **read-only** and require authentication.

| Method | Path | Role | Description |
|--------|------|------|-------------|
| `GET` | `/api/v1/audit-logs` | admin | List audit logs with filters |
| `GET` | `/api/v1/audit-logs/{id}` | admin | Get a single audit log entry |

Audit log listing is restricted to **admin** role. This is sensitive data — org members and viewers should not have access to the full audit trail.

### 6.2 Query parameters for `GET /api/v1/audit-logs`

| Parameter | Type | Description |
|-----------|------|-------------|
| `actor_type` | `AuditActorType` | Filter by actor type (`user`, `agent`, `system`, `webhook`) |
| `user_id` | uuid | Filter by specific user |
| `action` | `AuditAction` | Exact action match (e.g. `session.created`) |
| `action_prefix` | string | Action prefix match (e.g. `session.` returns all session actions) |
| `resource_type` | `AuditResourceType` | Filter by resource type |
| `resource_id` | string | Filter by specific resource |
| `session_id` | uuid | Filter by correlated session |
| `project_id` | uuid | Filter by correlated project |
| `since` | ISO 8601 | Lower bound on `created_at` |
| `until` | ISO 8601 | Upper bound on `created_at` |
| `limit` | int | Page size (default 50, max 200) |
| `cursor` | string | Cursor for keyset pagination (entry ID) |

### 6.3 Response format

```json
{
  "data": [
    {
      "id": 12847,
      "org_id": "550e8400-e29b-41d4-a716-446655440000",
      "actor_type": "user",
      "actor_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "user_id": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      "action": "project.started",
      "resource_type": "project",
      "resource_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "details": {
        "previous_status": "draft",
        "new_status": "active"
      },
      "request_id": "req-abc123",
      "ip_address": "192.168.1.100",
      "user_agent": "Mozilla/5.0 ...",
      "session_id": null,
      "project_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "created_at": "2026-03-15T14:30:00Z"
    }
  ],
  "meta": {
    "next_cursor": "12846"
  }
}
```

### 6.4 Handler (`internal/api/handlers/audit_logs.go`)

```go
type AuditLogHandler struct {
    store *db.AuditLogStore
}

func NewAuditLogHandler(store *db.AuditLogStore) *AuditLogHandler {
    return &AuditLogHandler{store: store}
}
```

### 6.5 Router registration

```go
// Admin-only routes
r.Group(func(r chi.Router) {
    r.Use(middleware.RequireRole("admin"))

    r.Get("/api/v1/audit-logs", auditLogHandler.List)
    r.Get("/api/v1/audit-logs/{id}", auditLogHandler.Get)
    // ... existing admin routes ...
})
```

---

## 7. Instrumentation Strategy

### 7.1 Where to emit audit entries

Audit entries should be emitted at the **handler/service boundary** — the point where a user or system action translates into a state change. This is typically:

- **API handlers**: For user-initiated actions (e.g. `sessionHandler.TriggerFix`, `projectHandler.Start`)
- **Worker job handlers**: For system-initiated actions (e.g. PM plan creation, session completion)
- **Webhook handlers**: For externally-triggered actions (e.g. issue ingestion)

### 7.2 Emission pattern

Audit entries are emitted **after** the primary operation succeeds. If the operation fails, no audit entry is written. This prevents phantom entries.

```go
// Example: in sessionHandler.TriggerFix
func (h *SessionHandler) TriggerFix(w http.ResponseWriter, r *http.Request) {
    // ... existing logic to create session and enqueue job ...

    // Emit audit entry (fire-and-forget; the emitter logs errors internally)
    resID := session.ID.String()
    h.auditEmitter.EmitUserAction(r.Context(), db.UserActionParams{
        OrgID:        orgID,
        UserID:       user.ID,
        Action:       models.AuditActionSessionCreated,
        ResourceType: models.AuditResourceSession,
        ResourceID:   &resID,
        Details:      json.RawMessage(`{"issue_id":"` + issueID.String() + `"}`),
        RequestID:    requestIDFromContext(r.Context()),
        IPAddress:    addrFromRequest(r), // returns *netip.Addr parsed from r.RemoteAddr
        UserAgent:    stringPtr(r.UserAgent()),
        SessionID:    &session.ID,
    })
}
```

### 7.3 Fire-and-forget semantics

Audit log emission **must not** block or fail the primary operation. If the audit insert fails (e.g. DB connection issue), the error is logged via the structured logger but the API response is unaffected. This is a deliberate trade-off: we prefer occasional missing audit entries over degraded user experience.

### 7.4 Priority instrumentation order

Instrument these high-value actions first:

1. **Auth events** — `auth.login`, `auth.logout`, `auth.register`
2. **Session lifecycle** — `session.created`, `session.completed`, `session.failed`, `session.cancelled`
3. **Team management** — `team.member_invited`, `team.member_role_changed`, `team.member_removed`
4. **Settings & credentials** — `settings.updated`, `credential.updated`, `credential.deleted`
5. **Project lifecycle** — `project.created`, `project.started`, `project.paused`, `project.deleted`
6. **Everything else** — PM actions, webhook ingestion, question answers

---

## 8. Retention & Scaling Considerations

### 8.1 Volume estimates

| Action category | Estimated frequency | Volume/month (100 orgs) |
|----------------|--------------------|-----------------------|
| Auth events | ~5-50/day/org | ~150K |
| Session lifecycle | ~10-100/day/org | ~300K |
| Project actions | ~1-20/day/org | ~60K |
| Team/settings | ~1-5/day/org | ~15K |
| Webhook ingestion | ~10-500/day/org | ~1.5M |
| **Total** | | **~2M/month** |

At this volume, a single unpartitioned table with proper indexes will handle reads and writes comfortably for the first year.

### 8.2 Future: time-based partitioning

When the table exceeds ~50M rows, partition by month on `created_at`:

```sql
-- Future migration (not included in initial rollout)
CREATE TABLE audit_logs (
    ...
) PARTITION BY RANGE (created_at);

CREATE TABLE audit_logs_2026_03 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
```

### 8.3 Retention policy

Default retention: **90 days**. Configurable per org via `organizations.settings`:

```json
{
  "audit_log_retention_days": 90
}
```

A scheduled job (`audit_log_cleanup`) runs daily, deletes entries older than the retention period. The immutability trigger allows deletes only through a superuser/migration path — the cleanup job would bypass the trigger via a session-level `SET LOCAL` or a dedicated cleanup function.

---

## 9. Migration from Legacy Table

The migration renames `audit_log` to `audit_log_legacy`. No data migration is performed — the legacy table has minimal data and a different schema. After the new system is stable (30 days), a follow-up migration drops `audit_log_legacy`.

---

## 10. What This Design Does NOT Include

- **Real-time streaming of audit events** — SSE/WebSocket streaming is out of scope. The table is queryable via API.
- **Export to external SIEM** — Future work. The structured schema makes it straightforward to build a CDC pipeline or export job.
- **Automatic revert capabilities** — Audit logs record what happened; they do not provide undo. Revert functionality (e.g. reverting a session's commits) is a separate feature.
- **Frontend UI for audit logs** — A dedicated audit log viewer page is out of scope for the initial implementation. Admins can query via API.
