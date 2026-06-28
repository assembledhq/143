# Design: Session Changesets and Stacked PRs

> **Status:** Future

## Summary

The product problem is simple: a session can produce a large useful diff, but the
right review shape is often several smaller PRs. 143 should let the user split a
session's work into multiple reviewable branches without losing the session
context, then keep those branches understandable when one branch depends on
another.

A **changeset** is a PR slot inside a session. It is intentionally thin:

- one title and summary
- one branch/worktree
- zero or one PR
- optional parent changeset for stacking
- status, preview, readiness, and PR health for that branch

Changesets are not a replacement for Git, Graphite, or a patch-management system.
They are 143's control-plane record for normal Git branches that belong to the
same session.

## Goals

- Preserve today's simple one-session, one-PR path.
- Let one session own multiple publishable branches/PRs.
- Support stacked changesets, where PR B is based on PR A.
- Let users and agents explicitly target a changeset before editing.
- Let agents split a large diff into smaller branches using normal coding
  workflows.
- Keep restack behavior understandable when a lower branch changes or merges.
- Keep publishing, previews, readiness, PR health, audit, and issue links on the
  platform path instead of raw `gh pr create`.

## Non-Goals

- Do not build a full Graphite, Phabricator, merge queue, or stack landing
  system in the first version.
- Do not model rich patch identity, atom groups, or every hunk as first-class
  durable state unless later experience proves it is needed.
- Do not require agents to understand an elaborate changeset protocol.
- Do not expose branch choreography as the primary user model.
- Do not make manual branch pushes or raw GitHub CLI PR creation the platform
  contract.

## Product Model

The product model has three separate concepts:

| Concept | Meaning |
| --- | --- |
| Session | Shared context, transcript, sandbox, rollup diff, previews, and publishing state. |
| Thread/tab | Conversation lane inside a session. |
| Changeset | Publishable branch/PR slot inside a session. |

All threads in a session can see the changeset list. A thread can work on the
whole session or a specific changeset, but mutating actions must have an explicit
target.

### Branch Terms

Use separate names for the two branch relationships a stacked PR needs:

| Field | Meaning |
| --- | --- |
| `target_branch` | Ultimate merge target for the stack, usually `main`. |
| `base_branch` | Direct GitHub PR base for this changeset. For a stacked changeset, this is the parent changeset branch. |
| `working_branch` | The branch owned by this changeset. |

For example:

```text
Changeset 1: Foundation        target main  base main             working 143/foundation
Changeset 2: API integration   target main  base 143/foundation   working 143/api
Changeset 3: UI wiring         target main  base 143/api          working 143/ui
```

### Stacking

The only required graph edge is:

| Relation | Meaning |
| --- | --- |
| `stacked_on` | This changeset's branch is based on another changeset branch. |

This forms a forest: each changeset has at most one parent and may have many
children.

Use a stack when a branch needs an earlier branch to build, test, preview, or
make semantic sense:

```text
Changeset 1: Foundation        branch 143/foundation  base main
Changeset 2: API integration   branch 143/api         base 143/foundation
Changeset 3: UI wiring         branch 143/ui          base 143/api
```

Independent branches can still exist in one session when they share planning
context, but stacked branches are the main design center.

## Required Invariants

143 should own only the durable safety rails that make multi-PR sessions usable.
Agents should own the code changes, split proposals, conflict resolution, and
explanations.

Required invariants:

- **Explicit target before mutation.** A mutating turn, push, preview, or
  readiness run targets the whole session, one changeset, or a stack prefix/top.
- **One branch head expectation per push.** Before 143 pushes a changeset branch,
  the remote head must still match the head 143 expected to update. If it does
  not, 143 imports or asks before overwriting.
- **Lower changeset edits mark descendants stale.** When a stacked changeset
  changes, descendants become `needs_restack`.
- **Parent merges have a clear next action.** When a parent PR merges,
  descendants need retarget/rebase help.
