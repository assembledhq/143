# Design: GitHub Organization Auto-Join

> **Status:** Implemented | **Last reviewed:** 2026-06-10

## Problem

Verified-domain auto-join (97) captures teammates by company email, but a
large share of 143's audience signs in with GitHub using a personal or
private email. For them the strongest possible team signal isn't the
inbox — it's membership in the company's GitHub organization, which 143
already knows about: every workspace that connects repositories has the
GitHub App installed on that org. A developer who clicks "Sign in with
GitHub" should land directly in their team's workspace, with zero extra
clicks and zero extra consent screens.

## Survey of prior art

- **Vercel / Codecov / Sentry's GitHub-org onboarding** map a GitHub org
  to a team and gate joining on live org membership. None of them add a
  verification ceremony — the GitHub App installation *is* the proof,
  because GitHub only lets org owners install apps.
- **`read:org` on the login OAuth** (the other obvious route) is rejected:
  it widens the consent screen for every user, and OAuth-App org access
  restrictions (commonly enabled at security-conscious companies) silently
  hide membership, producing un-debuggable "why wasn't I auto-joined"
  failures precisely for the customers that matter. A GitHub App
  installation token is exempt from those restrictions.
- **Join behavior** goes one step further than 97: new signups are
  placed directly in the workspace, and existing accounts are captured
  automatically at their next GitHub login — no prompted "join your
  team" surface. An email domain is a heuristic, so 97 kept existing
  users' joins prompted; membership in an owner-administered GitHub org
  is an unambiguous entitlement, so prompting would be pure friction.
  Join-only — removal from the GitHub org never revokes 143 membership
  (revocation sync is SCIM territory, out of scope).

## Design

### Trust model

Domain capture needed DNS TXT proof because a domain is a string anyone
can type. Here, three facts replace the ceremony entirely:

1. **Claim authority**: installing the GitHub App on an org requires
   GitHub org-owner privileges. The installation↔workspace link
   (`github_installation_org_links`) was created by a 143 admin completing
   that flow. Enabling auto-join on the link requires 143 `admin` role.
2. **User identity**: the signing-in user's GitHub user id is the OAuth
   subject itself — no email attestation needed. The entire
   `email_verified_at` machinery of 97 is bypassed, not extended; the
   nOAuth class of attack doesn't apply.
3. **Membership**: checked against GitHub with the installation token at
   the moment of join (live), so there is no stale-verification window at
   all — unlike DNS, where a daily sweep bounds the damage of a lapsed
   record, here the authorization check *is* the source of truth.

Consequence: the only persistent state is admin intent (a toggle) plus a
discovery index (the roster, below). No tokens, no statuses, no
re-verification lifecycle.

### Discovery vs. authorization

The one hard problem is *candidate discovery*: given a signing-up GitHub
user, which auto-join-enabled installations might they belong to? GitHub
offers no "which orgs is this user in" query without user-side `read:org`
(rejected above). Fanning out a live membership probe to every enabled
installation on every signup is O(enabled installations) GitHub calls on
the login path — fine today, unacceptable at a few hundred teams.

So the design splits the two concerns:

- **Discovery** is a local lookup against a synced member roster
  (`github_org_members`), populated when auto-join is enabled and kept
  fresh by `organization` webhooks plus a daily reconciliation sweep.
  Login never blocks on per-installation fanout.
- **Authorization** is a single live
  `GET /orgs/{login}/memberships/{username}` call with the installation
  token at the moment a membership is actually granted, requiring
  `state == "active"`. Roster staleness can therefore delay discovery but
  can never admit someone GitHub no longer vouches for (e.g. an
  offboarded employee signing up through a stale roster row).

### Database schema

Migration `000170_github_org_auto_join` (renumber against origin/main
before push):

```sql
-- Admin intent lives on the existing link row: auto-join is precisely a
-- property of the installation↔workspace relationship.
--
-- The DB default stays false even though the product behavior is
-- default-ON for newly connected orgs (see "Enabling capture"). A
-- column default of true would retroactively flip every existing
-- installation to capturing — a consent problem (those admins opted
-- into repo access, not a membership policy) — and would collide with
-- the exclusivity index wherever one installation links to multiple
-- workspaces. Default-on is applied programmatically at link creation,
-- where eligibility can actually be checked.
ALTER TABLE github_installation_org_links
    ADD COLUMN auto_join_enabled boolean NOT NULL DEFAULT false;

-- One workspace captures a GitHub org globally. The link table
-- deliberately allows one installation to serve multiple workspaces
-- (shared repos); auto-join must not inherit that — mirrors
-- idx_org_domains_verified_domain from 97.
CREATE UNIQUE INDEX idx_github_install_links_auto_join
    ON github_installation_org_links (installation_id)
    WHERE status = 'active' AND auto_join_enabled;

-- Discovery roster. Global (keyed by installation), like
-- github_installations; populated only while a capture is enabled.
CREATE TABLE github_org_members (
    -- lint:no-org-id reason="global GitHub org roster keyed by installation, shared like github_installations"
    installation_id bigint      NOT NULL REFERENCES github_installations(installation_id) ON DELETE CASCADE,
    github_user_id  bigint      NOT NULL,
    github_login    text        NOT NULL,
    synced_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_id, github_user_id)
);
CREATE INDEX idx_github_org_members_user
    ON github_org_members (github_user_id);

-- Full-sync watermark; NULL until the first successful sync (drives the
-- "syncing…" state in settings and the reconciliation sweep's ordering).
ALTER TABLE github_installations
    ADD COLUMN roster_synced_at timestamptz;
```

