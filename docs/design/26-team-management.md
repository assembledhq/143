# Design: Team Management

This document describes how 143.dev enables organization admins to manage their team — inviting new members, assigning roles, and removing users. Today, users can only join via self-signup (GitHub OAuth, Google OAuth, or email registration), and there's no way for admins to control who's in their org or what role they have. This creates a gap: the RBAC middleware enforces three roles (`admin`, `member`, `viewer`), but nothing in the UI or API lets admins assign or change them.

## Problem

- **No invite flow.** New team members must self-register and somehow end up in the right org. There's no way to invite someone by email.
- **No role management.** The `users.role` column exists and RBAC middleware enforces it, but admins can't change anyone's role.
- **No member visibility.** Admins can't see who's in their org, when they joined, or what role they have.
- **No member removal.** Once a user is in the org, there's no way to remove them.

## Industry Context

Every major AI-powered developer tool has converged on the same team management pattern:

| Product | Terminology | Roles | Invite Flow | UI Location |
|---------|-------------|-------|-------------|-------------|
| OpenAI (ChatGPT Business) | Team | Owner, Admin, Member | Email invite | Settings → Members |
| Anthropic (Claude Team) | Team | Admin, Member | Email invite | Settings → Members |
| Cursor | Team | Owner, Admin, Member | Email invite | Settings → Team |
| Perplexity (Pro Teams) | Team | Admin, Member | Email invite | Settings → Team |
| GitHub Copilot (Business) | Organization | Owner, Member | Email invite | Org settings → Members |
| Windsurf (Teams) | Team | Admin, Member | Email invite | Settings → Team |

The pattern is consistent: "Team" terminology, email-based invites, 3-4 roles, and a members list in settings. Our existing 3-role model (`admin`, `member`, `viewer`) aligns well — no new roles are needed at this stage.

## Data Model

### Existing: `users` table (no changes)

The `users` table already has everything needed for team membership:

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| email | text | unique |
| name | text | |
| role | text | `admin`, `member`, `viewer` |
| github_id | bigint | GitHub user ID (from OAuth) |
| github_login | text | GitHub username |
| avatar_url | text | |
| created_at | timestamptz | |

The `role` column is already used by `middleware.RequireRole()` — team management simply exposes it to admins.

### New: `invitations` table

Tracks pending email invitations. An invitation is a one-time-use token that allows a new user to join an org with a specific role.

```sql
CREATE TABLE invitations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    email       text        NOT NULL,
    role        text        NOT NULL DEFAULT 'member',  -- 'admin', 'member', 'viewer'
    invited_by  uuid        NOT NULL REFERENCES users(id),
    token       text        NOT NULL UNIQUE,             -- crypto/rand URL-safe token
    status      text        NOT NULL DEFAULT 'pending',  -- 'pending', 'accepted', 'revoked', 'expired'
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);

CREATE INDEX idx_invitations_org_pending ON invitations (org_id, created_at DESC)
    WHERE status = 'pending';
CREATE UNIQUE INDEX idx_invitations_org_email_pending ON invitations (org_id, email)
    WHERE status = 'pending';
CREATE INDEX idx_invitations_token ON invitations (token)
    WHERE status = 'pending';
```

**Design decisions:**

- **Partial unique index on `(org_id, email) WHERE status = 'pending'`** — prevents duplicate pending invites for the same email, but allows re-inviting someone whose previous invite was revoked or expired.
- **Token column** — a 32-byte `crypto/rand` value, base64url-encoded. Used in the accept link. Not the same as the row ID (which is a predictable UUID).
- **`invited_by`** — tracks who sent the invite for audit purposes.
- **`expires_at`** — invitations expire after 7 days by default. Expired invitations are not cleaned up; they stay as historical records.
- **No cascade delete** — if an inviter is removed, their invitations remain valid.

### Schema additions to `01-database-schema.md`

The `invitations` table should be added to the Core Tables section of `docs/design/01-database-schema.md`:

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| email | text | invited email address |
| role | text | role to assign on accept: `admin`, `member`, `viewer` |
| invited_by | uuid | FK -> users, who sent the invite |
| token | text | unique, crypto-random accept token |
| status | text | `pending`, `accepted`, `revoked`, `expired` |
| expires_at | timestamptz | default: 7 days from creation |
| created_at | timestamptz | |
| accepted_at | timestamptz | nullable, set when accepted |

