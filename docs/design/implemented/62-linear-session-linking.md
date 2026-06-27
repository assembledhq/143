# 62 - Linear Session Linking and Bidirectional Updates

> **Status:** Implemented | **Last reviewed:** 2026-05-06
>
> **Depends on:** [./04-ingestion.md](./04-ingestion.md), [./08-pr-and-ship.md](./08-pr-and-ship.md), [../future/59-session-issue-decoupling-and-multi-issue-linking.md](../future/59-session-issue-decoupling-and-multi-issue-linking.md)

## Summary

Make Linear a first-class, two-way collaborator on a 143 session.

When a user creates a session and references a Linear issue (by URL or by identifier like `ACS-1234`), the system should:

1. Detect that reference, attach the issue via `session_issue_links`, and surface it in the session detail view as a clickable, status-aware card.
2. Allow the linked Linear issue to be the only starting input for a session and use its context to start the coding agent run.
3. Post a stable Linear **attachment** on the issue page (with a single live comment for activity-feed visibility) idempotently across re-detections and turn boundaries.
4. Move the primary linked Linear issue forward through workflow states as the session progresses, with conservative guards and a per-org/per-team/per-session opt-out.
5. Prefix the GitHub PR title with the primary Linear identifier so Linear's native GitHub integration claims the PR automatically.

Contract: **143 sessions own their own lifecycle. Linear reflects session events; Linear changes never affect 143 sessions.** This must be visible in product copy, not buried in the design.

## Problem

The Linear ingestion adapter, GraphQL client, and `session_issue_links` join model already exist. None of them are wired together for the session-create path. Today, pasting `https://linear.app/acs/issue/ACS-1234` into the composer leaves the URL as opaque prompt text:

- The system does not recognize it. Linear is unaware work has started.
- The session detail view shows nothing about Linear.
- The resulting PR has no Linear prefix and is not auto-linked back in Linear.

Result: people in Linear cannot tell that an agent is working on the issue, and the agent gets no structured issue context beyond whatever the user pasted manually.

## Goals

- Accept Linear references as full URL, bare identifier, or existing `linear`-source `issues` row.
- Allow a session to start from a linked Linear issue alone, with no additional user prompt required.
- Resolve one unambiguous primary Linear issue and snapshot its context before turn 1 starts, without requiring user confirmation.
- Render linked issues in the session detail view with state, priority, assignee, and a Linear deep link.
- Pull a bounded Linear context package (description, comments, attachments/links, and key metadata) into the agent's starting context.
- Post a stable Linear attachment + single rolling comment that idempotently reflect session state.
- Move the primary Linear issue forward (link → in-progress, PR open → in-review, PR merged → done) under explicit guards.
- Prefix PR titles `[KEY-N] ` to claim Linear's native GitHub integration.
- Preserve the zero-issue session fast path: if Linear is not enabled or no reference is detected, nothing changes.

## Non-Goals

- Replacing Linear's native GitHub integration. We coexist with it and explicitly defer "close on merge" when it is configured.
- Auto-canceling 143 sessions when their linked Linear issue is closed. Reverse-direction sync is read-only.
- Creating new Linear issues from inside a session.
- Multi-workspace Linear (one org → multiple Linear workspaces).
- Backfilling past sessions with Linear attachments.

## Prerequisites already in place

| Piece | Where |
|---|---|
| Linear OAuth + `IntegrationStore` | `internal/api/handlers/integrations.go` |
| Linear GraphQL client (`GetTask`, `UpdateTask`, `commentCreate`) | `internal/services/integration/linear.go` |
| Linear webhook ingestion → `issues` rows with `source = linear` | `internal/services/ingestion/linear.go` |
| `session_issue_links` join (primary/related, position, repo invariant) | per design 59 |
| Async worker registry | `internal/worker/handlers.go` |
| Session detail enrichment of `LinkedIssues` | `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx` |
| PR title pipeline (120-char clamp, regen on title resync) | `internal/services/github/pr.go` |

This design is wiring, not a new subsystem.

## High-level UX