No new status enums: intent is the boolean, sync progress is
`roster_synced_at`, and permission readiness is observed live (below)
rather than persisted — persisted copies of GitHub-side state are exactly
what go stale.

Roster rows are deleted when auto-join is disabled, when the capturing
link is deactivated, or when the installation is uninstalled — we don't
retain member lists nobody asked us to hold.

### Enabling capture (default-on for new connections)

Auto-join is **on by default when a GitHub organization is connected**:
the cleanest go-forward experience is that linking your company's GitHub
org *is* setting up team access, with the toggle as the opt-out. Two
paths share one `tryEnableAutoJoin` routine:

- **At link creation** (the GitHub App install/link callback in
  handlers/integrations.go): after the link row is written, run the
  eligibility steps below and enable on success. New installations
  accept the manifest's current permission set, so `members: read` is
  present and the common case enables silently. Any ineligibility
  (user-account installation, captured by another workspace, permission
  somehow missing) leaves the flag off with the matching settings badge
  — never an error in the connect flow; connecting repos must not fail
  because membership capture can't start. Audit-logged with the linking
  admin as actor.
- **Explicitly**, via `PATCH /api/v1/team/github-orgs/{installation_id}`
  with `{"auto_join_enabled": true}` — the re-enable path, and the only
  path for links that predate this feature (existing connections are
  never flipped on retroactively; their admins consented to repo
  access, not a membership policy).

Eligibility, in order:

1. Verify an active link exists between the caller's org and the
   installation, and `account_type == "Organization"` (user-account
   installations have no members) — else `422 NOT_AN_ORGANIZATION`.
2. Probe the App's granted permissions via `GET /app/installations/{id}`
   (app JWT). If the `members: read` organization permission is missing —
   the App manifest gains it, but **existing installations only get it
   after an org owner accepts the updated permissions** — fail with
   `412 MEMBERS_PERMISSION_MISSING` and include the deep link
   `https://github.com/organizations/{login}/settings/installations/{id}`
   so the UI can render "an owner of {login} must approve updated
   permissions". The `installation.new_permissions_accepted` webhook
   flips this without polling.
