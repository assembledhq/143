# 92 - Slackbot Product Surface

> **Status:** Future
> **Last reviewed:** 2026-05-29
>
> **Depends on:** Slack OAuth integration, session/job creation APIs, durable preview control plane, human input requests, and notification delivery primitives.

## Problem

143 should be usable from Slack without turning Slack into a second full product UI. The Slack app should let teams receive completion notifications, start work from natural engineering conversations, ask coding questions, create/open previews, and answer agent questions without leaving Slack.

The current Slack integration is oriented around channel monitoring and context sync. It can connect Slack, store a token, select channels, poll recent messages, summarize threads, and make that context available to PM/session workflows. That is useful input context, but it is not yet a Slackbot surface: it does not receive Slack events, post bot messages, handle mentions, open modals, map Slack users to 143 users, or route interactive actions into durable jobs.

## Product Principle

Mentioning `@143` should be enough to start useful work. The user should not need to choose between "ask" and "start work" before the system does anything. Every explicit mention in a channel, and every DM message to the bot, should create or continue a normal 143 session with the Slack thread or DM conversation attached as context.

The Slack thread is the lightweight live surface. The canonical record remains the 143 session. The bot should show that it is thinking/running, post a useful answer or progress summary back into the Slack thread, and always include a link to the 143 session for full transcript, diffs, preview controls, and PR state.

Slackbot should avoid passive participation. It should not inspect ordinary unmentioned channel messages to decide whether to answer. In channels, explicit mentions are the trigger.

## Scope

In scope:

- Slack App Home as the personal 143 command center.
- Mention and DM based session kickoff for coding questions, fixes, previews, and investigations.
- Human-input request delivery and response from Slack.
- Automation completion/failure notifications.
- Preview create/open/restart/refresh/extend controls from Slack.
- Channel invitation support, but only for mention-driven behavior in channels.
- Best-effort Slack user mapping to 143 users for attribution, authorization, and DMs.

Out of scope:

- Passive channel listening that decides whether to answer unmentioned messages.
- Autonomous responses to arbitrary channel traffic.
- Replacing the web session transcript with Slack as the canonical execution UI.
- Slack as a general-purpose admin settings surface.

## Surfaces

### 1. App Home

Slack App Home is the main Slack-native dashboard. It should make the product surface discoverable without requiring slash command memorization.

Top-level sections:

- `Start from Slack`
- `Needs your response`
- `Active work`
- `Active previews`
- `Recent automation runs`
- `Slack connection`

`Start from Slack` opens a small composer for users who want to begin from App Home instead of a channel. It should create a normal 143 session using org/user defaults and only ask for missing essentials such as repository when the request cannot be resolved.

`Needs your response` lists pending human-input requests assigned to the user. Each row includes the session title, requesting agent/thread, concise question, and primary actions.

`Active previews` lists previews the user recently started or is subscribed to, with actions such as `Open`, `Refresh`, `Restart`, `Stop`, and `Extend`.

`Recent automation runs` lists recent completed/failed runs for automations the user created or subscribes to, with links to sessions, PRs, and previews.

### 2. Mentions and DMs

The bot responds in channels only when explicitly mentioned. It may also be used in DMs. A mention or DM creates a 143 session immediately, using the Slack message and available thread context as the initial user prompt.

Examples:

```text
@143 why is the preview failing?
@143 explain this stack trace
@143 start a fix for this issue
@143 create a preview for branch jsmith/navbar-redesign
@143 what changed in this PR?
```

Mention handling should resolve enough context to start the session:

- Slack channel, thread root, replies, permalink, and attached files/links.
- Mentioning Slack user and best-effort mapped 143 user, if available.
- Repository, PR, issue, Sentry URL, preview URL, branch, or file paths when detectable.
- Org/channel defaults such as repository or preferred agent.

If required context is missing, the bot should still create the session and ask the clarifying question in the Slack thread. When a repository or preview target cannot be resolved, the Slack reply should offer concise buttons or select menus rather than forcing the user into a separate setup flow:

```text
I can help, but I need a repository first.

[Select repository] [Open session]
```

Replies in channels should stay threaded. DMs can use a normal conversational sequence, but durable work should still link to the canonical 143 session.

### 3. Slack Session Lifecycle

Every Slack-started session should have a clear lifecycle in the originating thread.

On receipt, the bot should ack quickly:

```text
Starting a 143 session for this thread...

Session: https://143.dev/sessions/abc123
```

While computing, the bot should keep Slack lightweight but visibly alive. It may update the same message or add sparse thread updates for major state changes:

- `Reading the Slack thread`
- `Resolving repository context`
- `Running agent`
- `Inspecting files`
- `Running tests`
- `Starting preview`
- `Calling tool: <tool name>`
- `Waiting on command: <command>`
- `Waiting for your input`
- `Completed`
- `Failed`