## API

All team management endpoints live under `/api/v1/team/`. They follow the standard JSON envelope format from `02-api-server.md`.

### List members

```
GET /api/v1/team/members
Roles: admin
```

Returns all users in the org with their role, email, name, and avatar.

```json
{
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "email": "alice@example.com",
      "name": "Alice Chen",
      "role": "admin",
      "avatar_url": "https://avatars.githubusercontent.com/u/12345",
      "created_at": "2025-01-10T08:00:00Z"
    },
    {
      "id": "550e8400-e29b-41d4-a716-446655440001",
      "email": "bob@example.com",
      "name": "Bob Smith",
      "role": "member",
      "avatar_url": null,
      "created_at": "2025-01-12T14:30:00Z"
    }
  ]
}
```

### Create invitation

```
POST /api/v1/team/invitations
Roles: admin
```

Sends an email invitation to join the org. If the email is already a member of the org, returns an error. If a pending invitation already exists for this email, returns an error.

**Request:**

```json
{
  "email": "carol@example.com",
  "role": "member"
}
```

**Response (201):**

```json
{
  "data": {
    "id": "660e8400-e29b-41d4-a716-446655440002",
    "email": "carol@example.com",
    "role": "member",
    "status": "pending",
    "invited_by": {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "Alice Chen"
    },
    "expires_at": "2025-01-24T08:00:00Z",
    "created_at": "2025-01-17T08:00:00Z"
  }
}
```

**Error cases:**

| Condition | Status | Code |
|-----------|--------|------|
| Email already in org | 409 | `ALREADY_MEMBER` |
| Pending invite exists for email | 409 | `INVITE_EXISTS` |
| Invalid email format | 400 | `VALIDATION_ERROR` |
| Invalid role | 400 | `VALIDATION_ERROR` |

### List invitations

```
GET /api/v1/team/invitations
Roles: admin
```

Returns pending invitations for the org.

```json
{
  "data": [
    {
      "id": "660e8400-e29b-41d4-a716-446655440002",
      "email": "carol@example.com",
      "role": "member",
      "status": "pending",
      "invited_by": {
        "id": "550e8400-e29b-41d4-a716-446655440000",
        "name": "Alice Chen"
      },
      "expires_at": "2025-01-24T08:00:00Z",
      "created_at": "2025-01-17T08:00:00Z"
    }
  ]
}
```

### Revoke invitation

```
DELETE /api/v1/team/invitations/{id}
Roles: admin
```

Sets the invitation status to `revoked`. The accept link stops working.

**Response (204):** No content.

### Accept invitation

```
POST /api/v1/team/invitations/accept
Roles: public (no auth required)
```

This is the endpoint hit when a user clicks the invite link in their email. The token proves the invitation is legitimate. The user either already has an account (and gets added to the org) or needs to register first.

**Request:**

```json
{
  "token": "base64url-encoded-token"
}
```

**Response (200) — user exists:**

```json
{
  "data": {
    "action": "joined",
    "org_name": "My Team",
    "redirect_url": "/dashboard"
  }
}
```

**Response (200) — user needs to register:**

```json
{
  "data": {
    "action": "register",
    "email": "carol@example.com",
    "org_name": "My Team",
    "redirect_url": "/register?invitation=base64url-encoded-token"
  }
}
```

**Error cases:**

| Condition | Status | Code |
|-----------|--------|------|
| Token invalid or not found | 404 | `INVITE_NOT_FOUND` |
| Invitation expired | 410 | `INVITE_EXPIRED` |
| Invitation already accepted | 410 | `INVITE_ALREADY_USED` |
| Invitation revoked | 410 | `INVITE_REVOKED` |

**Flow:** When the user clicks the email link (`{FRONTEND_URL}/invite/accept?token=...`), the frontend calls this endpoint. Based on the response:
- If `action: "joined"`, the user is already authenticated and now part of the org — redirect to dashboard.
- If `action: "register"`, the frontend redirects to the registration page with the invitation token pre-filled. After registration completes, the auth handler checks for a pending invitation token, accepts it, and places the user in the correct org with the invited role.

### Change member role

```
PATCH /api/v1/team/members/{id}/role
Roles: admin
```

