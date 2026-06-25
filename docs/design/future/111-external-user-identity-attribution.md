# Design: External User Identity Attribution

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-25
>
> **Depends on:** [../implemented/69-linear-agent.md](../implemented/69-linear-agent.md), [../implemented/101-slackbot-implementation-plan.md](../implemented/101-slackbot-implementation-plan.md), [./92-slackbot-product-surface.md](./92-slackbot-product-surface.md), [./50-multi-organization-membership.md](./50-multi-organization-membership.md)

## Part 1: Product Design

## Problem

Slack and Linear can start real 143 work before 143 knows which human in the
product owns the external account. When that happens, the session is attributed
to the generalized 143 bot or to an unmapped team session. That is acceptable
as a fallback, but it is worse than the product should be:

- PR authorship cannot use the right user's GitHub App token.
- Personal coding-agent credentials and user-scoped capabilities cannot be used.
- Audit logs and session attribution read as "bot" instead of "Alice."
- Human-input DMs, approvals, and sensitive requests cannot reliably route to
  the right person.
- Admins cannot quickly see who still needs to connect accounts.

The goal is to make identity association easy for every user in an org while
preserving a safe team fallback when no verified association exists.

## Prior Art

Devin, Codex, Claude Code, Cursor, and Linear-style agent products converge on
a two-layer setup: admins install the workspace app, then each person links
their own account for personal attribution. Email matching reduces setup work,
but explicit account linking remains the strongest signal.

## Product Principles

- **Explicit wins.** A user-confirmed provider link is the strongest signal.
- **Heuristics should save clicks, not invent identity.** Exact verified email
  matches may auto-link; fuzzy matches only create suggestions.
- **Team fallback is visible.** Unmapped work can run as team-owned work when
  policy allows, but Slack, Linear, session detail, and audit logs must say so.
- **One identity surface.** Slack, Linear, GitHub-adjacent hints, and future
  providers should share one "People mappings" settings surface.
- **Capabilities follow confidence.** Personal credentials, PR authorship,
  private DMs, and approval authority require a trusted mapping.
- **No provider profile prompt injection.** External display names are labels
  only. Agent prompts may include sanitized handles plus stable external IDs.

## Confidence Ladder

143 resolves external users through a provider-independent ladder:

| Level | Source | Product behavior |
| --- | --- | --- |
| 100 | Self-linked by signed-in 143 user | Full personal attribution and personal capabilities |
| 90 | Admin-linked mapping | Full personal attribution; audit source is admin |
| 80 | Exact verified email match | Auto-link when org policy allows; otherwise suggest |
| 60 | SSO/SCIM or verified-domain directory match | Suggest or auto-link behind enterprise policy |
| 40 | Fuzzy name / handle / GitHub username hint | Suggest only, never auto-attribute |
| 0 | No match | Team session / bot fallback only |

Once a stable mapping is created, future sessions use the mapping directly and
do not repeat email or fuzzy matching unless the mapping is revoked.

## User Flows

### Slack and Linear starts

For Slack, 143 resolves org, channel policy, repo defaults, and the Slack user
ID. For Linear, 143 resolves the AgentSession creator user ID and email when
Linear exposes it. If a trusted mapping exists, the session is attributed to
that 143 user. If an exact verified email match is allowed, 143 persists an
`email_match` link. Otherwise, policy decides whether to start team-owned work.
Unmapped starts show "Connect account," restrict personal capabilities, and
claim the external user ID after the person signs in to 143.

### Admin setup

Decision: put this in Settings -> Team as an `External identities` section,
not under each integration. This is a people-management problem first: admins
need to answer "which 143 members are connected across Slack and Linear?" and
"which external actors are still unmapped?" in one place.

Placement on the Team page:

- Add `Slack` and `Linear` identity columns to the existing members table.
  Each cell shows a compact linked persona chip, an `Unlinked` state, or a
  warning state such as `Email match pending`. This makes the common audit path
  scan naturally alongside role, email, and membership status.
- Add an `External identities` tab or subpage for the operational queue:
  connected provider workspaces, recently seen unmapped users, suggested
  matches, bulk approval for exact email matches, provider scope health, and
  link/relink/unlink actions.
- Keep integration settings focused on install health and scopes. Slack and
  Linear settings should deep-link to the Team external-identities section with
  provider filters applied, but the canonical mapping table lives with team
  membership.

Every manual mapping change is audit-logged.

### App Home / self-service

Slack App Home should show a personal `Connect your 143 account` state until
the Slack user is linked. After linking, it shows the mapped 143 user, active
org selector, personal defaults, and a `Disconnect` action.

The web user settings page should show connected external identities across
Slack, Linear, and future providers, with a clear distinction between
self-linked, admin-linked, and automatically matched identities.

## Capability Policy

External identity resolution should improve attribution and personalization
without making normal work feel blocked.

