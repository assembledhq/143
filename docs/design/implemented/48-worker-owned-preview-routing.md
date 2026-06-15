# Design: Worker-Owned Preview Routing

> **Status:** Implemented | **Last reviewed:** 2026-06-12

This document records the deployed multi-node preview contract. It is the follow-on to [44-sandbox-preview-server.md](44-sandbox-preview-server.md) and describes how preview traffic and preview lifecycle are split between app nodes and worker nodes.

## Summary

- App nodes keep the public preview edge.
- Worker nodes own preview runtime execution.
- Preview routing is explicit and durable; it does not rely on app-node Docker access or container-IP reachability.

## Responsibilities

### App nodes

- Serve `/api/v1/sessions/{id}/preview/*`
- Mint and validate bootstrap/session preview access
- Resolve the worker that owns a preview or live session container
- Host the wildcard preview gateway for `<preview-id>.preview.<domain>`
- Reverse proxy browser HTTP/WebSocket traffic to the owning worker

### Worker nodes

- Hydrate or reuse sandboxes
- Run `preview.Manager` and provider lifecycle operations
- Own browser-inspector actions and HMR snooping
- Proxy browser traffic from the app gateway into the local preview provider
- Run recycle and cleanup loops for previews they own

## Durable Ownership

- `preview_instances.worker_node_id` identifies the worker that owns an active preview.
- `sessions.worker_node_id` identifies the worker that owns the session's live sandbox/container.
- Live-container preview reuse requires `sessions.worker_node_id`. If a session still has `container_id` but no worker owner, the API fails closed instead of guessing.

## Worker Selection

When `Start Preview` is requested:

1. Reuse `preview_instances.worker_node_id` if an active preview already exists.
2. Otherwise reuse `sessions.worker_node_id` if the session has a live container.
3. Otherwise pick the least-loaded active preview-capable worker, using `CountActivePreviewsByWorker`.

Cold starts reserve a `starting` preview row and enqueue durable startup work to the selected worker. Live-container reuse never retries onto a different worker; if a pinned startup worker is later declared dead before claim, the job target-node fallback allows another worker to claim and reassign the reserved preview.

## Internal Auth

App-to-worker preview RPC uses a dedicated signed preview token. The signing
key is the ordered `PREVIEW_RPC_SECRETS` keyring: the first configured secret
signs new tokens, and every configured secret validates inbound tokens. When
`PREVIEW_RPC_SECRETS` is unset, the process falls back to `SESSION_SECRET` so
older deployments can adopt the keyring without an immediate production secret
change. Normal deploys must not rotate this keyring.

Claims include:

- `org_id`
- `target_node_id`
- `session_id` or `preview_id`
- `action`
- `exp`

Workers reject tokens whose target node, org scope, action, or preview/session binding does not match the request.

Secret rotation is deliberate and ordered:

1. Deploy keyring-capable code while signing with the existing secret.
2. Configure `PREVIEW_RPC_SECRETS=old,new` so all generations validate both
   values while still signing old tokens.
3. After the fleet and old preview drain window are compatible, configure
   `PREVIEW_RPC_SECRETS=new,old` so new tokens use the new secret.
4. Remove `old` only after the maximum worker preview drain window plus token
   TTL has elapsed.

## Public Gateway Flow

1. Browser requests `https://<preview-id>.preview.<domain>`.
2. App-node preview gateway validates preview access.
3. Gateway loads the preview instance, resolves `preview_instances.worker_node_id`, and looks up that worker's `preview_internal_base_url`.
4. Gateway signs a short-lived `proxy` preview token.
5. Gateway reverse proxies to `http(s)://<worker-internal>/internal/preview/{preview_id}/proxy/...`.
6. Worker proxy dials the local provider and streams HTTP/WebSocket traffic to the sandbox.

This keeps the browser edge stable while avoiding any Docker or provider coupling on app nodes.

