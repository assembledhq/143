# Design: 143.dev

> **Status:** Partially Implemented | **Last reviewed:** 2026-05-27

[143.dev](http://143.dev) is an open-source platform that turns customer pain and production errors into safe code fixes that ship automatically.

It’s an open-source platform that connects customer pain directly to code fixes.

The public homepage positions 143 as shared coding-agent infrastructure for engineering teams: one team-visible workspace for context, integrations, cloud agent execution, previews, review loops, audit logs, and usage analytics. The landing page uses a small shared type scale for product copy so hero, section, feature, body, button, and footer text stay visually consistent. See [implemented/81-homepage-positioning-refresh.md](implemented/81-homepage-positioning-refresh.md).

The public website should add first-party docs at `/docs`, backed by curated repo-authored Markdown/MDX under `docs/public` and implemented inside the existing Next.js frontend with Fumadocs. The docs surface should publish guides, self-hosting material, and references without exposing internal design/production-debugging docs, and should include AI-native affordances such as raw Markdown access and `/llms.txt`. Detailed design: [future/85-public-docs-fumadocs/README.md](future/85-public-docs-fumadocs/README.md).

The system aggregates issues from support, Sentry, and Linear, prioritizes them by real business impact, runs a coding agent to generate a fix, opens a PR, and measures the customer impact after deploy. Repository-native CI/CD remains the source of truth for build, test, and deploy validation.

# Overall flow

## Identity and organization context

- The current product is single-organization per user, but the intended long-term identity model is **one user identity, many organization memberships**. A user represents the human account (email, GitHub ID, Google ID); membership represents access to an organization and carries the user's role for that org.
- Organization memberships now support a fourth assignable role, `builder`, alongside `admin`, `member`, and `viewer`. The stored `member` role is user-facing as `Engineer` to distinguish it from generic team membership and the narrower `Builder` role. `Builder` is more constrained than `member`: it can use build/session workflows and personal coding-agent setup, but it does not inherit the broader member repo/settings/evals/project/PR-shipping surface until explicit builder guardrails exist.
- Builder PR creation is guarded by org-level builder permissions. By default, builders must complete a clean first-party Review loop on the session before creating a PR; admins can disable that requirement under General settings when an org wants builders to publish without the extra review gate.
- All product data remains scoped to exactly one `org_id`. Multi-organization support should change how the active org is resolved for a request, not introduce cross-org views by default.
- GitHub App installations are platform access grants, not 143 ownership boundaries. Multiple 143 organizations may link to the same GitHub installation, but each GitHub repository has one active owning 143 organization at a time; other org-scoped rows for that repo must remain disconnected/history-only until an explicit transfer occurs. Detailed design: [future/78-exclusive-github-repo-ownership.md](future/78-exclusive-github-repo-ownership.md).
- The detailed future design lives in [future/50-multi-organization-membership.md](future/50-multi-organization-membership.md). The key product guardrail is that single-org users should see no new UI or onboarding complexity.
- Invitation acceptance is **token-driven and immediate**. Opening an invite while already signed in should claim it right away and switch the tab's active org to the invited org; unauthenticated invite flows should carry a prominent "invitation pending" callout with org and target identity so the auth screen never looks like a generic login.
- Invitations have an explicit acceptance method: `email`, `github`, or `either`. GitHub-locked invitations may still store a durable email address for notification and future contact workflows, but only the matching GitHub OAuth identity can accept them; the stored email is not proof of identity for that invite.
- Explicit organization switches should persist across logins. The user-visible selection is still tab-local while browsing, but the system should remember the user's last explicitly selected org as the default seed for the next fresh login so multi-org users land back where they left off.
- Audit log entries are expected to be self-describing. Every emitted audit event should include structured `details` with operator-useful context such as resource names, source/provenance, runtime choices, job IDs, related IDs, counts, and before/after changes. Audit details must not copy secrets, full document contents, large diffs, or access tokens; use booleans, lengths, hashes, masked summaries, and IDs instead.
- Audit log browsing should behave like a stable event feed rather than page-replacement pagination. The primary interaction is newest-first cursor pagination with inline `Load older` append behavior, optional time-range narrowing, and a non-jarring "new events" banner when fresh activity arrives while the operator is inspecting older history. Detailed design: [future/67-audit-log-feed-pagination.md](future/67-audit-log-feed-pagination.md).
- Toast notifications should behave like platform-owned status surfaces rather than skinned third-party banners: success toasts are compact and auto-dismissing by default, error toasts align with the shared recoverable-error visual language, and dismiss controls only appear when needed in the top-right instead of the main content flow. Detailed design: [implemented/55-toast-notifications.md](implemented/55-toast-notifications.md).
- Free-form form fields that can receive focus on phones, especially multi-line composers, automation inputs, settings textareas, search bars, command-palette search fields, and filter controls, must compute to at least `16px` on small screens to prevent iOS Safari auto-zoom while preserving denser desktop typography. This guarantee belongs in shared UI primitives such as `Input`, `Textarea`, `SelectTrigger`, and `CommandInput` so app-level search and filter surfaces inherit it even when callers request compact desktop text. Automation select-style controls should match the same mobile/desktop text sizing and height as neighboring text inputs.
- Precompiled third-party Tailwind stylesheets must load before the app's Tailwind entry so app responsive utilities can override generic utility classes from vendor CSS.
- The production frontend image should build Next.js from the repo-shaped `frontend` directory and run the repo-scoped standalone server at `frontend/server.js`, with `.next/static` and `public` staged under the same `frontend` runtime path. This mirrors Next's standalone output when file tracing spans repo-level assets such as public docs.
- Frontend typing paths are a first-class product constraint. Session composers, search fields, and other continuous-input surfaces should keep keystroke state local, isolate heavier sibling UI behind memoized boundaries, and carry regression tests for render churn when adjacent controls are data-heavy or polled.
- Mobile session detail should collapse dashboard header chrome, session header chrome, and thread/tab chrome into a single compact top bar focused on the active conversation. The persistent secondary action on that bar should be the existing details-panel affordance for `Overview`/`Changes`/`Validation`/`Preview`, while thread navigation and other low-frequency session actions move behind the right-side menu. In that menu, creating an agent tab belongs inside the Tabs section so it reads as adding a peer conversation lane rather than modifying the current session. Search, new-session creation, and broad session navigation remain on the sessions list rather than inside an open session. Detailed design: [implemented/70-mobile-session-top-bar-consolidation.md](implemented/70-mobile-session-top-bar-consolidation.md).
- Compact desktop session detail should sit between full desktop and phone layouts: from tablet/split-screen widths up to wide desktop, the global app sidebar becomes a slim icon rail, the sessions list expands from the session-switcher rail into a full-height list pane instead of a floating menu, and the active conversation stays visible to the right when there is enough width.
- The authenticated app shell should be pinned to the dynamic viewport and own scrolling inside explicit content panes. Mobile scroll containers, especially session panes and transcripts, should contain overscroll so pull gestures at a pane boundary do not chain to the browser/page and expose unused background space.
- Browser tabs should identify both product and context with `143 | <page>`. Static routes resolve titles from a central frontend registry, detail routes fall back to generic entity names, and loaded entity pages can override the generic fallback with the specific entity title, such as a session title. Unknown future routes derive a readable fallback from the path until they are added to the registry. Detailed design: [implemented/85-browser-page-titles.md](implemented/85-browser-page-titles.md).
- Session follow-up messages should render optimistically in the transcript as soon as the user sends them. Persistence, sandbox resume, and downstream agent/backend work happen after that local echo; the UI must not wait for the POST round-trip before showing the user's own message.
- Session detail should promote thread navigation immediately when a second thread is being created. On desktop, the quiet single-thread header must expand into the visible tab row as soon as tab creation starts, and the add-thread affordance should read as a compact inline control rather than a dominant button.
- Existing session detail opens at the most relevant continuation point instead of the top: saved per-user/per-thread position wins, active threads land at the live edge, inactive threads anchor to the latest assistant turn, and multi-thread sessions restore the last active thread before transcript content is shown. Thread messages also expose a cursor-friendly latest-window API with anchor metadata for bottom-first loading; see [implemented/56-session-open-position.md](implemented/56-session-open-position.md).
- Session detail transcript loading must stay bounded for long-running sessions: thread views open on a latest message window and fetch log rows only for the loaded message turns, with older transcript content loaded explicitly as the user scrolls back. Detailed design note: [implemented/86-session-transcript-log-windowing.md](implemented/86-session-transcript-log-windowing.md).
- Session detail SSE is a live wake-up path, not the sole source of truth. The client reconciles session detail on stream open/reopen, polls while sessions or threads are in volatile working states, and status/done SSE payloads include current thread state so a missed or partial status event does not leave the transcript stuck on stale thread state.
- Session composers should treat pasted clipboard images the same as dropped or picker-selected uploads so screenshots can be attached directly from the textarea in both new-session and follow-up flows.
- Manual session creation should accept attachments-only starts. If the user uploads screenshots, photos, or files without typing prompt text, the create action stays enabled, the initial turn persists those attachments, and the session can begin with a placeholder title until the agent or user adds more text.
- First-party uploads use a larger multipart request body envelope than normal JSON API requests so a single file up to the product file-size limit can be uploaded with form overhead; session creation and follow-up requests should carry uploaded file URLs, not embedded file bytes.
- First-party session upload URLs are resolved by the worker before a coding-agent turn starts, copied into the sandbox, and listed in the prompt as sandbox-local files. External attachment URLs remain textual links in v1. Detailed design: [implemented/79-session-attachment-delivery.md](implemented/79-session-attachment-delivery.md).
- Preview browser origins should be runtime-owned by the backend rather than a build-time frontend constant. The preview status API returns the authoritative `preview_origin`, and production wildcard preview domains depend on edge DNS plus automated wildcard TLS issuance at the reverse proxy (for example, Caddy with a DNS-challenge provider module).
- Previewed apps that depend on browser cookies should keep their browser-facing API calls on the preview origin, typically by proxying `/api/*` inside the sandbox to the local backend. Build-time public API origins must not point back at the production app origin, or preview-scoped session/CSRF cookies split from the request destination.
- Preview readiness is a two-sided contract: the application must pass its in-sandbox readiness probe, and the worker must be able to dial the sandbox container IP/primary port before the preview is marked ready. Resource sizing is tiered by topology so single-service previews stay cheap, normal multi-service previews remain modest, and multi-service previews with managed infrastructure get enough CPU/memory to avoid starving their own dev servers without exceeding the normal sandbox slot model. Repos can declare bounded preview `resources.requests` and `resources.limits` in `.143/config.json` for CPU, memory, and ephemeral storage; resolved CPU, memory, and disk limits are persisted with preview and usage records for capacity tracking and future billing.
- Preview secrets should be modeled as admin-managed bundles: repos commit a named bundle reference and service scope, while 143 resolves values from managed storage or external secret sources and delivers them as env vars or generated config files inside the preview. This keeps values out of GitHub and agent diffs while supporting repos that expect files such as `.env` or `development.conf.json`; see [future/87-preview-secret-bundles.md](future/87-preview-secret-bundles.md).
- Preview controls should treat launch, retry, and restart as one user intent to get the session running with the current workspace config, keep startup diagnostics inline, and keep lifetime adjustment bounded behind secondary actions. Detailed designs: [implemented/82-preview-lifetime-controls.md](implemented/82-preview-lifetime-controls.md), [implemented/84-preview-command-header.md](implemented/84-preview-command-header.md).
- Session preview freshness is backend-authoritative: sessions carry a monotonically increasing workspace revision, preview launches/recycles stamp the revision they started from, and the Preview tab surfaces a compact stale-state marker plus `Refresh preview` action when newer session changes are available. Detailed design: [implemented/89-session-preview-freshness.md](implemented/89-session-preview-freshness.md).
- Branch previews are first-class preview targets keyed by repository, branch, commit SHA, and config name, with durable preview-origin target URLs and a create flow outside session detail. PR footers should link to the target preview origin directly, while the preview gateway resolves that stable target host to the current runtime instance and shows a minimal start/restart/status surface, including the last stopped time when known, when no runtime is ready. Session and PR preview surfaces resolve through this branch-preview control plane rather than treating runtime instances as the durable user-facing artifact. Detailed design: [implemented/83-branch-and-pr-previews.md](implemented/83-branch-and-pr-previews.md).
- The Preview tab uses a command-header hierarchy so `Open Preview` is the clear primary action when a preview is ready, while lifecycle operations like stop, restart, and lifetime changes live behind secondary preview actions. Ready-state status is quiet metadata rather than a competing pill. Detailed design: [implemented/84-preview-command-header.md](implemented/84-preview-command-header.md).
- Settings pages should not rely on desktop-only table layouts on phones. Shared headers should give actions a full-width mobile lane, and dense settings lists should collapse into stacked rows/cards with inline labels instead of forcing horizontal scanning.
- Integration settings should use compact overview cards for provider identity, connection health, and high-level connected scope summaries such as repository or channel pills. Detailed provider configuration belongs in a right-side sheet so GitHub repository access, Linear routing, Slack channel monitoring, token replacement, and disconnect controls do not make the integration list uneven or hard to scan.
- Wide, horizontally scrollable product tables should keep column headers sticky on mobile and desktop so rows remain understandable while scanning long ranked or settings lists.
- Settings no-data states that sit inside existing cards or table shells should use the shared inline empty-state treatment: icon, concise title, one explanatory sentence, and a local action when the user can resolve the missing setup. Coding-agent auth stacks use this pattern for personal auths, org fallback, and org-level coding auths so first-run setup does not read like loose helper text.
- Automation run history should use a clear execution-row layout: strong status-first rows, a consistent metadata rail, room for result or failure snippets, and collapsible quiet-run groupings for low-signal streaks. Scope this to `/automations/:id` first and leave `/sessions` unchanged while the pattern is validated. Wireframes: [future/72-execution-list-wireframes.md](future/72-execution-list-wireframes.md).
- Automation creation and detail are now goal-first surfaces: `/automations/new` opens on a centered composer with inline repository, cadence, run time, and timezone controls, compact template starters, and advanced settings in a sheet for lower-frequency defaults such as identity, model, reasoning, branch, scope, priority, and review; `/automations/:id` leads with readable goal markdown, latest-run context, full history, and a supporting metadata rail. Run history belongs in the main content flow where rows have enough width for status, title, metadata, and session navigation; the rail is reserved for schedule, identity, model, and aggregate stats rather than a duplicate recent-runs list. Detailed design: [implemented/84-automation-goal-first-ux.md](implemented/84-automation-goal-first-ux.md).
- PR health repair actions should expose durable in-progress state from the server rather than relying on mutation-local button spinners. For the current PR `health_version`, the health response carries `active_repairs`, the session detail banner suppresses conflicting repair/merge CTAs, and operators can jump into the active repair session after refreshes, navigation, or multi-viewer handoff. Detailed design: [implemented/74-pr-repair-in-progress-ux.md](implemented/74-pr-repair-in-progress-ux.md).
- PR Overview commands should preserve the original PR session as the single transcript. Agent-backed commands such as `Fix tests` and `Resolve conflicts` should continue that session with short visible prompts and structured command context, using normal snapshot continuation when trustworthy and PR-head reconstruction when needed; direct commands such as `Push changes` and `Merge` stay session-bound without creating repair sessions. The PR detail action row presents `Merge` first, followed by `Resolve conflicts`, `Review`, and `Push changes` when those actions are available. Detailed design: [implemented/76-pr-repair-session-continuity.md](implemented/76-pr-repair-session-continuity.md).

## Autopilot workspace UX

- `Sessions` is the main operating surface for active work: watching runs, reviewing diffs, following PR state, and giving agents guidance.
- `Autopilot` is a supporting background automation surface. It has shifted from a recommendation-first briefing page to a unified **issue-and-run queue** that shows what the system is likely to work on automatically, what already ran, and what is available for manual kick-off. Detailed design: [implemented/75-autopilot-issue-and-run-queue.md](implemented/75-autopilot-issue-and-run-queue.md).
- The Autopilot queue should sort high **priority fit** upstream work to the top by combining impact and implementation straightforwardness; connected sessions and PRs appear only as row state/actions.
- Each Autopilot issue row should make automation state explicit: whether it autoran, is queued/running, needs review, opened a PR, failed, or is ready for a manual kick-off.
- The previous recommendation hero remains useful, but only as a compact summary strip above the queue rather than the dominant page artifact.
- When required prerequisites are missing, route the user to `/onboarding` for the progressive setup sequence: (1) choose coding agent, (2) connect GitHub, (3) add optional integrations. The post-setup `Autopilot` surface stays focused on the issue-and-run queue.
- Coding agent selection uses a **single card with an agent dropdown** (default `Codex`) instead of multiple parallel agent cards. This reduces first-run decision fatigue and keeps focus on the primary onboarding path.
- The selected agent card always presents one clear next action: sign in (Codex) or configure credentials (Claude/Gemini), with a persistent settings entrypoint.
- Codex remains visually recommended to guide most users toward the quickest "time to first fix" path while preserving flexibility for teams with existing Anthropic/Google setups.
- Contextual PM steering still lives on `Autopilot`, but it should stay secondary to the issue queue as compact summaries, filters, and side-sheet detail rather than displacing the ranked table. Low-frequency PM admin controls like model selection and cadence live in `Autopilot settings`.

- Step 0: Connect repositories and build codebase context
    - Users sign in with GitHub OAuth and install the 143.dev GitHub App on their organization/repos. The GitHub App (same auth model used by Codex web, Claude Code web, and other modern AI coding platforms) provides fine-grained, short-lived installation tokens for repo access, and can also mint user-to-server tokens when a human authorizes PR creation on their behalf. No personal access tokens are required.
    - For each connected repo, the system automatically builds a **Repository Context Package** — a structured body of knowledge including architecture docs (CLAUDE.md, AGENTS.md), coding conventions extracted from the codebase and past PR reviews, a feature-to-file map (which files own which features), test infrastructure knowledge (how to run tests, what patterns are used), and a dependency map (service boundaries, safe-to-change-in-isolation analysis).
    - The system actively helps teams build and maintain this context: auto-generating it from the codebase, suggesting updates when code changes via push webhooks, and measuring **context quality** (e.g. "your repo has 40% file coverage in context docs, agents working on undocumented areas fail 3x more").
    - This context package is injected into every agent run, giving agents deep understanding of the codebase before they start working. This is arguably the single most important factor in agent success.
- Step 1: Ingest and aggregate customer and engineering context from:
    - Support tickets
    - Sentry errors
    - Linear issues
    - Integration setup is initiated from dashboard integration cards (`Autopilot` + `Integrations` pages). "Connect Linear" starts OAuth at `/api/v1/integrations/linear/login`, exchanges the callback code at `/api/v1/integrations/linear/callback`, stores the org-scoped Linear access token in `org_credentials` (`provider = linear`), and creates/reuses an active org-scoped Linear integration record for ingestion.
    - Linear can also trigger coding-agent sessions directly when the 143 app user is assigned or @-mentioned. Signed `AgentSessionEvent` webhooks create a `linear_agent_sessions` bridge row, enqueue `linear_agent_event`, resolve the target repository from team/project/label/default mappings, and then run the normal session workflow. Team mappings are keyed by Linear team key (for example `VIR`), not the opaque Linear team UUID; the settings UI should prefer the cached Linear team directory so admins pick readable teams like `Virtuous Cycle (VIR)` and the API stores the key. The Linear integration settings surface exposes the org default repository fallback plus team/project-specific repository overrides so admins can use a best-effort default while routing individual Linear teams to different repos. Linear-created 143 sessions use the same session-level autonomy/token defaults as other automatic session creation paths, and pre-session failures posted back to Linear include a direct operator debug link for the Linear AgentSession bridge row. The process-wide `LINEAR_AGENT_ENABLED` flag gates new inbound webhooks only; queued jobs and already-created sessions continue to drain.
- Step 2: Prioritize and identify top issues based on business impact
    - The system determines how many customers were affected, regression severity, and optionally (if you integrate Salesforce or some other CRM) the revenue risk.
    - The admins can specify product context (philosophy + direction + focus/avoid areas) to steer prioritization.
    - A **PM agent** now runs on a batch cadence, clusters related issues, produces a prioritized plan, and can propose new **repo-scoped projects** for human review when it finds a strategic opportunity (replacing per-issue auto-triggering for automation).
    - **Projects** are the primary long-term control surface. The canonical review surface for PM-proposed projects lives in `Projects`, while `Autopilot` shows lightweight PM proposal summaries and links users into that review flow. Each project can be `finite` (completes) or `evergreen` (continuous maintenance) with optional cadence-based execution and project-scoped quick actions.
    - Session and project sidebars should feel native on phones: a left swipe reveals an archive action beneath the row, with an iOS-style icon-led affordance, progressive fill/ready-state feedback as the user approaches the auto-archive threshold, tap-to-close behavior when the row is already open, fully opaque resting rows, and a fully collapsed hidden action tray until a deliberate swipe begins so edge-bounce scrolling never flashes the archive affordance. Use a centered single-surface archive tray instead of layered sub-panels.
- Step 3: Execute a coding agent
    - Auto-attempt decisions are based on explicit policy gates such as priority, complexity, aggressiveness, repository configuration, and available worker capacity.
    - Recurring team automations should start from a **structured template library** rather than bare one-line prompts. The default templates are frontend-defined issue-style prompts with explicit sections for task framing, output requirements, and verification, plus a deeper `/automations/templates` browse page for less common workflows.
    - Automations carry a typed visual identity (`icon_type` + `icon_value`) so list/detail surfaces can show a distinct emoji today while preserving the API shape for future image-backed icons.
    - Automation-triggered coding runs should use a prompt shape that stays **close to regular goal-driven sessions** so behavior is predictable across surfaces. The automation's goal should flow through as the raw task text, repository conventions and integration-tool instructions should match what a comparable session sees, and the system should avoid wrapping that goal in extra PM-analysis framing that changes agent behavior unexpectedly.
    - Interval automations support an explicit **run-at time** (`interval_run_at`) in UTC with 5-minute precision, so teams can choose both cadence ("every N days/weeks/hours") and a predictable execution wall-clock time ("at 09:35 UTC").
    - Automation goal editors should reuse the same prompt-authoring trigger surface as manual sessions for repository-aware `@` mentions and slash commands instead of maintaining a separate textarea-only UX.
    - Automations can run an optional **pre-PR self-review loop** before publication: a simple automation setting chooses `0` to `5` review/fix passes, the coding agent runs its standard review command against its own diff, fixes issues in the same sandbox, stops early when the agent reports the review is clean, and only proceeds to PR creation when the loop exits clean or policy allows bypass. Detailed design: [implemented/78-review-agent-loops.md](implemented/78-review-agent-loops.md).
- The Sessions area supports **one-off manual sessions** through a dedicated `/sessions/new` creation page with a chat-style composer. Users can start a manual run from free-form instructions, file/photo attachments, optional image URLs, and repository-aware `@` mentions for files/directories without waiting for PM planning cadence. Composer attachment previews should make uploaded screenshots easy to inspect before submit.
- Session image uploads accept HEIC/HEIF from iPhone photo flows, but normalize them to JPEG at upload time before storage and agent handoff because Codex/OpenAI vision and Claude/Claude Code do not accept HEIC as a native image input format.
- While the user is on `/sessions/new`, the session sidebar treats the unsaved draft as the current selected row instead of highlighting the first saved session. Saved sessions remain available below a small visual break, and keyboard navigation moves from the draft into the saved-session list only after the user explicitly navigates.
- When Linear is actively integrated, the manual-session composer should expose an `Add linear issue` affordance in the add menu and render added issues as removable chips in the composer instead of dumping pasted Linear refs back into the raw textarea. The action should stay hidden when Linear is not connected.
    - Once a Linear issue is attached through that picker, the resulting session/thread transcript should keep that linkage visible on the user message itself via a capitalized `Linear` tag plus the issue key, and follow-up detection should scan both free-form message text and detached structured references so picker-added issues are linked the same way pasted inline refs are.
    - On desktop, the session composer can expose repo, branch, model, and reasoning controls inline. On mobile, it now switches to a **launch-first layout** that uses a full-screen, scrollable, viewport-safe composer shell, keeps the textarea and submit action visible, and moves advanced run settings into a compact secondary surface across new-session, quick-create, and resumed follow-up composer flows.
    - The `/sessions/new` composer behaves like a modern desktop AI chat surface for images: the full primary composition region, including the `Let's build` hero and composer card, is a shared drag-and-drop target for screenshots/photos with subtle active-state motion and immediate attachment previews.
    - Supported coding agents expose a **reasoning-level control** in the session composer, with per-agent personal defaults stored server-side in `users.settings` and edited from `My settings`. Users can also store a personal default coding-agent model in `users.settings`; new manual sessions apply that model when no per-session model override is selected. The effective reasoning level is persisted on the session so resumed turns keep the same effort level instead of silently reverting to the agent's product default.
    - The resumed follow-up composer should resolve `@` mentions against the **current session workspace state** when a live sandbox or durable snapshot exists, so newly created or renamed files from the active branch appear immediately instead of waiting for repository-tree cache expiry or a remote provider refresh. Repo-level tree search remains the fallback when no session workspace is available.
    - Sessions are the execution primitive and now support **issue-less execution plus explicit linked-issue context**. Sessions own execution state, conversation history, sandbox state, and review/PR flow; issues remain backlog/prioritization records. Both phases of the migration have shipped: explicit session policy fields, `session_issue_links` as the only write-model truth, per-turn linked-issue snapshots, issue-less manual session creation, and removal of the legacy `sessions.issue_id` column. See [implemented/59-session-issue-decoupling-and-multi-issue-linking.md](implemented/59-session-issue-decoupling-and-multi-issue-linking.md). Richer multi-issue editing UX remains future work.
    - Session detail treats an existing PR as the authoritative publish outcome. Once a PR record exists, the UI should prefer that over stale snapshot or PR-attempt error fields so users do not see false failure banners after the PR is already available.
    - Session detail keeps the manual `Merge PR` action hidden until GitHub has explicitly reported passing checks. A clean merge state without check confirmation is treated as still in-flight, not merge-ready. When GitHub reports zero checks, 143 treats that as merge-ready only if the PR base branch has no required status checks configured; otherwise the empty check set remains provisional until GitHub reports the required checks on the latest head SHA.
    - Manual session `@` mentions are persisted as **structured input references** alongside the visible prompt text. The backend stores canonical reference metadata on the initial session message and lets each agent adapter translate those references into the downstream agent's native prompt/input format.
    - Manual sessions are **interactive by default**: after each turn the worker snapshots the sandbox + agent state, stores the latest diff/summary on the session, and returns the session to `idle` so the user can send a follow-up message. Follow-up messages also resume paused sessions such as `awaiting_input`, `needs_human_guidance`, and completed terminal states from the saved snapshot when one still exists. PR creation only starts when the user explicitly ends the session.
    - Coding-agent requests for human input use a provider-neutral, durable **human input request** abstraction rather than provider-specific UI handling. Claude `AskUserQuestion`, tool approvals, Codex approval/interrupt events, and future agent action choices normalize into the same pending request model, with clean option/action rows in session detail and a reliable resume path after the user answers. Detailed design: [implemented/78-agent-human-input-requests.md](implemented/78-agent-human-input-requests.md).
    - Manual-session cancel should interrupt the **actual agent CLI process**, not just the worker-side stream reader. The current implementation tracks provider runtime metadata plus a per-agent cancellation spec and records cancel requests durably so a cancel pressed before worker registration is honored as soon as the worker starts the agent runtime. When the public API process does not own the live cancel handle, it first sends a direct authenticated worker control request to the session's recorded sandbox worker; a targeted `cancel_session` worker job remains a fallback for older or temporarily unreachable workers. The session detail UI gives immediate stop feedback by replacing "Agent is working..." with "Stopping agent..." once the request is accepted locally, disables duplicate stop clicks while the process unwinds, treats terminal cancel status as a stopped state even when the status event omits fresh thread rows, and distinguishes resumable stops with "Stopped. You can send a follow-up when ready." The long-term direction is a **provider-owned live command handle** with native stdin/interrupt support rather than adapter-side wrappers; see [future/70-live-agent-command-handles.md](future/70-live-agent-command-handles.md).
    - The shared follow-up composer on session detail should keep draft typing isolated from the transcript and detail-side render work. Typing in the textarea must not force the whole transcript, review surface, or side panel to rerender on every keystroke.
    - Session titles should remain **user-correctable** after creation. Editing the title from session detail updates the canonical session title used in list/detail surfaces and synchronously propagates that edit to any already-open GitHub PR title so the user-visible title stays aligned across the app and GitHub.
    - The sessions sidebar should derive its active-row highlight from the active child route under the `/sessions` layout, not from assumptions about leaf-route params being available in shared layout UI. If the current session appears in the list, it should remain highlighted across client navigation, while `/sessions/new` must never highlight a historical session row. The active state should read clearly at a glance through a slightly stronger tinted surface and edge treatment, but stay restrained enough that the sidebar still feels calm and consistent.
    - Session detail should use a **single shared composer** across chat and code review. Draft text, attachments, model override, and plan-mode state should persist when switching between transcript and diff review, and open review comments should surface as chips inside that same shared composer instead of a separate review-only input.
    - On phones, the session conversation view should prioritize transcript space over persistent chrome: the bottom status footer stays hidden, and the shared follow-up composer rests as a single-line textarea until focused or already populated, expanding only when the user is actively composing.
    - While a session is actively `running`, the shared composer should remain **sendable rather than locked**. Follow-up instructions queue behind the in-flight turn through the normal message API, and the UI keeps both `Send` and `Cancel` available so operators can steer or stop work without waiting for the current response to finish.
    - The long-term runtime model for session tabs/threads should use a **durable per-thread inbox plus live per-thread runtime handles**, not either pure turn-bound app queueing or pure opaque agent-owned queues. The inbox remains the platform's source of truth for ordering, audit, recovery, and backpressure, while a live runtime handle gives low-latency direct follow-up delivery into the active coding-agent process when that thread is currently owned by a worker and the adapter has declared a provider-native live-input formatter. Detailed target architecture: [future/75-thread-runtime-handles-and-durable-inbox.md](future/75-thread-runtime-handles-and-durable-inbox.md).
    - The long-term product model for session tabs is **tmux panes in one shared sandbox**: the session owns the sandbox, worktree, branch, preview, PR, and final shipping artifact, while each tab/thread owns an independent agent runtime, durable inbox, transcript, status, cancellation path, and delivery cursor. Backend admission now allows multiple admitted sibling tabs to process concurrently inside the same session-owned sandbox, with explicit sandbox holders, lease-fenced thread runtimes, thread-deduped continuation jobs, retry-on-sibling-publish behavior for shared-container races, grouped inbox delivery summaries on thread and session-detail payloads, adapter-declared provider-native input protocols, dead-letter/unknown-delivery replay UX, snapshot quiescence checks for publish paths, and reaper recovery for expired runtime leases. Detailed design: [implemented/88-shared-sandbox-thread-runtimes.md](implemented/88-shared-sandbox-thread-runtimes.md).
    - Session detail supports **multiple agent tabs inside one sandbox**, with the tab strip directly above the transcript and shared composer rather than in the session-details side panel. A session owns the sandbox and final PR, while each tab owns its own transcript, status, model/provider choice, and message stream. The current implementation supports multiple blank tabs, creates new tabs immediately from the current tab/session defaults instead of stopping in a setup modal, lets untouched idle tabs switch to a different coding agent before their first message, routes first messages through the selected thread, and can run admitted sibling tabs concurrently in the shared sandbox up to the backend running-thread cap. The strip uses the shared line-tab active underline, keeps the blue dot only for running/pending or not-yet-viewed tabs, shows durable inbox delivery and file-overlap state in tab surfaces, and exposes a per-tab close affordance that archives the tab without deleting its historical record. Blank tabs can be added not only while a session is active, but also after it reaches a resumable paused/terminal state such as `completed`, so "add tab" matches the same continuation contract as sending another follow-up message. Closed tabs are **archived per-session thread records** rather than deleted, which removes them from the visible strip and frees capacity under the per-session tab limit while preserving historical transcript/audit state. (In the UI we call these tabs; in backend code they are threads.) Use this only when agents should share the same branch and filesystem; independent PR streams should remain separate sessions. Detailed design: [implemented/68-sandbox-agent-tabs-and-threads.md](implemented/68-sandbox-agent-tabs-and-threads.md).
    - Session detail exposes a **Review** action that starts a bounded review/fix loop inside the current sandbox: choose a native-review-capable coding agent, choose whether fix turns should make minimal review-clearing changes or address every review finding, create one review-loop tab, run that agent's native `/review` command against the live working tree, let the same agent fix its own findings, and repeat sequentially until the agent reports clean or the pass limit is reached. The setup defaults to minimal fixes and to a different coding agent from the main session when a supported alternative exists, making independent review easier while keeping one combined branch/PR. Before a PR exists, the action appears as a compact Overview readiness card; once a PR exists, it lives in the PR health action row with Merge, Fix tests, and related PR actions. PR lifecycle actions stay visible when temporarily blocked and explain disabled states with tooltips; actions are hidden only when structurally irrelevant to the current artifact state. Detailed designs: [implemented/78-review-agent-loops.md](implemented/78-review-agent-loops.md), [implemented/85-pr-lifecycle-action-states.md](implemented/85-pr-lifecycle-action-states.md).
    - Session detail is usable without a mouse. The keyboard model uses `j`/`k` and arrow keys for session-list movement, native paging keys for transcript scrolling, `i` to focus the shared composer, `d` to toggle session details, `[`/`]` plus `Ctrl+Tab` where available for agent tabs, guarded `p`-prefixed PR commands for shipping actions, and `?` for shortcut discovery. Direct shortcuts preserve native text entry contexts and ignore menus/dialogs. Detailed design: [implemented/73-session-keyboard-navigation.md](implemented/73-session-keyboard-navigation.md).
- Reopening an existing session should optimize for continuation rather than chronology: restore the user's last reading position when available; otherwise open active sessions at the live edge and inactive sessions at the start of the latest assistant turn rather than the top of the transcript. The preferred implementation loads the newest useful message window first, uses a minimal bottom-aligned transcript skeleton only until that first real window is ready, and fetches older history above the viewport without moving the content the user is reading. In multi-thread sessions, the continuation state also includes the last active thread; reopen should restore that thread before applying the per-thread scroll anchor so the UI does not silently fall back to the first tab. Legacy pre-migration scroll keys that only recorded a raw numeric `0` should be treated as "no meaningful saved position" so older top-of-thread state does not mask the newer continuation-first reopen behavior; structured saved positions remain authoritative, including an intentional top position. Mount-time skeleton state is not a meaningful saved position and must not be persisted before the initial anchor decision resolves. Detailed rationale lives in [implemented/56-session-open-position.md](implemented/56-session-open-position.md).
    - PR and preview recovery should distinguish three checkpoint-loss states with calm, actionable copy: **snapshot expired** (a previously-saved checkpoint aged out under retention), **checkpoint not captured** (the run completed without saving a reusable checkpoint), and **checkpoint unavailable** (the DB still points at a saved checkpoint key but the blob is missing from storage). Only the first state should imply age-based expiry; the latter two should prompt the user to send a new message to rebuild the sandbox without overstating severity. These notices are post-run recovery states, not active-run status messages, and should not appear before the session reaches a point where a reusable checkpoint was actually expected.
    - Multi-node deployments use shared object storage for session snapshots rather than node-local disk so worker-written snapshots can be hydrated later by API nodes. Successful snapshot saves emit `snapshot_size_bytes` and are tracked in the platform health dashboard for cross-region/cross-provider capacity planning; see [implemented/54-s3-session-snapshots.md](implemented/54-s3-session-snapshots.md). Cross-region workers use an explicit private address plane: Postgres keeps a primary private listener on `DB_BIND_IP`, db Tailscale enrollment advertises `DB_BIND_IP/32` automatically, Redis enrollment advertises `REDIS_PRIVATE_IP/32` automatically, remote workers in `TS_WORKER_HOSTS` always accept advertised routes, app nodes that proxy previews to tailnet-backed workers must also enroll in the tailnet, `pg_hba.conf` admits the Tailscale tailnet range, and worker provisioning can enroll mapped hosts with role-specific `TS_AUTH_KEY_*` secrets and publish `tailscale ip -4` as the preview address. Existing app/db/redis nodes can be enrolled with the Tailscale-only provisioning mode, which does not touch Docker containers or volumes.
    - PR creation failures should use a short toast (`PR creation failed`) and keep the actionable failure detail inline in the session header as a shared recoverable-error callout with clear title/body/action treatment, so the cause stays attached to the PR flow even after the toast disappears and error styling stays consistent with app-wide error toasts.
    - The inline code review surface should remain usable even on very long diff lines: comment composers should be visually compact, anchored near the commented line, and decoupled from the diff row's full scroll width so submit/cancel actions stay close to the text input.
- The session review surface is a structured code-review UI rather than a raw diff blob: parsed file/hunk rendering, inline comments, keyboard navigation, repo browsing, and scroll-synced file-tree highlighting are part of the intended core loop. Review now supports **GitHub-style directional context expansion** with boundary-specific icon controls anchored in the line-number gutter: up arrows above changes reveal prior context, and down arrows below changes reveal following context. This is backed by richer file-context metadata from the sandbox. The diff collection path also has a dedicated authoritative collector that compares the workspace against the immutable recorded base commit and appends untracked files, with typed snapshot persistence for review provenance; see [implemented/55-code-diff-context-navigation.md](implemented/55-code-diff-context-navigation.md). PR creation continues to use snapshot-backed workspace pushes for shipping fidelity.
    - Session list, sidebar, and initial session detail payloads must not hydrate, marshal, or parse raw diffs. List/detail endpoints return metadata and `diff_stats`; embedded thread rows return only a tiny diff-present marker. The full raw diff and `diff_history` are fetched lazily from the dedicated diff endpoint only when the user opens the Changes/review surface. This keeps sessions with accidentally huge diffs loadable and prevents one pathological diff from slowing unrelated sessions on the page.
    - On mobile, diff review should use a **dedicated full-screen file reader** rather than relying on the desktop-style split between the center review pane and the detail-sheet file list. The conversation-level `Files changed` affordance should jump straight into that diff reader, while the bottom-sheet `Changes` panel remains the file index for subsequent navigation. Detailed implementation lives in [implemented/69-mobile-diff-review-viewport.md](implemented/69-mobile-diff-review-viewport.md).
    - The `Changes` file list should keep the parsed diff's order aligned with the diff viewer. It uses a grouped tree when that tree can preserve the sequence, and falls back to a flat exact-order list when interleaved paths would otherwise force the grouped view to reorder files; see [implemented/68-consistent-review-file-ordering.md](implemented/68-consistent-review-file-ordering.md).
    - The agent runs in a sandboxed container and produces a code diff.
    - The sandbox is the filesystem permission boundary for coding agents. Agent CLIs launched inside the gVisor-isolated container should auto-approve routine in-workdir execution so users do not have to grant file-by-file or `git diff`-style approvals after choosing to run the agent. Claude Code runs headlessly with `--permission-mode bypassPermissions` because `acceptEdits` still expects interactive approvals for normal review commands; 143 only intercepts explicit human questions. Network-capable actions still need explicit sandbox or tool-level policy because production sandboxes intentionally retain public internet egress while blocking metadata and private network destinations.
    - Production sandboxes share the `143-sandbox` bridge with preview infrastructure and a `sandbox-dns` sidecar at a pinned bridge IP. The bridge must leave Docker inter-container communication at its default so gVisor sandboxes can reach that DNS sidecar; host firewall rules provide the egress boundary for metadata and private-network destinations.
    - Worker startup verifies the configured sandbox runtime by launching a small health-check image under that runtime. The image is version-pinned, configurable via `SANDBOX_HEALTH_CHECK_IMAGE`, and lazy-pulled when missing so fresh or pruned worker hosts do not depend on Docker image cache state.
    - Production workers treat sandbox disk usage as a first-class capacity boundary: deploys prune unused Docker artifacts after a healthy rollout, sandbox containers carry durable ownership labels, worker-local GC reconciles labeled host containers with DB session ownership, worker startup performs a Docker-first cleanup pass for DB-unreferenced containers before accepting jobs, sandbox admission can run a bounded short-grace pressure cleanup before reporting local capacity exhaustion, and worker startup fails closed when Docker cannot enforce `SANDBOX_DISK_LIMIT_GB`. Details: [implemented/76-sandbox-disk-guardrails.md](implemented/76-sandbox-disk-guardrails.md).
    - Worker capacity is visible from the primary Grafana operations dashboard. Workers emit low-volume platform health samples for current running sessions, turn-held sandboxes, active previews, preview-held sandboxes, running jobs, and host CPU/RAM utilization by worker node so operators can compare current and historical load when deciding whether to scale the worker fleet. API-capable processes also emit control-plane warnings for aged runnable jobs and missing fresh worker heartbeats so a fully stale worker fleet is still observable from a process that remains alive. Sandbox-capacity retries should requeue toward an active worker whose heartbeat metadata advertises free sandbox slots, while the worker-local admission gate remains authoritative when the job actually runs. Sandbox-capacity dead-letter logs include the final local slot check and current worker-load counts for running sessions, session sandbox containers, active previews, preview-held sandboxes, and running session/preview jobs, because capacity is per worker node and can be consumed by previews or in-flight reservations even when visible session concurrency looks low; user-visible failure text should stay concise and recovery-oriented.
    - Sandbox GitHub write access uses a **per-session host credential socket plus in-sandbox `gh` wrapper**, not a long-lived token embedded in the container env. The auth path now follows sandbox lifetime: preview-held containers keep their credential socket alive across turns, while fresh/hydrated continue-session sandboxes recreate the socket and bootstrap on create.
    - Worker startup treats that credential socket path as a runtime health invariant: production workers verify gVisor is configured with host Unix-domain socket access and run a startup-only sandbox preflight that mounts a throwaway socket into `/run/143-auth` and exercises the real `143-tools auth-token` helper before accepting session jobs. This protects session-start latency by keeping the probe out of per-session container creation.
    - Production worker hosts use a single idempotent reconciliation script (`deploy/scripts/reconcile-worker-host.sh`) for host invariants shared by cloud-init, SSH provisioning, routine deploys, and manual repair. It owns the pinned `143-sandbox` bridge, sandbox egress firewall, `/etc/143/sandbox-resolv.conf`, worker sysctl, and `/var/run/143/sandbox-auth` as uid/gid `1000:1000` with mode `0750`. Both worker cloud-init and SSH provisioning install the same narrow deploy-user `NOPASSWD` sudo grant so first-boot workers can be added to `FLEET_HOSTS` and survive routine fleet deploys without requiring root SSH repair. Production workers still preflight the sandbox-auth directory before registering as job-capable so a Docker-created `root:root 0755` bind-mount source cannot claim sessions and fail later at turn start.
    - Repo-backed sessions also treat the **session working branch as a guarded runtime invariant**: fresh runs create a canonical working branch, continue-session fresh clones recreate it, PR creation reuses it, and sandbox bootstrap installs a branch-specific `pre-push` hook so accidental pushes to the wrong branch fail locally before they reach GitHub. See [implemented/66-session-branch-guardrails.md](implemented/66-session-branch-guardrails.md).
    - User-visible publish actions (`open_pr`, `push_pr_changes`, and branch creation) must atomically persist both the queued action state on the session row and the worker job row in the same Postgres transaction. The Redis/job wake-up is post-commit and best-effort only; dedupe conflicts may repair legacy state by committing the queued action against the already-existing job, but a failed state transition or failed enqueue must roll back the whole action so the UI cannot show an infinite in-flight spinner with no matching durable work.
    - Long-running sessions must survive routine platform deploys and worker restarts. Worker infrastructure therefore uses deploy guardrails, lease-based dead-owner recovery for in-flight jobs, DB-owned running-job verification before worker shutdown completes, checkpoint-aware resume from the last committed turn after unplanned owner loss, and durable per-session executors that move `run_agent`/`continue_session` turn ownership out of the deployable worker process. Once a sandbox-bound retry discovers that a live container is owned by a specific worker, the retry must persist `target_node_id` so the next attempt goes back to that worker; if that worker is dead, the existing dead-node fallback may claim the job elsewhere and clear the stale container reference before hydrating from snapshot. `run_agent` recovery follows the same wrong-node targeting path as follow-up turns so checkpointed recovery does not get trapped on an unpinned retry after a worker generation is replaced. Recovery is bounded when no durable checkpoint exists: after repeated worker-loss attempts before the first checkpoint, the session/thread fail with recovery-specific metadata instead of restarting the same turn indefinitely. Recovery logs must include whether a checkpoint was available and whether the platform restarted from scratch so lost intra-turn work is visible. Sandbox-capacity retry exhaustion and stale-sandbox cleanup dead-letter exhaustion likewise terminalize the affected session/thread so the UI does not remain stuck in `running`; the user-facing failure copy stays concise while final slot counts and worker-load details are logged for operators. See [51-worker-deploy-safety.md](backlog/51-worker-deploy-safety.md) and [82-durable-session-executors.md](implemented/82-durable-session-executors.md).
    - Session runtime control should use a **soft default budget plus graceful, checkpoint-first shutdown**, not a single blunt timeout. Healthy long-running work may extend within policy; when the platform must stop a run it should first try to preserve resumable state and surface clearly whether the latest turn was checkpointed or the user is resuming from an earlier committed state. The implemented control plane now tracks per-session soft/hard deadlines, a 15-minute no-progress budget, parsed active tool execution, recent progress signals, bounded runtime extensions, graceful-stop reasons, checkpoint metadata, and queued/recovering worker-recovery state so runtime decisions and resume UX are observable in the session record. Adapter-level CLI liveness is not progress by itself: only parsed tool/sandbox lifecycle events suppress no-progress shutdown. Runtime watchdog failures terminalize matching `run_agent`/`continue_session` jobs, and lease renewal refuses to keep session jobs alive after their session is terminal. Detailed runtime behavior lives in [future/60-agent-runtime-timeouts-and-checkpointed-shutdown.md](future/60-agent-runtime-timeouts-and-checkpointed-shutdown.md).
    - Optional live preview for sandbox output is served from an **isolated preview origin** using short-lived preview tokens, never from the main app origin. This prevents untrusted preview code from sharing the app's authenticated browser context. In multi-node production the public preview edge stays on app nodes while sandbox hydrate/reuse, preview lifecycle, inspector actions, and cleanup run on worker nodes. Initial preview startup is a durable, idempotent `start_preview` worker job so app deploys, worker replacement, and client disconnects do not cancel or corrupt hydrate/build/readiness. Preview target and preview instance are durable user-facing identities; preview runtime is the live leased worker attachment that owns the sandbox/server endpoint. Routing uses `preview_runtimes.endpoint_url`, and gateway tokens bind to runtime ID plus epoch so old tokens cannot target a different worker runtime. Worker deploys use blue/green worker generations: the new worker starts with a unique node ID and host port, becomes eligible for new work, then old workers are marked draining in the background. Draining workers are excluded from new cold-start selection but remain routable for previews they already own until those previews stop or the preview drain timeout expires. If the owner is lost, the preview URL remains durable but the preview becomes `unavailable` and restartable instead of exposing worker-token internals. Extra worker generation ports are required for non-blocking preview-preserving deploys because every advertised worker host port must be reachable from app nodes. Without an extra port, deploy falls back to a blocking drain; after Docker releases the old worker's host port, the deploy may reuse that same endpoint while stale runtime leases expire, because previews owned by the stopped generation are unavailable and restartable. See [implemented/44-sandbox-preview-server.md](implemented/44-sandbox-preview-server.md), [implemented/48-worker-owned-preview-routing.md](implemented/48-worker-owned-preview-routing.md), [implemented/77-durable-preview-start.md](implemented/77-durable-preview-start.md), and [future/88-preview-runtime-ownership-drain.md](future/88-preview-runtime-ownership-drain.md).
    - Public wildcard preview routing must treat preview-gateway liveness separately from per-preview failure responses. A sandbox app, worker hop, or individual preview returning `502`/`503` should surface for that request without marking the shared app-node preview gateway upstream unhealthy, because a single upstream health penalty can fan out into a wildcard preview outage.
    - Preview dependency installation is a first-class optional phase (`preview.install`) between infrastructure init and service startup. 143 computes a lockfile/config/runtime cache key, stores success markers under `.143/cache/preview-install/`, cleans only repo-declared `clean_paths`, and surfaces install failures as `PREVIEW_INSTALL_FAILED` with persisted startup log tails. Repos should move package-manager install logic out of preview service commands when they need platform-owned install reuse and cleanup.
    - Branch/PR preview startup uses worker-local filesystem snapshots whenever possible. For pinned commit previews, the worker computes a cache key from declared install lockfiles, the commit SHA, and the resolved preview config, restores a matching built workspace before launch, and records a fresh snapshot after a successful cold launch. Configs that deliver preview secrets as generated files are excluded so plaintext secret files never enter the startup cache. Session previews do not use this commit-keyed cache because they may include unpushed agent changes; they instead reuse the live sandbox container when available or hydrate the exact session snapshot.
    - The per-user active preview limit is an organization General settings control, defaulting to 4 active previews per user. Hitting that guardrail should return per-user-specific recovery copy so users know they are not seeing generic worker or node capacity pressure.
    - The preview browser surface should treat the backend-supplied `preview_origin` as authoritative at runtime. Build-time `NEXT_PUBLIC_*` templates are acceptable as a local fallback, but production preview URLs must come from the live API/worker config so wildcard preview routing stays correct across rolling deploys and image promotion.
    - Next.js production previews that run standalone output must stage generated `.next/static` assets and `public` assets next to the standalone server before launch so CSS, JavaScript chunks, fonts, and images are served from the preview origin.
    - Preview recycle state is persisted with the preview lifecycle record rather than held only in worker memory so long-lived previews can be safely recycled after API restarts or deploys.
    - Preview sandbox holds are only valid while a preview is non-terminal. Any transition to `stopped`, `failed`, `expired`, or `unavailable` must clear `preview_holding_container` so session stale-container recovery can distinguish a live preview owner from a historical preview row.
    - When no preview is currently active, the Preview tab should still surface the latest terminal preview lifecycle row when one exists, including when that preview started and when it stopped. This keeps stopped previews distinct from sessions that have never launched a preview.
- Preview startup should keep service/container stdout and stderr available as an optional, hidden-by-default log stream in the Preview tab. The default startup canvas stays phase-focused, but users can expand raw preview logs to understand slow boots, installs, migrations, compiles, or readiness delays while the preview is still starting.
- Connected preview secrets use preview secret bundles managed from Settings → Preview. Repo config may reference a bundle by name, allowlist env vars or generated file paths, and target services; runtime launch decrypts the bundle for the active org/repository and delivers values as process environment variables or writes pasted private file contents into the preview workspace. The Preview settings page is the single admin surface for both repo-scoped preview secret bundles and scoped preview API tokens, using table-based inventories with dialogs/sheets for create/edit flows; see [implemented/88-preview-settings-page.md](implemented/88-preview-settings-page.md).
- Agent runtime credentials should be managed as visible, ordered auth stacks rather than hidden single-provider settings. The settings UX is a prioritized list of coding agent auths with an explicit default and fallback order across personal and organization scopes; see [implemented/57-coding-agent-settings-rethink.md](implemented/57-coding-agent-settings-rethink.md).
    - Amp and Pi now use these same coding-agent auth primitives instead of storing auth in `agent_config` or borrowing sibling-agent keys; the implementation notes live in [implemented/58-amp-pi-coding-agent-auth-alignment.md](implemented/58-amp-pi-coding-agent-auth-alignment.md).
    - Coding-agent auth stacks persist temporary rate-limit health in `coding_credentials` and derive `rate_limited` UI/API status without changing the stored credential status. Runtime credential selection skips future-limited auths across workers, preserves the personal-before-org fallback order for Codex, Claude Code, Gemini, Amp, Pi, and future coding agents, and makes continued sessions resolve fresh auth before each turn; see [implemented/78-coding-agent-rate-limit-fallback.md](implemented/78-coding-agent-rate-limit-fallback.md).
    - Token-usage persistence must preserve the difference between **direct provider-reported cost** and **derived estimates**. Claude can report session USD directly, Codex subscription usage is naturally denominated in credits, and some agents expose only tokens. The shared agent token-usage contract therefore stores normalized USD when available, provider-native cost when USD is not the native unit, and provenance (`direct` vs `derived`) so billing surfaces do not treat "not reported" as "zero". Detailed rollout notes live in [implemented/46-billing-usage-dashboard.md](implemented/46-billing-usage-dashboard.md).
    - The usage page now supports **billing rollups plus execution analytics by user, agent, model, and reasoning**. Org totals still read from `usage_hourly`, while a separate `usage_hourly_execution` rollup powers execution filters, breakdown tables, CSV exports, and stacked token-by-model charts without raw-session scans. Capacity-specific execution rows are still stored for backend queries and exports, alongside synthetic all-capacities rows that let agent/model/reasoning views report exact per-hour sessions and concurrency without double-counting capacity changes. See [implemented/66-usage-breakdown-by-agent-model-reasoning.md](implemented/66-usage-breakdown-by-agent-model-reasoning.md).
    - If the agent asks a clarifying question or requests an approval/action choice during execution, the run pauses through the shared human-input request contract. The user can answer in the session UI, provide free-form guidance where allowed, or **resume the session locally** via CLI (e.g., `143 resume <run-id>` or `claude --resume <session-id>`) to take over the sandbox interactively.
    - When a run fails, the system generates a **human-readable failure explanation** with actionable next steps — see [17-failure-communication.md](implemented/17-failure-communication.md). Failures are classified by sub-type and feed back into the system to improve future runs.
- Step 4: Open PR and ship
    - Users can publish a session as a branch without opening a PR. This uses the same snapshot-backed GitHub push path as PR creation, tracks branch-only state separately from PR creation state, and keeps the session eligible for a later PR. Details: [implemented/80-session-branch-only-publish.md](implemented/80-session-branch-only-publish.md).
    - The system opens a new PR on github, using whatever Github template already exists. PR descriptions should preserve the repo template's structure and fill its existing fields as well as possible; the only content appended outside the template is a small 143.dev/session links footer. User-initiated PRs should prefer GitHub App user-to-server tokens so the PR is authored as the triggering human; unattended flows fall back to the installation token.
    - Sandbox agents and automations should request PR creation through `143-tools create_pr`, not direct `gh pr create` calls. The CLI uses a short-lived internal token scoped to the session repository and queues the same `open_pr` workflow as the product UI, so repo PR templates, 143 session links, Linear issue context, dedupe, and PR state tracking stay consistent across human and automation-triggered PRs.
    - Repository-native CI/CD is responsible for validating the branch after PR creation. 143 no longer runs a separate product-owned validation stage. See [implemented/71-remove-validation-stage.md](implemented/71-remove-validation-stage.md).
    - It makes sure to attach the relevant Linear issue to the PR title, or references the original sentry issue / customer complaint, while keeping title cleanup minimal and relying on the LLM to generate the reviewer-facing phrasing when available
    - Session detail now includes a compact **PR health** row near the existing top-of-Overview PR/error notice area, backed by synced GitHub state, reconciliation, and org-scoped SSE updates so operators can see conflicts or failing tests quickly and launch one-click repair actions like `Resolve conflicts` and `Fix tests`; see [implemented/61-pr-state-sync-and-repair-actions.md](implemented/61-pr-state-sync-and-repair-actions.md).
    - PR health updates use the org-scoped SSE stream as the primary live path. Because Redis pub/sub does not replay events missed while a tab is hidden or reconnecting, the client reconciles the current PR health once whenever the stream connects or reconnects. When GitHub is still computing mergeability, PR health uses a neutral `mergeability_pending` state, keeps Merge visible but disabled in a checking state, and retries via exponential backoff instead of suppressing repair actions indefinitely behind a stale unknown state.
    - Failing-test badges in that PR health row should support a lightweight **CI job drilldown** on hover, listing the known check runs and whether each is `passed`, `failed`, or `pending` without forcing the user to open GitHub just to answer "what is red right now?"; see [implemented/65-pr-health-check-status-hover.md](implemented/65-pr-health-check-status-hover.md).
    - Linked PRs that are closed without merge should be treated as a clear terminal outcome in session surfaces rather than falling back to green/open-looking CI state. Sessions list rows render an explicit `Closed` PR badge, and session detail shows a compact terminal closed-state summary instead of open-only repair actions; see [implemented/62-pr-closed-state-ux.md](implemented/62-pr-closed-state-ux.md).
    - Sends the PR for human review (depending on the settings, could be a push notification or just puts it out for a group of reviewers).
- Step 5: Observe impact and close the customer loop
    - After a fix is deployed, the system automatically evaluates whether it reduced real customer pain.
    - Each shipped PR captures baseline production metrics before deploy and an observation window after deploy. After a deploy, the system will do automated checks to attribute impact.
    - It will measure:
        - Sentry error rate changes
        - support ticket volume changes
        - latency or reliability improvements
    - Finally the system classifies the outcomes as successful or not.
- Step 6: PR review feedback → agent improvement loop
    - By default, review comments on 143-generated PRs are captured and run through a multi-stage filtering pipeline (structural pre-filter → merge-gate → adoption check → directive detection → classification → dedup). An org setting can expand this to all PRs.
    - When a reviewer requests changes on a 143-generated PR, the system offers to re-run the agent with that feedback incorporated (auto-apply), rather than making the human fix it manually.
    - Generalizable reviewer feedback is accumulated into a per-repo knowledge base and materialized as a `.143/learned-conventions.md` file in the repo — version-controlled, transparent, and editable by the team. The agent reads this file as part of its context for all future runs.
    - Reviewer trust tiers (maintainer, contributor, external) control how quickly patterns are promoted. Adoption evidence (was the suggestion reflected in the final merged code?) further weights pattern confidence.
    - Reviewer acceptance rates are tracked per issue type, so the system learns which categories of fixes the agent handles well vs. poorly.
    - This creates a flywheel: every human review makes every future agent run better.

**The system tracks 6 steps, but the core demo is Steps 1-3-4: ingest a Sentry error → run an agent → open a PR. Everything else is optimization on this loop.**

# State Machines

The following state machines define valid status transitions for the core entities. These are authoritative — no code should transition an entity to a status not shown here.

## Issue Status

```
                          ┌───────────────┐
                          │     open      │ ◄── created by ingestion
                          └───────┬───────┘
                                  │ prioritization scores computed
                                  ▼
                          ┌───────────────┐
                     ┌──▶ │    triaged    │ ◄── eligible for agent run
                     │    └───────┬───────┘
                     │            │ agent run started
                     │            ▼
                     │    ┌───────────────┐
                     │    │  in_progress  │ ◄── agent run active
                     │    └───┬───────┬───┘
                     │        │       │
     validation fail │        │       │ PR merged + deploy detected
     or run failed   │        │       ▼
                     │        │  ┌───────────────┐
                     └────────┘  │   observing   │ ◄── experiment running
                                 └───────┬───────┘
                                         │ experiment completed
                                         ▼
                                 ┌───────────────┐
                                 │     fixed     │ ◄── outcome = success
                                 └───────────────┘

Other terminal statuses (reachable from open, triaged, or in_progress):
  - wont_fix  — admin dismisses manually
  - duplicate — dedup merges into another issue
```

Note: If a fix causes a regression (outcome = `regression`), the issue transitions back from `observing` → `triaged` so it can be re-attempted.

## Agent Run Status

```
┌─────────┐   job claimed    ┌─────────┐   agent exits     ┌───────────┐
│ pending │ ───────────────▶ │ running │ ──────────────▶   │ completed │
└────┬────┘                  └────┬────┘                   └─────┬─────┘
     │                            │                              │
     │ admin cancels              │ sandbox crash/timeout        │ validation
     ▼                            │ or agent error               │
┌───────────┐                     ▼                              ▼
│ cancelled │              ┌────────────┐               ┌──────────────┐
└───────────┘              │   failed   │               │  validation  │
                           └────────────┘               │   passed     │
                                                        └──────┬───────┘
                                                               │
                                                    │
                                                    ▼
                                             ┌─────────────┐
                                             │  pr_ready   │
                                             └─────────────┘
                                                                         │
                                                                   admin approves
                                                                         │
                                                                         ▼
                                                                  ┌─────────────┐
                                                                  │  pr_created  │
                                                                  └─────────────┘
```

Note: `skipped` is also a valid status, set when the aggressiveness gate rejects an auto-triggered run before execution.

## Experiment Status

```
┌──────────┐   baseline window      ┌───────────┐   observation window    ┌───────────┐
│ baseline │ ─────────────────────▶ │ observing │ ──────────────────────▶ │ completed │
└──────────┘   ends (= deploy time) └───────────┘   ends                  └───────────┘

Outcome (set on completion): success | no_change | regression | inconclusive
```

If outcome is `regression`, the linked issue transitions back to `triaged`.

## PR Status

```
┌────────┐   approved + merged    ┌────────┐
│  open  │ ─────────────────────▶ │ merged │
└───┬────┘                        └────────┘
    │
    │ author/admin closes
    ▼
┌────────┐
│ closed │
└────────┘
```

# Decision Matrix: Should We Attempt This Issue?

Three controls interact to determine whether an issue gets an automatic agent run. They operate **sequentially** — each is a gate that must pass before the next is evaluated. See [24-design-resolutions.md](24-design-resolutions.md) Resolution 1 for the full flowchart.

```
Issue eligible (score > threshold, status = open/triaged, direction_alignment > -0.5)
        │
        ▼
GATE 1: Autonomy Level (pre-run — "should we auto-trigger?")
        │
        ├── manual      → never auto-trigger; admin must click "Fix This"
        ├── auto_simple  → auto-trigger only for medium/low severity, score < 60
        └── auto_all     → auto-trigger for all eligible issues
        │
        ▼
GATE 2: Aggressiveness (pre-run — "is this issue within our complexity tolerance?")
        │
        ├── issue.complexity_tier <= max_tier_for_aggressiveness_level? → proceed
        └── above max tier? → skip (auto) or warn (manual trigger)
        │
        ▼
AGENT EXECUTES IN SANDBOX
        │
        ├── success with diff → enqueue PR creation
        ├── awaiting input    → pause for user answer
        └── error/no useful output → mark failed with explicit failure category
```

**Key rule**: These gates never interact with each other. Priority and complexity decide whether to attempt work; execution status, diff availability, validation, and PR/review state decide what happens after a run.

# Failure Recovery

Every failure type has a defined recovery path. This prevents ambiguity during implementation.

## Agent Run Failures

| Failure Type | What Happens | Retry? | Issue Status |
|-------------|-------------|--------|-------------|
| **Sandbox crash** (OOM, infrastructure) | Run marked `failed`, `failure_category = tooling`, `failure_sub_type = sandbox_crash` | Auto-retry once. If second attempt fails, stop and notify. | Stays `in_progress` during retry, returns to `triaged` after final failure |
| **Timeout** | Run marked `failed`, `failure_category = tooling` | No auto-retry. User can retry manually with longer timeout. | Returns to `triaged` |
| **Agent error** (non-zero exit, no diff) | Run marked `failed`, failure analyzed by LLM | No auto-retry. User sees explanation + next steps. | Returns to `triaged` |
| **LLM API error** (rate limit, outage) | Run marked `failed`, `failure_category = tooling`, `failure_sub_type = api_error` | Auto-retry with exponential backoff (max 3 attempts). | Stays `in_progress` during retries, returns to `triaged` after exhaustion |

## Validation Failures

| Failure Type | What Happens | Retry? | Issue Status |
|-------------|-------------|--------|-------------|
| **Tests fail** (`test_regression`) | Validation marked `failed`, run gets failure explanation | No auto-retry. User can retry or review diff. | Returns to `triaged` |
| **Security violation** | Validation marked `failed` | Never auto-retry. Cannot be overridden. | Returns to `triaged` |
| **Direction/quality/correctness fail** | Validation marked `failed` | No auto-retry. Admin can override (except security). | Returns to `triaged` |
| **CI failure** | Validation marked `failed` | No auto-retry. May be flaky CI — user can retry. | Returns to `triaged` |

## Pipeline Failures

| Failure Type | What Happens | Retry? |
|-------------|-------------|--------|
| **Webhook processing fails** | `webhook_deliveries.status = failed`, attempts incremented | Up to 3 retries with exponential backoff (1s, 4s, 16s). After exhaustion: logged, polling worker catches it on next sync. |
| **Polling sync fails** | `integration_sync_runs.status = failed` | Next scheduled sync (every 5 min). After 3 consecutive failures: integration status set to `error`, alert shown in UI. |
| **PR creation fails** | Job retried | Up to 3 attempts. After exhaustion: run stays `completed` with no PR, admin notified. |
| **Experiment evaluation fails** | Experiment stays in `observing` | Retried on next evaluation cycle. After 3 failures: outcome set to `inconclusive`. |

## Post-Deploy Regression

When an experiment outcome is `regression`:
1. Issue transitions from `observing` → `triaged` (making it eligible for re-attempt)
2. A `production_learnings` record is created with `severity = high`
3. Admin is notified with the regression details
4. The system does NOT automatically revert the PR — revert is a manual admin action
5. The learning is injected into future agent runs on similar issues

# Tech Stack

## Backend: Go

- **HTTP Router**: `go-chi/chi` — lightweight, stdlib-compatible
- **Database Driver**: `jackc/pgx` — fastest Go Postgres driver
- **Database Access**: Direct pgx v5 — type-safe store functions with `CollectRows`/`RowToStructByName`, no ORM or codegen
- **Migrations**: `golang-migrate/migrate`
- **Logging**: `rs/zerolog` -> Vector -> VictoriaLogs/Grafana
- **Error Tracking**: Sentry for frontend exceptions today; backend exception capture should converge there as well
- **Monitoring & Alerting**: Grafana is the operational control plane for logs, alerting, and notification routing. See [54-production-alerting.md](backlog/54-production-alerting.md)
- **Container Management**: Docker SDK (`docker/docker`)

## Frontend: Next.js + React + shadcn/ui

**Framework Decision**: Next.js (App Router) was chosen over Nuxt (Vue) and SvelteKit because:

1. **shadcn/ui is native React** — no adaptation layer needed. Vue and Svelte ports exist but are less mature.
2. **AI ecosystem** — Vercel AI SDK, React Server Components for streaming agent logs, and the broadest AI tooling support all target React/Next.js first.
3. **Contributor base** — React has the largest community, making it easiest for open-source contributors.

Key libraries:
- **UI Components**: shadcn/ui (Radix UI + Tailwind)
- **Server State**: TanStack Query (React Query)
- **Charts**: Recharts
- **Validation**: Zod
- **Icons**: Lucide React

## Database: PostgreSQL 18

Single system of record. Bundled in Docker Compose for local dev, swappable to managed Postgres (RDS, Cloud SQL) for production.

## Logging & Monitoring

- **VictoriaLogs + Grafana**: Primary centralized logging, provisioned dashboards, and operational alerting. Structured JSON logs are shipped by Vector and queried in Grafana. Vector API/health configuration lives in `deploy/vector.yaml`, not unsupported CLI flags, and deploy verification fails closed when the collector is missing, unhealthy, restarting, or otherwise not healthy. The shared Vector healthcheck probes `127.0.0.1:8686` to match the IPv4 API bind and avoid `localhost` IPv6 resolution failures. Repo-owned dashboards and `vmalert` rules are synced by `deploy-logging` so observability config changes apply with normal logging-node deploys. The platform-health dashboard should lead with actionable operator signals: ready jobs waiting, oldest wait, dead-lettered jobs, active sandbox containers, and worker CPU/RAM headroom, with API and session drilldowns below the headline snapshot. Alertmanager notifications route through a tiny repo-owned Slack relay so the production logging node can keep using plain Slack incoming webhook URLs without custom per-environment message formatting. The logging stack tolerates missing warning/critical webhook URLs by falling back to disabled local sinks so observability deploys do not block on notification wiring. This is the current production observability backbone.
- **Sentry**: Primary exception monitoring and developer-facing error triage. Frontend SDKs are already configured; backend exception capture should be added so Sentry becomes the system of record for application errors.
- **Alerting model**: Page on aggregated service symptoms in Grafana; send exception issues from Sentry primarily to Slack unless they meet a paging threshold. See [54-production-alerting.md](backlog/54-production-alerting.md).

# Build Order

The system should be built in phases. Each phase produces a usable milestone. The ordering principle is: **get to "Sentry error → PR" as fast as possible.** That's the demo. That's the tweet. That's the moment a user decides this product is real.

## Phase 1: Foundation + Repo Onboarding (docs: 01, 02, 03, 10, 13) — COMPLETE

Build the skeleton that everything else plugs into, including GitHub authentication and repo connection.

1. **Database schema + migrations** (01) — ✅ Two migration files: `000001_init` (orgs, users, sessions, repos, integrations, jobs, nodes, audit_log) and `000002_core_domain` (issues, agent_runs, validations, PRs, deploys)
2. **Go API server scaffold** (02) — ✅ Chi v5 router with 8+ middleware (auth, CORS, logging, rate limiting, metrics, RBAC, body limits, webhook signature verification). Handlers for health, auth, repos, webhooks, issues, runs, settings.
3. **GitHub OAuth flow** (13) — ✅ Login/callback/logout with state token CSRF protection, user upsert, 30-day sessions, HttpOnly cookies
4. **GitHub App setup** (13) — ✅ Installation webhook handling (created/deleted). JWT token generation + caching. Manifest-based app creation endpoint not yet implemented (convenience feature, not blocking).
5. **Repository management** (13) — ✅ Full CRUD store, UpsertFromGitHub for idempotent webhook sync, DisconnectByInstallationID for cleanup, installation token management
6. **Frontend scaffold** (03) — ✅ Next.js 16 + App Router + shadcn/ui + TanStack Query. Pages: Overview, Issues, Runs, Settings, Analytics (placeholder), Costs (placeholder). Vitest test suite with MSW mocks.
7. **Docker Compose + Makefile** (10) — ✅ Postgres 17 + Go server (air hot reload) + Next.js frontend. Makefile with dev, test, migrate, build, lint targets.
8. **Success metrics instrumentation** — ✅ Prometheus metrics middleware (http_requests_total, http_request_duration_seconds, http_requests_in_flight). `/metrics` endpoint. CI/CD via GitHub Actions with coverage gates (70% backend, 80% frontend); frontend coverage runs on scheduled/manual CI while hot-path PR/main runs prioritize fast failure detection.

**Milestone**: ✅ You can start the app, sign in with GitHub, connect repositories, and see connected repos in the dashboard. Core metrics are being captured from the first run.

## Phase 2: Sentry Ingestion (doc: 04) — COMPLETE

Connect Sentry first. It's the highest-signal, most automated source — stack traces give agents exactly what they need.

1. **Sentry webhook endpoint** — ✅ `HandleSentry()` in ingestion_webhooks.go with signature verification, delivery tracking, supports created/regression events
2. **Sentry adapter** — ✅ `SentryAdapter` parses webhooks, extracts stack traces, severity mapping, occurrence/customer counts, tags, timestamps. Full test coverage.
3. **Normalization + deduplication** — ✅ `NormalizedIssue` struct with `sha256(source:externalID)` fingerprinting, `ON CONFLICT` upsert with smart merging (increment occurrences, max customer count, update severity)
4. **Polling worker** — ✅ `SentryAPIClient` in sentry_api.go with project issues polling, `sync_sentry` job handler in worker/handlers.go, uses `integration_sync_runs` for tracking sync state
5. **Issues UI** — ✅ Data table with severity/status/source badges, occurrence count, customer count, relative timestamps. Filter dropdowns for status, source, and severity fully implemented in frontend. Backend supports cursor pagination.

**Milestone**: ✅ Sentry errors appear in the dashboard via both webhooks and polling sync. Issues can be filtered and browsed with full UI controls.

## Phase 3: Agent Execution + Validation + PR (docs: 06, 07, 08, 17) — COMPLETE

**This is the "aha moment."** Connect a repo, see a Sentry error, click "Fix This," get a PR. Ship these three together because none is useful alone.

The core execution pipeline is fully wired end-to-end. DB schema, stores, API handlers, services, and frontend are all implemented. All 6 validation checks are now implemented.

1. **Sandbox container management** — ✅ Docker SDK integration in `providers/docker.go` with full container lifecycle (Create/CloneRepo/Exec/ReadFile/WriteFile/Destroy). gVisor runtime support, security hardening (dropped capabilities, read-only rootfs, non-root user, PID limits, tmpfs with noexec). Configurable CPU/memory/timeout limits.
2. **Claude Code adapter** — ✅ `adapters/claude_code.go` implements AgentAdapter interface. `PreparePrompt()` builds system+user prompts with stack trace extraction and file hints. `Execute()` runs Claude Code CLI in sandbox, parses streaming JSON output, collects git diff. Prompt injection defense included.
3. **Agent orchestrator** — ✅ `orchestrator.go` implements full run lifecycle: concurrency check per org → status update → fetch issue/repo → get adapter → prepare prompt → create sandbox → clone repo → execute agent with log streaming → enqueue follow-up jobs (open_pr or analyze_failure) → cleanup. Worker handlers (`run_agent`, `open_pr`, `analyze_failure`) are wired to services.
4. **Worker sandbox concurrency model** — ✅ Worker parallelism is configurable per node via `WORKER_PROCESS_COUNT`, which controls how many in-process worker loops can claim jobs. Live container admission is separately capped by `WORKER_MAX_ACTIVE_SANDBOXES`: `0` derives from the node's final `WORKER_PROCESS_COUNT`, while values `>0` are explicit per-host overrides. Worker bucket defaults set both values per machine class so mixed fleets can safely run different caps; explicit env values win on each host. The live gate counts local Docker sandboxes plus in-flight reservations, so preview-held/hydrated containers cannot push a node past its machine-aware cap. Sandbox sizing is configurable via `SANDBOX_CPU_LIMIT`, `SANDBOX_MEMORY_LIMIT_MB`, and `SANDBOX_DISK_LIMIT_GB`; org-level run concurrency remains enforced separately by `max_concurrent_runs` (default 10). Self-hosting sizing guidance lives in `docs/self-hosting/worker-capacity-tuning.md`.
5. **Basic context injection** — ✅ `PreparePrompt()` injects repository conventions from ContextDocs. `extractFileHints()` pulls file paths from Sentry stack trace frames. `extractStackTrace()` produces human-readable stack traces from Sentry raw data.
6. **Human-in-the-loop** — ✅ Coding agents can emit provider-neutral human-input requests for free text, choices, action choices, and tool approvals. The orchestrator persists `session_human_input_requests`, marks the session/thread `awaiting_input`, checkpoints the sandbox, and resumes through `continue_session` after the answer API records the user's response. Legacy `session_questions` remain as compatibility data.
8. **Log streaming** — ✅ SSE endpoint (`GET /runs/{id}/logs/stream`) now uses Redis Streams fan-out when Redis is configured, with replay from `Last-Event-ID`, bounded per-client backpressure, and graceful fallback to the legacy 1s Postgres polling path when Redis is unavailable. Frontend `LogViewer` connects via EventSource with auto-reconnection.
9. **Full validation pipeline** — ✅ `validation/service.go` implements all 6 checks in fail-fast order: (1) **direction_check** — LLM verifies fix aligns with issue and product direction, (2) **correctness_check** — LLM verifies logical correctness, no introduced bugs, (3) **regression_test_check** — LLM verifies regression test is included, (4) **security_scan** — regex-based secret/SQLi detection, (5) **quality_check** — diff size limits (warn >200, fail >500 lines), (6) **ci_check** — detects project type and runs tests. Repositories can opt into pinned sandbox tool installs, bootstrap commands, and extra deterministic CI commands via `.143/config.json` (for example `dependencies: {"golangci-lint": "2.10.1"}`, `bootstrap.commands: ["npm ci"]`, `validation.commands: ["npm run lint:js"]`) without making repo-specific lint tooling part of the global sandbox image. Preview config also lives in this repo-level config file. LLM checks use an injectable `LLMClient` interface for testability. Diffs wrapped in `<code_diff>` tags for prompt injection defense. Graceful fallback to "skipped" when LLM is not configured. Validate method accepts issue context for LLM checks.
10. **PR creation** — ✅ `github/pr.go` implements full GitHub API flow: get base branch SHA → create branch → parse diff → create blobs/tree/commit → update ref → create PR → add labels → store in DB → update run and issue status. PR body includes agent summary, issue metadata, and validation results.
11. **PR tracking** — ✅ Full `PullRequestStore` with CRUD operations. Webhook handlers process `pull_request` events (merged/closed tracking, deploy record creation) and `pull_request_review` events (approval/changes_requested tracking).
12. **Failure communication** (17) — ✅ Rule-based `FailureService` in `failure.go` classifies 9 failure types (timeout, sandbox crash, API error, build failure, empty diff, test regression, security violation, large diff, missing context). Each produces human-readable explanation, category, sub-type, next steps, and retry recommendation. Persisted to DB and displayed in frontend.
13. **Fix Queue UI** — ✅ Runs list page with grouped tabs (All/Active/Needs Review/Failed/Completed), status badges, and duration display. Run detail page with tabs: Overview (status/timestamps/result), Logs (live streaming LogViewer), Diff (DiffViewer component), Validation (results table for all 6 checks), PR (GitHub link/status/review status/branch/body). Failure details section shows explanation, category, next steps as bulleted list, and retry button.

**Milestone**: ✅ The core "Sentry error → Fix This → agent run → validation → PR" pipeline is fully complete including all 6 validation checks.

## Phase 4: Prioritization + Routing (docs: 05, 12) — COMPLETE

Now that fixes are flowing, rank issues so the most impactful ones surface first.

1. **Scoring algorithm** — ✅ `prioritization/service.go` implements full composite scoring with configurable weights (default: customer_impact=0.35, severity=0.25, recency=0.20, revenue_risk=0.20). Sub-scores: `computeCustomerImpact` (log2-scaled from affected_customer_count), `computeSeverity` (critical=100 → low=25), `computeRecency` (exponential decay with 168h half-life). Direction alignment via LLM call clamped to [-1,1], applied as `score * (1 + 0.3*alignment)`. Eligibility gating: direction > -0.5, status open/triaged, score > org threshold. Stores results via `PriorityScoreStore.Upsert` and `ComplexityEstimateStore.Upsert`. Auto-enqueued after issue ingestion via `ingestion/service.go`.
2. **Complexity estimation** — ✅ `prioritization/service.go` `EstimateComplexity` uses LLM to classify issues into 5 tiers (trivial/simple/moderate/complex/very_complex) with confidence scores, issue type, reasoning, estimated files/tokens. Heuristic fallback based on severity when LLM unavailable.
3. **Auto-trigger** — ✅ `CheckAutoTrigger` implements 4 sequential gates: (1) autonomy level must be "auto_all" or "auto_simple", (2) if auto_simple, complexity tier must be ≤2, (3) aggressiveness tier limit (`conservative=2, moderate=3, aggressive=4, maximum=5`) must not be exceeded, (4) concurrent running agent count must be below org's max_concurrent cap. On pass, enqueues a `run_agent` job.
4. **DB stores** — ✅ `db/priority_scores.go` with Upsert (ON CONFLICT issue_id DO UPDATE), GetByIssueID, ListByOrg (with eligible_only filter, ORDER BY score DESC), DeleteByIssueID. `db/complexity_estimates.go` with Upsert, GetByIssueID, ListByOrg (with optional maxTier filter).
5. **API endpoints** — ✅ `handlers/priority.go` exposes: GET `/api/v1/issues/{id}/priority` (viewer+), GET `/api/v1/issues/{id}/complexity` (viewer+), GET `/api/v1/priority-scores` with `eligible_only` filter (viewer+), POST `/api/v1/issues/{id}/reprioritize` (admin-only, enqueues prioritize job with dedup key).
6. **Worker handler** — ✅ `worker/handlers.go` `prioritize` handler calls `ComputeScore` → `EstimateComplexity` → `CheckAutoTrigger` in sequence. Validate handler updated to fetch issue and pass to validation service for LLM context.
7. **Settings UI** — ✅ Settings page rewritten with: Agent Execution controls (autonomy level select, aggressiveness select, max concurrent input), Prioritization section (product direction textarea, priority weight grid with real-time sum validation, minimum score threshold). Save via PATCH with success/error feedback.
8. **Priority display** — ✅ Issues page enhanced with: priority score badge (green ≥70, yellow ≥40, gray <40), complexity tier badge (green trivial/simple, yellow moderate, red complex/very_complex), eligibility indicator dot (green/gray), sort dropdown (Last seen / Priority). Priority sort uses LEFT JOIN with priority_scores, ORDER BY score DESC NULLS LAST.
9. **Issues sort by priority** — ✅ `db/issues.go` IssueFilters extended with Sort field. When `Sort == "priority"`, query uses LEFT JOIN on priority_scores table. Frontend passes sort param via `useQueryState`.

**Milestone**: ✅ Full prioritization pipeline: ingestion → auto-score → complexity estimate → auto-trigger gates → agent run. Settings UI gives orgs control over autonomy, aggressiveness, weights, and direction.

## Phase 5: Observability + Impact (docs: 09, 18) — NOT STARTED (partial deploy tracking exists)

Close the loop — measure whether fixes actually helped.

`deploys` table exists and deploy records are already created automatically when PRs are merged (via `HandlePullRequestEvent` in `github/pr.go`). Experiments/metrics tables are missing. No experiment or outcome logic:

1. **Deploy detection** — ⚠️ PARTIAL. Deploy records are created automatically on PR merge (github/pr.go:255-264) with commit SHA and environment. However, there is no external deploy webhook handler (e.g., from CI/CD systems) for non-PR deploys.
2. **Baseline + observation metric collection** — ❌ No experiments table, no metric collection
3. **Outcome classification** — ❌ No comparison or classification logic
4. **Impact display** — ❌ No impact UI
5. **Production outcome feedback loop** (18) — ❌ No outcome analysis or learning injection

**Milestone**: ❌ Unblocked — Phases 3 and 4 are complete. PRs are shipping and prioritization is live.

## Phase 6: Review Feedback Loop (doc: 11) — COMPLETE

Turn human PR reviews into agent improvements.

1. **Review comment capture + processing pipeline** — ✅ `review_comments` table with migration, `ReviewCommentStore` (Create with ON CONFLICT dedup, GetByID, ListByOrg with filters, ListByPullRequest, ListActionableByPullRequest, UpdateClassification, MarkApplied, CountPendingByPR). Webhook handlers capture both `pull_request_review` (top-level review body) and `pull_request_review_comment` (inline diff comments). Multi-stage processing pipeline in `feedback/service.go`: structural pre-filter (bot accounts, short comments, emoji-only, CI patterns) → LLM classification (actionable, category, generalizable, generalized rule) → pattern dedup. Job queue integration via `process_review_comment` and `update_review_patterns` handlers.
2. **Auto-apply feedback** — ✅ `RevisionContext` type added to `AgentInput`. Claude Code adapter injects revision instructions (formatted feedback, comment summary, previous diff) into system prompt for revision runs. `PRService.PushRevision` method pushes commits to existing PR branch via GitHub API (get head SHA → create blobs/tree/commit → update ref → post summary comment). `FormatRevisionFeedback` in feedback service formats actionable comments for prompt injection.
3. **Review patterns KB** — ✅ `review_patterns` table with insert-only versioning pattern. `ReviewPatternStore` (Create, GetByID, ListByRepo with status filter, ListActiveByRepo, FindMatchingRule case-insensitive, UpdatePattern with insert-only versioning, IncrementOccurrence with auto-promotion candidate→active at 2+ occurrences). API endpoints: GET `/review-patterns/*` (viewer+), PATCH `/review-patterns/{id}` status update (admin), PUT `/review-patterns/{id}` rule edit (admin). Frontend API client wired.
4. **Curated context document** — ✅ `GenerateConventionsDoc` in feedback service produces `.143/learned-conventions.md` content grouped by category (Style, Logic, Edge Cases, Architecture, Testing, Security, Performance, Nits) with occurrence counts. API endpoint for review comments listing: GET `/review-comments` with pull_request_id and filter_status filters.

**Milestone**: ✅ Full review feedback loop: webhook capture → processing pipeline → pattern KB → conventions doc generation → revision context injection.

## Phase 7: Codebase Context — Advanced (doc: 14) — NOT STARTED

Deepen the context layer based on what you've learned from real agent runs about what context actually matters.

`repositories` table has a `context_quality` column ready, but context package tables (`repo_context_packages`, `repo_context_entries`, `repo_file_map`) are not created:

1. **File map generation** — ❌ No tables, no LLM classification
2. **Convention extraction** — ❌ No extraction logic
3. **Test infrastructure discovery** — ❌ No discovery logic
4. **Quality scoring** — ❌ DB column exists on repos, no scoring algorithm
5. **Incremental updates** — ❌ No push webhook context updates
6. **Context UI** — ❌ No context UI

**Milestone**: ❌ Unblocked — Phases 3-4 provide real agent runs and prioritization data to learn from.

## Phase 8: Evals + Quality Gates (doc: 16) — NOT STARTED

Now that you have real production data and observed failure modes, build the evaluation system on solid ground.

No eval infrastructure tables exist. Entirely future work:

1. **Eval taxonomy + schema** — ❌ No eval tables
2. **Dataset pipeline** — ❌ No dataset infrastructure
3. **Grader stack** — ❌ No grader implementation
4. **Release gates + rollout** — ❌ No release gate tables or logic
5. **Continuous eval flywheel** — ❌ No flywheel

**Milestone**: ❌ Partially unblocked — Phases 3-4 are complete, Phase 5 still needed for full production data.

## Future: Additional Ingestion Sources

After the core loop is proven with Sentry:

- **Linear ingestion** — webhook + polling adapter, issue type classification
- **Support tool ingestion** — Zendesk/Intercom adapters, customer pain extraction
- **Additional agent adapters** — Codex, Gemini CLI, custom agents
- **Time to First Fix optimization** (doc 15) — demo mode, quick-win scan, progress UX

# Architecture

143.dev is designed to be:

- Open source first
- Self-hostable in minutes, but scalable to multi-machine production with a one-line setup command
- Simple in local development
- [If needed] to be extensible into a hosted enterprise cloud in the future

## Horizontal Scaling Model

143.dev uses a **symmetric, peer-based architecture** with Postgres as the sole coordination layer. There is no special "primary" node — every node runs the same binary and can serve any role:

- **`--mode=all`** (default): Runs API + workers + scheduler. Multiple `all` nodes can run simultaneously — the scheduler uses a Postgres advisory lock so only one instance runs cron jobs, with automatic failover if that node dies.
- **`--mode=api`**: API + UI only. Stateless. Scale behind a load balancer.
- **`--mode=worker`**: Job processing + agent sandboxes only. Scale for compute.

No service discovery or orchestrator required. A new node joins the cluster by pointing at the same `DATABASE_URL`. See [10-infrastructure.md](10-infrastructure.md) for full details.

Routine production fleet deploys target only `app` and `worker` roles. DB, Redis, and logging rollouts are explicit maintenance actions, app-host Docker daemon restarts require an explicit maintenance flag, and routine app deploys leave Caddy running unless its Dockerfile, Caddyfile, or edge env changed so frequent deploys do not recycle the Cloudflare-facing origin.

## Systems

### PostgreSQL

Postgres will serve as the primary system of record. It will store:

- Ingested issues (support, Sentry, Linear)
- Prioritization metadata
- Agent runs
- Validation results
- PR links and deploy events
- Experiments and outcomes
- Audit trail

Initially, Postgres will be bundled into the single setup container, but we’ll build it so that it’s easy to migrate to RDS or some hosted Postgres system in the future.

### Coding container runtime

Each agent run will execute inside of an isolated sandbox. Each will include:

- resource limits (CPU, memory, time)
- restricted filesystem
- controlled network access

### Web application container

The main 143.dev container includes:

- API server
- web UI
- background worker loop
- job scheduler
- post-deploy experiment evaluator
- Integration logic for:
    - Github: PR creation, status checks, deploy signals
    - Sentry: Issue webhooks as well as retrieval of issues via the API. Also linkage of issues to Github PRs in the PR body.
    - Linear: Webhooks and retrieval of issues via the API. Also linkage of issues to Github PRs in the PR title.

# Dashboard onboarding UX

- The Overview dashboard keeps users in setup context when configuring coding agents.
- In the "Connect your coding agent" card, the primary follow-up path should lead into a clean coding-agent settings surface centered on a prioritized auth table, not a maze of per-provider forms.
- The settings model should make the effective default and fallback order visible at a glance while still supporting fast in-flow add-auth actions from onboarding.
- The UX goal is fast in-flow completion first, with a clear handoff to a system-level control surface when deeper configuration is needed.

# **Why 143?**

The name comes from the XP-80 Shooting Star project. In 1943, a small team at Lockheed Skunk Works designed and built the first US jet fighter in exactly **143 days**. They did it by killing the bureaucracy and giving a small, autonomous team the freedom to execute.

That's the logic behind **143.dev**.

Fixing production bugs usually sucks because of the overhead like parsing logs, reproducing state, and context switching, not necessarily because the fix itself is hard. We use **autonomous AI agents** to handle that grunt work. The agents act like your Skunk Works team: they isolate the issue, verify the root cause, and tee up the solution so you can just ship it.