- **Stack health is visible.** The UI summarizes whether the stack is coherent,
  stale, blocked, externally changed, partially merged, or ready.
- **Split source stays stable until accepted.** While breaking a large diff into
  changesets, the original session diff remains available as the split source so
  the agent and user can see what has and has not been assigned.
- **One mutating runtime per changeset worktree.** Read-only inspection can be
  concurrent, but edits, restack, and push operations need a changeset worktree
  lease.

These invariants are enough to make stacked PRs trustworthy without forcing the
platform to model every patch detail.

## Agent Interaction

Changesets should be available to coding agents through three simple channels.

### Prompt Context

When a session has changesets, agent prompts include a compact stack summary:

```text
Session changesets:
1. Foundation        PR #101  branch 143/foundation  base main             state open
2. API integration   PR #102  branch 143/api         base 143/foundation   state needs_restack
3. UI wiring         draft    branch 143/ui          base 143/api          state planned

Current target: Changeset 2 - API integration
Allowed mutation: target changeset worktree
Affected descendants: Changeset 3
```

The prompt gives enough context for the agent to reason about the stack without
needing to call tools for every step.

### Worktree

The primary agent interface remains the filesystem. When the composer target is
a changeset, the runtime starts in that changeset's worktree. The agent edits
files normally.

The platform maps:

```text
composer target -> changeset -> branch/worktree -> agent cwd
```

The agent should not need to manually create worktrees, checkout stack branches,
or infer which PR it is editing.

### `143-tools changesets`

Use `143-tools` as the lightweight control plane for changeset state and platform
actions. Agents can inspect state and request audited backend actions, while 143
keeps auth, audit, PR templates, readiness, previews, and issue links intact.

Initial command shape:

```bash
143-tools changesets list
143-tools changesets current
143-tools changesets status
143-tools changesets diff --changeset <id>
143-tools changesets create --title "API integration" --stacked-on <id>
143-tools changesets materialize --changeset <id>
143-tools changesets split-status
143-tools changesets publish --changeset <id>
143-tools changesets publish-stack
143-tools changesets restack --from <id>
143-tools changesets import-remote --changeset <id>
```

The agent may propose splits and invoke these tools, but durable state changes
go through 143 APIs behind the CLI. Direct `gh pr create` is not the product
path.

`split-status` compares the original session diff with the combined changeset
diffs. It should show unassigned files/hunks, duplicate/conflicting changes, and
whether the current split appears complete. It is a guide for agents and users,
not a durable hunk-ownership database.

## Core Flows

### One PR

The default path remains unchanged. Every session has one implicit primary
changeset. If no second changeset exists, the UI behaves like today's one-PR
session.

```text
session -> primary changeset -> branch -> optional PR
```

### Split A Large Diff

1. The user asks to split the session into smaller PRs.
2. The agent reviews the session diff and proposes a small set of changesets:
   title, summary, stack order, and rough file/hunk ownership.
3. The UI shows the proposed stack in the PR details surface.
4. The original session worktree remains the **split source**. It keeps the full
   draft diff available until the user accepts the split or publishes.
5. 143 materializes changeset branches/worktrees in stack order. Each changeset
   worktree starts from its direct `base_branch`.
6. The agent copies or applies the relevant parts of the split source diff into
   each changeset worktree using normal Git and filesystem operations.
7. `split-status` compares the original session diff with the combined
   changeset diffs and shows unassigned or duplicate/conflicting changes.
8. 143 runs readiness/build checks per branch or stack prefix.
9. The user publishes one changeset or the whole stack.

The split does not need a durable hunk database in v1. The agent can use Git
diffs, worktrees, split-status, and user feedback to make clean branches. If this
later proves too fragile, richer patch ownership can be added as an
implementation detail.

### Split Status

During splitting, users and agents need confidence that the large original diff
has been accounted for. 143 should provide a simple split progress view:

```text
Split progress: 82% assigned
Unassigned:
- src/auth/session.ts
- package-lock.json

Duplicate or conflicting:
- src/api/types.ts appears in Foundation and API integration
```

