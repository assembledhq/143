# Design: Session Branch-Only Publish

> **Status:** Implemented | **Last reviewed:** 2026-05-15

Session publishing supports a branch-only path alongside pull request creation. The branch action reuses the snapshot-backed sandbox push pipeline so file modes, symlinks, binaries, branch guardrails, GitHub auth, and post-push snapshot promotion match the PR path.

The branch-only action has its own session state (`branch_creation_state`, `branch_creation_error`, `branch_url`) instead of overloading `pr_creation_state`. This keeps a branch-only publish from blocking a later `Create PR` action on the same session.

The session detail publish control remains PR-first. The primary button opens a PR, while the adjacent dropdown exposes `Create branch` for users who want to fetch and test the code locally before opening a PR.