Changes a member's role. Admins cannot change their own role (prevents lockout).

**Request:**

```json
{
  "role": "viewer"
}
```

**Response (200):**

```json
{
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440001",
    "email": "bob@example.com",
    "name": "Bob Smith",
    "role": "viewer"
  }
}
```

**Error cases:**

| Condition | Status | Code |
|-----------|--------|------|
| Cannot change own role | 400 | `CANNOT_CHANGE_OWN_ROLE` |
| User not in org | 404 | `MEMBER_NOT_FOUND` |
| Invalid role | 400 | `VALIDATION_ERROR` |

### Remove member

```
DELETE /api/v1/team/members/{id}
Roles: admin
```

Removes a user from the org. Admins cannot remove themselves. The user's sessions are invalidated immediately.

**Response (204):** No content.

**Error cases:**

| Condition | Status | Code |
|-----------|--------|------|
| Cannot remove self | 400 | `CANNOT_REMOVE_SELF` |
| User not in org | 404 | `MEMBER_NOT_FOUND` |
| Last admin in org | 400 | `LAST_ADMIN` |

## Router Changes

New routes are added to the admin-only group in `internal/api/router.go`:

```go
// Admin-only routes
r.Group(func(r chi.Router) {
    r.Use(middleware.RequireRole("admin"))

    // ... existing admin routes ...

    // Team management
    r.Get("/api/v1/team/members", teamHandler.ListMembers)
    r.Patch("/api/v1/team/members/{id}/role", teamHandler.ChangeRole)
    r.Delete("/api/v1/team/members/{id}", teamHandler.RemoveMember)
    r.Get("/api/v1/team/invitations", teamHandler.ListInvitations)
    r.Post("/api/v1/team/invitations", teamHandler.CreateInvitation)
    r.Delete("/api/v1/team/invitations/{id}", teamHandler.RevokeInvitation)
})

// The accept endpoint is public (no auth — token-based)
r.Post("/api/v1/team/invitations/accept", teamHandler.AcceptInvitation)
```

## Data Access Layer

### `internal/db/invitations.go`

```go
type InvitationStore struct {
    db DBTX
}

func NewInvitationStore(db DBTX) *InvitationStore {
    return &InvitationStore{db: db}
}

const invitationSelectColumns = `id, org_id, email, role, invited_by, token, status, expires_at, created_at, accepted_at`

func (s *InvitationStore) Create(ctx context.Context, inv *models.Invitation) error {
    query := `INSERT INTO invitations (org_id, email, role, invited_by, token, expires_at)
              VALUES (@org_id, @email, @role, @invited_by, @token, @expires_at)
              RETURNING id, status, created_at`
    row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
        "org_id":     inv.OrgID,
        "email":      inv.Email,
        "role":       inv.Role,
        "invited_by": inv.InvitedBy,
        "token":      inv.Token,
        "expires_at": inv.ExpiresAt,
    })
    return row.Scan(&inv.ID, &inv.Status, &inv.CreatedAt)
}

// GetByToken looks up an invitation by token regardless of status.
// The handler inspects the Status field to return the correct error code
// (INVITE_EXPIRED, INVITE_ALREADY_USED, INVITE_REVOKED, vs INVITE_NOT_FOUND).
func (s *InvitationStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
    query := fmt.Sprintf(`SELECT %s FROM invitations WHERE token = @token`, invitationSelectColumns)
    rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
    if err != nil {
        return models.Invitation{}, fmt.Errorf("query invitation: %w", err)
    }
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Invitation])
}

func (s *InvitationStore) ListPendingByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Invitation, error) {
    query := fmt.Sprintf(`SELECT %s FROM invitations WHERE org_id = @org_id AND status = 'pending' ORDER BY created_at DESC`, invitationSelectColumns)
    rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
    if err != nil {
        return nil, fmt.Errorf("query invitations: %w", err)
    }
    return pgx.CollectRows(rows, pgx.RowToStructByName[models.Invitation])
}

func (s *InvitationStore) Accept(ctx context.Context, id uuid.UUID) error {
    query := `UPDATE invitations SET status = 'accepted', accepted_at = now() WHERE id = @id AND status = 'pending'`
    _, err := s.db.Exec(ctx, query, pgx.NamedArgs{"id": id})
    return err
}

func (s *InvitationStore) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
    query := `UPDATE invitations SET status = 'revoked' WHERE id = @id AND org_id = @org_id AND status = 'pending'`
    ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        return pgx.ErrNoRows
    }
    return nil
}
```

