# Design: Canary and Stable Release Channels

> **Status:** Partially Implemented | **Last reviewed:** 2026-07-14

> **Related docs:** [overall.md](overall.md), [10-infrastructure.md](10-infrastructure.md), [91-non-disruptive-worker-blue-green-deploys.md](future/91-non-disruptive-worker-blue-green-deploys.md), [100-slack-webhook-ingress-durability.md](implemented/100-slack-webhook-ingress-durability.md), [01-database-schema.md](implemented/01-database-schema.md)

## Implementation Status

The code paths are implemented and default-off (everything defaults to the
`stable` channel; single-plane fleets behave exactly as before). Operator
runbook: [docs/self-hosting/release-channels.md](../self-hosting/release-channels.md).

### Implemented

- Migration 000245: `organizations.release_channel`, `jobs.channel`,
  `nodes.channel`, the channel-leading claim index, `schema_compat_floors`.
- Channel stamping at the jobs INSERT chokepoint; channel predicate in
  `ClaimNextRunnable` (no cross-channel fallback); `CHANNEL` config
  validated at startup; per-line `channel` log field; node registration.
- Scheduler candidacy gated to the stable channel.
- Canary host guard (`CANARY_ORIGIN` + `ORG_NOT_ON_CANARY`) and the preview
  gateway app-origin allow-list (bootstrap postMessage + frame-ancestors).
- Canary plane infra: `api-canary`/`frontend-canary` services in
  `docker-compose.app.yml`, Caddy `canary.{$DOMAIN}` vhost, `app-canary` /
  `worker-canary` deploy roles, canary-first fleet barrier,
  `APP_SCHEMA_MODE=verify` stable preflight.
- `cmd/migrate`: destructive gate (`STABLE_MAX_MIGRATION`), floor recording,
  `verify` subcommand.
- `promote.yml` (version computation, soak policy, tag-after-verify release
  cutting, GHCR retags, `stable` branch, rollback via existing tag),
  `deploy.yml` `DEPLOY_ROLES` phasing + stable-floor resolution,
  `.github/release.yml`, cross-version compatibility workflow,
  destructive-migration lint in `cmd/lint-schema`.

### Outstanding

- Operational rollout itself (DNS, OAuth callback registration, canary
  hosts in `FLEET_HOSTS`, org flips, first promotion) — see the runbook.
- Per-channel Grafana dashboard splits and alert routing (canary → Slack
  warning, stable keeps paging) — logs and queue samples already carry the
  channel dimension.
- The `v*` tag repository ruleset (GitHub settings, not code).
- Decide Redis sharing (per-channel logical DB vs version-tolerant keys)
  before Phase 2 flips an org.
- Optional polish from the design: `RELEASE_VERSION` surfaced in the UI
  footer, one-time-token SSO handoff between hosts, canary-scoped
  scheduler.

Split 143.dev into two release channels running against **one shared Postgres**:

- **`canary`** — `canary.143.dev`, always the latest green `main`, deployed
  continuously (today's pipeline, unchanged). The Assembled dogfood org(s)
  live here.
- **`stable`** — `143.dev`, pinned to an explicitly promoted release, moved
  one version at a time. Every customer org lives here.

The database is deliberately **not** split. 143's value is accumulated
context — repos, sessions, automations, integration installs, history — and a
forked staging database dogfoods a different product. Instead we split the
*compute planes* (API + frontend + worker pools) by channel and pay for the
shared schema with an enforced compatibility contract on migrations.

## Problem

Today every green commit on `main` builds images and rolls the entire fleet
(`.github/workflows/deploy.yml` → `deploy/scripts/deploy-fleet.sh`). Customers
receive every `main` commit within minutes of merge. There is no window where
the team experiences a change before customers do, and no way to hold
customers on a known-good version while a regression is investigated.

We want:

1. Customers on an aged, pinned version that only moves when we decide it
   should ("one version at a time").
2. The team dogfooding latest `main` continuously, against the **real**
   production dataset and the real integrations (GitHub App, Slack app,
   Linear webhooks) — not a synthetic shadow environment.
3. A promotion step that is boring: by the time a version reaches customers,
   its migrations and behavior have already been live against production data
   for days.

## Goals

- Customer-facing (`stable`) deploys become explicit, operator-triggered
  promotions of an already-soaked `main` SHA.
- Dogfood (`canary`) keeps the current merge-to-deploy latency.
- One database, one schema, one set of integrations, one backup story.
- Schema changes are burned in on canary before any stable code depends on
  them; the compatibility window is machine-enforced, not tribal knowledge.
- Channel isolation of *execution*: canary code never executes a stable org's
  work, and vice versa.
- Incremental rollout: every phase is independently shippable and reversible.

## Non-Goals

- **Separate databases or a data-sync pipeline.** If hard customer-data
  isolation from unreleased code ever becomes a compliance requirement, that
  is a different design (fully duplicated stack, duplicated GitHub/Slack
  apps, snapshot seeding). Snapshot-restore rehearsal for risky migrations is
  covered here instead (see [Blast radius](#blast-radius-and-accepted-risks)).
- Per-customer progressive rollouts / percentage-based traffic shifting.
  Channels are org-level and there are exactly two.
- Replacing the worker blue/green rollout mechanics
  ([91](91-non-disruptive-worker-blue-green-deploys.md)). Each channel's
  worker pool keeps using them unchanged; canary just rolls far more often.
- Multi-region or high-availability changes.

## Naming

`canary` was chosen over `preview` because *preview* is already a core product
concept (session preview runtimes, `*.preview.143.dev`, the preview gateway on
API port 9090, `WORKER_PREVIEW_DRAIN_TIMEOUT`, …). Overloading it would make
every conversation about this system ambiguous.

Two existing uses of "canary" are **unrelated** to this design and keep their
meaning:

- `worker_deploy_waves.canary_count` — the first N hosts of a worker rollout
  wave within a single deploy.
- `eval_datasets` release gates' `canary_stages` — staged rollout percentages
  for eval-gated features.

In this doc, *channel* always means the `stable`/`canary` release channel.

## Architecture Overview

```
                        Cloudflare / DNS
                              │
                    ┌─────────▼─────────┐
                    │   Caddy (app host) │
                    └──┬──────┬──────┬──┘
        143.dev        │      │      │       canary.143.dev
   ┌───────────────────┘      │      └───────────────────┐
   │                          │ *.preview.143.dev        │
┌──▼───────────┐         ┌────▼─────┐          ┌─────────▼────┐
│ api:8080     │         │ api:9090 │          │ api-canary   │
│ frontend     │         │ (preview │          │ frontend-    │
│ (pinned tag) │         │  gateway,│          │ canary       │
│              │         │  stable) │          │ (latest main)│
└──────┬───────┘         └────┬─────┘          └──────┬───────┘
       │                      │                       │
       │              ┌───────▼────────────────┐      │
       │              │       PostgreSQL        │      │
       └──────────────►  single shared schema  ◄──────┘
                      │  jobs.channel routing   │
                      └───┬────────────────┬────┘
                          │                │
                ┌─────────▼──────┐  ┌──────▼──────────┐
                │ stable workers │  │ canary worker(s)│
                │ (pinned tag)   │  │ (latest main)   │
                │ + scheduler    │  │ no scheduler    │
                └────────────────┘  └─────────────────┘
```

- The app host runs the canary `api-canary`/`frontend-canary` services **in
  the same compose project** as the stable `api`/`frontend` pair (additional
  services in `docker-compose.app.yml`, not a second project — a separate
  compose project would land on its own default network, invisible to
  Caddy's `dynamic a` DNS resolution). Caddy routes by hostname to container
  DNS names — no new host ports, no second origin server.
- Worker hosts are assigned a channel. Stable workers run the pinned tag;
  canary worker(s) run latest `main`.
- Postgres, Redis, and logging hosts are shared and unchanged.

## Core Invariants

1. **The schema is owned by canary.** Migrations run only from the canary
   deploy pipeline (the app-barrier `migrate up` step that exists today in
   `deploy/scripts/deploy.sh`). The live schema version is therefore always
   `>=` what either plane's code expects. Stable deploys never migrate; they
   preflight.
2. **Old-on-new is the only supported skew direction.** Stable (older) code
   runs against a newer schema. This is already the system's posture: worker
   deploy preflight gates on `schema version >= expected`
   (`cmd/worker-deployctl` `--expected-schema-version`), and every routine
   deploy runs migrations before rolling code. This design stretches that
   window from minutes to days; it does not create a new direction of skew.
3. **Release-window compatibility contract.** Every commit on `main`
   (including its migrations, job payload shapes, enum values, and
   cross-channel protocols) must remain compatible with the *currently
   promoted stable release*, not merely the previous commit. Enforced by CI
   and deploy gates (see [The compatibility contract](#the-compatibility-contract)).
4. **Execution is channel-scoped; data is shared.** A job enqueued for a
   canary org is claimable only by canary workers, and vice versa. There is
   **no cross-channel fallback**: if the canary pool is down, canary jobs
   queue (the affected users are the team). Any code path that scans
   cross-org rows (scheduler, retention, dead-node requeue, queue health)
   runs on the stable plane and must tolerate rows written by newer code.
5. **UI/API version is host-scoped; execution channel is org-scoped.** A
   dogfood user who visits `143.dev` sees the stable UI, but their org's jobs
   still execute on canary workers. The canary host refuses sessions from
   stable-channel orgs.

## Database Schema

All changes are additive. The column adds are metadata-only on the fleet's
Postgres 18 (constant defaults take the fast path on PG11+).

```sql
ALTER TABLE organizations
  ADD COLUMN release_channel text NOT NULL DEFAULT 'stable'
  CONSTRAINT organizations_release_channel_check
  CHECK (release_channel IN ('stable', 'canary'));

ALTER TABLE jobs
  ADD COLUMN channel text NOT NULL DEFAULT 'stable'
  CONSTRAINT jobs_channel_check
  CHECK (channel IN ('stable', 'canary'));

-- Claim-path index. Mirrors the existing pending-claim ordering with the
-- channel as the leading column so each pool scans only its own backlog.
CREATE INDEX idx_jobs_pending_claim_channel
  ON jobs (channel, priority DESC, created_at)
  WHERE status = 'pending';

ALTER TABLE nodes
  ADD COLUMN channel text NOT NULL DEFAULT 'stable'
  CONSTRAINT nodes_channel_check
  CHECK (channel IN ('stable', 'canary'));

-- Written by the canary migration gate when it applies a destructive
-- migration; read by stable promote/rollback preflights. Exists because
-- schema_migrations stores only a bare version integer, and a checkout of
-- an older SHA does not contain the newer migration files that carry the
-- destructive-ok-after annotations. See "The compatibility contract".
-- lint:no-org-id reason="schema compatibility metadata, not tenant data"
CREATE TABLE schema_compat_floors (
    migration_version bigint PRIMARY KEY,  -- the destructive migration that was applied
    stable_floor      bigint NOT NULL,     -- min max-migration a deployable stable ref must contain
    applied_at        timestamptz NOT NULL DEFAULT now()
);
```

- `organizations.release_channel` is the single source of truth for org
  assignment. Channel assignment is an operator action (SQL via the
  privileged path initially; an admin surface is future work). **Flip orgs
  only while quiescent** — no active session executors or preview runtimes.
  Those are pinned to nodes via `target_node_id`; after a flip, new turn
  jobs carry the new channel while targeting an old-channel node, and are
  claimable only once that node looks dead/draining, at which point the
  other pool recovers them via the snapshot-hydration path built for
  crashes. Legal, but a degraded experience — drain first.
- `jobs.channel` is **stamped at enqueue time**, inside the single
  `INSERT INTO jobs` chokepoint in `internal/db/jobs.go`, via
  `COALESCE((SELECT release_channel FROM organizations WHERE id = @org_id), 'stable')`.
  One store-level change covers every enqueue helper and is immune to new
  call sites. It is a snapshot, not a live join: moving an org between
  channels affects new jobs only, and in-flight/pending jobs finish on the
  channel they were enqueued for. Jobs with no org context (system cleanup,
  retention, cross-org scans) are stamped `stable`.
- `nodes.channel` is reported at registration from worker/app config, for
  dashboards, deploy tooling, and claim-side validation.
- `schema_compat_floors` is written exactly when the canary migration gate
  applies a gated destructive migration (recording that migration's version
  and its annotated floor) and is queried by stable deploy preflights — no
  annotation parsing or extra git checkout at promote/rollback time.
- Tenancy lints: `organizations` and `nodes` are already `allowedNoOrgID`
  tables; `jobs` already carries `org_id`. No new exemptions needed.

`ClaimNextRunnable` (`internal/db/jobs.go`) gains a channel predicate that
composes with the existing status/run_at/target-node logic:

```sql
WHERE j.status = 'pending' AND j.run_at <= now()
  AND j.channel = @channel          -- NEW: worker's configured channel
  AND ( j.target_node_id IS NULL
        OR j.target_node_id = @node_id
        OR d.id IS NOT NULL )
```

Dead-node requeue needs no change: requeued jobs keep their `channel` column,
so work stranded by a dead canary worker is picked up by the next canary
worker, never by a stable one.

## API Contract

**No new public API routes.** Changes are limited to:

- **Host guard middleware.** Requests to the canary hostname
  (`CANARY_HOST=canary.143.dev` config) with a session whose org is
  `stable`-channel are redirected to the primary domain (HTML) or rejected
  with `403 {"error": "org_not_on_canary"}` (API). The stable hostname
  serves any org.
- **Sessions stay host-only — one login per host.** The session cookie is
  deliberately issued with no `Domain` attribute
  (`writeSessionAndCSRFCookies` in `internal/api/handlers/auth.go`), and
  that is load-bearing: `*.preview.143.dev` serves untrusted preview apps,
  and the preview gateway forwards every request cookie except its own
  `__Host-preview_session` to the proxied app (`stripPreviewCookie` in
  `internal/api/gateway/preview_gateway.go`). A cookie widened to
  `Domain=.143.dev` would be attached by browsers to every
  `{id}.preview.143.dev` request and proxied straight into arbitrary repo
  code — full session takeover. **Invariant: the app session cookie is
  never scoped to `.143.dev`.** Dogfood users log in on each host
  (sessions are DB-backed, so both logins share one account); a signed
  one-time-token SSO handoff between the two hosts — the same pattern as
  the preview bootstrap exchange — is optional future polish. Allowed
  origins / CORS config gains the canary origin.
- **OAuth callbacks:** the existing GitHub App and Slack app gain
  `canary.143.dev` redirect/callback URLs (both providers support multiple
  callback URLs per app). Webhook URLs are **not** duplicated — see below.
- The canary frontend is built/configured with the canary origin in its
  `NEXT_PUBLIC_*` values; `PREVIEW_ORIGIN_TEMPLATE` is identical on both
  planes (preview URLs are channel-agnostic).

## Job Routing, Workers, and the Scheduler

**Worker channel.** A new `CHANNEL` env (config default `stable`) is set per
worker host. It flows into node registration, the claim predicate, log
fields, and deploy tooling. One dedicated canary worker host is enough to
start (team-sized load); capacity is added per channel like any other worker.

**Scheduler candidacy is stable-only.** The periodic-jobs scheduler runs an
advisory-lock leader election among worker-capable nodes
(`cluster.NewScheduler` starts under `MODE=worker|all` in
`cmd/server/main.go`). Without a gate, a canary worker could win the lock and
enqueue system-wide cron work from unreleased code. Rule: **scheduler
candidacy requires `CHANNEL=stable`.** Consequences, accepted deliberately:

- Cron *enqueue* logic always runs pinned code; new scheduled features
  activate at promotion, not at merge.
- Enqueued-by-old, executed-by-new is fine: a canary org's `ingest_sync` job
  is enqueued by stable scheduler code but stamped `canary` and executed by a
  canary worker. Newer code reading older payloads is ordinary backward
  compatibility.
- If a canary-scoped scheduler is ever needed (testing scheduler changes
  pre-promotion), it must use a distinct advisory lock ID and scan only
  canary-channel orgs. Out of scope for v1.

**Webhook front door stays on stable.** GitHub and Slack allow one webhook
URL per app, so ingress cannot be split by channel. This is fine because
ingress is already persist-then-process
([100-slack-webhook-ingress-durability](../implemented/100-slack-webhook-ingress-durability.md)):
verify, persist to `webhook_deliveries`, enqueue, ack. The enqueued job is
stamped with the org's channel, so *processing* runs on the right code.
Consequence: changes to the thin ingress layer itself only take effect at
promotion — a standing reason to keep that layer thin.

**Preview gateway stays on stable.** `*.preview.143.dev` terminates at the
stable `api:9090` gateway, which looks up the runtime in the shared DB and
proxies to the owning worker — including runtimes owned by canary workers for
dogfood sessions. Two protocol surfaces therefore join the compatibility
window:

- *Gateway↔worker*: canary (newer) workers must keep serving the stable
  (older) gateway's runtime protocol until promotion catches up.
- *Gateway↔app*: the preview bootstrap page pins its `postMessage` token
  handshake to a **single** app origin today (`GatewayConfig.AppOrigin` /
  `bootstrapHTML` in `internal/api/gateway/preview_gateway.go` — token
  messages from any other origin are ignored), and the CSP is derived from
  the same value. Previews opened from `canary.143.dev` will not bootstrap
  until the gateway accepts an **allow-list** of app origins
  (`{https://143.dev, https://canary.143.dev}`). Because the gateway runs
  stable code, that change must be live on the stable plane *before* the
  dogfood org flips to canary — free during Phase 2, while both planes
  still track `main`, but a hard sequencing constraint thereafter.

## The Compatibility Contract

This is the price of the shared database. It must be enforced by tooling,
because a single careless migration merged to `main` would otherwise break
customer-facing stable at whatever hour canary auto-deploys it.

### Rules

1. **Additive anytime.** New tables; new columns that are nullable or have
   defaults; new indexes; new job types; new enum-like values *written* only
   by canary code.
2. **Destructive only behind a gate.** `DROP TABLE`, `DROP COLUMN`,
   `RENAME`, `ALTER COLUMN ... TYPE`, `SET NOT NULL` on existing columns —
   only after the stable plane has been promoted past the last release whose
   code depends on the old shape (expand → migrate writes → *promote* →
   contract).
3. **Data shapes are schema.** New JSON payload fields, new status/enum
   values, and new cross-channel protocol fields count. Stable code paths
   that scan cross-org rows (scheduler, retention such as
   `DeleteExpiredCompleted`, queue health, dashboards) will read
   canary-written rows and must tolerate values they don't recognize.
   Reviewers should treat "old code meets this row" as a standard review
   question, same as tenancy.
4. **Forward-only in production.** `.down.sql` files remain for local dev.
   Canary rolls back by redeploying an older SHA — legal without schema
   rollback precisely because old-on-new is the supported direction.
5. **Release branches carry no migrations.** Hotfix branches (below) may not
   add migration files; sequential numbering belongs to `main` alone. A fix
   that needs schema change is promoted from `main` instead.

### Enforcement

**(a) Destructive-migration lint + deploy gate.** Extend the migration lint
family (`cmd/lint-schema` precedent) to classify statements in new
`migrations/*.up.sql` files. Destructive DDL fails CI unless annotated in the
established marker style:

```sql
-- lint:destructive-ok-after schema="000240" reason="stable >= 000240 no longer reads issues.legacy_state"
ALTER TABLE issues DROP COLUMN legacy_state;
```

The threshold is a migration number: *the stable plane's deployed ref must
itself contain migration `000240`* (i.e., stable code was built after the
expand step landed). The **canary deploy pipeline** resolves the
current stable release (the version ledger, (d) below), checks out its tag,
and computes that ref's max migration number — the
same computation `worker_expected_schema_version` does in
`deploy/scripts/deploy.sh` — and refuses to run any pending destructive
migration whose threshold exceeds it. The deploy gate, not PR CI, is
authoritative: PR CI can't know when promotion will happen; the gate blocks
the migration until stable has actually moved.

When the gate *does* apply a destructive migration, it records
`(migration_version, stable_floor)` in `schema_compat_floors` (schema
above). This persistence is what makes the floor enforceable later:
`schema_migrations` stores only a bare version integer, and a
promote-or-rollback checkout of an older SHA does not contain the newer
migration files whose comments carry the annotations.

**(b) Cross-version test job.** A CI job on `main` checks out the current
stable release's tag and runs its DB-backed store/service test packages
against a database migrated with **latest `main`'s** migrations. This
directly exercises "pinned binary, new schema" — the exact failure mode this
design must prevent. Scope (store-layer subset vs full suite) and the
mechanics of pinning the template DB schema are an open question below.

**(c) Schema preflights everywhere.** Workers already assert
`schema >= expected` at deploy. Stable **app** deploys get the same check in
place of the `migrate up` step — stable never invokes `migrate` at all
(older golang-migrate binaries have version-dependent behavior when the DB
is ahead of their migration set, so the preflight replaces the step rather
than wrapping it). The stable preflight additionally enforces the
destructive floor: the target ref's max migration number must be `>=`
`max(stable_floor)` over all applied rows in `schema_compat_floors`.

**(d) The version ledger.** The GitHub Release marked `make_latest`
(readable at `GET /repos/assembledhq/143/releases/latest`) is the
authoritative pointer to the currently-deployed stable version. Immutable
`vX.Y.Z` tags record history, and a workflow-maintained `stable` branch
mirrors the pointer for git-native consumers. Both gates above resolve the
pointer via the Releases API (branch as fallback). Full mechanics under
"Version scheme and GitHub release mechanics" in the Deploy Pipeline
section.

## Deploy Pipeline

**Build (unchanged).** Every green `main` build already pushes
`ghcr.io/assembledhq/143-{server,sandbox,frontend}:<sha>` alongside
`:latest`. Images are immutable by SHA — build once, promote by SHA, no
rebuild at promotion time.

**Canary deploy (the current `deploy.yml`, retargeted).** On green `main`:

1. Run migrations via the canary api service
   (`docker compose run --rm --no-deps api-canary /bin/migrate up` — same
   app-barrier semantics as today, plus the destructive-migration gate and
   floor recording from (a)).
2. Roll `api-canary`/`frontend-canary` on the app host. These are
   additional services **in `docker-compose.app.yml` itself** — the same
   compose project as the stable plane — so Caddy's `dynamic a` DNS
   resolution reaches them on the shared project network. (A second compose
   project would create its own default network, invisible to Caddy; the
   app compose file declares no shareable network today.) The file
   interpolates two tags — `IMAGE_TAG` (stable, pinned) and
   `CANARY_IMAGE_TAG` (latest) — and each plane's deploy updates only its
   own variable and runs a service-scoped
   `up -d --no-deps <its services>`, so a canary rollout never recreates
   stable containers and vice versa (deploy.sh's recreate helpers get an
   explicit per-plane service list).
3. Roll canary worker hosts (existing blue/green machinery, `CHANNEL=canary`).

`FLEET_HOSTS` grows channel-suffixed roles, e.g.
`app:10.0.0.2,worker:10.0.0.4,worker-canary:10.0.0.8,db:10.0.0.3,…`. The
canary app plane is colocated on the existing app host (a role variant of
`app` in `deploy.sh` scoped to the canary services), so no new app host
is required. Caddy gains a `canary.{$DOMAIN}` vhost mirroring the main-domain
blocks with `api-canary`/`frontend-canary` upstreams; `*.preview.{$DOMAIN}`
is untouched.

**Stable deploy (new `promote.yml`).** `workflow_dispatch` with a `sha`
input (default: a suggested candidate, below) and a `bump` input
(`minor` default | `patch` | `major`):

1. Verify the SHA passed CI, images exist in GHCR, and the soak policy is
   satisfied (or an explicit `override` input with reason is set).
2. Resolve the current stable release via the Releases API and compute the
   next version from `bump` (e.g. `v1.42.0` → `v1.43.0`). The promote
   concurrency group serializes runs, so version numbering cannot race.
3. Run the stable schema preflight (no migrations; includes the
   `schema_compat_floors` check).
4. Deploy `app,worker` roles at that SHA — the same `deploy-fleet.sh` path
   and concurrency lock used today, so overlapping promotions queue.
5. Only after the fleet verifies green: cut the release — annotated tag,
   GitHub Release, image retags, `stable` branch fast-forward (mechanics
   below) — and post the release link to Slack. **Tag-after-verify is
   deliberate**: a failed promotion mints no version, so the ledger only
   ever contains releases that actually reached customers, and numbering
   stays clean at any promotion frequency.

**Version scheme and GitHub release mechanics.** Promotions are frequent,
so most releases are minor bumps of a `vMAJOR.MINOR.PATCH` scheme:

- **MINOR** — every routine promotion (`v1.42.0` → `v1.43.0`). At a
  weekly-or-faster cadence the minor number grows quickly; that is normal
  and expected.
- **PATCH** — hotfix promotions on the current line (`v1.43.0` → `v1.43.1`).
- **MAJOR** — reserved for deliberate milestones, chiefly self-hoster
  breaking changes (renamed env vars, required infra changes, migration
  floor jumps). Never bumped automatically.

This is a release-train marker for an application, not library semver: the
number encodes promotion history, not an API-compatibility promise. Start at
`v1.0.0` on the first promotion — the product already serves customers.

Cutting a version involves, in GitHub terms:

- **An annotated tag** `vX.Y.Z` at the promoted SHA, created by the
  workflow via the Git Data API (a tag *object* plus its ref, not a
  lightweight ref) so it carries tagger, date, and message, and
  `git describe` works for local tooling.
- **A GitHub Release** on that tag created with
  `generate_release_notes: true` — GitHub compiles the merged-PR list
  between the previous release tag and this one automatically.
  `.github/release.yml` groups the notes by PR label (e.g. its own section
  for `143-generated` PRs, exclusions for chores), which makes changelog
  curation nearly free given most PRs already carry labels.
- **The "latest" pointer is the release marked `make_latest`**, readable at
  `GET /repos/assembledhq/143/releases/latest`. This — not a moving git
  tag — is the authoritative "what is stable running" pointer that the
  destructive-migration gate and the cross-version CI job resolve. A moving
  `stable-current` tag was considered and rejected: moved tags do not
  propagate on a plain `git fetch` (clients silently keep the stale one)
  and conflict with tag-immutability rules. A **`stable` branch**,
  fast-forwarded by the workflow at cut time, is the git-native mirror for
  humans and the fallback when the API is unavailable.
- **A repository ruleset targeting `v*` tags** restricts tag creation to
  the release workflow and maintainers and blocks deletion and updates —
  version tags are immutable once cut.
- **GHCR version tags by digest**: the workflow retags all three images
  without rebuilding
  (`docker buildx imagetools create -t ghcr.io/assembledhq/143-server:v1.43.0
  ghcr.io/assembledhq/143-server@<digest>`), so `:v1.43.0` is byte-identical
  to the `:sha` image that soaked on canary. An optional floating `:stable`
  image tag gives self-hosters an auto-tracking pin, while `:latest` keeps
  meaning latest `main` (canary).
- **The version string is deploy metadata, not build metadata.** Images are
  promoted by SHA and never rebuilt, so the version cannot be baked in via
  ldflags the way `internal/version.BuildSHA` is. Stable deploys pass
  `RELEASE_VERSION` as container env; the status/health surface and UI
  footer show both (`v1.43.0` + short SHA). Canary surfaces SHA only.

An alternative wiring — publish a Release in the GitHub UI and let a
`release: published` (or `push: tags: 'v*'`) workflow run the deploy — is
more GitHub-native but mints the version before the deploy is known-good
and moves the preflights after the tag exists. Rejected in favor of
dispatch-then-tag; the Releases page remains the human-facing record either
way.

The self-hosting dividend: versioned releases with generated notes and
digest-pinned images give self-hosters a supported pin-and-upgrade cadence
for free — today their options are `:latest` or a raw SHA.

**Rollback.**

- *Stable*: re-run `promote.yml` pointing at an existing release tag
  (`vX.Y.Z`). A rollback mints **no new version** — the workflow redeploys
  that release's SHA, re-marks the release `make_latest` (the Releases API
  supports flipping it on older releases), and moves the `stable` branch
  back, so the ledger records only forward code progress while the pointer
  reflects reality. Legal floor: the target SHA's migration set must
  satisfy every recorded row in `schema_compat_floors` — the preflight
  enforces this with one query, no annotation parsing or extra checkout
  needed, because the floors were persisted when the destructive
  migrations were applied. In practice destructive migrations are rare and
  gated, so almost any recent release is a valid target.
- *Canary*: redeploy an older `main` SHA. Never requires schema rollback
  (invariant 2).

## Promotion Policy

- **Cadence:** target weekly; more often is fine when canary has been quiet.
- **Soak:** default candidate is the newest `main` SHA at least **3 days**
  old, with no unresolved canary regressions newer than it. The dashboard
  split below is what makes "canary has been quiet" checkable rather than
  vibes.
- **Expedite:** allowed with a second maintainer's approval (the `override`
  input), e.g. for a customer-blocking fix.
- **Hotfix path:** prefer promoting a newer soaked SHA. When stable needs a
  fix *now* and `main` has unsoaked risk on top of it: branch
  `hotfix/v1.43.x` from the `v1.43.0` tag, cherry-pick the fix (which must
  land on `main` first), no migration files permitted, promote that SHA
  with `bump=patch` → `v1.43.1` (marked `make_latest`, since it is the
  current line). The next regular promotion from `main` supersedes the
  hotfix branch.
- A promotion also **unblocks** any destructive migrations that were waiting
  on it; expect the following canary deploys to apply queued contract steps.

## Observability and Alerting

- `CHANNEL` becomes a standard zerolog field (like `service`) and a Vector
  label, so `make logs-query Q='channel:canary AND level:error …'` works.
- Grafana dashboards gain a channel dimension (errors, primary-operations,
  worker-runtime, queue health). `QueueHealthSamples` reports per
  `(channel, queue)` so a canary backlog can't hide inside stable numbers —
  and vice versa.
- **Alert routing:** stable alerts keep paging semantics; canary alerts go
  to a Slack warning channel. A broken canary is a bad dogfood day, not an
  incident.
- Node/deploy dashboards read `nodes.channel` to show which generation of
  which channel is live where.

## Blast Radius and Accepted Risks

Stated plainly, because this is the trade the design makes:

- **Shared-data risk.** Canary runs unreleased code against the production
  database. A mis-scoped `UPDATE` or a data-corrupting bug in latest `main`
  can damage rows customers see, even though customers never execute canary
  code. Note this is strictly *less* customer exposure than today, where
  customers run every `main` commit — but it does not go to zero, and a
  separate-DB design is the only thing that would take it to zero.
  Mitigations, all of which exist or extend existing practice:
  - Org-scoped write discipline enforced by `lint-schema`/`lint-stores`.
  - Org-level `feature_flags` for risky cross-org or write-heavy paths, so
    "on main" and "active for the dogfood org" can be decoupled.
  - Verified PITR: `install-pg-backups.sh` + `restore-test.sh` are the real
    safety net; keep restore tests honest.
  - **Snapshot rehearsal for contract migrations:** before un-gating a
    destructive migration, run it against a restored production snapshot
    (the `restore-test.sh` output) rather than discovering lock behavior or
    data surprises on the live DB.
- **Stable front doors constrain some canary testing.** Webhook ingress and
  the preview gateway run pinned code; changes to those layers are only
  fully exercised at promotion. Both are deliberately thin; keep them thin.
- **No cross-channel job fallback.** A dead canary pool means dogfood work
  queues until it's fixed. Accepted: the affected users are us.
- **Both planes' app containers share one host's headroom.** Frontend/API
  are lightweight relative to workers; revisit (dedicated canary app host)
  only if resource contention shows up.
- **Redis is shared.** App-level caches (e.g. mention index) must treat
  cached shapes like DB shapes (old code may read new entries) — or, cheaper,
  the canary plane uses a separate Redis logical DB index and dodges the
  problem. Decide during Phase 2.

## Implementation Plan

Each phase ships alone and changes nothing for customers until Phase 3.

1. **Channel plumbing (no behavior change).** Migration for
   `organizations.release_channel`, `jobs.channel` (+ claim index),
   `nodes.channel`, and `schema_compat_floors`; stamp channel in the
   jobs-store insert chokepoint (`internal/db/jobs.go`); add `CHANNEL`
   config; claim predicate; scheduler candidacy gate. Everything defaults
   `stable`; the fleet is untouched.
2. **Stand up the canary plane.** `api-canary`/`frontend-canary` services in
   `docker-compose.app.yml` (+ `CANARY_IMAGE_TAG`), Caddy vhost, host guard
   middleware, per-host session/origin/OAuth-callback config, the
   preview-gateway app-origin allow-list (must be live on the stable plane
   before any org flips — automatic during this phase, while both planes
   still track `main`), one `worker-canary` host, `FLEET_HOSTS` roles,
   deploy script role variants. Transitionally, `deploy.yml` deploys *both*
   planes from `main` (identical code, so behavior is unchanged). Flip the
   Assembled org to `canary` **while it has no active sessions or preview
   runtimes**; verify routing, job isolation, preview bootstrap from the
   canary host, logging labels.
3. **Pin stable.** `deploy.yml` deploys canary only; add `promote.yml` and
   the `v*` tag ruleset + `.github/release.yml` notes config; first
   promotion is the then-current head (a no-op deploy that starts the
   clock) and cuts `v1.0.0`, seeding the version ledger and the `stable`
   branch; stable app deploys switch from `migrate up` to schema preflight.
4. **Enforcement + polish.** Destructive-migration lint and canary deploy
   gate; cross-version test job; per-channel dashboards and alert routing
   split; promotion runbook in `docs/guides`.

## Open Questions

- **Cross-version test scope:** store/service packages only (fast, catches
  schema breaks) vs. the full suite of the pinned ref (slower, catches
  behavioral coupling)? Likely start narrow and widen on the first miss.
- **Redis:** shared with version-tolerant keys, or per-channel logical DB?
- **Channel admin surface:** SQL-only is fine for two orgs; decide whether a
  support-facing toggle is worth building when design partners want early
  access to canary.
- **Second dogfood org:** a `canary` org per staff member vs. one shared
  Assembled org — affects how representative dogfood load is.
- **Preview gateway channelization:** if gateway↔worker protocol changes
  become frequent, revisit routing `*.preview` per runtime-owner channel
  instead of pinning the gateway to stable.
- **Cross-host login ergonomics:** is one login per host acceptable
  long-term, or is the signed one-time-token SSO handoff (preview-bootstrap
  pattern) worth building? Widening the session cookie is not an option
  (see API Contract).