Worker control-plane auth/routing failures are private implementation details.
The worker marks those responses with `X-143-Preview-Worker-Error`, and the
public gateway translates marked invalid-token/missing-token failures plus
runtime-owner mismatches into `503 PREVIEW_RUNTIME_UNAVAILABLE` or an HTML
restart overlay for document navigations. Unmarked `401`/`403` responses from
the previewed application are preserved.

## Cleanup and Recycle

- Recycle is already worker-scoped via `worker_node_id`.
- Cleanup is also worker-scoped: expired and idle sweeps only act on previews owned by the local worker.
- PR-close and session-reaper teardown use a worker-aware stopper that routes stop requests to the owning worker.

## Node Metadata Contract

Preview-capable workers publish the following metadata through `nodes.metadata`:

- `preview_capable = true`
- `preview_rpc_auth_check = true`
- `preview_internal_base_url = http(s)://<worker-private-host>:8080`
- `build_sha`

`NODE_ID` must be stable in production so ownership written to the database remains routable across deploys and restarts.

Workers only advertise `preview_capable = true` and `preview_rpc_auth_check = true` after boot-time local recovery work is complete and the HTTP listener is bound. Cold-start selection requires both flags plus `preview_internal_base_url`, so a worker cannot receive new preview ownership until its advertised preview RPC endpoint has been verified. This prevents app nodes from routing preview lifecycle calls to a worker that is registered in the cluster but not yet accepting internal preview RPC.

`preview_capable` gates cold-start worker selection. Existing previews and live session sandboxes remain pinned by `worker_node_id`; app nodes may resolve that owner with `preview_internal_base_url` even during the short interval before the worker re-advertises cold-start capability, so deployment readiness races do not orphan already-created preview rows.

App nodes also treat gateway-to-worker reachability as part of the runtime health contract. The preview gateway marks the current active runtime unavailable with `unavailable_reason = endpoint_unreachable` when proxying to the runtime endpoint fails, and an app-side reachability monitor periodically dials active runtime endpoints to catch ready-but-undialable runtimes before a user opens the preview. Runtime loss is epoch-guarded so stale probes cannot tear down a newer replacement runtime. This keeps the durable preview URL restartable while avoiding stale `ready` rows whose worker heartbeat is fresh but whose endpoint cannot be reached from the app tier.

Startup recovery is worker-scoped. A worker only rehydrates sandbox-auth sockets for preview-held sessions whose active `preview_instances.worker_node_id` matches its own `NODE_ID`; it must not scan or rebind sockets for containers owned by peer workers.

## Deployment Contract

- App nodes do not mount Docker and do not run Chrome.
- App-node Caddy terminates both the main app domain and the wildcard preview domain, proxying wildcard preview traffic to the API preview gateway port.
- Worker nodes mount Docker, run the preview-capable server in `MODE=worker`, and run a Chrome sidecar for inspector features.
- Candidate app generations run `worker-deployctl preview-auth-check` before
  app cutover. The command signs an `auth_check` token with the candidate
  process keyring and calls every active/draining worker that advertises
  `preview_rpc_auth_check = true` at `/internal/preview/auth-check`. Any
  rejection fails the app deploy closed.
- Candidate worker generations run the same auth check scoped to the newly
  started worker node before old-worker drain. Worker deploys intentionally do
  not require worker-to-worker reachability across the whole fleet; app deploys
  are the fleet-wide app-to-worker compatibility gate. A candidate worker that
  cannot serve its own advertised internal preview endpoint is rolled back
  before older generations are drained.
- `preview_rpc_auth_check` is an opt-in capability flag so the first rollout
  that introduces the endpoint does not fail against older draining workers
  that cannot serve it yet. After workers have rolled, future deploys enforce
  preview RPC key compatibility against the active/draining preview fleet.

## Known Limitation

This design routes to the worker that already owns a live container or active preview. It does not attempt live-container migration across workers. Snapshot-backed hydrate remains the cross-node recovery mechanism.
