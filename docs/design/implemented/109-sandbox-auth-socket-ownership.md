# Design: Sandbox GitHub-Auth Socket Ownership

> **Status:** Implemented | **Last reviewed:** 2026-07-15

## Implementation status

Implemented. The core finding while building this was that **socket ownership
already lived in the long-lived worker**: the per-turn `session-executor` uses
`sandboxauth.RemoteBrokerClient` to acquire/release a holder lease over HTTP
(`/internal/sandbox-auth/acquire|release`), and the worker's `Broker`/`Server`
bind the actual Unix socket. So the executor never bound a local socket — the
real bug was narrower than this doc first assumed: the socket was pinned to the
**turn holder lease**, so `Broker.releaseLocked` closed it at every turn
boundary, and nothing re-bound it after a worker restart except a node-scoped
rehydrate that missed rolling deploys.

What shipped, therefore, is not "move ownership to the worker" (already true) but:

- **A container lease in the broker** (`EnsureContainerLease` /
  `ReleaseContainerLease`, `brokerEntry.containerPinned`): the socket stays open
  while the container is alive, independent of turn holders. `releaseLocked`
  closes only when no holders remain **and** the container pin is cleared.
- **A worker-side reconciler** (`agent.SandboxAuthSocketReconciler`) grounded in
  local Docker (`provider.ListManagedSandboxes`, which carries session/org id
  labels) rather than `worker_node_id` — so it re-pins the same host containers
  across a rolling deploy that changes the node id. It runs once synchronously at
  startup (before jobs are accepted) and then on a 30s interval, and releases
  pins for containers that have disappeared from the host.
- **Cross-generation safety** (`Broker.ContainerSocketState`,
  `Server.SocketPath`, quiet liveness probe via `io.EOF` in `handleConn`): when
  the reconciler sees a session it has no local listener for but whose socket is
  already live, it **adopts rather than steals** it (a sibling generation is
  draining on the same host) and takes over only once that listener is gone.

The node-scoped DB rehydrate pass (`RehydrateSandboxAuthListeners`,
`SessionStore.ListContainerHoldingSessions`) and the broker's `EnsurePrepared`
were removed — the reconciler supersedes them.

The "Proposed design" below is retained for context; the migration steps are
done except where noted under **Residual gap**.

## Problem

Sandboxed coding agents push to GitHub through a per-session Unix-domain socket.
The host listens on `<socket-dir>/<session-id>/sock`; the sandbox dials it at
`/run/143-auth/sock` via a directory bind-mount, and the host replies with a
fresh GitHub token resolved per request (see
[65-unified-coding-credentials.md](65-unified-coding-credentials.md)
and `internal/services/sandboxauth/`). The per-request resolve is deliberate:
it issues a fresh repository-bound token for each action without exposing
user-to-server GitHub credentials inside the sandbox. Push tokens have
`contents:write`; API tokens have read-only contents and pull-request access.
Neither can create PRs directly; agents request the server-owned workflow with
`143-tools pr create`. See [118-durable-session-publication.md](118-durable-session-publication.md).
Production worker/all configuration requires `SANDBOX_AUTH_SOCKET_DIR`, and
startup fails if the directory preflight fails. The legacy `GITHUB_TOKEN`
environment fallback is retained only for local development and test setups.

The socket has a **lifetime-ownership bug**: it is bound by the **per-turn
`session-executor` process**, but the sandbox container **outlives every
executor**.

Concretely:

- A turn is run by a one-off `session-executor` process (the
  `143-worker-run-*` containers, dispatched by `DurableSessionExecutorDispatcher`).
- That process builds the `sandboxauth.Broker` and binds the socket inside
  `prepareSandboxGitHubAuth` → `Listen` (`orchestrator.go`).
- On exit it runs `SandboxAuthShutdown` → `Broker.Shutdown`, which closes **all**
  its session sockets and unlinks the files (`session_executor_main.go`,
  `broker.go`).
- The container is kept alive across turns by a **turn hold**
  (`turn_holding_container`) or a **preview hold** (`preview_holding_container`).