### User store additions (`internal/db/users.go`)

New methods on the existing `UserStore`:

```go
func (s *UserStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.User, error) {
    query := fmt.Sprintf(`SELECT %s FROM users WHERE org_id = @org_id ORDER BY created_at ASC`, userSelectColumns)
    rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
    if err != nil {
        return nil, fmt.Errorf("query users: %w", err)
    }
    return pgx.CollectRows(rows, pgx.RowToStructByName[models.User])
}

func (s *UserStore) UpdateRole(ctx context.Context, orgID, userID uuid.UUID, role string) error {
    query := `UPDATE users SET role = @role WHERE id = @id AND org_id = @org_id`
    _, err := s.db.Exec(ctx, query, pgx.NamedArgs{"role": role, "id": userID, "org_id": orgID})
    return err
}

func (s *UserStore) Delete(ctx context.Context, orgID, userID uuid.UUID) error {
    query := `DELETE FROM users WHERE id = @id AND org_id = @org_id`
    _, err := s.db.Exec(ctx, query, pgx.NamedArgs{"id": userID, "org_id": orgID})
    return err
}

func (s *UserStore) CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
    query := `SELECT COUNT(*) FROM users WHERE org_id = @org_id AND role = 'admin'`
    var count int
    err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
    return count, err
}
```

## Go Models

### `internal/models/invitation.go`

```go
package models

import (
    "time"

    "github.com/google/uuid"
)

// Invitation is the DB row representation.
type Invitation struct {
    ID         uuid.UUID  `db:"id"          json:"id"`
    OrgID      uuid.UUID  `db:"org_id"      json:"org_id"`
    Email      string     `db:"email"       json:"email"`
    Role       string     `db:"role"        json:"role"`
    InvitedBy  uuid.UUID  `db:"invited_by"  json:"-"`   // expanded in API response type
    Token      string     `db:"token"       json:"-"`   // never exposed in API responses
    Status     string     `db:"status"      json:"status"`
    ExpiresAt  time.Time  `db:"expires_at"  json:"expires_at"`
    CreatedAt  time.Time  `db:"created_at"  json:"created_at"`
    AcceptedAt *time.Time `db:"accepted_at" json:"accepted_at,omitempty"`
}

// InvitationResponse is the API response type with the inviter expanded.
// The handler constructs this by joining the invitation with the inviter's user record.
type InvitationResponse struct {
    ID        uuid.UUID  `json:"id"`
    Email     string     `json:"email"`
    Role      string     `json:"role"`
    Status    string     `json:"status"`
    InvitedBy UserBrief  `json:"invited_by"`
    ExpiresAt time.Time  `json:"expires_at"`
    CreatedAt time.Time  `json:"created_at"`
}

// UserBrief is a minimal user representation for embedding in responses.
type UserBrief struct {
    ID   uuid.UUID `json:"id"`
    Name string    `json:"name"`
}
```

Notes:
- `Token` has `json:"-"` — it's never included in API responses. The token is only used internally for accept-link generation and validation.
- `InvitedBy` on the DB model is a `uuid.UUID`, but in API responses it's expanded to `InvitationResponse.InvitedBy` (a `UserBrief`). The handler loads the inviter's name via the user store.

## Invitation Email

When an admin creates an invitation, the invitation link must be delivered to the invitee. **Currently, no SMTP infrastructure exists** — email delivery is future work (see doc 22). For now, the invitation link is logged to the server console using zerolog. The API response includes the invitation metadata (but not the token) so the admin can manually share the link if needed.

When SMTP is eventually configured, the email format will be:

**Subject:** `You've been invited to join {org_name} on 143.dev`

**Body:**

```
{inviter_name} has invited you to join {org_name} on 143.dev as a {role}.

Accept the invitation:
{FRONTEND_URL}/invite/accept?token={token}

This invitation expires in 7 days.

---
If you weren't expecting this email, you can ignore it.
```

**Token generation** reuses the existing `generateRandomString(32)` helper from `internal/api/handlers/auth.go` (crypto/rand, hex-encoded).