**Path A — paste a URL or identifier into the composer.** Session creation returns quickly, but turn 1 does not start until one unambiguous primary Linear ref has been resolved and its context snapshot captured. Additional refs are linked asynchronously as related. Linear gets an attachment + "agent started" comment shortly after.

**Path B — references picker.** If the user attaches an existing `linear`-source `issues` row through the `@` references mention, the link is created in the session-create transaction. The async detector skips it.

Picker-added Linear refs must stay visible in the transcript. Session/thread chat should render a capitalized `Linear` tag plus the concrete issue identifier (for example `ACS-1234`) for user messages that include detached Linear references, rather than hiding that state in side metadata or a generic `linear issue` label.

**Path C — Linear is not enabled.** Detection is a no-op. No errors, no warnings.

A small "Linked: paste a Linear URL or `ACS-1234`" affordance below the composer makes the feature discoverable. As detection runs, it populates with chips.

### Pre-start preparation step

To keep the startup contract clear, separate "session created" from "agent turn 1 started":

- the create request may return before all Linear work is done
- the session enters a short-lived preparing/starting state
- primary Linear issue resolution and context snapshotting happen in that pre-start step
- turn 1 starts only after the primary Linear context package is available

This is stricter than "link it later if we can". If the user linked a primary Linear issue, the first agent turn should either have that issue's details or not start yet.

### Issue-only session start

If the user links exactly one Linear issue and provides no other prompt text, that is still a valid manual session. The linked issue becomes the starting brief for the agent.

In this mode:

- the session title defaults to the Linear issue title unless the user set one explicitly
- the agent starts from the fetched Linear context package, not an empty prompt
- the UI should make it clear that the run is "Starting from Linear issue `ACS-1234`"

This is a first-class path, not an edge case. Users should be able to kick off work from a Linear issue without rewriting the ticket into a second prompt.

If the user does provide additional prompt text, that text is preserved and passed along too. The starting brief for the agent is:

1. the user's explicit prompt text, if any
2. the structured Linear context package for the linked primary issue

In other words, linking a Linear issue never replaces user intent; it enriches it. The main product requirement is that when a user pastes or links a Linear issue, the coding agent has access to that issue's details without the user needing to retype them.

### Multi-issue policy

Multi-issue sessions are allowed, but they are intentionally asymmetric:

- a session may link multiple Linear issues
- it must still have exactly one **primary** issue
- any additional issues are **related** issues

All linked Linear issues contribute context to the agent. The primary issue still owns lifecycle behavior: it drives branch hinting, Linear write-backs, and workflow-state automation. Related issues appear in the UI and are fully fetched into the session context package, but they do not get automatic comments, attachments, or state transitions in v1.

This is the right tradeoff for the expected usage pattern: it supports the real "one fix, several related tickets" case without turning one session into a symmetric multi-issue workflow engine.

### Composer controls must express distinct semantics

This feature introduces two distinct intents:

- **Private session**: use Linear as local context only. No attachment, no comment, no state transition, and no other write is sent to Linear.
- **Don't auto-update Linear**: still create the durable attachment + single live comment, but suppress workflow-state transitions. This is for teams that want visibility in Linear without letting 143 move board state.

These controls should be adjacent in the composer and use product copy that makes the difference obvious:

- `Private: don't post this session to Linear`
- `Visible in Linear, but don't move issue status`

Default remains "visible + state automation on", but the distinction must be explicit in product and settings copy.

## Detection

### Inputs scanned

A bounded, deterministic set: turn-0 message body, user-provided session title, titles of `references` of type `text`/`link`, and any user-set branch name. We do not scan attachment OCR, file contents, or agent output.

If the user supplies no free-form prompt but does provide a Linear link via pasted URL/identifier or references picker, detection still runs and may fully determine the session's starting context.

### Patterns

1. **Linear URL**: `https?://linear\.app/(?P<workspace>[^/]+)/issue/(?P<key>[A-Z][A-Z0-9_]{0,9}-[0-9]+)(?:/[^\s)]*)?` — high-confidence; workspace slug must match the org's connected workspace.
2. **Bare identifier**: `\b(?P<key>[A-Z][A-Z0-9_]{0,9}-[0-9]+)\b` — only treated as a candidate if its prefix matches a **known team key** for the org's Linear workspace. Team keys are fetched on OAuth install, cached, and refreshed every 24h. This avoids false positives like Jira keys, AWS resource IDs, and internal codes.