3. Flip the flag; the partial unique index turns a concurrent or existing
   claim by another workspace into `409 GITHUB_ORG_ALREADY_CAPTURED`
   (existence-only error — don't name the holding workspace).
4. Enqueue a roster sync job: paginate
   `GET /orgs/{login}/members?per_page=100` (active members only — GitHub
   excludes pending invitees and outside collaborators, which is the
   policy we want) and replace the installation's roster in one
   transaction, then stamp `roster_synced_at`.

Disabling clears the flag and deletes the roster. Both directions emit
audit events.

### Join paths

1. **New GitHub OAuth signup** (`Callback` in handlers/auth.go). After
   pending invitations (which keep precedence) and before domain capture
   (`tryDomainAutoJoin`), look up the roster by the OAuth user's GitHub
   id, join against enabled active links, live-confirm membership, and
   reuse the 97 transaction (`createAutoJoinUser`: create user, grant
   `member` via GrantAtLeast, pin last_org_id, issue session) — the only
   delta is that no email attestation is required or implied;
   `email_verified_at` stamping continues to follow provider attestation
   independently, exactly as today. If the user matches several captured
   orgs, join all of them (they are a member of each of those GitHub
   orgs) and pin last_org_id to the oldest-enabled link for
   determinism — a leftover "you can also join…" prompt is exactly the
   residual UI this feature avoids. Any failure (GitHub 4xx/5xx, roster
   miss) falls through to domain capture and then classic fresh-org
   signup; auto-join must never break login.
2. **Existing users are captured at their next GitHub OAuth login, not
   prompted.** The callback has already resolved the account; a roster
   match + live membership confirm grants any missing memberships
   (GrantAtLeast `member`) before the session is issued, with the same
   provenance toast as signup. Grants only ever happen at an
   authenticated, user-initiated GitHub moment — never silently
   overnight from a webhook; a long-lived session simply catches up on
   its next login. The 97 joinable surface (`GET /api/v1/orgs/joinable`,
   `POST /api/v1/orgs/{id}/join`) is untouched and remains domain-only.
   Corollary an admin must be able to see: while auto-join is on, the
   GitHub org *is* the roster — removing a member in 143 doesn't
   survive their next login unless they also leave the GitHub org or
   auto-join is disabled. The remove-member dialog says so (UI §5).
3. **Google/password users** have no `github_id` and simply never match;
   they're covered by domain capture, or by GitHub-org capture on their
   first GitHub OAuth login (which links accounts under 97's hardened
   rules).

Role is hardcoded `member`, as in 97 and for the same footgun reasons.

### Webhooks & reconciliation

The App subscribes to two additional events (manifest change, no
re-approval needed for event subscriptions):

- `organization` — `member_added` / `member_removed` upsert/delete roster
  rows; `renamed` refreshes `account_login` (membership URLs are built
  from it). Ignored for installations without an enabled capture.
- `installation` — `new_permissions_accepted` lets the settings UI flip
  from "awaiting approval" without polling; the existing `deleted`
  handler (which already deactivates links) additionally clears
  `auto_join_enabled` and the roster.

A leader-elected scheduler sweep (same pattern and budget discipline as
`recheckVerifiedDomains`) re-lists members for enabled installations
roughly daily, bounded per tick and ordered by `roster_synced_at`, to
heal missed webhooks. A definitive `403` (permission revoked) disables
auto-join and emits a system audit event — mirroring the DNS sweep's
"observed loss of proof disables capture, re-enabling is an explicit
admin action" rule. Transient errors stamp nothing.

### API contract

Admin, org-scoped, RequireRole admin:

- `GET /api/v1/team/github-orgs` → linked installations:
  `{ github_orgs: [{ installation_id, account_login, account_type,
  auto_join_enabled, members_permission: "granted"|"missing",
  roster_synced_at, captured_by_other_org: bool }] }`. Permission state is
  probed via app JWT with a short in-process cache (admin page, low
  traffic); `captured_by_other_org` is existence-only.
- `PATCH /api/v1/team/github-orgs/{installation_id}` body
  `{ auto_join_enabled: bool }`; errors `404` (no active link),
  `409 GITHUB_ORG_ALREADY_CAPTURED`, `412 MEMBERS_PERMISSION_MISSING`
  (with settings deep link), `422 NOT_AN_ORGANIZATION`.

User-scoped: none. The 97 joinable/join endpoints are untouched
(domain-only); GitHub capture grants memberships inside the OAuth
callback instead of via a join surface.

### UI

Deliberately minimal: no new pages, no wizard, no org-switcher changes
at all. The joiner-side experience is strongest when it's invisible
(sign in with GitHub → land in the team workspace); the UI work is two
settings surfaces plus two moments that need real copy. Login,
verify-email, onboarding, and the org switcher are untouched.

**1. Settings → Team: regroup under one "Auto-join" heading.** Rather
than adding a fourth sibling section next to Members / Invitations /
Verified domains, fold verified domains and the GitHub org rows under a
single heading with one sentence of shared explanation. Both row types
share the existing anatomy from `verified-domains-section.tsx` (icon +
identity + status badge + switch); a GitHub row is simpler than a domain
row — no TXT instructions, no copy buttons. For newly connected orgs the
switch arrives already ON (see "Enabling capture") — the row exists so
admins can *see and revoke* the policy, not to make them assemble it;
links predating the feature show it OFF until explicitly enabled.

```
Auto-join
People who match these rules join as members automatically —
no invitation needed.

┌─────────────────────────────────────────────────────────────┐
│ 🌐  acme.com                    [Verified]   Auto-join (on) │
├─────────────────────────────────────────────────────────────┤
│   GitHub organization acme-corp              Auto-join (on) │
│    Anyone in acme-corp on GitHub can join this workspace.   │
└─────────────────────────────────────────────────────────────┘
  [ add domain… ________________ ]  [Add domain]
```

The GitHub row has three states, driven by `members_permission` and
link status. A healthy row carries **no badge** — the switch already
says on/off, and a "Ready"/"Enabled" chip would only restate it.
Badges are reserved for the two exceptional states, where the state
*is* the instructions (the same role the DNS card plays for pending
domains) and the copy must name the GitHub org, because the person who
can fix it (a GitHub org owner) may not be the admin looking at the
screen:

```
Healthy                no badge; switch enabled (on/off)
Needs GitHub           switch disabled, amber (reuse failed-DNS style)
approval               │  An owner of acme-corp needs to approve
                       │  updated permissions on GitHub.
                       │  [Review on GitHub ↗]   ← settings deep link
Unavailable            switch disabled
                       app uninstalled, or captured elsewhere
                       (existence-only: "already connected to
                        another workspace")
```

Disable confirmation borrows the domains toggle's reassurance line —
"Stops new automatic joins. Nobody is removed." — which preempts the
most common admin hesitation. If the workspace has no GitHub
installation, the row is not hidden; it shows a quiet "Connect GitHub
to enable" line linking to Integrations (hidden features are
undiscoverable; empty states are advertising).

