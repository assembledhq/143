# Design: Durable Preview Start

> **Status:** Implemented | **Last reviewed:** 2026-05-12

Preview startup is durable worker work, not a long-running app-to-worker HTTP request. This prevents routine app deploys, browser disconnects, or API write-deadline behavior from cancelling sandbox hydrate, build, or readiness probes after the user clicks `Start Preview`.

## Contract

`POST /api/v1/sessions/{id}/preview` validates the session and user, selects a preview-capable worker, reserves a visible `preview_instances` row in `starting`, enqueues a `start_preview` job on the `preview` queue, and returns `202 Accepted` with the reserved preview. The reservation and enqueue happen in one transaction. The job uses dedupe key `start_preview:<session_id>` and `jobs.target_node_id` for worker affinity.

The worker job owns the slow path: sandbox reuse or hydrate, workspace `.143/config.json` detection when the client did not send explicit config, preview provider launch, service readiness, and final transition to `ready`, `partially_ready`, or `failed`.

## Failure Visibility

`preview_instances` is the durable operation record. Startup failures mark the reserved row `failed` with the same classified diagnostic text used by synchronous launch errors, and the job is dead-lettered rather than retried for user-actionable failures such as missing config, unavailable snapshots, sandbox races, image pull failures, init script failures, or readiness timeouts.

`GET /api/v1/sessions/{id}/preview` returns the latest failed preview when no active preview exists. This keeps async startup diagnostics visible in the preview panel while still allowing a new retry to reserve a fresh `starting` preview because failed rows are not active.

Start jobs are at-least-once work. A worker can be replaced after it reserves the preview and creates child `preview_services` or `preview_infrastructure` rows, but before it persists a provider handle or marks the preview failed. Retrying the same job must therefore re-enter launch without tripping child-row uniqueness constraints; service and infrastructure child rows are upserted and reset to their starting/provisioning state for the same `(preview_instance_id, name)`.

## Routing

Only initial startup moved to jobs. Stop, restart, bootstrap, proxy, console, screenshot, inspector, interaction, assertion, and visual-diff routes remain synchronous and worker-routed because they operate on an already-owned active preview.

If a pinned target worker is declared dead before the job is claimed, the existing jobs target-node fallback allows another worker to claim it. The start runner then reassigns the reserved preview row to the claiming worker before hydrate/launch.