The implementation can compute this from Git diffs instead of storing per-hunk
ownership. The goal is practical reviewability:

- show what remains unassigned
- show files or hunks that appear in more than one changeset
- show changes intentionally omitted after user confirmation
- block "Publish stack" by default while unassigned changes remain
- allow explicit publish with omissions when the user confirms the split source
  includes discarded or unrelated work

### Edit A Lower PR

1. The user selects a lower changeset, such as `Foundation`.
2. The composer target becomes that changeset.
3. 143 leases the changeset worktree and starts the agent there.
4. Before push, 143 checks that the remote branch head still matches the expected
   head.
5. After push, descendants are marked `needs_restack`.
6. The user can ask 143 to restack descendants.
7. Clean restacks can push automatically. Conflict-resolution restacks can use an
   agent. Semantic descendant changes require confirmation.

This is the main reason changesets need to exist as product state rather than
only ad hoc branches.

### Restack Descendants

Given:

```text
PR 1: A -> main
PR 2: B -> A
PR 3: C -> B
```

If `A` changes, then `B` and `C` become stale. 143 should restack from the first
stale descendant downward.

Allowed automatic actions:

- mark descendants as `needs_restack`
- cleanly replay descendants
- refresh readiness, preview, and PR health
- push restacked branches when the expected remote head still matches

Allowed agent actions:

- resolve merge/rebase conflicts needed to preserve descendant intent
- update imports, generated files, call sites, and formatting caused by the lower
  change
- explain non-trivial conflict resolutions

Actions requiring confirmation:

- changing descendant product intent
- dropping descendant behavior to avoid a conflict
- folding descendant work into a lower PR
- changing stack topology
- overwriting an unexpected remote branch head

### Worktree Leases

Each materialized changeset has one worktree inside the session sandbox. Mutating
operations require a lease on that worktree:

- agent turns that edit the changeset
- restack jobs that rewrite the branch
- push/publish jobs that snapshot and push the branch
- preview/readiness jobs that need a stable branch snapshot

Only one mutating lease may exist for a changeset worktree at a time. Read-only
diff, status, and PR views may attach concurrently. Restacking or pushing a
descendant is blocked while another thread is actively editing that descendant.

The lease is a coordination primitive, not a user-facing workflow. Users should
see plain language such as "API integration is being edited in Tab 2" or
"Restack is waiting for UI wiring edits to finish."

### Parent Merge

When a parent PR merges:

1. Mark descendants as `needs_restack`.
2. Retarget the immediate child PR to the merged parent's base branch, usually
   trunk.
3. Rebuild the child's branch so the merged parent commits disappear from the
   child diff while the child's own changes remain.
4. Repeat down the stack.
5. Refresh PR metadata, previews, readiness, and stack health.

If GitHub state conflicts with 143's stored branch expectations, pause and
reconcile rather than guessing.

## UI

For one-PR sessions, keep today's UI. Hide changeset-specific chrome until a
second changeset exists.

For multi-PR sessions, the PR details view becomes the place to inspect the
changeset stack.

```text
Pull requests
Repository: assembledhq/143        Target: main

Stack health: 2 descendants need restack after #101 changed

+------------------------------+---------------------------------------------+
| Changesets                   | Selected: #102 API integration              |
|                              | Base: 143/foundation  Head: 143/api         |
| 1  #101 Foundation   open    | State: open            CI: passing          |
| 2  #102 API          active  | Review: changes       Tests: failing       |
| 3  #103 UI           stale   | Affected descendants: #103                  |
|                              |                                             |
|                              | [Fix tests] [Address review] [Merge]        |
+------------------------------+---------------------------------------------+
```

Rows should show: stack position, title, PR/draft state, base branch, branch
head, readiness/CI, preview state, stale/restack status, unpushed changes, and
active thread ownership.

