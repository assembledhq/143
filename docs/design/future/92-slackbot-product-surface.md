# 92 - Slackbot Product Surface

> **Status:** Partially implemented
> **Last reviewed:** 2026-06-01
>
> **Depends on:** Slack OAuth integration, session/job creation APIs, durable preview control plane, human input requests, and notification delivery primitives.

## Problem

143 should be usable from Slack without turning Slack into a second full product UI. The Slack app should let teams receive completion notifications, start work from natural engineering conversations, ask coding questions, create/open previews, and answer agent questions without leaving Slack.

The original Slack integration was oriented around channel monitoring and context sync. It could connect Slack, store a token, select channels, poll recent messages, summarize threads, and make that context available to PM/session workflows. That remains useful input context, but this design expands Slack into an interactive Slackbot surface.

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

## Implementation Status

The current implementation provides the first usable Slackbot backend surface, but the full product described in this document is not complete. This section is the source of truth for what exists today versus what still needs work.

### Implemented

- Slack OAuth stores bot install metadata in `slack_installations` and encrypted bot credential config.
- Public Slack callback endpoints exist for Events API, slash commands, and interactions:
  - `POST /api/v1/webhooks/slack/events`
  - `POST /api/v1/webhooks/slack/commands`
  - `POST /api/v1/webhooks/slack/interactions`
- Slack request signing verification, timestamp replay protection, request-size limiting, event dedupe, and URL verification are implemented.
- Supported Events API routing includes:
  - `app_mention` and `message.im` to `slack_start_or_continue_session`
  - `app_home_opened` to `slack_sync_app_home`
  - `app_uninstalled` / `app_uninstalled_team` disconnect handling
  - `app_rate_limited` logging/metrics
  - `member_joined_channel` setup messaging when the joined user is the bot
- Slash commands enqueue Slack-started sessions.
- Interactions route by `action_id` / `callback_id` to repository selection, App Home start, account-link prompt, human-input answers, channel setup/configuration, and preview controls.
- Slack App Home renders sections for start, pending responses, recent Slack-started sessions, active previews, recent automation runs, organization display, and Slack connection basics.
- App Home can open a start-session modal, and modal submission starts a normal Slack-origin session.
- Slack-started mention, DM, slash, and App Home sessions create or continue canonical 143 sessions with `origin = slack`.
- Slack thread reuse checks the linked session status and starts a fresh session instead of continuing terminal non-resumable sessions.
- Slack thread links persist team, channel/DM, thread key, permalink, triggering Slack user, mapped 143 user, and team-session flag in `slack_session_links`.
- Slack user mapping supports self-linking from the authenticated product API and best-effort email matching from Slack profile data when scopes allow it.
- Admin Slack user mapping APIs support creating, replacing, and deleting `admin_linked` Slack-to-143 user mappings.
- Channel settings persist default repository, default branch, response visibility, allowed actions, and notification subscription JSON.
- Authenticated Slack settings APIs expose bot install metadata, bot reinstall, bot-visible channels with connected settings, channel setting updates, user-link listing, self-link, and self-unlink.
- Slack thread context, detected references, and file metadata are bounded and included in the initial session prompt.
- Slack acks and final replies are posted back to Slack, and final replies truncate long assistant responses with a session link fallback.
- Final Slack replies append concrete user-facing outcome context when available: branch URL, PR URL/status/CI state, preview URL/status, and compact diff stats.
- Slack progress/final/human-input/notification messages are recorded in `slack_outbound_messages`.
- Human-input requests can be delivered to the originating Slack thread and answered from Slack through choice buttons or a free-form modal by linked users.
- Slack notifications can fan out to subscribed channels or configured Slack user DMs from channel setting subscription JSON.
- Implemented notification event paths include session completion/failure, automation completion/failure through session completion hooks, PR opened, preview ready/failed, and human-input requested.
- Preview actions are wired for open, refresh/restart, stop, and extend. Refresh/restart currently both recycle the preview.
- Slack preview actions use a shared Slack authorization service for channel capability checks, mapped-user membership roles, and the narrow unmapped-user allowance for originating team sessions.
- Slack channel invite setup posts a channel-visible setup message with configure/start actions.
- Channel `response_visibility = dm` is honored for Slack-started session acks, progress updates, final replies, and human-input delivery. DM delivery opens a bot DM to the originating Slack user and falls back to the thread if a DM cannot be opened.
- Stored Slack inbound payloads redact known transient/secret fields such as `response_url`, `trigger_id`, and `token`.
- Slackbot metrics exist for inbound events, session starts, outbound messages, Slack API failures, interaction actions, Slack Events API rate-limit signals, and Slack message update latency.

