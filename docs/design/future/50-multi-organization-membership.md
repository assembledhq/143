# Design: Multi-Organization Membership

> **Status:** Not Started | **Last reviewed:** 2026-04-21
>
> **Depends on:** [01-database-schema.md](../01-database-schema.md), [03-frontend.md](../03-frontend.md), [20-security-architecture.md](../implemented/20-security-architecture.md), [34-repo-ribbons-nav.md](../backlog/34-repo-ribbons-nav.md)

## Problem

Today each user belongs to exactly one organization. The relationship is modeled by a `users.org_id NOT NULL` foreign key; every resource (`repositories`, `issues`, `sessions`, `projects`, `memories`, etc.) is scoped by that same `org_id`. Accepting an invitation to a different org silently replaces the user's current org, so someone who wants to contribute to more than one org would lose access to the first.

The user story we want to support is GitHub's: *one identity, many orgs*. A developer should be able to have a work org (closed-source) and a separate open-source org under the same email and GitHub login, without juggling accounts. This matters most for contributors who want to use 143 on an open-source project while also using it at work, and for small consultancies that sit across several customer orgs.

This is a cross-cutting change — database, auth/session middleware, every API handler that reads `org_id`, and the frontend nav. The design goal is to do this change **without making life harder for the 95% of users who only have one org**.

## Recommendation

Add this capability, but treat it as an **identity-model correction**, not as a small UI switcher feature. The long-term model should match GitHub: one human identity, one GitHub login, many organization contexts.

Do **not** solve this by creating duplicate user rows per org for the same email or GitHub account. That appears simpler in the short term, but it creates worse product ambiguity: sign-in has to choose between duplicate identities, OAuth tokens fragment, invitations become confusing, and "remove from org" can accidentally become "delete the person." A membership table is the right foundation.

The first shipped version should stay narrower than the full design:

- Ship the membership model, active-org request resolution, per-membership roles, existing-user invitation acceptance, `/auth/me` membership metadata, and the org switcher hidden for single-org users.
- Defer command-palette switching, create-org-from-switcher, accept-invite modal polish, and deep-link auto-resolve until the core model has soaked.
- Keep the single-org invariant as a hard rollout gate: users with one membership should see no new controls and no changed default URLs.

## Goals

1. A user can belong to multiple orgs with a single login.
2. A single-org user sees **zero** change — no switcher in the nav, no new onboarding steps, same URLs, same API payloads.
3. Switching orgs is fast (one click, keyboard-reachable) and the mental model mirrors GitHub's "context" dropdown.
4. All existing org-scoped data stays strictly isolated. Switching context re-scopes what you see; it never leaks across orgs.
5. Invitations compose: accepting an invite to OrgB while you're in OrgA *adds* OrgB to your memberships, it doesn't overwrite OrgA.
6. Tabs are independent — one browser tab can be looking at OrgA while another tab of the same user looks at OrgB.

## Non-Goals