The selected changeset's detail panel should preserve the same action set as the
current pull request details card: create PR when unpublished, fix tests, address
review, push changes, merge when eligible, and any existing repair/readiness
actions. It should not replace those with generic navigation actions like open
PR, preview, diff, or restack.

Selecting a row scopes the rest of the session details view:

- **Create PR** creates or updates the PR for the selected changeset. In a
  one-PR session, this is the primary changeset and behaves exactly as today.
- **Preview** opens the selected changeset preview by default. If the selected
  changeset is stacked, the preview target should be explicit: selected
  changeset, stack through selected changeset, or stack top.
- **Changes/Diff** filters to the selected changeset's owned files/hunks. A user
  can still switch back to the whole session or stack diff.
- **Readiness/Fix tests** runs against the selected changeset branch head.
- **Merge** applies to the selected changeset PR and uses the repository's normal
  GitHub merge rules.
- **Restack** remains a stack/session-level action, surfaced in stack health or
  affected-descendant panels rather than replacing the PR details card actions.

Selecting a row does not silently retarget the composer. The `Ask agent` action
opens the composer with an explicit target chip for the selected changeset.

During splitting, the same surface should show split progress so users can trust
that the original large diff has been accounted for:

```text
Split progress: 82% assigned
Unassigned: 2 files
Duplicate/conflicting: 1 file
[View split status] [Ask agent to finish split]
```

## Preview And Readiness

Preview targets:

- one changeset branch
- stack prefix through a selected changeset
- stack top

The UI must make the target explicit: "Preview PR 2," "Preview stack through PR
2," or "Preview stack top."

Readiness is branch/scoped evidence:

- independent changeset: check that branch against trunk
- stacked changeset: check the stack prefix through that changeset

Published GitHub CI remains authoritative after PR creation. 143 readiness is
preflight evidence and reviewer handoff, not a replacement for repository CI.

## State Model

Important changeset states:

- `planned`
- `materializing`
- `published_branch`
- `pr_open`
- `needs_restack`
- `restacking`
- `restack_conflict`
- `external_update_detected`
- `ready`
- `merged`
- `abandoned`

Stack-level state should summarize the graph:

- one-pr
- draft-stack
- published
- coherent
- needs-restack
- restacking
- blocked
- external-update-detected
- partially-merged
- merged

## Data Model

Phase 1 should be additive and preserve today's one-PR behavior.

### `session_changesets`

Minimum columns:

- `id`
- `org_id`
- `session_id`
- `is_primary`
- `order_index`
- `title`
- `summary`
- `status`
- `target_branch`
- `base_branch`
- `working_branch`
- `stacked_on_changeset_id`
- `head_sha`
- `expected_remote_head_sha`
- `base_head_sha`
- `created_at`
- `updated_at`

Every existing session gets one primary changeset during backfill. That primary
changeset adopts the session's current branch and owns the default one-PR path.

### `pull_requests`

Add nullable `changeset_id` while keeping `session_id` for back-compat and
session-level rollups. PR creation idempotency moves from per-session to
per-changeset.

### Existing Session Branch Fields

Keep `sessions.working_branch` and `sessions.target_branch` during migration as
mirrors of the primary changeset. Do not remove them in the same migration that
introduces changesets.

## API

Add changeset APIs without breaking current PR routes:

- `GET /sessions/{id}/changesets`
- `POST /sessions/{id}/changesets`
- `PATCH /sessions/{id}/changesets/{changeset_id}`
- `POST /sessions/{id}/changesets/{changeset_id}/materialize`
- `GET /sessions/{id}/changesets/split-status`
- `POST /sessions/{id}/changesets/{changeset_id}/publish`
- `POST /sessions/{id}/changesets/publish-stack`
- `POST /sessions/{id}/changesets/{changeset_id}/restack-descendants`
- `POST /sessions/{id}/changesets/{changeset_id}/import-remote`

Keep `GET /sessions/{id}/pr` and `POST /sessions/{id}/pr` for compatibility.
They default to the primary changeset.

