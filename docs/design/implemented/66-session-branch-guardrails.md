# Session Branch Guardrails

> Status: Implemented
> Last reviewed: 2026-06-19

## Summary

Agent sessions now treat the session working branch as a first-class runtime invariant instead of a best-effort convenience.

The worker creates one canonical branch name for the session, injects that branch into sandbox bootstrap, and reuses the same name later during snapshot-backed PR creation. The sandbox bootstrap also installs a `pre-push` hook that rejects pushes from or to any branch other than the designated session branch.

## Product behavior

- Fresh repo-backed sessions create a dedicated working branch before the agent starts editing.
- The session persists that branch name on `sessions.working_branch`.
- Fresh-clone continue-session recovery recreates and checks out the same working branch instead of falling back to the base branch.
- Snapshot-backed PR creation prefers the persisted `working_branch` instead of inventing a second branch name.
- In-sandbox `git push` is guarded:
  - current local branch must equal the designated session branch
  - destination remote ref must equal `refs/heads/<designated-branch>`

## Runtime notes

- `git-bootstrap` now sets `push.autoSetupRemote=true` so a first plain `git push` from the designated branch naturally creates the matching upstream.
- The push guard lives in the repo’s `pre-push` hook, so it applies to ordinary agent-driven Git pushes as well as resumed sessions after bootstrap reruns.
- The guard is intentionally branch-specific rather than token-specific: it reduces accidental pushes to the wrong branch without changing GitHub auth behavior.
- Snapshot-backed PR/branch publish paths also guard server-side force pushes: before `--force-with-lease`, the push sandbox fetches the current remote session branch and refuses to overwrite it when the remote tree is not represented in the checkpoint. Metadata-only retry rewrites remain allowed when the remote tree equals the local tree.

## Why this shape

Before this change, the orchestrator created a local working branch, but PR creation later generated a separate remote branch name and sandbox bootstrap did not enforce branch affinity. That left too much room for drift and made “pushed to the wrong branch” failures plausible.

The implemented shape makes the safe path the default:

- one canonical branch name per session
- one persisted source of truth
- one sandbox-level guard against accidental branch drift