### Pre-start resolution vs async follow-up

- **Pre-start resolution**: resolve the full linked Linear issue set needed for turn 1, including exactly one primary and any additional related issues, and fetch the context package for each. This may happen inline in `CreateManual` when fast, or in a short-lived pre-start preparation step immediately after session creation. Either way, turn 1 waits for it.
- **Async follow-up (`link_linear_issue`)**: handles additional related refs, ambiguous/non-critical detection work, re-detection, and any write-back that does not need to block turn 1. Job key is `(session_id, source_inputs_hash)` for idempotency. Re-runs are no-ops.

This resolves design 59's "primary frozen at first turn" rule without weakening the product promise. If multiple Linear issues are linked at session start, turn 1 should have the details for all of them; only later re-detection and non-critical follow-up are deferred.

### Workspace verification

Workspace slug from URLs must match the connected workspace. Bare identifiers are confirmed via `GetTask`. Issues from other workspaces are dropped silently.

### Repository invariant carve-out (with revalidation)

Linear issues frequently have no repo association. Holding design 59's strict rule would block most adoption. We allow null-repo Linear issues to link, with two safeguards:

- on a Linear webhook reporting that a linked issue's repo association *changed*, re-validate the link. Mismatch flips it to a `stale` UI state (visible on the linked-issue card with a one-click "remove or repair" affordance).
- the link write logs the carve-out with an audit reason so we can quantify how often it triggers.

A mismatched explicit repo (issue says repo B, session is repo A) is still rejected at link time. Keep design 59's strict invariant as the default store behavior; the null-repo carve-out lives only in the Linear linker, which must emit an audit reason when it uses it.

### Ordering

If multiple refs are detected, the *first* by position becomes primary; the rest become related in source order. If primary was already set elsewhere (issue-triggered session, references picker), detection only adds related — it never overrides.

## Linking model

The async worker reuses existing `issues` rows via a shared `upsertLinearIssue(orgID, fetched)` helper used by both detection and webhook ingestion. It does `INSERT ... ON CONFLICT DO UPDATE`, prefers the more complete payload (webhook over `GetTask`), and breaks ties with a freshness timestamp. This removes the webhook-vs-detection race.

### Linear session-linking should be one owned service boundary

This work spans detection, issue upsert, attachment/comment writes, state transitions, issue-history reads, and team-key caching. It should live behind one coherent service boundary rather than being spread across handlers, workers, and ad hoc GraphQL calls.

Recommended ownership:

- **Session-create handler** decides only "resolve inline or enqueue pre-start preparation".
- **Linear session-linking service** owns detection resolution, `upsertLinearIssue`, provider-state reads/writes, attachment/comment/state mutations, and coexistence checks.
- **Webhook ingestion** remains the source of fresh external issue state, but calls the same shared upsert helper as the linker.

Without this boundary, the feature will drift into multiple interpretations of "what is linked?", "what did we already post?", and "when do we suppress updates?".

### Provider state should not pollute `session_issue_links`

The original draft proposed adding Linear-specific columns (`external_attachment_id`, `prior_state_id`) to `session_issue_links`. That table is provider-agnostic and Jira/Asana/etc. would each grow their own columns. Instead, introduce a side table:

```sql
CREATE TABLE session_issue_link_provider_state (
    link_id     uuid PRIMARY KEY REFERENCES session_issue_links(id) ON DELETE CASCADE,
    provider    text NOT NULL,
    state       jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
```

Linear keys live inside `state`: `attachment_id`, `comment_id` (the single live comment — see below), `prior_state_id`, `last_known_state`, `team_id`. This keeps the join clean and gives every future tracker a uniform home.

## Posting back to Linear

The user explicitly asked: *what's the right ID field to use so we can update Linear with a link to the running session?* Linear gives us four practical surfaces:

| Surface | Verdict |
|---|---|
| **Attachment** (`attachmentCreate` / `attachmentUpdate`) | **Primary write surface.** Designed for "this URL is related to this issue", server-side deduped on `(issue, url)`, carries a `metadata` JSON blob. |
| **Comment** (`commentCreate` / `commentUpdate`) | **Single live comment**, updated in place across milestones. |
| **Issue description footer** | Rejected. Hostile to user content, races with concurrent edits. |
| **Custom fields** | Rejected for v1. Cleanest "structured pointer" but requires every workspace to opt in and create the field. |

### The attachment is the durable handle

`attachmentCreate` returns a Linear-side `id` we persist as `provider_state.attachment_id`. That is the durable handle; the attachment URL is the human-visible deep link. `metadata` stores the back-reference.

The attachment metadata schema is **stable and documented from day one** (we want PMs to be able to build Linear custom views like "issues with a 143 attachment whose outcome = merged"):

```json
{
  "service": "143",
  "session_id": "<uuid>",
  "primary": true,
  "outcome": "running" | "pr_open" | "merged" | "ended_no_pr" | "failed"
}
```

Attachment title/subtitle update across the lifecycle:

| Event | Title | Subtitle |
|---|---|---|
| Linked | `143: <session title>` | `Running` |
| PR opened | same | `PR <#nnn> open` |
| PR merged | same | `PR <#nnn> merged` |
| Ended without PR | same | `Ended without PR` |
| Failed | same | `Failed` |

### One live comment, not three

Posting separate comments for "started", "PR opened", and "PR merged" would create adoption-breaking notification volume. Instead, create one comment on link-create, persist its ID, and update it in place via `commentUpdate` for milestones. This also dedupes worker replays: if `provider_state.comment_id` is set, update; otherwise create.

