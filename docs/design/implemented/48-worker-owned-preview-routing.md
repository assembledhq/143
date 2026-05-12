# Design: Worker-Owned Preview Routing

> **Status:** Implemented | **Last reviewed:** 2026-04-29

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

Cold starts may retry another worker when the selected worker returns preview-capacity exhaustion. Live-container reuse never retries onto a different worker.

## Internal Auth

App-to-worker preview RPC uses a dedicated signed preview token. Claims include:

- `org_id`
- `target_node_id`
- `session_id` or `preview_id`
- `action`
- `exp`

Workers reject tokens whose target node, org scope, action, or preview/session binding does not match the request.

## Public Gateway Flow

1. Browser requests `https://<preview-id>.preview.<domain>`.
2. App-node preview gateway validates preview access.
3. Gateway loads the preview instance, resolves `preview_instances.worker_node_id`, and looks up that worker's `preview_internal_base_url`.
4. Gateway signs a short-lived `proxy` preview token.
5. Gateway reverse proxies to `http(s)://<worker-internal>/internal/preview/{preview_id}/proxy/...`.
6. Worker proxy dials the local provider and streams HTTP/WebSocket traffic to the sandbox.

This keeps the browser edge stable while avoiding any Docker or provider coupling on app nodes.

## Cleanup and Recycle

- Recycle is already worker-scoped via `worker_node_id`.
- Cleanup is also worker-scoped: expired and idle sweeps only act on previews owned by the local worker.
- PR-close and session-reaper teardown use a worker-aware stopper that routes stop requests to the owning worker.

## Node Metadata Contract

Preview-capable workers publish the following metadata through `nodes.metadata`:

- `preview_capable = true`
- `preview_internal_base_url = http(s)://<worker-private-host>:8080`
- `build_sha`

`NODE_ID` must be stable in production so ownership written to the database remains routable across deploys and restarts.

Workers only advertise `preview_capable = true` after boot-time local recovery work is complete and the HTTP listener is bound. This prevents app nodes from routing preview lifecycle calls to a worker that is registered in the cluster but not yet accepting internal preview RPC.

`preview_capable` gates cold-start worker selection. Existing previews and live session sandboxes remain pinned by `worker_node_id`; app nodes may resolve that owner with `preview_internal_base_url` even during the short interval before the worker re-advertises cold-start capability, so deployment readiness races do not orphan already-created preview rows.

Startup recovery is worker-scoped. A worker only rehydrates sandbox-auth sockets for preview-held sessions whose active `preview_instances.worker_node_id` matches its own `NODE_ID`; it must not scan or rebind sockets for containers owned by peer workers.

## Deployment Contract

- App nodes do not mount Docker and do not run Chrome.
- App-node Caddy terminates both the main app domain and the wildcard preview domain, proxying wildcard preview traffic to the API preview gateway port.
- Worker nodes mount Docker, run the preview-capable server in `MODE=worker`, and run a Chrome sidecar for inspector features.

## Known Limitation

This design routes to the worker that already owns a live container or active preview. It does not attempt live-container migration across workers. Snapshot-backed hydrate remains the cross-node recovery mechanism.