### Remaining Work

- App Home organization selection is display-only. Multi-org Slack workspaces still need an actionable org selector before Slack actions can target alternate orgs.
- App Home account linking is not Slack-native end-to-end. The current flow points users to the web integrations surface.
- App Home active previews and automation runs are based on linked-user ownership, not full subscription membership.
- App Home Slack connection health does not yet show detailed install health, scopes, or remediation state.
- Repository selection is offered for some missing-repository starts, but there are no full missing-context flows for preview target, PR, branch, issue, Sentry, or session resolution.
- Mention/DM context detection does not yet resolve repository, PR, issue, Sentry URL, preview URL, branch, or file paths into structured typed context. Most detected references are still prompt text.
- Channel `response_visibility = dm` is not yet applied to general notification delivery; notification destinations still come from subscription JSON.
- Slack progress rendering is still coarse. Runtime/tool/test/command/preview milestones are not normalized into sparse Slack updates.
- Slack progress updates are not debounced by a per-thread timing policy.
- The initial ack message is not consistently updated into the terminal state; later progress/final messages may be separate.
- Final Slack output does not yet include next-action buttons for all outcomes.
- Human-input requests do not have assigned-user DM delivery because the durable request model has no assigned-user field.
- Human-input routing does not distinguish sensitive/personal requests from team-thread requests.
- Team sessions with no mapped user cannot yet be claimed and answered from Slack by any authorized 143 user.
- Specialized Slack handling for approval/deny, tool or command approval, and continue/stop/resume decisions is still generic choice/freeform behavior.
- `automation.run.failure_streak` is not emitted.
- `preview.stale` is not emitted.
- Automation notification content is still basic and does not consistently include run result, PR links, preview links, and next actions.
- Notification subscription management is only raw channel setting JSON; there is no Slack-native or richer product management flow.
- Preview actions that are not tied to a Slack-originating team session, such as App Home preview controls or standalone preview notifications without session identity, still require a mapped authorized 143 user.
- Channel configuration from Slack does not manage notification subscriptions.
- The setup message's App Home action currently opens the start-session flow rather than switching Slack to App Home.
- Slack-started sessions can be initiated by App Home, slash command, mention, and DM, but not generally by a "start work" button from arbitrary notifications/session updates.
- If a linked Slack thread starts a fresh session after a non-resumable terminal session, the link is repointed to the new session; there is still no separate historical new-thread-message pointer model.
- Team sessions are stored but not clearly surfaced in Slack or 143 as "started from Slack without a mapped 143 user."
- Slack session creation uses DB stores directly rather than the same higher-level creation service as `/sessions/new`.
- Direct preview creation for PR, branch, commit, or repository is not implemented.
- Slack preview creation for a session is currently an agent continuation prompt, not a direct durable preview-control-plane create call.
- `Open preview` links to the 143 preview page, not necessarily a short-lived isolated preview-origin URL.
- Admin-managed Slack user mapping APIs exist at the backend/API layer; richer product UI still needs to expose them.
- RBAC enforcement is partial. Preview controls now use shared mapped-user/channel/team-session authorization, but session start, PR requests, and other user-specific actions need the same consistent mapped-user authorization.
- User-authored PR creation from Slack is not implemented.
- Slack install resolution is team/app based and does not fully handle enterprise-aware multi-org Slack workspaces.
- Slash commands always enqueue a session; they do not open missing-context modals.
- Interactions still pass `trigger_id` through queued job payloads for modal-opening actions. Inbound event payloads redact it, but jobs still store it until the interaction handling model is moved partly into the synchronous callback path.
- Raw Slack message text is stored in inbound event payloads for audit/retry visibility; private-message minimization has not been designed.
- Session attribution is represented by `origin = slack` and `slack_session_links`, but there is no `session_attribution` row or `source_metadata` field carrying sanitized Slack source metadata.
- Slack context enters sessions as prompt text rather than structured attachments/references.

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