- **Cross-org data views.** No unified inbox, no "all orgs" feed. Every page is scoped to the active org.
- **Personal orgs as a distinct concept.** Every user's first org is a regular org; we use naming conventions, not a schema-level distinction. (See [Risk 6](#risk-6-personal-org-temptation).)
- **Org-level SSO / SCIM.** Enterprise features; out of scope.
- **Billing redesign.** Each org bills independently today and will continue to.
- **Path-based org routing** (e.g. `/org/<slug>/sessions`). We keep existing URLs. (See [Risk 3](#risk-3-ambiguous-deep-links).)

## Design

### The model in one paragraph

A **user** is an identity (email, optional GitHub ID, optional Google ID). A **membership** is the link between a user and an org, with a role. A user can have many memberships; an org has many members. The **active org** for a given request is determined by a header the client sends on every authenticated call. Every resource stays scoped to a single `org_id`; the backend just picks the right one per request.

### Schema changes

Introduce `organization_memberships`:

```sql
CREATE TABLE organization_memberships (
    user_id     UUID NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, org_id)
);
CREATE INDEX idx_memberships_user ON organization_memberships (user_id);
CREATE INDEX idx_memberships_org  ON organization_memberships (org_id);
```

`ON DELETE CASCADE` runs in both directions: deleting a user cleans up their memberships; deleting an org cleans up memberships, leaving the user rows intact. Zero-membership users are allowed; login shows an "You don't belong to any organizations yet" state (essentially the current first-run empty state).

Update `users`:

- **Remove** `users.org_id` after backfill. This column is the fundamental 1:1 constraint and has to go.
- **Remove** `users.role`. Role is now per-membership.
- Keep `users.email UNIQUE` (global). Email identifies a person, not a membership. This matches GitHub — one email = one account.
- Keep `users.github_id UNIQUE`, `users.google_id UNIQUE`. Same reasoning.
- During the compatibility window, API responses may keep `org_id` and `role` fields on the user object, but those values must be derived from the active membership. New backend code should treat users as identity records only.

Update `auth_sessions`:

- Rename `org_id` to `last_org_id` (or add `last_org_id` and backfill before dropping/renaming the old column). This field is a *bootstrap hint for new tabs of this session* (the "last org this user looked at"). It is **not** the authoritative active org — see below.
- Make `last_org_id` nullable. Zero-membership users and users whose last org was deleted must still be able to sign in and reach the empty/no-memberships state.

Update `invitations`:

- Invitations are recipient-identifier based, not email-only. `invitations.email` is nullable, `invitations.github_username` exists, and the table enforces `email IS NOT NULL OR github_username IS NOT NULL`.
- Preserve that shape. Multi-org membership should not move invitation recipient identity back to email-only.
- Invitation uniqueness and duplicate-member checks must handle both identifiers:
  - Pending email invite uniqueness stays scoped by `(org_id, email)` where email is present.
  - Pending GitHub invite uniqueness stays scoped by `(org_id, lower(github_username))` where GitHub username is present.
  - Membership checks should look up by email and by GitHub login against the active org's memberships.

### Active-org resolution (backend)

The active org is a **request-level concern**, not a session-level one. This is critical for tab independence and for avoiding data-leak-class perception bugs when a user switches orgs in one tab while others are open.

Per-request flow in the `Auth` middleware:

1. Read session token → load `AuthSession` → load `User` by `session.user_id` only. Do not require an org-bound user lookup.
2. Resolve active org:
   - If the request carries `X-Active-Org-ID: <uuid>`, use that.
   - Otherwise, fall back to `AuthSession.last_org_id`.
   - Otherwise, fall back to the user's persisted last-selected org preference (seeded by explicit switch actions and copied into new sessions at login).
   - Otherwise (no hint), pick the user's oldest membership.
3. Assert `(user.id, active_org_id)` has a row in `organization_memberships`. If not, fall back to the user's oldest remaining membership; if none, 401. Increment a metric on this fallback path — it should be vanishingly rare in steady state.
4. Set `user`, `org_id`, and active membership role on the request context. Downstream handlers continue to read `org_id` from context and RBAC reads the active role from context.

The frontend attaches `X-Active-Org-ID` via the API client, reading from an `ActiveOrgProvider`. The provider is seeded in this order: explicit `?org=<uuid>` in the current app URL, `sessionStorage` for the current tab, then the session bootstrap hint returned by `/auth/me`. Switching orgs updates React state and `sessionStorage`, then fires `POST /auth/active-org` so the current session and future logins open in the last-used org. Data mutation on switch is a hint, not a contract.

Do not use `localStorage` as the source of truth for active org. It is shared across tabs and violates the tab-independence goal. `sessionStorage` plus in-memory React state gives each tab its own active org while still surviving reloads in that tab.

The backend should not treat arbitrary `?org=` API query params as active-org overrides. `?org=` is for browser app URLs and copied links; the frontend converts it into `X-Active-Org-ID` after loading user memberships. Accepting `?org=` directly in middleware risks collisions with normal endpoint query semantics and makes active-org changes harder to audit.

### Data-scoping defense-in-depth

Today every query is manually scoped by `org_id` in SQL. With multi-org users, forgetting that filter no longer "fails safe" (returning the user's only org's data) — it could leak across orgs the user is also a member of. The blast radius grew, so the defenses need to grow too.

Three layers, all required:

1. **Org-bound store API.** Introduce a store construction path that takes `org_id` from the request context and returns handles whose query methods are pre-bound to that org. Org-scoped methods accept the scoped handle, not the raw pool. The goal is that "forget the `WHERE org_id = ...`" becomes a type error at the call site, not a runtime bug. A SQL-string wrapper that greps query text for `org_id` is not sufficient as the main defense — it can catch obvious misses but cannot prove the predicate is correct. The binding has to happen at the store/handle layer.
2. **Cross-org isolation integration test, per handler.** A shared test helper asserts that when a user with memberships in OrgA and OrgB requests resource X (created in OrgA) with OrgB active, they get 404 — not the resource, not a 500. Every handler under `internal/api/handlers/` gets one test case enumerated from a table.
3. **Static tenancy audit stays in CI.** Keep and expand the existing `internal/db/tenancy_test.go` SQL scan. It is cheap, catches the obvious "added a new query, forgot `org_id`" regression, and complements the store-layer defense.

We considered Postgres RLS keyed off a transaction-local `SET LOCAL app.current_org_id` and rejected it for 143's situation. RLS shines when the database serves queries from untrusted sources (Supabase's model); here all SQL is ours, which means the store-bound API gets most of the safety benefit with none of the operational tax — no owner-bypass footguns during migrations, no invisible predicates in query plans, no separate bypassing role for support tooling, and nothing new for pgx/PgBouncer transaction management. Revisit if we ever let customers run SQL directly.

The PR that ships the memberships table and middleware change is not mergeable without these layers.

### API surface

New / changed endpoints (all small):

- `GET /api/v1/auth/me` — extend response to include `memberships: [{org_id, org_name, role}]` and `active_org_id`. Keep `org_id` as a top-level field for backwards compatibility (derived from the active membership).
- `POST /api/v1/auth/active-org` — body `{org_id}`. Validates membership, updates `AuthSession.last_org_id` as the bootstrap hint, returns the new `/auth/me` payload. Called by the switcher UI.
- `POST /api/v1/orgs` — create a new org; the creator becomes `admin`. Already partially exists for signup.
- Existing team invitation endpoints continue to accept `{email?, github_username?, role}`. GitHub autocomplete/status endpoints remain org-scoped by the active org.
- All existing handlers unchanged — they read `org_id` from context, and the middleware does the right thing.

### Role semantics across orgs

`RequireRole(...)` middleware currently reads `user.Role` directly. It has to change: role is now per-membership, so it must look up `(user.id, active_org_id) → role`. The lookup is cached per-request alongside the user. Every call site gets the updated signature. A compile-time audit is straightforward because there's no implicit fallback — once `users.role` is removed, old usages won't compile.

Handlers and services should not read `user.OrgID` or `user.Role` for authorization decisions. `OrgIDFromContext` remains the source of request tenancy, and a new active-role context helper becomes the source for RBAC and role-gated behavior.

### Frontend: the org switcher

Component: `OrgContextSwitcher`. Mirrors the pattern of `RepoContextSwitcher` (`frontend/src/components/repo-context-switcher.tsx`) and lives in the sidebar immediately above it.

Behavior:

- **If the user has exactly one membership, the component returns `null`.** This is the single-org invariant. No pixel changes for today's users.
- If the user has ≥2 memberships, render a dropdown showing the current org name + role, with the full membership list below.
- Items at the bottom: "Create organization…" and "Accept invitation…" (opens a small modal). These can be deferred from the first release if core switching is otherwise ready.
- Selecting an org updates the active-org React context (so in-flight requests immediately carry the new `X-Active-Org-ID`), calls `POST /auth/active-org` to persist the bootstrap hint, invalidates the React Query cache, and reloads the current page. A clean reload is simpler and safer than trying to diff state across every open view.
- Switching org **clears the `?repo=` query param**, since repos are org-scoped. Enforced by the switcher's onChange, not left to callers.

The switcher should eventually be exposed as a command-palette entry (see [45-global-command-palette.md](../implemented/45-global-command-palette.md)) so keyboard users never need to mouse to the sidebar. This is not required for the initial membership cutover.

### Deep-link handling

Existing URLs like `/sessions/<uuid>` don't encode the org. Under multi-org this is ambiguous: if you paste a link to an OrgB session while active in OrgA, today's code returns 404. We fix that with two mechanisms:

1. **Opt-in `?org=<uuid>` on shareable URLs.** The "Copy link" action writes the current `active_org_id` into the browser URL as a query param. The frontend reads it, validates it against `/auth/me` memberships, stores it in tab-local active-org state, and sends it as `X-Active-Org-ID` on API requests. No path changes, no slug migration.
2. **Auto-switch on load (defer if needed).** If a handler 404s a resource, the frontend can check whether the resource's org is in the user's memberships via a small `GET /api/v1/resources/resolve?kind=session&id=<uuid>` endpoint that returns `{org_id}` for resources the user can access. If so, it prompts: "This session is in OrgB. Switch?" Confirmed → switch + navigate. The prompt-vs-silent trade-off is resolved toward *prompt* to avoid surprising users; we can revisit if usage data shows the prompt is noise.

The resolver endpoint must not become a cross-org ID oracle. It should only reveal an org if the requesting user is already a member of that org and has access to the target resource. Otherwise it returns 404.

Path-based routing (`/<org-slug>/...`) is rejected because it's a bigger migration than the rest of this design combined and pre-commits us to slugs we don't currently maintain. We can revisit if enterprise customers demand it.

### Signup and invitations

**New user, no invite.** Same as today: create user + org + admin membership in one transaction. The user names the org during onboarding; the default name is `"Personal"` (editable). The "Personal" default is a convention, not a schema distinction — users who never join another org will never notice the name.

**New user via email invite.** Invite acceptance creates the user and inserts a membership for the invited org. No implicit "personal org." If the user later wants their own org, they create one via "Create organization…".

**New user via GitHub-username invite.** The accept page routes through GitHub OAuth. The callback validates that the signing-in GitHub login matches `invitations.github_username`, creates or links the user identity, and inserts the membership. A GitHub-only invite should not require an email match, because GitHub may return a private/noreply email.

**Existing user, invite to another org.** Insert a membership for the invited org when either the user's email matches `invitations.email` or their linked GitHub login matches `invitations.github_username`. On success, automatically switch the active org to the newly joined org so the "you're in!" landing page shows the right place.

**Idempotent acceptance.** The accept endpoint upserts the membership (last-write-wins on role) and marks the invitation accepted. This handles the edge cases cleanly: re-invited while still a member, previously removed and re-invited, multi-tab double-submit, email+GitHub dual-identifier invites, etc.

**Existing-user claim flow.** If an invitee reaches the accept page while signed out and already has an account, the post-login flow must claim the invitation and add the membership. It must not silently create a replacement org context or leave the invitation unclaimed after sign-in.

**Removing a member.** Removing a member from an org deletes or deactivates the membership row; it does not delete the `users` row. Session invalidation must be org-aware: revoke or force re-resolution for the removed org, but do not log the same user out of other orgs where they still have memberships.

**Test matrix for acceptance** (explicit in the rollout): `{new user, existing single-org user, existing multi-org user, previously-removed user, currently-active member} × {email invite, GitHub-only invite, email+GitHub invite} × {accept, mismatch, reject, expire}`.

### GitHub OAuth and user credentials

GitHub identity remains global (`users.github_id UNIQUE`, `users.github_login` on the user). GitHub OAuth tokens used for PR authorship are currently stored in `user_credentials` under `(org_id, user_id, provider)`, which is compatible with multi-org membership but needs explicit product semantics:

- A user linking GitHub while active in OrgA creates/updates PR-authorship credentials for OrgA only.
- When the same user switches to OrgB, PR authorship falls back to OrgB's team default unless the user links GitHub for OrgB too.
- The UI should make this visible in agent/account settings: "GitHub linked for this organization" rather than "GitHub linked globally."

This keeps credential access org-scoped and avoids silently making a personal OAuth token available to every org the user joins.

### Observability

Rollout-critical, not optional:

- Per-request tag `active_org_id`. To keep cardinality bounded, tag by `org_tier` (free/paid) in metrics; use full `org_id` only in structured logs.
- Alert on the "membership revoked mid-session fallback" path firing at >0.1% of requests — that's a bug, not a user action.
- Alert on the cross-org isolation integration test failing in CI — blocks merge, pages oncall if it ever regresses on main.
- Audit log (separate append-only table) for: active-org change, membership create, membership delete, role change. These are security-relevant events and must be preserved independently of any other logs.

### Single-org invariant — concrete checklist

What stays identical for today's single-org users:

- No new UI elements visible (the switcher hides itself).
- No new required onboarding step (the default org name "Personal" is an editable pre-fill, not a new prompt).
- `/auth/me` response adds `memberships` and `active_org_id` but keeps `org_id` (derived from the single membership) for backwards compatibility.
- URLs unchanged.
- No new query params appear by default; `?org=` only appears in explicitly copied links.
- Acceptance criterion for rollout: on staging, replay a production traffic sample and assert zero behavioral diffs for single-org accounts (request/response bodies, status codes, side effects).

### Rollout

Each step is independently revertible until step 6.

1. **Schema migration.** Add `organization_memberships`. Backfill one row per existing user from `users.org_id` / `users.role`. Add nullable `auth_sessions.last_org_id` and backfill from `auth_sessions.org_id`. Keep `users.org_id` / `users.role` in place for one release as a read-through shim.
2. **Defense-in-depth landed first.** Ship the org-bound store API, expand the static tenancy audit, and add the cross-org isolation integration test harness *before* any behavioral change. This is the PR that makes the rest safe.
3. **Backend dual-read.** Middleware reads membership from the new table but asserts equivalence with `users.org_id` for single-org users. Log mismatches.
4. **Backend cutover.** Writes (invite accept, signup, org create, team role changes, member removal) go through the memberships table. Drop the equivalence assertion. `RequireRole` refactored to use active-org membership role.
5. **API + frontend additions.** `active-org` endpoint, expanded `/auth/me`, `X-Active-Org-ID` API-client header, `ActiveOrgProvider`, `OrgContextSwitcher` (shipped hidden for single-org users from day 1), copy-link `?org=` writer.
6. **Enable multi-org.** The membership infrastructure itself is the product-visible flip: once invitation acceptance and signup write through the memberships table, a user can hold memberships in multiple orgs without further gating.
7. **Deferred UX follow-ups.** Add command-palette switching, create-org-from-switcher, accept-invite modal polish, and optional deep-link resolver if usage shows copied links remain confusing.
8. **Schema cleanup.** Drop `users.org_id`, `users.role`, and old `auth_sessions.org_id`. Deferred by one release minimum; requires explicit sign-off and a week of soak without membership/role-related incidents. No auto-deploy.

## Implementation Checklist

### Phase 0: Decision and guardrails

- Confirm the initial scope is memberships + active-org switching only; defer cross-org views, command-palette switching, and deep-link auto-resolve.
- Add an internal runbook section for reverting to single-org behavior before any destructive schema cleanup.

### Phase 1: Schema and models

- Add `organization_memberships(user_id, org_id, role, created_at)` with `(user_id, org_id)` primary key and role check constraint.
- Backfill from `users.org_id` and `users.role`.
- Add nullable `auth_sessions.last_org_id`; backfill from `auth_sessions.org_id`.
- Preserve the existing invitation shape: nullable `invitations.email`, nullable `invitations.github_username`, and the email-or-GitHub check constraint.
- Add `models.OrganizationMembership`, `models.AuthMeResponse`, and typed role constants with validation tests.
- Keep `models.User.OrgID` and `models.User.Role` during the compatibility window, but mark them as active-membership-derived in comments and response construction.

### Phase 2: Stores

- Add `internal/db/organization_memberships.go` with methods for list-by-user, get-by-user-and-org, upsert, update role, remove membership, count admins, and oldest membership.
- Split identity responsibilities from membership responsibilities. User store methods should create, update, and look up identity fields; membership store methods should list org members, update roles, remove members, and count admins.
- Change team membership queries from `users` table role/org filters to joins through `organization_memberships`.
- Change member removal to remove membership, not delete `users`.
- Change duplicate-member checks to query memberships by email and GitHub login within the active org.
- Add `auth_sessions.last_org_id` update support for active-org switching.
- Add org-aware session invalidation or active-org fallback after membership removal.
- Add table-driven tests for every membership store method, including `org_id` predicates and last-admin checks.

### Phase 3: Auth and RBAC

- Change auth middleware to load users by `session.UserID` without requiring `users.org_id`, then resolve active org from `X-Active-Org-ID`, `session.last_org_id`, or oldest membership.
- Validate `(user_id, active_org_id)` membership on every authenticated request.
- Store active membership role in request context; update `RequireRole` to use that role.
- Add auth middleware tests for header-selected org, session fallback, invalid selected org fallback, no-membership login, revoked membership, and independent tab behavior.
- Update logging context to include active org and user ID from the resolved request context.

### Phase 4: Signup, OAuth, invitations

- Wrap signup user creation, org creation, membership insert, and session creation in transactions where partial state would be harmful.
- For existing users accepting invites, insert/upsert membership instead of rejecting or replacing org. Match invite ownership by email or linked GitHub login.
- For new users via email invite, create user + invited membership without creating an implicit personal org.
- For new users via GitHub-only invite, require GitHub OAuth and match the OAuth login to `invitations.github_username`.
- Ensure the post-login invite claim flow handles existing users and GitHub-login matches before redirecting to the app.
- Update GitHub/Google account linking so provider identity updates are independent of the user's active org.
- Emit auth and invitation audit events against the active/invited org, not a global user org field.
- Keep GitHub PR-authorship tokens org-scoped in `user_credentials`; update account/settings copy to reflect per-org linking.
- Add the invitation test matrix from this doc before enabling multi-org accepts.

### Phase 5: API and frontend

- Extend `/api/v1/auth/me` to return `{user, org_id, active_org_id, active_role, memberships}` while preserving existing top-level fields used by the app.
- Add `POST /api/v1/auth/active-org` to validate membership and update `auth_sessions.last_org_id`.
- Add an `ActiveOrgProvider` seeded from `?org=`, `sessionStorage`, then `/auth/me`.
- Update the API client to send `X-Active-Org-ID` on authenticated requests.
- Update React Query keys or invalidate all queries on org switch so old-org data cannot remain visible.
- Add `OrgContextSwitcher` above `RepoContextSwitcher`; hide it for exactly one membership; clear `?repo=` on org switch.
- Update `useAuth` and auth response types to expose `activeOrgID`, `activeRole`, and `memberships`.
- Update role-gated UI to use `active_role` or the active membership, not a global `user.role`.
- Preserve the team invite dialog's email and GitHub-username modes, including GitHub autocomplete/status behavior.
- Add frontend tests for hidden single-org switcher, switching orgs, clearing repo param, query invalidation, and per-org role gating.

### Phase 6: Isolation and rollout

- Add cross-org isolation integration tests for every handler that reads a resource by ID.
- Expand `internal/db/tenancy_test.go` to understand `organization_memberships` and any new auth exceptions.
- Migrate backend tests away from user-level org/role fixtures and toward active-membership fixtures.
- Migrate frontend tests away from global `user.role` mocks and toward active-role or active-membership fixtures, while preserving GitHub-username invitation coverage.
- Replay staging traffic for single-org users and compare request/response bodies, status codes, and side effects.
- Enable for internal users, then selected multi-org users, then all users.
- Keep `users.org_id`, `users.role`, and old `auth_sessions.org_id` for at least one release after full enablement.

### Phase 7: Cleanup

- Drop compatibility reads.
- Drop old columns and old check constraints only after explicit sign-off.
- Remove migration metrics after the soak window.

## Risks considered

### Risk 1: Shared session state across tabs

*Resolved in design.* We make the active org a request-level concern via `X-Active-Org-ID`, not session-level mutable state. `AuthSession.last_org_id` is a bootstrap hint for new tabs, not the authority. Each tab is independent.

### Risk 2: Global email uniqueness vs. future SSO-per-org

*Accepted.* If an enterprise customer later demands SSO-only access to their org while the same human has a personal account elsewhere, we'll handle it via per-org *authentication policies*, not by splitting identities. This is consistent with GitHub. Revisit when the first enterprise customer asks.

### Risk 3: Ambiguous deep-links

*Resolved in design* via auto-switch-with-prompt + opt-in `?org=` on shareable URLs. Path-based routing rejected.

### Risk 4: Data-scoping regressions now leak across orgs instead of failing safe

*Resolved in design* with a typed org-bound store API, the existing static tenancy audit, and mandatory per-handler cross-org isolation integration tests. Defense-in-depth lands before behavioral change (rollout step 2). Postgres RLS was considered and rejected — see the defense-in-depth section for the trade-off.

### Risk 5: Destructive column drop

*Resolved in design.* Rollout steps 1–7 are reversible; step 8 is deferred, gated on soak time, and requires explicit sign-off. Not auto-deployed.

### Risk 6: "Personal org" temptation

*Accepted with guardrail.* Introducing a first-class personal-namespace concept (à la GitHub's `user/repo`) is a bigger scope than this design. Instead: default the first-org name to "Personal" as a convention. If post-launch data shows users regularly want to migrate data between their "personal" org and a shared one, we invest in a first-class concept then.

### Risk 7: `RequireRole` silently breaks

*Resolved in design.* The signature change to take active-org context is explicit, and removing `users.role` means old call sites won't compile — no silent fallback.

### Risk 8: GitHub App installation fan-out

*Follow-up required, not blocking.* A 143 GitHub App installation is linked to one 143 org today. Multi-org membership makes it more likely that a user administers two 143 orgs that could both plausibly want the same GitHub installation. We keep the current constraint (one GitHub installation → one 143 org) and file a follow-up to decide whether to relax it.

### Risk 9: Invitation edge cases

*Resolved in design* via idempotent accept + explicit test matrix.

### Risk 10: Org deletion cascades to users

*Resolved in design.* `ON DELETE CASCADE` applies to memberships from both sides; user rows survive org deletion. Zero-membership users see the first-run empty state on login.

## Open Questions

1. **Auto-switch on deep-link — prompt vs. silent** (Risk 3). Starting with prompt to avoid surprise. Revisit after two weeks of usage data.
2. **Default org name.** "Personal" is the proposal. Alternatives: "<first-name>'s workspace," or a required prompt. Going with "Personal" because it minimizes onboarding friction.

## Success Criteria

- A user with two memberships can switch between orgs in <300ms (UI), seeing the correct data each time.
- A user with one membership sees no visual or behavioral change vs. before this ship (validated by staging traffic replay before the product-visible rollout).
- Zero cross-org data-leak incidents in the 30 days after multi-org is enabled (step 6).
- Invitation acceptance success rate does not regress.
- <0.5% of sessions hit the "membership revoked fallback" path in steady state.
- Every handler under `internal/api/handlers/` has a cross-org isolation test in CI.
