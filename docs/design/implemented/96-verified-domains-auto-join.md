# Verified Domains & Auto-Join (Domain Capture)

## Problem

Getting teammates into a workspace required an explicit invitation per
person. Enterprise tools (ChatGPT/Claude Enterprise, Figma, Google
Workspace) instead let an admin verify ownership of the company email
domain once; from then on anyone signing in with an email on that domain
lands in the team workspace automatically.

## Survey of prior art

- **Verification tiers.** Email-tier proof (an existing member has a
  verified address on the domain) is what Notion/Linear/Clerk use to gate a
  *prompted* "join your team" flow. DNS TXT verification is what
  Figma/Google/OpenAI/Anthropic/WorkOS require before *automatic* capture.
  Since we auto-join, we require DNS proof.
- **Join behavior.** New signups are placed directly in the org (Figma
  model). Existing users get a prompted "Workspaces you can join" surface
  (Linear model) rather than forced migration.
- **Safeguards** (universal across vendors):
  - Hard denylist of public/free/disposable email providers — gmail.com can
    never be claimed (`internal/services/domains/public_domains.go`).
  - Only provider-verified emails count. The nOAuth (Entra) and
    Zendesk→Slack (CVE-2024-49193) account takeovers both exploited
    products trusting unverified emails for domain-based access.
  - Exact-domain match only, no subdomain wildcards.
  - One org per verified domain, enforced by a partial unique index.
  - Lowest-privilege default role (`member`), admin-only settings,
    audit-logged.

## Design

### Data model

`organization_domains` (migration 000164): org_id, domain (lowercase),
verification_token, status (`pending`/`verified` CHECK), auto_join_enabled,
timestamps. Unique on (org_id, domain); partial unique on domain WHERE
verified — pending claims may coexist, first successful DNS verification
wins globally.

`users.email_verified_at` records the latest attestation of the user's
address (Google `email_verified`, GitHub `/user/emails` verified flag, a
clicked verification link, or a claimed *emailed* invitation token — the
in-app accept-by-ID path deliberately doesn't count, clicking a button
proves nothing about the inbox). The stamp tracks the *current* email in
both directions: an OAuth login whose provider reports the (just-upserted)
address as unverified CLEARS it, so a stamp can never outlive the address
that earned it. A failed `/user/emails` fetch is "no information" and
leaves the stamp alone. Backfilled for Google users; GitHub users get
stamped on next login.

`email_verification_tokens` carries the password-signup verification flow:
Register sends a 24h single-use link (`/verify-email?token=…`); confirming
stamps the address and — when its domain is captured — auto-joins the org
on the spot, mirroring OAuth capture. Resends invalidate earlier links.
Without verification, password accounts never qualify for domain joins
(otherwise registering `ceo@victim.com` with a password would be a
one-step workspace takeover).

### Verification

Admin adds a domain in Settings → Team and receives a TXT record:
`_143-verify.<domain>` → `143-domain-verify=<128-bit hex token>` (apex
also accepted). "Verify" does a live DNS lookup
(`internal/services/domains`). NXDOMAIN means "not yet", only hard
resolver failures surface as errors.

### Join paths

1. **New OAuth signup** (`tryDomainAutoJoin` in handlers/auth.go): if the
   provider attests the email and its domain has a verified auto-join row,
   `createAutoJoinUser` transactionally creates the user, grants a
   `member` membership (GrantAtLeast), stamps email_verified_at, pins
   last_org_id to the joined org, and issues the session — instead of
   `createSignupOrg`. Pending invitations still take precedence; any
   auto-join failure falls back to the classic fresh-org signup so login
   never breaks. For GitHub, capture matches *any* GitHub-verified address
   (`selectGitHubAutoJoinEmail`, primary first), not just the profile
   email — engineers commonly keep the profile email private (noreply
   fallback) or personal while the verified work address sits in
   `/user/emails`; the matched address becomes the account identity,
   unless an existing account already owns it.
2. **Password signup**: Register emails a verification link; confirming it
   auto-joins the captured org (see above).
3. **Existing users**: `GET /api/v1/orgs/joinable` lists matching orgs
   (verified email + verified domain + auto-join on − existing
   memberships); `POST /api/v1/orgs/{id}/join` re-validates eligibility
   server-side and grants `member`. Surfaced in the org switcher under
   "Workspaces you can join", next to pending invitations. For unverified
   emails on a captured domain, the response carries an existence-only
   `email_verification_required` flag (no org identity until the address
   is proven) that drives a "verify your email to join your team" prompt.

### Linking hardening

OAuth account-linking by email match (full access to an existing account)
requires the provider to attest the address — GitHub must list it verified
in `/user/emails` (a failed fetch fails closed), Google must claim
`email_verified`. This closes the nOAuth/Nhost class of unverified-email
takeover, which domain capture would otherwise make more attractive.

### Domain hygiene

A leader-elected scheduler sweep re-checks each verified domain's TXT
record roughly daily (`recheckVerifiedDomains`, gated by
`last_checked_at`). After 3 consecutive affirmative-missing checks
(`failed_checks`; resolver errors don't count and don't stamp), auto-join
is disabled and a system audit event emitted — bounding how long an
expired/transferred domain keeps admitting new members. The verified claim
itself is kept so nobody else can grab the domain, and re-enabling is an
explicit admin action. Orgs are capped at 10 domain claims.

### Endpoints

Admin (RequireRole admin, org-scoped): GET/POST `/api/v1/team/domains`,
POST `/api/v1/team/domains/{id}/verify`, PATCH/DELETE
`/api/v1/team/domains/{id}`. User-scoped (outside OrgContext, like
pending invitations): GET `/api/v1/orgs/joinable`, POST
`/api/v1/orgs/{id}/join` (claim-rate-limited), POST
`/api/v1/auth/email-verifications` (resend, 3/min). Public: POST
`/api/v1/auth/email-verifications/confirm` (token is the credential,
single-use, claim-rate-limited).

### Audit

`team.domain_added` / `domain_verified` / `domain_updated` /
`domain_removed` on the org, and `team.member_auto_joined` whenever a user
enters via the domain (both signup capture and in-app join).

### Invitation-surface gating

The in-app pending-invitations surfaces (list, accept-by-id, decline-by-id)
only match by email when the account's address is attested
(`recipientEmailForInvitations`; unattested → "" → no-match). This closes
the spoofed-email invitation takeover: password signup requires no email
proof, so an attacker could otherwise register victim@corp.com and accept
(or maliciously decline) the victim's pending invitations at whatever role
they carry. The emailed token link still works unverified (possession IS
receipt proof, and now stamps verification), and github-addressed invites
still match (the session's GitHub identity comes from OAuth). Known
transitional window: GitHub users who haven't logged in since the
email_verified_at migration see email-matched invites only after their
next OAuth login; the email link covers them meanwhile.

## Out of scope / future

- Retroactive capture of existing accounts into the org (OpenAI-style
  forced migration) — deliberately not built; existing users keep choice.
- Configurable default role per domain (Clerk offers this; we hardcode
  `member` — auto-granting elevated roles domain-wide is a footgun, and
  admins can promote after join).
- Automatic re-enable of auto-join when a lapsed domain's TXT record
  reappears (kept manual on purpose — DNS observations shouldn't undo an
  admin-visible state change; the domains UI surfaces both the failing
  re-check streak and the automatic disable so the admin can act).