## Frontend UI

The frontend lives at `frontend/src/` and uses Next.js (App Router), React, TanStack Query, and shadcn/ui. Team management is a new section within the existing Settings page at `frontend/src/app/(dashboard)/settings/page.tsx`. The current settings page is a single-page layout with sections — we add a "Team" section using the same component patterns (Card, Button, Input, Label, Badge, etc.).

### Settings → Team tab

```
┌─ Settings ──────────────────────────────────────────────────┐
│                                                             │
│  [General]  [API Keys]  [Team]  [Notifications]             │
│                                                             │
│  Team Members                              [Invite Member]  │
│  ┌─────────────────────────────────────────────────────────┐│
│  │  (avatar) Alice Chen                                    ││
│  │  alice@example.com        Admin ▾   Current user         ││
│  ├─────────────────────────────────────────────────────────┤│
│  │  (avatar) Bob Smith                                     ││
│  │  bob@example.com          Member ▾        [Remove]      ││
│  ├─────────────────────────────────────────────────────────┤│
│  │  (avatar) Dana Lee                                      ││
│  │  dana@example.com         Viewer ▾        [Remove]      ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
│  Pending Invitations                                        │
│  ┌─────────────────────────────────────────────────────────┐│
│  │  carol@example.com   Member   Invited by Alice Chen     ││
│  │  Expires in 5 days                      [Revoke]        ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Invite modal

Clicking "Invite Member" opens a modal:

```
┌─ Invite Member ─────────────────────┐
│                                     │
│  Email address                      │
│  ┌─────────────────────────────┐    │
│  │ carol@example.com           │    │
│  └─────────────────────────────┘    │
│                                     │
│  Role                               │
│  ┌─────────────────────────────┐    │
│  │ Member                    ▾ │    │
│  └─────────────────────────────┘    │
│  Can trigger fixes and view data.   │
│                                     │
│         [Cancel]  [Send Invite]     │
│                                     │
└─────────────────────────────────────┘
```

Role descriptions in the selector:

| Role | Description |
|------|-------------|
| Admin | Full access. Can manage team, settings, and credentials. |
| Member | Can trigger fixes and view data. Cannot manage team or settings. |
| Viewer | Read-only access to issues, runs, and repositories. |

### Role change

The role dropdown on each member row is an inline select. Changing it immediately fires `PATCH /api/v1/team/members/{id}/role`. The current user's own row shows the role as a static badge (no dropdown), since admins can't change their own role.

### Remove member

The "Remove" button shows a confirmation dialog before calling `DELETE /api/v1/team/members/{id}`. The last admin in the org cannot be removed — the button is disabled with a tooltip.

## Permissions Assessment

The current 3-role model (`admin`, `member`, `viewer`) is appropriate for this stage:

- **Matches industry norms.** Most AI tools at similar scale use 3-4 roles. Our three roles cover the common cases: full control (admin), operational use (member), and read-only observation (viewer).
- **Already enforced.** The RBAC middleware and route groups in `router.go` already gate access by role. Team management just makes roles editable.
- **Avoids premature complexity.** Fine-grained permissions (per-repo access, custom roles, permission policies) add significant complexity. These can be added later when real user demand appears.

**Recommendation:** Keep the 3-role model as-is. If granular permissions are needed in the future, add a `permissions` JSONB column to the `users` table for per-user overrides, rather than building a full RBAC system upfront.

## Security Considerations

- **Invitation tokens** are 32-byte `crypto/rand` values, base64url-encoded. They are not guessable UUIDs.
- **Token is write-only** — the token value is never returned in API responses (`json:"-"`). It's only embedded in the invitation email link.
- **Invitation expiry** — 7 days by default. Expired tokens are rejected at acceptance time.
- **Rate limiting** — invitation creation is subject to the global rate limiter. No additional per-endpoint rate limiting is needed initially.
- **Session invalidation on removal** — when a member is removed, their active sessions should be invalidated by deleting all `sessions` rows for that user.
- **Last admin protection** — the API prevents removing or demoting the last admin in an org.
- **Org scoping** — all queries filter by `org_id` via `middleware.OrgContext`, consistent with the existing data access pattern.

## Migration

### Database migration

```sql
-- migrations/000XXX_create_invitations.up.sql

