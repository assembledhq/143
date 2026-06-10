# Design: Follow-up Workspace-Backed Mentions

> **Status:** Implemented | **Last reviewed:** 2026-05-12

## Summary

The continue-session composer now resolves `@` mentions against the current
session workspace instead of a cached remote repository tree. Follow-up
mentions use a session-scoped workspace path index backed by Redis and an
in-process hot cache, with lazy rebuilds in the API and proactive warmup from
the worker after successful snapshot-producing turns.

The new-session composer at `/sessions/new` still uses repository-backed
mention search. The product split is intentional:

- new-session mentions answer "what exists in this repository/branch?"
- continue-session mentions answer "what exists in this session right now?"

## What shipped

### Backend API

- Added `GET /api/v1/sessions/{id}/composer/files?q=...` for follow-up mention search.
- Kept the existing `GET /api/v1/session-composer/files` endpoint for repo-backed flows.
- Preserved the existing response shape:
  `models.ListResponse[models.SessionInputReference]`

### Workspace source selection

Follow-up mention search now resolves the session workspace using the same
source-selection contract as session file browsing:

1. live container when a sandbox is attached
2. session snapshot when there is no live sandbox but a usable `snapshot_key` exists
3. explicit `NO_SANDBOX` / workspace-unavailable failure when neither exists

The shared resolver lives in `internal/api/handlers/session_workspace.go` so
file browsing and mention search cannot drift.

### Session mention index

- Added a path-only recursive workspace index in `internal/services/workspace`.
- The index stores files and directories, skips `.git`, and caps both entry
  count and serialized size.
- Live Docker-backed index builds use a recursive listing fast path instead of
  one Docker exec per directory, and skip common dependency/build output
  directories such as `node_modules`, `.next`, `dist`, `build`, `target`, and
  `vendor`.
- Ranking is shared with the existing mention picker semantics:
  - path prefix
  - basename prefix
  - basename contains
  - shorter path
  - lexical tie-break

### Cache architecture

- Added a two-tier mention-index cache:
  - Redis for cross-node sharing
  - in-process LRU for hot lookups
- Redis entries are keyed by session + workspace source fingerprint:
  - snapshot-backed: snapshot key
  - live fallback: container id + turn/workspace-generation metadata
- Live cache keys intentionally ignore `last_activity_at` so status/message
  churn does not force a full workspace re-index; `workspace_generation` is the
  invalidator for filesystem-changing events.

### Proactive warmup

The worker now pre-warms the mention index after successful snapshot-producing
turns and graceful-stop checkpoints by building the index from the live
sandbox and storing it under the resulting snapshot-backed cache key.

This reduces first-query latency on the common "agent finished, user types
another follow-up immediately" path.

### Frontend

- The continue-session composer now queries
  `/api/v1/sessions/{id}/composer/files`
- `/sessions/new` and other repo-backed surfaces remain on the original
  repository mention endpoint
- Picker UI, chips, keyboard behavior, and insertion semantics are unchanged

## Key files

- `internal/api/handlers/session_composer.go`
- `internal/api/handlers/session_workspace.go`
- `internal/api/handlers/session_files.go`
- `internal/services/workspace/mention_index.go`
- `internal/services/workspace/mention_index_cache.go`
- `internal/services/agent/orchestrator.go`
- `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx`
- `frontend/src/lib/api.ts`
- `frontend/src/lib/query-keys.ts`

## Verification

Backend:

- `go vet ./...`
- `go build ./...`
- `go test ./...`

Frontend:

- `npm run typecheck`
- `npm run lint`
- `npm run build`
- targeted Vitest run for the continue-session composer mention path