## Engineering Implementation

The Slackbot implementation should be built as a first-class integration surface, not as special cases inside session handlers. Slack inbound handlers should authenticate Slack, normalize the request into org-scoped events, and enqueue jobs. Session creation, preview control, human-input responses, and notifications should continue through the same service/job paths used by the web app.

### Slack App Configuration

Use one Slack App with bot user enabled.

Request URLs:

- Events API: `POST /api/v1/webhooks/slack/events`
- Slash commands: `POST /api/v1/webhooks/slack/commands`
- Interactivity and modals: `POST /api/v1/webhooks/slack/interactions`
- OAuth callback: existing `/api/v1/integrations/slack/callback`, upgraded to bot install semantics.

Event subscriptions:

- `url_verification`: required by Slack during Events API setup.
- `app_mention`: starts or continues Slack-thread sessions from channel mentions.
- `message.im`: starts or continues sessions from bot DMs.
- `app_home_opened`: renders or refreshes App Home.
- `app_uninstalled` / `app_uninstalled_team`: marks installation disconnected.
- `app_rate_limited`: logs Slack event delivery pressure.
- `member_joined_channel`: optional, useful for detecting when the bot was invited and posting channel setup.

Do not subscribe to generic `message.channels` or `message.groups` for passive channel listening in v1. Mention-only channel behavior comes from `app_mention`.

Bot scopes:

- `app_mentions:read`: receive direct mentions of the app.
- `im:history`: receive DMs sent to the bot.
- `chat:write`: post, update, and delete bot messages.
- `commands`: slash commands, if `/143` is supported.
- `users:read`: read basic Slack user profile data.
- `users:read.email`: best-effort Slack-to-143 user mapping by email.
- `channels:read`: resolve public channel metadata.
- `groups:read`: resolve private channel metadata where the app is present.
- `channels:history`: fetch public-channel thread context when the app is in the channel.
- `groups:history`: fetch private-channel thread context when the app is in the channel.
- `im:read`: resolve DM channel metadata.
- `im:write`: open DMs for notifications and human-input requests.
- `files:read`: optional, for reading files attached to messages used as session context.
- `links:read` / `links:write`: optional later phase for 143 URL unfurls.

`chat:write.public` should not be required for v1 because the bot should only post in DMs or channels where it is installed/invited. Add it only if the product intentionally supports posting to public channels the app has not joined.

Web API methods used:

- `oauth.v2.access`: exchange OAuth code for bot token and install metadata.
- `auth.test`: validate bot token and discover bot identity.
- `chat.postMessage`: post ack, progress, final result, and notifications.
- `chat.update`: update the initial status/progress message.
- `chat.delete`: optional cleanup of obsolete progress messages.
- `chat.postEphemeral`: show private account-link or permission prompts.
- `conversations.replies`: fetch thread context for mentioned threads.
- `conversations.info`: resolve channel names and privacy.
- `conversations.open`: open DM channels for notifications.
- `views.publish`: render App Home.
- `views.open` / `views.update`: modals for missing repository/preview context.
- `users.info` / `users.lookupByEmail`: map Slack users to 143 users.
- `files.info`: optional attachment metadata lookup.
- `chat.unfurl`: optional later phase for 143 URL unfurls.