| Capability | Without trusted mapping |
| --- | --- |
| Start work from allowed Slack channel or Linear issue | Allowed as team-owned work |
| Use personal agent credentials | Falls back to team/default credentials |
| Create PR | Allowed through GitHub App fallback; prompt to link for personal authorship and credit |
| Answer non-sensitive team human-input request | Allowed when channel/team policy permits a team answer |
| Receive assigned/sensitive human-input DM | Falls back to web or originating thread until linked |
| Approve tool/command/PR actions | Allowed only through team-policy fallback; otherwise ask to link/sign in |
| Change integration settings | Requires normal 143 org role, not provider mapping alone |

When a capability falls back, Slack/Linear should explain what happened and make
linking feel like an upgrade for tracking, credit, and personal defaults.

## Product Copy Rules

- Use "team session" for work started by an unmapped external user.
- Use "Connect account" for self-link actions.
- Use "Mapped by email" only in settings/audit surfaces, not in Slack threads.
- Always show the external actor when available: "Slack @alice started this
  team session" or "Linear Alice started this issue session."
- Do not present the external actor as a 143 user unless the mapping is trusted.
  The canonical actor is either the mapped 143 user plus provider label, or an
  unmapped Slack/Linear user label.

## Rollout

1. **Shipped 2026-06-25:** unified data model, Linear-link backfill migration,
   typed Go models, direct pgx stores, resolver service, admin/user API
   endpoints, suggestion queue actions, audit enum coverage, and tenancy tests.
2. Add self-link claim links for Slack App Home, Slack mention fallback, and
   Linear-started session fallback.
3. Add settings table for mappings, suggestions, scope health, and unlinking.
4. Route personal capabilities through trusted mappings and provide team
   fallbacks where policy allows.
5. Migrate Slack mapping rows into the unified table and continue moving
   Slack/Linear callers from provider-specific stores to the resolver. Existing
   `linear_user_links` rows with resolved users are backfilled into
   `external_user_links`; compatibility helpers remain for older callers.

## Non-Goals

- Replacing 143 authentication with Slack or Linear login.
- SCIM implementation in the first version.
- Cross-org provider identity sharing. Mappings remain org-scoped.
- Fuzzy automatic identity matching.
- Making Slack or Linear the canonical transcript; sessions remain canonical.

## Part 2: Engineering Spec

## Domain Model

```go
type ExternalIdentityProvider string
const (
    ExternalIdentityProviderSlack  ExternalIdentityProvider = "slack"
    ExternalIdentityProviderLinear ExternalIdentityProvider = "linear"
)

type ExternalUserLinkSource string
const (
    ExternalUserLinkSourceSelfLinked  ExternalUserLinkSource = "self_linked"
    ExternalUserLinkSourceAdminLinked ExternalUserLinkSource = "admin_linked"
    ExternalUserLinkSourceEmailMatch  ExternalUserLinkSource = "email_match"
    ExternalUserLinkSourceDirectory   ExternalUserLinkSource = "directory"
)

type ExternalUserLinkStatus string
const (
    ExternalUserLinkStatusActive  ExternalUserLinkStatus = "active"
    ExternalUserLinkStatusRevoked ExternalUserLinkStatus = "revoked"
)

type ExternalUserLink struct {
    ID                  uuid.UUID                `db:"id" json:"id"`
    OrgID               uuid.UUID                `db:"org_id" json:"org_id"`
    Provider            ExternalIdentityProvider `db:"provider" json:"provider"`
    ProviderWorkspaceID string                   `db:"provider_workspace_id" json:"provider_workspace_id"`
    ProviderUserID      string                   `db:"provider_user_id" json:"provider_user_id"`
    UserID              uuid.UUID                `db:"user_id" json:"user_id"`
    Source              ExternalUserLinkSource   `db:"source" json:"source"`
    Status              ExternalUserLinkStatus   `db:"status" json:"status"`
    Confidence          int                      `db:"confidence" json:"confidence"`
    ExternalEmail       *string                  `db:"external_email" json:"external_email,omitempty"`
    ExternalHandle      *string                  `db:"external_handle" json:"external_handle,omitempty"`
    ExternalDisplayName *string                  `db:"external_display_name" json:"external_display_name,omitempty"`
    LinkedByUserID      *uuid.UUID               `db:"linked_by_user_id" json:"linked_by_user_id,omitempty"`
    CreatedAt           time.Time                `db:"created_at" json:"created_at"`
    RevokedAt           *time.Time               `db:"revoked_at" json:"revoked_at,omitempty"`
}
```

All enum-like types need `Validate() error` methods and table-driven tests.

## Tables

```sql
CREATE TABLE external_user_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    source text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    confidence integer NOT NULL,
    external_email text,
    external_handle text,
    external_display_name text,
    linked_by_user_id uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    CHECK (provider IN ('slack', 'linear')),
    CHECK (source IN ('self_linked', 'admin_linked', 'email_match', 'directory')),
    CHECK (status IN ('active', 'revoked')),
    CHECK (confidence BETWEEN 0 AND 100)
);

CREATE UNIQUE INDEX idx_external_user_links_active_external
    ON external_user_links (org_id, provider, provider_workspace_id, provider_user_id)
    WHERE status = 'active';

CREATE INDEX idx_external_user_links_user
    ON external_user_links (org_id, user_id)
    WHERE status = 'active';
```

Suggestions are separate from links because they are not authoritative:

```sql
CREATE TABLE external_user_link_suggestions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    suggested_user_id uuid NOT NULL REFERENCES users(id),
    reason text NOT NULL,
    confidence integer NOT NULL,
    external_email text,
    external_handle text,
    external_display_name text,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    dismissed_at timestamptz,
    CHECK (provider IN ('slack', 'linear')),
    CHECK (confidence BETWEEN 0 AND 100)
);

CREATE UNIQUE INDEX idx_external_user_link_suggestions_open
    ON external_user_link_suggestions (
        org_id, provider, provider_workspace_id, provider_user_id, suggested_user_id
    )
    WHERE dismissed_at IS NULL;
```

Provider-specific tables such as `linear_user_links` may remain during
migration, but new code should depend on `external_user_links`.

Self-linking from Slack/Linear uses short-lived claim tokens:

```sql
CREATE TABLE external_user_link_claims (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    source_context jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    claimed_by_user_id uuid REFERENCES users(id),
    claimed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider IN ('slack', 'linear'))
);
```

Tokens are bearer claims: one-time use, random, hashed at rest, and valid for
at most 30 minutes.

## Services

```go
type ExternalIdentityResolver interface {
    ResolveExternalActor(ctx context.Context, orgID uuid.UUID, input ExternalActorInput) (ExternalActorResolution, error)
    CreateSelfLinkClaim(ctx context.Context, orgID uuid.UUID, input ExternalActorInput, sourceContext map[string]any) (ExternalUserLinkClaim, string, error)
    ClaimSelfLink(ctx context.Context, token string, claimingUserID uuid.UUID) (ExternalUserLink, error)
    AdminLinkExternalUser(ctx context.Context, orgID uuid.UUID, req AdminExternalUserLinkRequest) (ExternalUserLink, error)
    RevokeExternalUserLink(ctx context.Context, orgID uuid.UUID, linkID uuid.UUID, revokedByUserID uuid.UUID) error
}

type ExternalActorInput struct {
    Provider            ExternalIdentityProvider
    ProviderWorkspaceID string
    ProviderUserID      string
    Email               *string
    EmailVerified       bool
    Handle              *string
    DisplayName         *string
}

type ExternalActorResolution struct {
    MappedUserID      *uuid.UUID
    LinkID            *uuid.UUID
    Source            *ExternalUserLinkSource
    Confidence        int
    TeamFallback      bool
    LinkRequiredFor   []string
    SuggestedUserID   *uuid.UUID
    ClaimURL          *string
}
```

Resolution order:

1. Active `external_user_links` exact match.
2. Exact verified email match to active org member when org policy allows.
3. Directory/SCIM match when implemented.
4. Suggestion creation from fuzzy hints.
5. Team fallback.

The resolver is the only place where Slack/Linear workers should decide whether
an external user maps to a 143 user.

## APIs

All endpoints are under `/api/v1` and require active org membership.

```text
GET    /integrations/external-user-links
POST   /integrations/external-user-links
DELETE /integrations/external-user-links/{id}
GET    /integrations/external-user-link-suggestions
POST   /integrations/external-user-link-suggestions/{id}/approve
POST   /integrations/external-user-link-suggestions/{id}/dismiss
POST   /integrations/external-user-link-claims/{token}/claim
GET    /users/me/external-identities
DELETE /users/me/external-identities/{id}
```

List responses follow `{data, meta}`. Errors follow the standard
`{error: {code, message, details}}` format.

## Slack Integration Changes

- Replace Slack-specific mapping reads with `ExternalIdentityResolver`.
- Add `Connect account` buttons to unmapped ack, App Home, and team-session
  final messages.
- Persist `mapped_user_id`, `external_user_link_id`, and `team_fallback` in
  Slack session attribution metadata.
- If `response_visibility = dm` but the user is unmapped, attempt only provider
  DM delivery; do not treat that as a trusted 143 assigned-user DM.

## Linear Integration Changes

- Migrate `linear_user_links` into `external_user_links`.
- On AgentSession creation, resolve the creator through the unified resolver.
- Keep exact verified email auto-linking as the default low-friction path.
- Do not let email matching overwrite `self_linked` or `admin_linked` rows.
- Store `external_user_link_id` and attribution source in agent session bridge
  metadata for audit and debugging.

## Security And Audit

- Audit every create, replace, revoke, approve, dismiss, and claim action.
- Never auto-link from unverified provider email.
- Never auto-link from display name, real name, or handle alone.
- Do not expose raw claim tokens in logs.
- Provider user IDs and workspace IDs are stable identifiers and may be logged;
  provider emails should follow existing PII logging rules.
- Revoking a link affects future capability checks only; historical sessions
  keep their original attribution snapshot.

## Tests

- Store tests verify every query filters by `org_id`.
- Resolver table tests cover exact link, self-link precedence, admin-link
  precedence, verified email match, unverified email rejection, suggestion, and
  team fallback.
- Slack worker tests verify unmapped users become team sessions and mapped users
  get personal attribution.
- Linear worker tests verify creator mapping and email-match persistence.
- API handler tests cover RBAC, cross-org isolation, claim-token expiry, and
  duplicate active mapping conflicts.