Intermediate Slack state should show enough background activity for teammates to see that the run is making progress. Tool names, high-level command names, short sanitized status snippets, test phase names, preview startup phases, and human-input waits are appropriate. Keep these updates compact and avoid flooding the channel. Full raw logs, long stack traces, token counts, sandbox IDs, executor IDs, and internal job IDs still belong in the 143 session/log surfaces.

The final Slack output should present the agent's final response directly in the thread. For most sessions, do not compute a separate Slack-only summary. If the agent already produced a concise final answer, use it as-is with light Slack formatting and append the durable links. If the final answer is too large for Slack, truncate gracefully and link to the full 143 session.

When the final response is ready, the bot should make the thread read as a completed result, while preserving useful progress context:

- Update the initial "starting/running" message to a concise terminal state.
- Keep or collapse intermediate tool/progress updates when they help explain what happened.
- Show the final agent response as the main output.
- Append user-facing outcome data such as changed files, PR links, preview URL/status, required next action, and the 143 session URL when relevant.
- Avoid exposing internal-only identifiers such as sandbox IDs, executor IDs, and internal job IDs.

Example final reply:

```text
The preview is stale because the session has newer workspace changes than the running runtime. I started a refresh and it is ready now.

Preview: https://preview.example
Session: https://143.dev/sessions/abc123

[Open preview] [Open session]
```

The full transcript, diffs, logs, preview diagnostics, and PR state live in 143. Slack mirrors the outcome and enough progress to keep the thread understandable, but the final Slack thread should read like a polished result rather than a live agent log.

### 4. Human Input Requests

When an agent asks for clarification, approval, or an action choice, Slack should be a delivery channel for the existing durable human-input request abstraction.

Supported request types:

- Free-form answer.
- Multiple-choice answer.
- Approval/deny.
- Tool or command approval.
- Continue/stop/resume decisions.

Delivery rules:

- DM the assigned user when the request is personal.
- Reply in the originating Slack thread when the session was started from that thread and the request is not sensitive.
- Include a web fallback link for every request.
- If the session is a team session with no mapped 143 user, post the request back to the originating thread and let any authorized 143 user claim or answer it from Slack or the web UI.

Slack interaction submits should update the same pending request row used by the web UI. The Slack message should then update to show that the request was answered, by whom, and when.

### 5. Notifications

Slackbot should deliver selected notification events as bot-authored Block Kit messages, not only incoming webhook text.

Initial event families:

- `automation.run.completed`
- `automation.run.failed`
- `automation.run.failure_streak`
- `session.completed`
- `session.failed`
- `pr.opened`
- `preview.ready`
- `preview.failed`
- `preview.stale`
- `human_input.requested`

Automation notifications should support per-automation subscriptions. A channel can subscribe to an automation, and individual users can subscribe to DM notifications. Completion messages should include the run result, session link, PR links, preview links, and obvious next actions.

Preview notifications should include lifecycle controls where safe:

- `Open`
- `Refresh`
- `Restart`
- `Stop`
- `Extend`
- `Open session`

No notification action should bypass normal org/channel policy. When the Slack user maps to a 143 user, actions should use that user's role. When there is no mapping, actions may only operate on the originating team session and only within the channel's configured allowed actions.

## Channel Behavior

The bot can be invited to channels, but channel behavior is mention-only by default.

When invited, the bot posts a short setup message visible to the channel:

```text
143 is available in this channel.

Mention me to ask coding questions, start work, or create previews.

[Configure channel] [Open App Home]
```

Channel settings:

- Default repository, optional.
- Default response visibility: thread reply or DM.
- Allowed Slack-started session capabilities, such as previews or PR requests.
- Notification subscriptions.

There is no passive channel watcher in this design. The bot should not inspect every channel message and decide whether to answer unless a future design explicitly introduces an opt-in assistive mode.

## Slack-Started Sessions

Slack is an entry point into normal 143 sessions. It should not run a parallel Slack-only agent flow.

Sessions may be initiated by:

- App Home composer.
- Slash command.
- Channel mention.
- DM message.
- Button from an existing Slack notification or session update.

The created session should persist:

- Slack team ID.
- Channel ID or DM ID.
- Thread timestamp.
- Message permalink.
- Triggering Slack user ID.
- Mapped 143 user ID when available.
- Whether it is a user-associated session or team session.

If the mention is a question, the session should answer the question and post the answer in Slack. If it is a request to fix, build, preview, or investigate, the session should run that work and post progress plus the result. The user should not need to preselect a mode.

Resulting Slack messages should include:

- Session title/status.
- Repository/base branch when resolved.
- Triggering Slack user, plus mapped 143 user when known.
- Links to session, PR, and preview when available.
- Sparse threaded status updates for major milestones only.
- A final response that replaces/collapses transient agent-run details and presents the user-facing outcome cleanly.

Advanced configuration should use org/channel/user defaults. Use Slack modals or select menus only when required context is missing or the action has unusually high impact.

## Preview Mode

Slack preview actions should route through the durable preview control plane.

Supported actions:

- Create preview for a session, PR, branch, or commit.
- Open preview.
- Refresh stale preview.
- Restart failed preview.
- Extend lifetime.
- Stop preview.

