# Release channels: canary and stable (operator runbook)

How to run 143 as two release channels over one database — `canary`
(latest `main`, dogfood orgs) and `stable` (pinned promoted releases,
customer orgs) — and how to promote, hotfix, and roll back. Design rationale
and contracts live in
[docs/design/118-canary-stable-release-channels.md](../design/118-canary-stable-release-channels.md).

Single-plane deployments need none of this: with no canary hosts, no
`CANARY_ORIGIN`, and default `DEPLOY_ROLES`, everything behaves exactly as
before the split.

## One-time setup

1. **DNS + certificates.** Point `canary.<domain>` at the app host (same A
   record as the apex). Caddy already carries the `canary.{$DOMAIN}` vhost.
2. **OAuth callbacks.** Add `https://canary.<domain>/api/v1/auth/github/callback`
   (and the Slack equivalents) to the existing GitHub/Slack apps. Webhook
   URLs are NOT duplicated — ingress stays on the stable plane.
3. **Secrets bundle** (`.env.production.enc` in the infra repo):
   - `CANARY_ORIGIN=https://canary.<domain>`
   - Add fleet entries: `app-canary:<app-host-ip>` (same host as `app`) and
     one or more `worker-canary:<ip>` hosts (provision like any worker).
4. **GitHub repo configuration:**
   - Repository variable `DEPLOY_ROLES` (see phases below) and optionally
     `PROMOTE_SOAK_DAYS` (default 3).
   - A repository ruleset targeting `v*` tags: restrict creation to the
     release workflow and maintainers; block deletion and updates.
   - The `stable` branch and `v*` tags are owned by `promote.yml` — no
     branch protection that would reject its force-pushes to `stable`.
   - The `stable-promotion` environment (auto-created on the first
     promotion run): add **required reviewers** to it so every promotion —
     including soak overrides — needs a second maintainer's approval.
   - Optional: a `PROMOTE_SLACK_WEBHOOK_URL` secret (Slack incoming
     webhook) announces promotions and rollbacks; skipped when unset.

## Phased rollout

| Phase | `DEPLOY_ROLES` | What happens |
|-------|----------------|--------------|
| 0 (today) | unset (`app,worker`) | Single plane; every green main deploys to customers. |
| 1 | unset | Land the channel plumbing (already merged with this doc). No behavior change: every org, job, node defaults to `stable`. |
| 2 | `app-canary,app,worker,worker-canary` | Both planes track `main` (identical code). Flip dogfood orgs to canary; verify routing, job isolation, preview bootstrap from the canary host. |
| 3 | `app-canary,worker-canary` | Stable is pinned. `main` merges deploy canary only; stable moves via `promote.yml`. The first promotion cuts `v1.0.0`. |

## Flipping an org's channel

Flips are operator actions, performed **while the org is quiescent** (no
active session executors or preview runtimes — pinned jobs stamped with the
new channel would otherwise wait out the old node's crash-recovery path):

```sql
UPDATE organizations SET release_channel = 'canary' WHERE id = '<org-uuid>';
```

The canary host guard caches channel lookups for up to a minute; a freshly
flipped org may need ~60s before `canary.<domain>` serves it.

## Promoting a release

Run the **Promote Stable** workflow (`promote.yml`) with:

- `ref`: the `main` SHA to promote. Policy default: the newest SHA at least
  `PROMOTE_SOAK_DAYS` old with no unresolved canary regressions newer than
  it — check the canary error dashboards before promoting.
- `bump`: `minor` for routine promotions (the common case), `major` only
  for deliberate self-hoster-breaking milestones.

The workflow verifies CI, enforces soak (override with a reason to
expedite), deploys the stable plane with the schema preflight
(`migrate verify` — stable never migrates), and only after the fleet
verifies green cuts the annotated `vX.Y.Z` tag, the GitHub Release with
generated notes, digest-identical GHCR version tags, and fast-forwards the
`stable` branch.

A promotion also unblocks any destructive migrations that were waiting on
it (their `lint:destructive-ok-after` floors); expect the next canary
deploys to apply the queued contract steps.

## Hotfixing stable

Prefer promoting a newer soaked SHA. When stable needs a fix now and `main`
carries unsoaked risk on top of it:

1. Land the fix on `main` first.
2. `git checkout -b hotfix/v1.43.x v1.43.0`, cherry-pick the fix.
   **No migration files on hotfix branches** — sequential numbering belongs
   to `main`; a fix needing schema changes is promoted from `main` instead.
3. Run Promote Stable with the hotfix SHA and `bump=patch` → `v1.43.1`.

## Rolling stable back

Run Promote Stable with the **existing release tag** (e.g. `v1.42.0`) as
`ref`. The workflow redeploys that release's SHA, re-marks the release as
"latest", and moves the `stable` branch back — no new version is minted.
The schema preflight enforces the destructive floor: a target older than an
applied destructive migration's floor is refused.

Canary rollback is a revert on `main` (or a manual
`deploy/scripts/deploy-fleet.sh <key> <sha> app-canary,worker-canary` run
from a machine with the secrets checkout) — re-running an old deploy
workflow run does nothing, because `deploy.yml` skips any SHA that is no
longer the head of `main`. The schema never rolls back — old code on newer
schema is the supported direction.

## Destructive migrations

`lint-schema` (CI) refuses `DROP TABLE`, `DROP COLUMN`, `RENAME`,
`ALTER COLUMN ... TYPE`, and `SET NOT NULL` in new migrations unless the
file carries:

```sql
-- lint:destructive-ok-after schema="000240" reason="stable >= 000240 no longer reads issues.legacy_state"
```

The canary deploy gate (`STABLE_MAX_MIGRATION`, resolved from the latest
release) defers the migration until stable has been promoted past the
floor, then records it in `schema_compat_floors` so promote/rollback
preflights can enforce it without parsing annotations. Rehearse destructive
migrations against a restored snapshot (`restore-test.sh`) before
un-gating them.

## What to watch

- Logs carry a `channel` field on every line:
  `make logs-query Q='channel:canary AND level:error AND _time:[now-1h,now]'`.
- `jobs.channel` / `nodes.channel` split queue health and fleet views by
  plane; a canary worker outage queues dogfood jobs (deliberately — there
  is no cross-channel fallback) and pages nobody.
- The **Cross-Version Schema Compatibility** workflow runs the promoted
  release's store tests against latest main's schema on every main push; a
  red run means a migration broke the release-window contract and should be
  reverted or floor-annotated before the canary deploy gate meets it.