**2. Settings → Integrations: one cross-link, not a second control.**
The GitHub detail sheet gains a single read-only line — "Auto-join for
acme-corp members: On — manage in Team settings" — so the connection
surface points at the policy surface without duplicating state. Because
connecting now enables capture by default, the post-connect success
state says so in one line ("Members of acme-corp can now join this
workspace automatically") — disclosure at the moment the default takes
effect, with the Team-settings link as the opt-out path.

**3. Org switcher: nothing.** Because existing accounts are captured
at login rather than prompted (join path 2), GitHub adds no rows to
"Workspaces you can join" — that section stays domain-only, exactly as
97 built it. One less list entry type, one less mechanism for users to
parse, and the strongest version of "auto" there is.

**4. Provenance toast for the silently-joined.** Today an OAuth signup
captured by domain lands in the workspace with no explanation (only the
email-verification path shows "Welcome to {org}"). GitHub capture makes
silent placement the common case — at signup *and* at the login that
captures an existing account — and waking up inside a workspace you
never asked to join is the moment a user feels either magic or
surveilled. One dismissible banner on the session that performed the
capture supplies the missing line of provenance — added for *both*
capture sources, closing the gap 97 left:

```
┌─────────────────────────────────────────────────────────┐
│ ✓ You've joined Acme's workspace because you're a       │
│   member of the acme-corp GitHub organization.    [✕]   │
└─────────────────────────────────────────────────────────┘
```

Contract note: the OAuth callback is a redirect, so the join's
provenance must travel with it — append one-time query params to the
post-login redirect (`?joined_org=<id>&joined_via=github_org|domain`,
display-only; the membership itself is already in `/auth/memberships`).
This covers signup and login-time capture identically. The verify-email
path already returns `joined_org` in-band and needs nothing new.

**5. Remove-member dialog: one conditional sentence.** Login-time
capture means admin removal doesn't stick while the target remains in
the captured GitHub org (join path 2). When that's the case, the
existing remove confirmation gains one line: *"They're a member of
**acme-corp** on GitHub and will rejoin on their next sign-in — remove
them from the GitHub organization too, or turn off auto-join."* Honest
at the moment it matters, instead of a support ticket later.

### Audit

`team.github_org_auto_join_enabled` / `…_disabled` (admin or system, with
`account_login` and reason for system disables), and the existing
`team.member_auto_joined` gains `source: "github_org"` +
`github_org_login` alongside the domain fields (both signup capture and
login-time capture of existing accounts).

## Security considerations

- **No email trust anywhere in the path.** The grant keys off the OAuth
  session's GitHub user id and a live membership check; spoofable
  identifiers (profile email, display name) are never consulted.
- **Live confirm at grant time** closes the roster-staleness window;
  `state == "active"` excludes invited-but-not-accepted members.
- **Exclusivity** via partial unique index prevents a second workspace
  from capturing an org, with the database as the arbiter under
  concurrency (same trick as verified domains).
- **Fail closed**: missing roster row, failed live check, missing
  permission, or any GitHub error → no grant, normal signup proceeds.
- **Roster privacy**: member lists are held only while a capture is
  enabled and deleted on disable/uninstall.

## Out of scope / future

- Membership revocation sync (GitHub removal → 143 removal). Join-only,
  matching 97. Revisit alongside SCIM/enterprise directory sync —
  especially since login-time capture makes GitHub the effective roster
  while auto-join is on.
- GitHub team → 143 role mapping (e.g. `acme/platform-admins` → admin).
  Same elevated-role footgun 97 declined; admins promote after join.
- Background capture between logins (a `member_added` webhook
  immediately adding an existing 143 account). Grants stay tied to
  authenticated, user-initiated moments; a webhook-driven add would
  move someone's account while they sleep.
- Incremental user-side `read:org` fallback for orgs that won't install
  the App — adds a second consent hop for a near-empty segment of a
  coding-agent platform's audience.
- Retroactive *domain* capture of existing accounts stays out, as in
  97 (email domain is a heuristic; GitHub org membership is an
  entitlement, which is why login-time capture is in scope here but
  forced domain migration is not).