Session detail DTOs should add `changesets: []ChangesetSummary` while preserving
the existing scalar PR summary for one-PR UI compatibility.

## Rollout

Break implementation into four handoff-sized phases. Each phase should preserve
the existing one-PR session behavior.

### Phase 1: Primary Changeset Substrate

Goal: introduce changesets without changing the visible product for normal
sessions.

Tasks:

- Add `session_changesets` with org scoping, session FK, primary flag, order,
  branch fields, stack parent field, status, and timestamps.
- Add nullable `pull_requests.changeset_id`; keep `pull_requests.session_id`.
- Backfill one primary changeset per existing session and attach existing PRs to
  it.
- Keep `sessions.working_branch` and `sessions.target_branch` as mirrors of the
  primary changeset.
- Move PR creation idempotency from session-scoped to changeset-scoped while
  making existing `POST /sessions/{id}/pr` default to the primary changeset.
- Add tests for backfill, per-org filtering, primary uniqueness, and "one-PR
  session behaves as today."

### Phase 2: Changeset APIs And Read-Only UI

Goal: let the app represent multiple changesets, but avoid branch splitting and
restack behavior.

Tasks:

- Add `GET /sessions/{id}/changesets`, `POST /sessions/{id}/changesets`, and
  `PATCH /sessions/{id}/changesets/{changeset_id}`.
- Add `changesets: []ChangesetSummary` to session detail responses while keeping
  the scalar PR summary for compatibility.
- Update batch PR hydration so multiple PRs per session do not collapse into one.
- Update the PR details view to show the changeset list only when `N > 1`.
- Preserve the current PR details card actions in the selected changeset panel:
  create PR, fix tests, address review, push changes, and merge.
- Scope Create PR, Preview, Changes, readiness, and Merge to the selected
  changeset.
- Add frontend/API tests for one changeset, multiple changesets, and selected
  changeset scoping.

### Phase 3: Split Proposal And Materialization

Goal: support "split this large diff" before publishing multiple PRs.

Tasks:

- Store the original session diff as the split source.
- Track per-changeset owned paths/hunks enough to detect unassigned and duplicate
  ownership.
- Add split-status APIs for assigned, unassigned, duplicate, and conflicting
  changes.
- Add one worktree per materialized changeset inside the session sandbox.
- Add materialization for stacked changesets in topology order.
- Run readiness/build checks against the selected changeset branch head.
- Add split proposal UI with file/hunk movement, fold, split, reorder, verify,
  and publish-green actions.
- Add tests for split status, materialization failure, and readiness scoping.

### Phase 4: Multi-PR Publish, Targeted Edits, And Restack

Goal: publish and maintain multiple PRs from one session.

Tasks:

- Add `POST /sessions/{id}/changesets/{changeset_id}/publish` and
  `POST /sessions/{id}/changesets/publish-stack`.
- Publish stacked PRs in topology order with correct base branches and PR body
  stack context.
- Add composer targeting for whole session, one changeset, changeset plus
  descendants, and stack.
- Mark descendants `needs_restack` when a lower changeset changes or merges.
- Add clean restack with expected remote-head checks before push.
- Add agent-assisted conflict resolution for restacks that preserve descendant
  intent.
- Add restack delta UI showing clean replay, mechanical fallout, or semantic
  changes.
- Add parent-merge handling: retarget child PRs, rebuild descendants, refresh
  previews/readiness, and pause on unexpected GitHub state.
- Add tests for stacked publish, selected changeset edits, stale descendants,
  safe push checks, parent merge handling, and semantic-edit confirmation.

## Open Tensions

- **Independent sibling PRs.** They can be changesets when shared session context
  matters, but separate sessions remain better for truly independent work.
- **Split quality.** The first version relies on agents, Git diffs, user review,
  and readiness/build failures to make good splits. Rich patch ownership can
  wait.
- **How much to automate restack.** Clean mechanical restacks should be easy.
  Semantic changes should stay explicit.