The comment body is mandatory-prefixed `🤖 143 automated update` so readers can immediately distinguish bot voice from human voice (relevant because the comment is authored by the integration installer's account; see below).

### Authoring identity

Linear OAuth integrations on most plans do not expose a true bot identity, so posts appear under the installer's account. That is an adoption blocker, not a polish item. **Bot identity / OAuth Actor support is a hard prerequisite for phase 2.** Until resolved, the install flow shows an explicit advisory and lets the org admin designate a service account.

## Session detail view

`LinkedIssueCard` renders identifier chip, title (deep link to `https://linear.app/<workspace>/issue/<KEY-N>`), state name + color, priority badge, assignee avatar, source badge, and role badge (primary / related). Primary renders first; related follow per design 59 ordering.

- For related issues, the subtitle says "Related context — primary work tracked in `<primary KEY>`" so the primary/related asymmetry is visible, not surprising.
- For issues marked duplicate-of in Linear, render the duplicate-of relationship inline with a "View canonical: ACS-2000" link. Posts back still target the user-typed identifier.
- For issues from Linear teams the requesting user lacks access to, render a redacted card showing identifier + "you don't have access to this Linear team" only. (Org integration tokens have wider visibility than per-user tokens; we filter at render time.)
- An SSE event `session.links.changed` is published on every link insert/remove/promote so newly-linked issues appear without page reload.

We do not embed Linear in iframes. Tab handoff is what users expect.

### Realtime contract and fallback behavior

`session.links.changed` should be a first-class API contract, not just a UI enhancement. It should identify the session and the kind of link change (`inserted`, `removed`, `promoted`, `stale`, `refreshed`). If SSE is missed, the session-detail query remains the source of truth; SSE is an invalidation/update hint, not the only correctness mechanism.

### Operator-facing debug affordance

This feature has many legitimate reasons to do nothing: cross-workspace drops, unknown team keys, private mode, per-team disable, per-session disable, coexistence suppression, recent human edits, debounce, and rate-limit retries. Operators need a compact "why did/didn't Linear update?" surface on the session detail page or audit log drawer.

Minimum useful fields:

- detection source (`url`, `identifier`, `reference_picker`, `mid_session_proposal`)
- current provider state summary (`attachment present`, `comment present`, `last synced at`)
- latest write/skip decision and `skipped_reason`
- whether the session is private or state-sync-disabled

Ship this no later than the first phase that writes back to Linear; otherwise support and dogfooding will be guesswork.

## PR title and branch naming

### Title prefix

In `internal/services/github/pr.go`, prepend one `[<IDENTIFIER>] ` prefix for each linked Linear issue before the cleaned title, ordered primary first and then related issues by session-link order. Truncate the body, never the prefixes.

Edge cases:

- **Conventional commit prefix already present** (`feat:`, `fix(scope):`): insert the Linear prefixes *after* the conventional prefix → `feat: [ACS-1234] [ACS-1277] Add OAuth callback handler`.
- **Some identifiers already present** in the user-set title: do not double-prefix them.
- **Title resync** at turns 6/9/...: strip any leading Linear prefixes before re-prefixing from the current linked-issue set.
- **No linked Linear issues**: no prefix.

The reviewer-facing phrasing remains the body of the title. The Linear identifiers are routing prefixes, not the semantic title. `[KEY-N] ` keeps the issue link machine-readable without making the title feel tracker-generated.

### Branch naming hint

The session-create handler writes a `linear_identifier_hint` into `session_linear_context` when the fast path resolves a primary Linear ref. The agent's branch-naming logic reads this hint from hydrated session metadata to bias toward `<git_user>/<lower(identifier)>-<short-slug>`, which Linear's GitHub integration recognizes independently of the PR title.

## Bidirectional state sync

State sync is **on by default** for the primary linked Linear issue. If Linear is meant to be part of the workflow, it must reflect what is actually happening.

### 143 → Linear (write)

| 143 event | Linear transition |
|---|---|
| Link / first turn starts | first `started`-type state |
| PR opened | `started`-type state matching review-name preferences ("In Review", "Code Review", "PR Open"); else current |
| PR merged | first `completed`-type state, **suppressed if Linear's GitHub integration is configured for this repo** (avoid double-transition and double cycle/sprint membership writes) |
| Ended without PR / failed / canceled | no transition |

We use Linear workflow state **types** (`triage`, `backlog`, `unstarted`, `started`, `completed`, `canceled`) as the abstraction layer because workspaces customize state names. State-name preference list is configurable per-team.

### Guards (all must hold)

1. **Forward-only**: never move backwards. Already past target → no-op. Already in `completed` or `canceled` → no-op.
2. **Skip if user-touched recently**: if a human moved the issue within the last 10 minutes, skip. Read from Linear's issue history.
3. **Capture prior state**: persist `provider_state.prior_state_id` before each transition.
4. **Fire-once**: `(session_id, issue_id, event_kind)` is unique in `session_issue_link_state_events` (append-only). Replays are no-ops.
5. **Per-team override** (default inherit org default): some teams use Linear as a strict daily Kanban with manual moves; the per-team toggle lets them disable automation entirely.
6. **Per-session override**: "Don't auto-update Linear" toggle in the composer.
7. **Coexistence**: if Linear's GitHub integration has posted attachments to this issue, suppress our merge-time writes (state, cycle membership, anything).
8. **Primary only**: per design 59, related links never participate in lifecycle automation.
9. **Debounce**: if two transitions would fire within 30 seconds (e.g. fast PR open after start), debounce to the latter.

### Linear → 143 (read-only)

Webhook events on linked issues update the cached `LinkedIssueCard`. Banners only fire when the closure/cancellation is **recent (≤30 min) AND the session is currently running AND the change was performed by a human (not automation)**. Older or automated closures show as a subtle pill, not a banner. We never auto-cancel a session.

### Per-org configuration UI

Settings → Integrations → Linear gains a "Workflow state automation" panel with org-level defaults (all on by default), per-team overrides, and a preview ("We will move ACS-1234 from `Backlog` → `In Progress`"). Per-team override is loud in setup so misalignment surfaces early.

This settings surface should also explain the two write levels explicitly:

- **Post session links to Linear**: controls attachment + rolling comment behavior.
- **Move Linear workflow states automatically**: controls state transitions only.

These toggles map to different social/operational tradeoffs. Many teams will want the first before they are ready for the second.

### Schema additions

```sql
CREATE TABLE session_issue_link_state_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    session_id      uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_id        uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    event_kind      text NOT NULL,
    transition_from text,
    transition_to   text,
    skipped_reason  text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, issue_id, event_kind)
);
```

`skipped_reason` (`already_past_target`, `user_recent_edit`, `linear_github_integration_active`, `disabled_by_user`, `debounced`) is the audit trail for "why didn't Linear update?".

## Re-detection (propose mode)

Mid-session pasted Linear references are often casual citation, not declarations of linkage. Re-detection therefore runs in **propose mode**: a chip "Detected ACS-1240. Link as related?" appears in the session detail view, with one-click confirm and dismiss. Nothing is written to Linear or to `session_issue_links` until the user confirms.

This intentionally differs from session-create-time detection (auto-link) because intent is clear at create time and ambiguous later.

The primary link, once set, never changes (design 59). Re-detection can only propose related.

## Private session mode

Some uses want Linear context without team visibility — early experimentation, sensitive issues, exploratory repro work. The composer exposes a per-session "Private" toggle:

- Linking happens locally (issue fetched, attached, fed into prompt, rendered in detail view).
- No attachment, no comment, no state transition is written to Linear.
- The session detail shows a "Private session: Linear updates suppressed" pill.

The toggle is set at create time and frozen for the session's life, avoiding a confusing "post the missed events now" backfill. Org admins can change the default in Settings.

Private mode is intentionally stronger than "Don't auto-update Linear". Private means **no external trace in Linear at all**. "Don't auto-update Linear" still leaves a visible attachment/comment trail and only suppresses workflow automation.

## Agent context contract

The promise here is not only "Linear sees the session"; it is also "the agent gets structured issue context without the user hand-formatting it." That means the linked-issue prompt block must carry more than title + description for Linear issues.

If the user wrote additional free-form instructions, those remain part of the session input. Linear context is additive: it gives the agent the linked issue's details alongside the user's instructions, not instead of them.

For each linked Linear issue, the session bootstrap context should include:

- identifier
- title
- description
- current state name and state type
- priority
- assignee name
- team key/name
- duplicate-of target when present
- canonical Linear URL
- attachment metadata and URLs

It should also include a bounded slice of recent comments, newest-first or most-relevant-first, with a conservative cap per issue. The goal is to capture implementation-relevant discussion, not to dump the whole ticket thread.

Prompt rendering should preserve the primary/related distinction, but all linked issues should be present. The primary issue gets first position and the fullest treatment; related issues follow in deterministic order.

### Attachment handling

For Linear attachments, v1 should pull in:

- attachment title when present
- attachment URL
- attachment source/type metadata when present

These attachments should be passed to the agent as structured context references. If the attachment target is directly fetchable as supported text content through an existing integration or safe first-party fetch path, we may include a bounded excerpt. Otherwise, we include metadata + URL and let the agent decide whether to inspect it later.

Default to metadata-first, not "dump the whole ticket thread into the prompt", but the linked issue descriptions, recent comments, and attachment references together should be enough to start a run even when the user added no extra prompt text.

This context must come from the snapshotted linked-issue payload captured for the turn, not live reads during prompt rendering.

## Failure modes, idempotency, rate limits

| Failure | Behavior |
|---|---|
| Linear API down during primary pre-start fetch | Do not start turn 1 blind. Keep the session in a recoverable preparing/failed-to-load-context state and let the user retry. |
| Linear API down during secondary linking or write-back | Retry with backoff. Session continues. Link/write catches up on recovery. |
| Linear OAuth expired | Integration health check (every 6h) flags it. `doGraphQL` triggers refresh on 401. Banner in Settings. |
| Cross-workspace ref | Drop silently. |
| Rate limit (429) | `RetryableError` honoring `Retry-After`. Per-integration token bucket (~1,200 req/min) shapes traffic. |
| Concurrent attachment updates | Per-link write coalescing with `(session_id, issue_id)` job key and 5s coalesce window — latest event wins, intermediates dropped. |
| Comment replays after worker restart | `provider_state.comment_id` already set → use `commentUpdate`, not `commentCreate`. Belt-and-suspenders dedupe via `session_issue_link_state_events` insert before posting. |
| Linker partial failure | Local link insert and Linear-side post are decoupled. Retried independently. |
| Linked issue closed in Linear | Banner (under guards). Session unaffected. |
| Linked issue's Linear repo changes | Re-validate link; flip to `stale` if mismatched. |

## Observability and rollout metrics

This feature needs explicit instrumentation because many correct outcomes look like no-op behavior to users.

Track at minimum:

- inline pre-start success rate vs queued pre-start preparation rate
- pre-start preparation latency and failure rate
- link removal / repair rate (proxy for bad matches or stale repo drift)
- attachment/comment/state-write success rate and latency
- write suppression counts by `skipped_reason`
- Linear 401 / 429 frequency
- notification-volume proxy: comments created vs comments updated

Review these metrics during dogfooding before enabling default state automation broadly. If teams mostly keep attachment/comment writes but disable state moves, phase 4 defaults likely need adjustment.

## Open questions and follow-ons

- **Linear → 143 launcher** (a Linear command-bar action that opens the 143 composer pre-populated with an issue) is plausibly the highest-leverage flow in this whole space — engineers and PMs already live in Linear. Promoted to a phase-5 follow-on with its own design.
- **Multi-Linear-workspace per org**: design `provider_state.workspace_id` nullability with that future in mind even though v1 assumes single-workspace.
- **Cross-tracker dashboard / "AI-completed issues" Linear custom view templates**: cheap value-add once attachment metadata is stable.
- **MCP exposure of `linear.get_issue`**: lets the agent pull additional Linear context on demand instead of pre-fetching.
- **Aggregate "linked sessions" badge** when one Linear issue accumulates multiple sessions over its lifetime.
- **Two-tier attachment retention**: most-recent active session is the "live" attachment; older sessions get down-leveled subtitle (`ended`) and configurable 30-day retention.
- **Bot identity** investigation (E11/P13) — must conclude before phase 2.

## Migration / Rollout

| Phase | Scope |
|---|---|
| **1. Local linking only** | primary Linear pre-start resolution + context snapshotting; `link_linear_issue` for secondary linking; team-key cache; `LinkedIssueCard`; SSE link-change events; composer affordance. No Linear writes. |
| **2. Linear writes** | `attachmentCreate`/`Update`/`Delete`; single live comment with `commentUpdate`; `session_issue_link_provider_state` table; rate-limit token bucket; per-link coalescing; bot identity resolution. |
| **3. PR title prefix and branch hint** | Title prepend with conventional-commit handling; `linear_identifier_hint` consumed by agent branch naming. |
| **4. Bidirectional state sync** | `session_issue_link_state_events`; transitions with all guards; coexistence detection; banners on Linear → 143; settings panel with per-team overrides; per-session "don't auto-update" toggle. |
| **5. Polish & reverse direction** | Linear → 143 launcher (separate design); "refresh from Linear"; admin "why was this attached?" debug; Linear custom-view templates. |

Each phase ships independently. Phase 4 is gated on phase 2 dogfooding outcomes; if attachment + single-comment volume is too noisy, resize that before adding state transitions.

## Risks

| Risk | Mitigation |
|---|---|
| Misdetection links wrong issue | Team-key cache prunes false positives; `GetTask` confirms; explicit "remove link" UI; audit log on every link write. |
| Notification fatigue in Linear | Single live comment (not per-event); transitions debounced 30s; per-team disable; per-session disable; private mode. |
| Linear rate limits cascade to session-create | Detection runs post-commit (or fast-path with strict budget); per-integration token bucket; retryable backoff. |
| Authoring identity awkwardness | Bot identity prereq for phase 2; mandatory `🤖 143 automated update` prefix in copy; advisory in install flow. |
| Linear's GitHub integration double-writes | Coexistence guard suppresses our merge-time writes when their attachments present. |
| Repo invariant carve-out drift | Lazy webhook-driven re-validation; `stale` UI state with one-click repair. |
| Provider-specific schema bloat | All Linear-only fields live in `session_issue_link_provider_state.state` jsonb. |
| 143 reliability coupled to Linear availability | All Linear writes are retryable worker jobs; detection is non-fatal; chaos test exercises this in CI. |
| Stale Linear state in 143 UI | Webhook-driven freshness; "synced N minutes ago" hint; refresh affordance. |
| Privacy leak via integration token | Redacted card for inaccessible teams; metadata contains no prompt content; comments contain stable header + URL only. |

## Testing requirements

End-to-end:

1. URL in prompt → primary link; bare identifier with known team key → primary link; bare identifier with unknown prefix → no link.
2. Two refs in prompt → first primary, second related.
3. Linear not enabled → silent no-op.
4. Cross-workspace URL → dropped silently.
5. Primary Linear issue resolution completes before turn 1 starts; the agent never begins without the primary issue context it was promised.
6. Token expired (401) → integration health flagged; primary pre-start fetch or later link/write succeeds after refresh.
7. Rate-limited (429) during non-critical write/link work → retried; link/write eventually succeeds.
8. Linear unavailable during primary pre-start fetch → session stays recoverable and does not start blind.
9. Re-running linker job is a no-op (idempotent).
10. PR title for a multi-issue Linear session includes one `[KEY-N] ` prefix for each linked Linear issue, ordered primary first and then related issues.
11. Title resync does not double-prefix; conventional-commit prefix is preserved with all Linear prefixes inserted after.
12. Attachment is server-side deduped — running linker twice yields one attachment.
13. Single comment created on link, updated in place at PR open and PR merge — never a second comment.
14. Concurrent attachment updates coalesce to the latest event.
15. State transition guards — forward-only, user-recent-edit skip, fire-once, debounce, coexistence-with-GitHub-integration suppression.
16. Linked issue closed in Linear while session running → banner only when recent + human + running.
17. Repo invariant: explicit-mismatch link rejected; null-repo link allowed; webhook re-validation flips stale state.
18. Re-detection mid-session is propose-mode only (no auto-link, no Linear writes).
19. Private session mode suppresses all Linear writes but populates locally.
20. Provider-state side table holds Linear fields; `session_issue_links` schema is provider-agnostic.
21. SSE `session.links.changed` updates the detail view without reload.
22. **Chaos test**: take Linear adapter offline for 30 minutes during a busy session period — sessions complete, PRs open, Linear catches up after recovery, no double-posts.
23. "Private" vs "Don't auto-update Linear" produce distinct behavior: private writes nothing; no-auto-update still posts attachment/comment but suppresses state transitions.
24. Null-repo carve-out is limited to Linear-specific linking code; generic session-issue linking still rejects null-repo issues.
25. Linked-issue prompt rendering includes the bounded Linear metadata contract for every linked Linear issue and uses snapshotted turn data, not live reads.
26. Operator debug surface shows latest detection source, provider-state summary, and last skip/write reason for a linked Linear issue.
27. A session can start from exactly one linked Linear issue and no free-form prompt text; the run still starts successfully.
28. The issue-only start path seeds the session from Linear description, bounded recent comments, and attachment references.
29. When the user provides both a Linear issue and extra prompt text, the agent receives both the user text and the structured Linear context package.
30. When multiple Linear issues are linked, exactly one is primary, all linked issues are fetched into the starting context, and only the primary gets automatic write-backs and workflow-state automation.

## Recommendation

Use **attachments as the primary Linear write surface**, **one live comment** updated in place for activity-feed visibility, and **forward-only state sync on by default** with explicit per-org/per-team/per-session opt-outs and coexistence guards against Linear's native GitHub integration.

Do the work in two paths: primary Linear resolution + context snapshotting before turn 1, and async follow-up for secondary linking and write-backs. Keep `session_issue_links` provider-agnostic by routing all Linear-specific persisted state through `session_issue_link_provider_state.state`. Resolve bot identity before any Linear writes ship.

The contract is one-way for forward progress (143 → Linear writes; Linear → 143 reads), and 143 sessions own their own lifecycle. That must be visible in product copy, not just here.