Slack docs references: `app_mention` requires `app_mentions:read`, DMs use `message.im` with `im:history`, `chat:write` allows posting/updating messages, `commands` enables slash commands, and `users:read.email` is required for user email access. See Slack docs for [`app_mention`](https://docs.slack.dev/reference/events/app_mention/), [`message.im`](https://docs.slack.dev/reference/events/message.im), [`chat:write`](https://docs.slack.dev/reference/scopes/chat.write), [`commands`](https://docs.slack.dev/reference/scopes/commands), [`users:read.email`](https://docs.slack.dev/reference/scopes/users.read.email), and [Events API](https://docs.slack.dev/apis/events-api/).

### Public Webhook APIs

These endpoints are unauthenticated from the 143 session-cookie perspective. They must authenticate Slack using the Slack signing secret.

#### `POST /api/v1/webhooks/slack/events`

Accepts Slack Events API envelopes.

Behavior:

1. Read body with a 1MB limit.
2. Verify `X-Slack-Signature` and `X-Slack-Request-Timestamp`; reject stale timestamps.
3. Handle `url_verification` inline by returning the `challenge`.
4. Resolve installation by `team_id` / `enterprise_id` / `api_app_id`.
5. Deduplicate by Slack `event_id`.
6. Ignore bot/self messages.
7. Persist inbound event metadata.
8. Enqueue the appropriate job.
9. Return 200 quickly.

Job routing:

- `app_mention` -> `slack_start_or_continue_session`
- `message.im` -> `slack_start_or_continue_session`
- `app_home_opened` -> `slack_sync_app_home`
- `app_uninstalled*` -> `slack_mark_installation_inactive`
- `app_rate_limited` -> log/metric only

#### `POST /api/v1/webhooks/slack/commands`

Accepts slash command payloads such as `/143`.

Behavior:

1. Verify Slack signature.
2. Resolve installation and user.
3. Deduplicate by a hash of team/channel/user/command/trigger timestamp where Slack does not provide a durable event ID.
4. Ack with a lightweight ephemeral or in-channel response.
5. Enqueue `slack_start_or_continue_session` or open a modal when required context is missing.

#### `POST /api/v1/webhooks/slack/interactions`

Accepts button, select, modal, and App Home interaction payloads.

Behavior:

1. Verify Slack signature.
2. Decode the `payload` form field.
3. Resolve installation and user.
4. Deduplicate by action payload ID / view ID / message timestamp tuple.
5. Route by `callback_id` / `action_id`.
6. Ack quickly.
7. Enqueue mutating work.

Representative action IDs:

- `slack_select_repository`
- `slack_open_session`
- `slack_create_preview`
- `slack_refresh_preview`
- `slack_extend_preview`
- `slack_answer_human_input`
- `slack_link_account`

### Authenticated Product APIs

Admin/settings APIs should stay under normal app auth and RBAC.

- `GET /api/v1/integrations/slack/bot`: install metadata, scopes, bot user, health.
- `POST /api/v1/integrations/slack/bot/reinstall`: start upgraded OAuth flow.
- `GET /api/v1/integrations/slack/channels`: Slack channels visible to bot, with connected settings.
- `PATCH /api/v1/integrations/slack/channels/{slack_channel_id}`: set default repository, allowed actions, notification subscriptions.
- `GET /api/v1/integrations/slack/user-links`: list Slack user mappings.
- `POST /api/v1/integrations/slack/user-links`: admin-managed create/replace Slack user mapping with `source = admin_linked`.
- `DELETE /api/v1/integrations/slack/user-links/{id}`: admin-managed delete for a Slack user mapping.
- `POST /api/v1/integrations/slack/user-links/me`: link current 143 user to Slack user.
- `DELETE /api/v1/integrations/slack/user-links/me`: unlink current user.

### Data Model

All new tables are org-scoped. Secrets stay in encrypted credential storage, not in integration config or these tables.

#### `slack_installations`

One active install per `(org_id, team_id, api_app_id)`.

```sql
CREATE TABLE slack_installations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid NOT NULL REFERENCES integrations(id),
    team_id text NOT NULL,
    team_name text NOT NULL DEFAULT '',
    enterprise_id text,
    api_app_id text NOT NULL DEFAULT '',
    bot_user_id text NOT NULL DEFAULT '',
    bot_id text NOT NULL DEFAULT '',
    scope text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'active',
    installed_by_user_id uuid REFERENCES users(id),
    installed_at timestamptz NOT NULL DEFAULT now(),
    last_event_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, team_id, api_app_id)
);
```

Encrypted Slack credential config should include:

```json
{
  "access_token": "xoxb-...",
  "team_id": "T...",
  "team_name": "Engineering",
  "bot_user_id": "U...",
  "bot_id": "B...",
  "scope": "app_mentions:read,chat:write,..."
}
```

The Slack signing secret is app-level. SaaS can keep it in env/config. Self-hosted deployments can configure it through existing protected credential/env paths. Do not expose it in integration API responses.

#### `slack_user_links`

Best-effort identity mapping. User ID is nullable to support observed Slack users before they link.

```sql
CREATE TABLE slack_user_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    user_id uuid REFERENCES users(id),
    slack_team_id text NOT NULL,
    slack_user_id text NOT NULL,
    slack_email text,
    slack_display_name text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'observed',
    linked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_user_id),
    UNIQUE (org_id, user_id, slack_team_id)
);
```

`source` values: `observed`, `email_match`, `self_linked`, `admin_linked`.

#### `slack_channel_settings`

Channel defaults and allowed Slack-started capabilities.

```sql
CREATE TABLE slack_channel_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_channel_name text NOT NULL DEFAULT '',
    channel_type text NOT NULL DEFAULT 'channel',
    default_repository_id uuid REFERENCES repositories(id),
    default_branch text,
    response_visibility text NOT NULL DEFAULT 'thread',
    allowed_actions text[] NOT NULL DEFAULT '{session,preview}',
    notification_subscriptions jsonb NOT NULL DEFAULT '{}'::jsonb,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id)
);
```

`allowed_actions` examples: `session`, `preview`, `pr_request`, `human_input`.

#### `slack_session_links`

Connects Slack threads/DMs to canonical 143 sessions.

```sql
CREATE TABLE slack_session_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_thread_ts text NOT NULL,
    slack_root_ts text NOT NULL DEFAULT '',
    slack_message_permalink text NOT NULL DEFAULT '',
    slack_user_id text NOT NULL DEFAULT '',
    mapped_user_id uuid REFERENCES users(id),
    team_session boolean NOT NULL DEFAULT false,
    latest_status_message_ts text,
    final_message_ts text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id, slack_thread_ts)
);
```

When another `@143` mention lands in the same Slack thread, reuse the existing session when it is resumable; otherwise create a new session and link it to the same Slack thread with a new thread-message pointer.

#### `slack_inbound_events`

Deduplication, audit, and retry visibility for Slack callbacks.

```sql
CREATE TABLE slack_inbound_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_event_id text,
    slack_team_id text NOT NULL,
    event_type text NOT NULL,
    channel_id text,
    user_id text,
    event_ts text,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'received',
    job_id uuid,
    error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE (org_id, slack_event_id)
);
```

For command/interaction payloads without Slack `event_id`, compute a deterministic idempotency key and store it in `slack_event_id`.

#### `slack_outbound_messages`

Tracks posted/updated Slack messages so jobs can update progress and final output idempotently.

```sql
CREATE TABLE slack_outbound_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_session_link_id uuid REFERENCES slack_session_links(id) ON DELETE CASCADE,
    notification_id uuid,
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_message_ts text NOT NULL,
    message_kind text NOT NULL,
    status text NOT NULL DEFAULT 'posted',
    last_payload_hash text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id, slack_message_ts)
);
```

`message_kind` examples: `ack`, `progress`, `final`, `notification`, `human_input`.

### Job Payloads

#### `slack_start_or_continue_session`

```json
{
  "org_id": "uuid",
  "slack_inbound_event_id": "uuid",
  "slack_installation_id": "uuid",
  "team_id": "T123",
  "channel_id": "C123",
  "thread_ts": "1710000000.000000",
  "message_ts": "1710000001.000000",
  "slack_user_id": "U123",
  "text": "<@U143> fix this",
  "permalink": "https://...",
  "source": "app_mention"
}
```

Handler responsibilities:

1. Load channel settings and user mapping.
2. Fetch thread context with `conversations.replies` when useful and permitted.
3. Resolve repository/preview/session references.
4. Create or resume a 143 session. Existing Slack thread links continue active or resumable sessions; terminal non-resumable sessions start a fresh session and repoint the Slack thread link.
5. Persist `slack_session_links`.
6. Post/update the Slack ack message with the session URL.
7. Enqueue normal `run_agent` / `continue_session` work.

#### `slack_post_run_update`

```json
{
  "org_id": "uuid",
  "session_id": "uuid",
  "slack_session_link_id": "uuid",
  "update_kind": "tool_started",
  "title": "Running tests",
  "summary": "npm test",
  "terminal": false
}
```

This job is fed by session/runtime events. It should debounce noisy updates and render only meaningful progress to Slack.

#### `slack_post_final_response`

```json
{
  "org_id": "uuid",
  "session_id": "uuid",
  "slack_session_link_id": "uuid",
  "final_message_id": "uuid"
}
```

Handler responsibilities:

1. Load the final assistant/session message.
2. Render that final response directly with Slack formatting.
3. Append session/preview/PR links.
4. Update the ack/progress message to terminal state.
5. Post or update the final Slack message idempotently.

#### `slack_handle_interaction`

Routes button/select/modal actions to existing services:

- Repository selection -> update `slack_channel_settings` or `slack_session_links` context and continue session.
- Human-input answer -> existing human-input answer API/service.
- Preview actions -> durable preview control plane.
- Open session -> link button only, no mutation.

#### `slack_sync_app_home`

Builds the user's App Home view from:

- linked user identity,
- pending human-input requests,
- Slack-started active sessions,
- active previews,
- recent automation runs,
- account-link/install health.

### Session Integration Points

Slack-started sessions should use the same session creation service as `/sessions/new`, with a `source = slack` or equivalent provenance field. If the current session model cannot represent team sessions without a user, add explicit nullable attribution fields instead of faking a user:

- `created_by_user_id` nullable for team sessions, or a separate `session_attribution` row.
- `source = slack`.
- `source_metadata` containing Slack team/channel/thread IDs, sanitized and non-secret.

Thread context should enter the session as structured attachments/references:

- Slack permalink.
- Root message text.
- Selected replies.
- File metadata/URLs when allowed.
- Detected references.

Do not paste unbounded Slack history into prompts. Bound by message count, size, and recency, and preserve the full permalink for human inspection.

### Progress Rendering

Session/runtime events should be normalized before reaching Slack:

- `session.created` -> ack with session URL.
- `context.resolved` -> repository/branch status.
- `tool.started` -> short "Calling tool" or "Running command" update.
- `tool.completed` -> only render if long-running or user-relevant.
- `preview.started` / `preview.ready` / `preview.failed` -> preview status.
- `human_input.requested` -> Slack action block.
- `session.completed` -> final agent response.
- `session.failed` -> concise failure message plus session URL.

Slack progress updates should be debounced, for example at most once every 5-10 seconds per Slack thread unless the event requires user input or reaches a terminal state.

### Security and Reliability

- Verify Slack signatures for every inbound endpoint before parsing payload semantics.
- Reject request timestamps outside a narrow replay window.
- Return 200 quickly for Slack callbacks; all slow work happens in jobs.
- Deduplicate events and interactions before enqueueing jobs.
- Never store Slack bot tokens, signing secrets, response URLs, or trigger IDs in plaintext tables.
- Do not log raw Slack payloads at info level; scrub tokens, response URLs, and private message text.
- Ignore messages from the Slackbot itself and other bot messages unless explicitly allowed.
- Apply org/channel allowed actions before starting previews or PR-related actions for team sessions.
- For mapped users, enforce normal 143 RBAC.
- For unmapped users, create team sessions only within channel defaults.
- Keep Slack output bounded. Long final responses should be truncated with a link to 143.
- Add metrics for inbound events, dedupe hits, session starts, Slack API failures, rate limits, and message update latency.

The existing Slack polling/summarization worker can remain separate. Its purpose is context ingestion, not interactive bot handling.

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
- Improve final Slack response rendering for questions, investigations, fixes, and preview requests.
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