So between turns — and during any executor restart (rolling deploy drain,
crash, OOM) — there is no process listening on the socket. The agent's next
`git push` dials a live bind-mounted path with nothing behind it and gets:

```
143-tools git-credential: sandboxauth: dial /run/143-auth/sock: connect: connection refused
fatal: could not read Username for 'https://github.com'
```

This is what the `Broker` doc comment already *claims* is impossible — it says
ownership lives "in the long-lived worker process." Reality drifted when turn
execution was split into one-off executors. This doc is the plan to realign
code with that intent.

### Observed incident (2026-06-22)

Session `7290ddb5-…` (turn-held, **no** preview hold) failed a push at 21:09
UTC. Its executor node `g20260622201202` was `node_marked_draining` at 21:04
(mid-turn) during a rolling deploy; the socket died with the drained executor.
The push only succeeded on the user's *next* turn (21:30), which re-ran
`prepareSandboxGitHubAuth`. There were **zero** `rehydrate:` log lines all day
across many restarts because the rehydrate safety net (a) covered only
preview-held containers and (b) was scoped to a deploy-stamped `worker_node_id`
that no longer matched the surviving row.

## Stopgaps already shipped (Option 1)

These reduce the failure rate but do **not** remove the root cause:

1. **In-sandbox connect-retry.** `sandboxauth.Client.dial` retries a refused /
   missing socket for ~30s (`protocol.go`). Turns the common brief re-bind gap
   into a short pause for `git push` instead of a hard failure. Bounded so a
   socket that is never coming back still fails in bounded time. This stays
   useful permanently, regardless of who owns the socket.
2. **Rehydrate covers turn holds.** `ListContainerHoldingSessions`
   (`session_store.go`) now returns running/recovering **turn-held** containers
   in addition to preview-held ones — exactly the set the orphan reconciler
   preserves — so a same-generation process restart re-opens their sockets at
   boot. Still scoped to `worker_node_id`, so it does **not** cover a rolling
   deploy that changes the node id.

The residual gap after Option 1: a **rolling deploy** (new node id) where the
re-bind has to wait for the next turn, and that turn is delayed longer than the
client retry budget.

## Goal

Bind the per-session socket in a process whose lifetime matches the
**container's** lifetime, so the socket is continuously available from container
create/adopt until container reap — independent of how many turn executors come
and go, and transparent across rolling deploys on the same host.

## Proposed design

### 1. The long-lived worker owns the socket

Move socket binding out of the per-turn executor and into the **long-lived
worker** process on the host (the process that already runs the orphan
reconciler and the rehydrate pass at startup).

- The worker opens the listener when a container is **created or adopted** for a
  session on this host, and closes it only when the container is **reaped**
  (the same lifecycle the orphan reconciler already tracks).
- The executor **stops hosting** the socket. It keeps the `Resolve()` call that
  stamps commit-identity env (`user.name` / `user.email` / co-author), but drops
  `Listen` and the `SandboxAuthShutdown`-on-exit. Exiting an executor must never
  unlink a live container's socket.