Preview creation from Slack should require enough context to resolve a preview target. If the message only says `@143 create a preview`, the bot should ask for a session, PR, branch, or repository.

Preview URLs should use the same short-lived token and isolated preview-origin rules as the web product.

## Identity and Authorization

Slack identity mapping is best effort. The bot should try to associate Slack users with 143 users for attribution, DMs, RBAC, and user-specific defaults, but a missing mapping should not block Slack from starting useful team work.

Mapping sources:

- Explicit App Home account link.
- Slack email lookup when scopes allow it.
- Admin-managed mapping for service/workspace edge cases.

Session ownership:

- If the Slack user maps to a 143 user in the org, create a user-associated session and apply that user's role/defaults.
- If the Slack user is not mapped, create a team session associated with the Slack channel/workspace and no 143 user.
- Team sessions use org/channel defaults for repository, agent, model, preview behavior, and notification destination.
- Team sessions should clearly show in Slack and in 143 that they were started from Slack without a mapped 143 user.

Authorization checks:

- If a mapped user exists, their role should govern repository/session/preview/PR actions.
- If no mapped user exists, the team session may run only within org/channel-level defaults and allowed capabilities.
- User-specific actions such as user-authored PR creation require a mapped and authorized 143 user.
- The bot should offer an account-link prompt, but linking is an enhancement path, not a prerequisite for starting a team session.

The Slack install is org-scoped. A Slack workspace may map to one active 143 organization by default. Multi-org Slack workspaces require an explicit org selector in App Home or channel settings before actions can run.

## Backend Shape

New request endpoints:

- `POST /api/v1/webhooks/slack/events`
- `POST /api/v1/webhooks/slack/commands`
- `POST /api/v1/webhooks/slack/interactions`

All Slack inbound endpoints must:

- Verify Slack request signatures.
- Bound request bodies.
- Deduplicate by Slack event/request IDs where available.
- Ack quickly.
- Enqueue durable jobs for LLM classification, session creation, preview actions, or notification updates.
- Ignore bot/self messages.

New jobs:

- `slack_handle_mention`
- `slack_handle_interaction`
- `slack_send_notification`
- `slack_sync_app_home`

The existing Slack polling/summarization worker can remain separate. Its purpose is context ingestion, not interactive bot handling.

## Data Model

Likely additions:

- `slack_installations`: org/team/app install metadata, bot user ID, scopes, status.
- `slack_user_links`: org ID, 143 user ID, Slack team ID, Slack user ID, source, timestamps.
- `slack_channel_settings`: org ID, team ID, channel ID, defaults, allowed actions, notification subscriptions.
- `slack_interaction_events`: dedupe/audit table for inbound event IDs and action payloads.
- `notification_deliveries`: if not already implemented by the notification system, delivery status keyed by notification/channel/provider target.

Credentials remain in encrypted credential storage. Integration config may expose safe metadata, but never bot tokens or signing secrets.

## Slack App Capabilities

Required capabilities depend on phase, but the full app likely needs:

- Bot token with message posting.
- Events API subscriptions for app mentions, DMs, and app/channel lifecycle events.
- Slash commands.
- Interactivity for buttons, select menus, and modals.
- App Home.
- Link unfurls for 143 URLs as a later enhancement.

The initial channel behavior should subscribe only to explicit mentions and DMs. Do not subscribe to or process ordinary channel messages for passive response decisions.

## Rollout

### Phase 1 - Bot Install and Notifications

- Upgrade Slack OAuth/app install to store bot metadata and required posting scopes.
- Implement Slack signature verification and interaction/event endpoints.
- Send automation/session/preview notifications to DMs or subscribed channels.
- Add user mapping and basic App Home account-linking.

### Phase 2 - App Home and Mentions

- Build App Home with Slack session composer, pending responses, active previews, and recent automation runs.
- Implement mention/DM session kickoff.
- Post loading/computing state and final output back to the Slack thread with the 143 session URL.

### Phase 3 - Slack Session Context and Output

- Persist Slack thread references on sessions.
- Post milestone updates back to the originating thread.
- Improve final Slack summaries for questions, investigations, fixes, and preview requests.
- Add modals/select menus only for missing required context.

### Phase 4 - Human Input Requests

- Deliver pending human-input requests through Slack.
- Support answer/approval interactions.
- Update Slack messages after responses.
- Keep web UI and Slack responses backed by the same durable request rows.

### Phase 5 - Preview Controls

- Add Slack preview create/open/refresh/restart/extend/stop actions.
- Add App Home active preview controls.
- Add notification actions for preview readiness and failure.

## Open Questions

- Should Slack-started sessions default to DM updates or thread updates when started from a public channel?
- Should channel settings be admin-only, or can channel members opt the channel into notifications?
- How should a shared Slack workspace choose among multiple 143 organizations?
- Should coding questions from Slack use the same agent runtime as coding sessions, or use a cheaper read-only responder inside the same session abstraction?
- How much transcript should be mirrored back to Slack before Slack becomes too noisy?