CREATE TABLE invitations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    email       text        NOT NULL,
    role        text        NOT NULL DEFAULT 'member',
    invited_by  uuid        NOT NULL REFERENCES users(id),
    token       text        NOT NULL UNIQUE,
    status      text        NOT NULL DEFAULT 'pending',
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);

CREATE INDEX idx_invitations_org_pending ON invitations (org_id, created_at DESC)
    WHERE status = 'pending';
CREATE UNIQUE INDEX idx_invitations_org_email_pending ON invitations (org_id, email)
    WHERE status = 'pending';
CREATE INDEX idx_invitations_token ON invitations (token)
    WHERE status = 'pending';
```

```sql
-- migrations/000XXX_create_invitations.down.sql

DROP TABLE IF EXISTS invitations;
```

## Implementation Plan

### Phase 1: Member list + role management

1. Add `ListByOrg`, `UpdateRole`, `Delete`, `CountAdmins` methods to `UserStore`.
2. Create `TeamHandler` with `ListMembers`, `ChangeRole`, `RemoveMember`.
3. Wire routes in `router.go`.
4. Add "Team" tab to Settings UI with member list and role dropdowns.

### Phase 2: Invitations

1. Create `invitations` table migration.
2. Create `InvitationStore` with `Create`, `GetByToken`, `ListPendingByOrg`, `Accept`, `Revoke`.
3. Create `Invitation` model.
4. Add invitation endpoints to `TeamHandler`.
5. Integrate with email delivery (SMTP or console fallback).
6. Add invite modal and pending invitations section to Team UI.
7. Update registration flow to check for invitation token and place user in correct org/role.

### Phase 3: Polish

1. Session invalidation on member removal.
2. Expired invitation cleanup (periodic job or lazy check on list).
3. Invitation resend functionality.

## Connection with Other Design Docs

**Database Schema (doc 01):**
- New `invitations` table (schema above should be added to doc 01)

**API Server (doc 02):**
- New `/api/v1/team/*` routes following existing conventions (JSON envelope, chi router, RBAC middleware)
- Accept endpoint is public, similar to auth routes

**Security Architecture (doc 20):**
- Invitation tokens use `crypto/rand` (not UUID)
- Session invalidation on member removal
- Org-scoped data access via `middleware.OrgContext`

**Notifications (doc 22):**
- Invitation emails use the same SMTP infrastructure
- Member removal could emit a notification event in the future

**First-Run Experience (doc 21):**
- The first user to register for an org gets the `admin` role (existing behavior)
- Subsequent users who join via invitation get the role specified in the invitation

**Dashboard Credentials (doc 25):**
- Credential management is admin-only — team management makes it possible to promote/demote users who can access credentials

## Implementation Notes

- **Handler uses interfaces** for store dependencies (matching the `credentialStore` pattern in `credentials.go`), enabling mock-based unit tests.
- **Accept flow uses transactions** via `TxStarter` / `pgx.BeginFunc` to atomically validate token + update invitation status + update user org/role.
- **Auth handler modifications**: `Register` and OAuth callbacks are updated to check for an `invitation` query parameter/body field, placing the user in the invited org with the invited role instead of creating a new org.

## Implementation Progress

- [x] Phase 1a: Add `ListByOrg`, `UpdateRole`, `Delete`, `CountAdmins` to UserStore
- [x] Phase 1b: Create `TeamHandler` with `ListMembers`, `ChangeRole`, `RemoveMember`
- [x] Phase 1c: Wire team routes in `router.go`
- [x] Phase 2a: Create `invitations` table migration (000007)
- [x] Phase 2b: Create `Invitation` model
- [x] Phase 2c: Create `InvitationStore`
- [x] Phase 2d: Add invitation endpoints to `TeamHandler`
- [x] Phase 2e: Update auth handler for invitation-aware registration
- [x] Phase 2f: Add unit tests for TeamHandler (25 tests covering all endpoints)
- [x] Phase 3a: Add frontend types (`InvitationResponse`) and API client methods (`api.team.*`)
- [x] Phase 3b: Add Team page with members list, role management, invite form, pending invitations
- [x] Phase 3c: Session invalidation on member removal (implemented in `RemoveMember`)

**Status: COMPLETE**