- The resolver must be wired on the worker side (it already is — rehydrate uses
  `o.identityResolver` via the worker's orchestrator).

Because the credential is resolved fresh per request by whichever process holds
the listener, moving the host end does not change the token semantics.

### 2. Deploy-proof, host-grounded scoping

Stop keying "which containers are mine" on the deploy-stamped `worker_node_id`.
Follow the orphan reconciler's proven pattern: enumerate candidates and gate the
actual bind on a **local `IsAlive` probe** (docker inspect only sees containers
physically on this host). A container on another host fails the probe and is
skipped, so a node never binds a socket it shouldn't — without depending on the
DB's node-id bookkeeping surviving a deploy.

Either: scan all container-holding sessions and `IsAlive`-gate each (simplest,
matches the reconciler; acceptable at current scale), or introduce a
**host-stable identifier** (the host portion of the node id, before the
`-g<deploy-ts>-<sha>` suffix) and scope by that. The host-stable id is the
better long-term choice if global scans become expensive.

### 3. Cross-generation handoff during rolling deploys

The hard case: during a rolling deploy two worker generations run on the **same
host** for a window — the old generation draining in-flight turns, the new
generation booting. Both can see the same container as alive. Binding rules must
prevent them from fighting over one socket file:

- **Do not blindly `os.Remove` + re-bind** a socket another live generation may
  be serving. Before re-binding, probe whether the existing socket already has a
  working listener (dial it); if it answers, adopt it / leave it alone rather
  than stealing the path.
- **A draining generation must not unlink sockets for containers it is handing
  off.** `Broker.Shutdown` on drain should release leases without removing the
  socket file when the container survives — only the reaper (container gone)
  unlinks.
- Make socket ownership a function of **container liveness**, not process
  lifetime, so "who should be serving this path right now" has a single
  unambiguous answer (the newest live worker generation on the host that owns
  the container).

### 4. Keep the bind-mount contract

No change to the directory bind-mount (`SandboxSocketDir`) — that already lets a
freshly bound socket file be picked up by the running container at lookup time.
The whole point is that re-binding at the same path is transparent to the
sandbox; this design just makes the re-binds rarer and never racy.

## Migration / rollout

1. Land Option 1 (done) — buys headroom and de-risks the window.
2. Add worker-owned `Listen` on container create/adopt **alongside** the
   executor's existing `Listen`, behind a flag, with the dial-before-rebind
   guard so the two can coexist during rollout.
3. Flip executors to skip `Listen` (keep `Resolve` for env). Verify via
   `make logs-query` that `listener started/closed` now tracks container
   lifecycle, not turn boundaries.
4. Remove `SandboxAuthShutdown` from the executor shutdown path.
5. Make rehydrate host-grounded (`IsAlive`-gated, deploy-proof) and drop the
   `worker_node_id` scoping once worker-owned binding is the only path.

## Verification

- Drain a worker node mid-turn (simulate a rolling deploy) and confirm an
  in-sandbox `git push` succeeds throughout, with no ECONNREFUSED.
- Confirm `sandboxauth: listener started/closed` events correlate with
  container create/reap, not with turn start/end.
- Confirm two overlapping worker generations on one host never trade ENOENT /
  "address already in use" on the same session socket.

## Residual gap (known, accepted)

The cross-generation guard lives in the **reconciler** (adopt-don't-steal). The
**turn-acquire** path (`Broker.Acquire` → `Server.Listen`) does not yet
dial-check, so it can still steal a socket a sibling generation owns. This is
safe in practice because turns for a session are serialized — a new generation
will not start a turn for session S while an old generation runs S's turn — so
concurrent same-session acquires across generations effectively don't happen.
The one remaining window: if a new-generation turn for S binds S's socket while
the old generation still pins it, the old generation's eventual `Shutdown`
unlinks the now-new-generation socket file, leaving S's socket dead until the
new generation's next reconcile tick (≤30s) — bridged by the in-sandbox client's
connect-retry. Fully closing this would require the turn-acquire path to adopt
rather than steal too (and the broker to re-validate adopted entries). Deferred
as not worth the added cross-process state for a sub-30s, retry-covered window.

## Open questions

- Global `IsAlive` scan vs. host-stable node id — pick based on expected
  concurrent-session count per host.
- Should the socket survive a *full* host reboot (container gone) — no; reaped
  container ⇒ unlink. Confirm tmpfiles.d still recreates the parent dir
  (`reconcile-worker-host.sh`).
- Interaction with `remote_broker_client` / multi-host preview routing — does any
  cross-host caller depend on the current per-executor broker?

## Related

- `internal/services/sandboxauth/` — protocol, broker, server, client.
- `internal/services/agent/sandbox_auth_rehydrate.go` — startup rehydrate pass.
- `internal/services/agent/reconciler.go` — the host-grounded, `IsAlive`-gated
  pattern to mirror.
- `internal/db/session_store.go` — `ListContainerHoldingSessions`,
  `ListOrphanedContainers`.
