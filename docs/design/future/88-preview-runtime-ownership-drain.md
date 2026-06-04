# Preview Runtime Ownership and Drain Semantics

> Status: In progress | Last reviewed: 2026-05-26

Preview URLs are durable product artifacts, but the process that can serve a
preview is a live worker attachment. The control plane therefore separates:

- Preview target: durable branch/session/config identity.
- Preview instance: durable lifecycle row for one visible preview URL.
- Preview runtime: live leased worker ownership for a specific sandbox/server
  attachment.

The v1 model pins a preview runtime to one worker. It does not live-migrate a
running preview. Runtime rows carry a monotonically increasing
`runtime_epoch`, so future migration can add an explicit handoff flow without
changing URL identity.

## Invariants

- Preview routing is authoritative from `preview_runtimes.endpoint_url`, not
  from mutable node metadata.
- The active runtime statuses are `starting`, `ready`, and `draining`.
- `preview_instances.worker_node_id`, `preview_handle`, and `port` remain
  denormalized compatibility fields; `preview_runtimes` owns routing.
- Gateway preview tokens include target node ID, runtime ID, and runtime epoch.
  Worker preview proxy/control-plane authorization validates all three.
- A draining worker keeps serving owned runtimes, but worker selection excludes
  draining nodes from new preview cold starts.
- If the owner is lost or the runtime lease expires, the preview instance moves
  to `unavailable`. Access sessions are not revoked so the gateway can still
  present a restartable unavailable surface.
- Worker-only authorization errors such as `WRONG_PREVIEW_WORKER` must not leak
  to users. The gateway translates owner mismatch or missing runtime into
  `503 PREVIEW_RUNTIME_UNAVAILABLE`.

## Lifecycle

Starting a preview creates a `preview_runtimes` row before launch with status
`starting`, the selected worker node, endpoint URL, epoch, and lease expiry.
After the worker persists the provider handle, it marks the runtime `ready` and
mirrors handle, port, and worker fields onto `preview_instances`.

Stopping a preview marks the active runtime `stopped`, marks the preview
`stopped`, cascades child service/infrastructure state, and revokes preview
access sessions.

Worker drain marks the node draining, marks owned active runtimes `draining`,
and continues runtime heartbeats while serving existing preview traffic. Before
process exit the worker waits up to `WORKER_PREVIEW_DRAIN_TIMEOUT`. If active
owned runtimes remain after the timeout, reconciliation marks them `lost` and
their preview instances `unavailable` before the endpoint can be reused.

Recycle/restart should create a new runtime epoch and stop or lose the previous
active epoch atomically. The schema supports this now; the first implementation
uses the same runtime row when the recycle stays on the existing owner and will
move to explicit epoch creation as the recycle path is split from same-worker
handle refresh.

## Deploy Safety

Worker blue/green deploys can only advertise an endpoint when both conditions
are true:

- Docker is not already listening on the candidate host port.
- No active `preview_runtimes.endpoint_url` equals
  `http://<worker_private_ip>:<port>`.

Routine CI worker deploys explicitly configure a small blue/green port range so
old worker generations keep serving owned previews while the replacement
generation starts on another reachable port. When no extra blue/green port range
is configured, the deploy blocks on old worker drain. After Docker releases the
old worker's host port, the deploy may reuse that same endpoint while stale
runtime leases expire; previews owned by the stopped generation are unavailable
and restartable.
