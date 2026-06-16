# 101 - Slackbot Implementation Plan

> **Status:** Phases 1-8 implemented; remaining follow-up is enterprise
> multi-org retargeting beyond the current single-active-org Slack install
> model
> **Last reviewed:** 2026-06-15
>
> **Builds on:** [../future/92-slackbot-product-surface.md](../future/92-slackbot-product-surface.md)

## Purpose

Slack should become a first-class way to use 143, not a secondary notification
pipe or a thin wrapper around the web app. The product-surface design defines
what the Slackbot should feel like: users mention `@143` or DM the bot, the bot
starts or continues normal 143 sessions, teammates can follow progress in the
Slack thread, and the canonical transcript, diffs, previews, pull requests, and
logs remain in 143.

This document turns that product direction into an engineering implementation
plan. It focuses on the remaining work needed to make Slack feel complete,
safe, and predictable: lifecycle rendering, admin configuration, context
resolution, preview and PR controls, human-input delivery, notifications,
identity, authorization, privacy, and operations.

## Product Goal

The target experience is simple:

1. A user mentions `@143` in a channel or sends a DM.
2. Slack acknowledges quickly and shows what 143 is doing.
3. 143 resolves enough context to run useful work, asking for missing context in
   Slack only when needed.
4. The agent runs as a normal 143 session.
5. Slack shows sparse progress and a polished final result with links to the
   session, PR, preview, or next action.

Slack remains a lightweight collaboration surface. It should not become a
second full session UI, an unbounded agent log stream, or a passive channel
listener. The bot responds in channels only when explicitly invoked, while DMs
act as direct conversations with the bot.

## Product Principles

- **Mention-first:** `@143` should be enough to start useful work. Users should
  not have to choose a mode before the bot does anything.
- **Canonical session:** Every meaningful Slack interaction should create or
  continue a normal 143 session. Slack mirrors status and outcome; 143 owns the
  durable transcript and controls.
- **Ask only when necessary:** Missing repository, branch, PR, preview, or user
  context should be gathered through concise Slack modals or selects.
- **Sparse progress:** Slack updates should be useful and quiet. Major
  milestones belong in Slack; raw logs, token counts, sandbox IDs, and long
  command output belong in 143.
- **Safe by default:** Channel capabilities, mapped-user roles, team-session
  limits, and privacy rules must apply before Slack actions mutate product
  state.
- **Admin clarity:** Settings should clearly distinguish interactive bot
  behavior, PM/context monitoring, notifications, and account linking.

## Product Surface Adjustments

The implementation should optimize for the simplest successful Slack path:
mention the bot, see what context it inferred, correct it if needed, and join
the canonical 143 session when deeper control is useful.

### Defaults First

The main Slack settings surface should start with Slackbot defaults, not a long
list of channels. Defaults should cover repository, branch, routing mode,
response visibility, allowed actions, and notification preset. Channel rows
should appear as inherited defaults unless an explicit override exists.

### Visible Inferred Context

Every Slack-started session ack should show the inferred context before or as
work begins:

```text
Starting a 143 session

Repo: assembledhq/143
Branch: main
Mode: Start work

[Join session] [Change repo] [Answer only]
```

The goal is to make the bot feel smart but correctable. If repo, branch, PR, or
preview target inference is uncertain, Slack should ask through a small modal
or select menu instead of launching vague work.

### Routing Modes

Natural language remains the default interface, but users and admins need
simple routing controls:

- `Auto`: infer whether the request needs a quick answer or durable work.
- `Answer only`: answer in Slack and create a lightweight session record.
- `Start work`: create or continue a normal work session immediately.

Slack should support explicit message-level overrides such as `@143 ask ...`
and `@143 start ...`, plus buttons like `Answer only` and `Start work from
this` when the inferred route is wrong.

### App Home

Slack App Home should be a personal command center, not the full admin settings
surface. It should focus on:

- account connection
- starting work
- pending responses
- active sessions
- recent results
- personal defaults

Implemented note: App Home now includes personal defaults from Slackbot settings
alongside pending responses, recent Slack-started work, active previews,
subscribed automation runs, and explicit org selection.

Deep installation, channel, notification, and user-link management belongs in
the web settings UI, with Slack modals reserved for quick channel setup and
missing-context collection.

### Active Work Cards

Ack messages, App Home entries, notifications, and final replies should use a
consistent active-work shape:

- title
- repo and branch
- status
- requester
- routing mode
- next action
- links to session, PR, and preview when available

Use `Join session` as the primary label when the destination is the live 143
session. Secondary actions should be context-specific: `Change repo`, `Open
preview`, `Review PR`, or `View changes`.

### Notification Presets

Notifications should expose presets before event-level checkboxes:

- `Quiet`: human input and failures only.
- `Balanced`: failures, completed Slack-started work, PR opened, and preview
  ready.
- `Verbose`: all session, automation, PR, preview, and human-input events.
- `Custom`: advanced event-family selection.

The detailed event list remains available for teams that need it, but first-run
configuration should not start with a large matrix of event names.

## Current Implementation Snapshot

The backend already provides the first interactive Slackbot surface:

- Slack OAuth stores bot credentials and install metadata.
- Signed Slack callbacks exist for Events API, slash commands, and
  interactions.
- `app_mention`, `message.im`, slash command, and App Home starts enqueue
  `slack_start_or_continue_session`.
- Slack-started sessions persist `slack_session_links`, sanitized
  `session_attributions`, acks, final replies, and outbound message metadata.
- Slack App Home, channel setup, human-input answers, preview actions, and
  notification fanout have backend paths.
- Shared Slack authorization checks channel capabilities, mapped-user roles, and
  originating team-session allowances.

Phases 1-8 of this plan are implemented: Slack lifecycle rendering is
centralized, progress updates are normalized and de-duplicated, settings expose
Slackbot defaults with per-channel overrides, and Slack starts resolve default
context with routing overrides, missing-context prompts, and correction
actions. Human-input delivery now carries sensitivity and preferred-channel
metadata, Slack approval actions are rendered and answered as approvals rather
than generic text, notification kinds are typed with template defaults, and raw
Slack inbound payloads have redaction plus a bounded retention cleanup path.
Known preview targets use direct preview control-plane paths, PR merge/create
actions use durable product jobs and services, and Slack health/metrics expose
operator-facing install and delivery signals.

## Target Architecture

Slackbot should use the same durable product paths as the web app.

```text
Slack callback
  -> verify signing secret and timestamp
  -> resolve Slack installation to org
  -> persist/dedupe inbound event
  -> enqueue Slack job
  -> worker resolves context/authorization
  -> normal session, preview, PR, notification, or human-input service path
  -> Slack lifecycle renderer posts or updates messages
```

Inbound HTTP handlers should stay thin. They authenticate Slack, normalize the
payload, persist an inbound event, and enqueue a job. Workers own contextual
work because Slack retries callbacks aggressively and expects a fast response.

## Core Domain Concepts

### Slack Installation

`slack_installations` is the org-scoped durable install record. It stores team,
enterprise, app, bot, scope, status, installer, and last-event metadata. The
bot access token remains in encrypted credential storage.

Implementation needs:

```go
type SlackInstallationHealth struct {
    Installation models.SlackInstallation `json:"installation"`
    RequiredScopes []string `json:"required_scopes"`
    MissingScopes []string `json:"missing_scopes"`
    LastEventAt *time.Time `json:"last_event_at,omitempty"`
    LastAuthCheckAt *time.Time `json:"last_auth_check_at,omitempty"`
    AuthOK bool `json:"auth_ok"`
    AuthError *string `json:"auth_error,omitempty"`
}
```

This should power the settings UI and make Slack install problems visible
without searching logs.

### Slackbot Defaults and Channel Overrides

Slackbot configuration should have two layers:

1. **Workspace/org-level defaults** for the Slackbot installation.
2. **Per-channel overrides** for channels that need different behavior.

The default path should be easy: an admin configures the Slackbot once, and new
channels inherit sensible defaults. Per-channel settings should be required
only when a channel needs a different repository, branch, response visibility,
allowed action set, routing mode, or notification policy.

Introduce a new org-scoped default settings table:

```sql
CREATE TABLE slack_bot_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    default_repository_id uuid REFERENCES repositories(id),
    default_branch text,
    routing_mode text NOT NULL DEFAULT 'auto',
    response_visibility text NOT NULL DEFAULT 'thread',
    allowed_actions text[] NOT NULL DEFAULT '{session,preview}',
    notification_preset text NOT NULL DEFAULT 'balanced',
    notification_subscriptions jsonb NOT NULL DEFAULT '{}'::jsonb,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_installation_id)
);
```

`slack_channel_settings` should remain the per-channel override table. Its
nullable fields should mean "inherit from Slackbot defaults" where practical,
while explicit values override the defaults.

Important fields:

- `default_repository_id`
- `default_branch`
- `routing_mode`
- `response_visibility`
- `allowed_actions`
- `notification_preset`
- `notification_subscriptions`
- `active`

Resolve effective behavior through one helper so workers and APIs agree:

```go
type EffectiveSlackChannelSettings struct {
    OrgID uuid.UUID `json:"org_id"`
    SlackInstallationID uuid.UUID `json:"slack_installation_id"`
    SlackTeamID string `json:"slack_team_id"`
    SlackChannelID string `json:"slack_channel_id"`
    DefaultRepositoryID *uuid.UUID `json:"default_repository_id,omitempty"`
    DefaultBranch *string `json:"default_branch,omitempty"`
    RoutingMode SlackRoutingMode `json:"routing_mode"`
    ResponseVisibility SlackResponseVisibility `json:"response_visibility"`
    AllowedActions []SlackChannelAction `json:"allowed_actions"`
    NotificationSubscriptions json.RawMessage `json:"notification_subscriptions"`
    NotificationPreset SlackNotificationPreset `json:"notification_preset"`
    HasChannelOverride bool `json:"has_channel_override"`
}
```

Store API:

```go
func (s *SlackBotSettingsStore) GetByOrg(ctx context.Context, orgID uuid.UUID) (models.SlackBotSettings, error)
func (s *SlackBotSettingsStore) Upsert(ctx context.Context, settings *models.SlackBotSettings) error
func (s *SlackChannelSettingsStore) GetEffectiveByChannel(ctx context.Context, orgID uuid.UUID, teamID, channelID string) (models.EffectiveSlackChannelSettings, error)
```

Add typed enum validation in `internal/models` for:

```go
type SlackResponseVisibility string

const (
    SlackResponseVisibilityThread SlackResponseVisibility = "thread"
    SlackResponseVisibilityDM     SlackResponseVisibility = "dm"
)

type SlackChannelAction string

const (
    SlackChannelActionSession    SlackChannelAction = "session"
    SlackChannelActionPreview    SlackChannelAction = "preview"
    SlackChannelActionPRRequest  SlackChannelAction = "pr_request"
    SlackChannelActionHumanInput SlackChannelAction = "human_input"
)

type SlackRoutingMode string

const (
    SlackRoutingModeAuto       SlackRoutingMode = "auto"
    SlackRoutingModeAnswerOnly SlackRoutingMode = "answer_only"
    SlackRoutingModeStartWork  SlackRoutingMode = "start_work"
)

type SlackNotificationPreset string

const (
    SlackNotificationPresetQuiet    SlackNotificationPreset = "quiet"
    SlackNotificationPresetBalanced SlackNotificationPreset = "balanced"
    SlackNotificationPresetVerbose  SlackNotificationPreset = "verbose"
    SlackNotificationPresetCustom   SlackNotificationPreset = "custom"
)
```

Settings APIs use typed request structs rather than raw anonymous handler
structs so validation can be tested directly. The UI presents Slackbot defaults
first, then shows channel rows as "inheriting defaults" unless an override
exists.

### Slack Session Link

`slack_session_links` connects a Slack thread or DM to a canonical 143 session.
It already tracks the team, channel, thread key, permalink, Slack user, mapped
143 user, team-session flag, latest status message, and final message.

Needed addition:

```go
type SlackSessionClaim struct {
    ID uuid.UUID `db:"id" json:"id"`
    OrgID uuid.UUID `db:"org_id" json:"org_id"`
    SlackSessionLinkID uuid.UUID `db:"slack_session_link_id" json:"slack_session_link_id"`
    ClaimedByUserID uuid.UUID `db:"claimed_by_user_id" json:"claimed_by_user_id"`
    ClaimedBySlackUserID string `db:"claimed_by_slack_user_id" json:"claimed_by_slack_user_id"`
    ClaimedAt time.Time `db:"claimed_at" json:"claimed_at"`
}
```

This turns `slack_claim_team_session` from an ephemeral acknowledgement into a
durable ownership transition.

### Slack Context References

Slack-started sessions should carry structured context, not only prompt text.

Needed model:

```go
type SlackContextReference struct {
    Kind SlackContextReferenceKind `json:"kind"`
    Value string `json:"value"`
    Source string `json:"source"` // message, thread, attachment, modal
    ResolvedID *uuid.UUID `json:"resolved_id,omitempty"`
    Metadata map[string]any `json:"metadata,omitempty"`
}

type SlackContextReferenceKind string

const (
    SlackContextRepository SlackContextReferenceKind = "repository"
    SlackContextPullRequest SlackContextReferenceKind = "pull_request"
    SlackContextIssue SlackContextReferenceKind = "issue"
    SlackContextSentry SlackContextReferenceKind = "sentry"
    SlackContextPreview SlackContextReferenceKind = "preview"
    SlackContextBranch SlackContextReferenceKind = "branch"
    SlackContextFilePath SlackContextReferenceKind = "file_path"
    SlackContextURL SlackContextReferenceKind = "url"
)
```

Slack starts serialize these into the session prompt and persist them as
first-class `SessionInputReference` records on the created or continued session
message. That keeps Slack context shared with web, external API, and downstream
agent paths without adding a Slack-only reference table.

## Implementation Phases

### Phase 1 - Slack Lifecycle Rendering

Status: implemented.

Audit notes:

- No remaining phase 1 gaps found. Ack, progress, waiting, terminal, and final
  response copy now flow through the Slack lifecycle renderer, with final
  content truncation, session links, and outcome actions covered by tests.

Goal: every Slack-started session reads cleanly from ack to final result.

Implement a small Slack lifecycle package, for example
`internal/services/slackbot/lifecycle.go`, that owns message text and Block Kit
rendering for:

- ack
- running
- waiting for human input
- completed
- failed
- final answer

Suggested structs:

```go
type SessionLifecycleState string

const (
    SessionLifecycleStarting SessionLifecycleState = "starting"
    SessionLifecycleRunning  SessionLifecycleState = "running"
    SessionLifecycleWaiting  SessionLifecycleState = "waiting"
    SessionLifecycleComplete SessionLifecycleState = "complete"
    SessionLifecycleFailed   SessionLifecycleState = "failed"
)

type SlackSessionRenderInput struct {
    OrgID uuid.UUID
    Session models.Session
    Link models.SlackSessionLink
    State SessionLifecycleState
    Title string
    Summary string
    Context SlackSessionContextSummary
    RoutingMode SlackRoutingMode
    Outcome SlackSessionOutcome
    TeamSessionClaimable bool
}

type SlackSessionContextSummary struct {
    RepositoryName string
    Branch string
    PullRequestURL string
    PreviewURL string
    Confidence string // high, medium, low
    Missing []MissingSlackContext
}

type SlackSessionOutcome struct {
    BranchURL string
    PullRequest *models.PullRequest
    Preview *models.Preview
    PreviewURL string
    DiffStats json.RawMessage
    RequiredNextAction string
}
```

`newSlackPostRunUpdateHandler` and `newSlackPostFinalResponseHandler` should
delegate all user-facing copy to this renderer.

Acceptance criteria:

- The original ack/status message is updated to terminal state.
- The ack shows inferred repo, branch, and routing mode when known.
- The primary action is `Join session` once a canonical session exists.
- Final answer is posted once, with durable links and outcome context.
- Long final content is truncated with a clear session fallback.
- Failures produce a user-facing Slack reply.

### Phase 2 - Progress Event Normalization and Debouncing

Status: implemented.

Audit notes:

- No remaining phase 2 gaps found. Progress events are normalized to Slack-safe
  kinds, duplicate kinds and rapid non-terminal updates are suppressed, and
  terminal updates bypass the debounce policy.

Goal: Slack progress is sparse, stable, and meaningful.

Add a progress normalizer that converts runtime/session events into Slack-safe
updates:

```go
type SlackProgressKind string

const (
    SlackProgressReadingContext SlackProgressKind = "reading_context"
    SlackProgressResolvingContext SlackProgressKind = "resolving_context"
    SlackProgressRunningAgent SlackProgressKind = "running_agent"
    SlackProgressRunningCommand SlackProgressKind = "running_command"
    SlackProgressRunningTests SlackProgressKind = "running_tests"
    SlackProgressStartingPreview SlackProgressKind = "starting_preview"
    SlackProgressWaitingForInput SlackProgressKind = "waiting_for_input"
    SlackProgressCompleted SlackProgressKind = "completed"
    SlackProgressFailed SlackProgressKind = "failed"
)

type SlackProgressUpdate struct {
    Kind SlackProgressKind `json:"kind"`
    Title string `json:"title"`
    Summary string `json:"summary,omitempty"`
    Terminal bool `json:"terminal"`
    OccurredAt time.Time `json:"occurred_at"`
}
```

Persist or compute a per-thread update policy:

```go
type SlackProgressPolicy struct {
    MinUpdateInterval time.Duration
    AlwaysSendTerminal bool
    SuppressDuplicateKind bool
}
```

The handler should skip updates that are too frequent or semantically
duplicative unless they are terminal.

Acceptance criteria:

- No Slack thread receives rapid-fire tool updates.
- Major events are visible.
- Terminal updates always appear.

### Phase 3 - Settings and Admin UX

Status: implemented.

Audit notes:

- No remaining phase 3 gaps found. The backend APIs, Slackbot defaults,
  inherited channel overrides, PM/context monitoring labeling, install health,
  reinstall action, admin user-link management, and custom notification event
  controls are implemented.

Implemented:

- Slack health, settings, channel list, channel override, and user-link APIs
  exist.
- Slackbot defaults cover repository, branch, routing mode, response
  visibility, allowed actions, and notification preset.
- Channel settings inherit defaults and can override routing and notification
  preset from the web settings sheet.
- The web settings sheet includes admin Slack user-link management backed by
  the user-link APIs.
- The web settings sheet exposes custom notification event subscriptions for
  teams that need event-family tuning.
- The old channel monitoring flow is relabeled as PM/context monitoring.
- Missing scopes and auth failures are visible with a reinstall action.

Goal: admins can understand and control Slack behavior without reading code.

Add product APIs:

```http
GET /api/v1/integrations/slack/health
GET /api/v1/integrations/slack/settings
PATCH /api/v1/integrations/slack/settings
GET /api/v1/integrations/slack/channels
PATCH /api/v1/integrations/slack/channels/{slack_channel_id}
GET /api/v1/integrations/slack/user-links
POST /api/v1/integrations/slack/user-links
DELETE /api/v1/integrations/slack/user-links/{id}
```

`GET /channels` should return both legacy monitoring selection and bot settings
so the UI can distinguish them:

```go
type SlackChannelAPI struct {
    ID string `json:"id"`
    Name string `json:"name"`
    Type string `json:"type"`
    BotConfigured bool `json:"bot_configured"`
    MonitoringEnabled bool `json:"monitoring_enabled"`
    Settings *models.SlackChannelSettings `json:"settings,omitempty"`
    EffectiveSettings models.EffectiveSlackChannelSettings `json:"effective_settings"`
}
```

Frontend settings should split Slack into:

- Install health
- Slackbot defaults
- Interactive bot channels
- PM/context monitoring
- Notifications
- User linking

The first-run settings flow should ask for only the defaults required to make
Slackbot usable:

1. default repository
2. default branch
3. routing mode
4. notification preset

Advanced channel overrides, custom event subscriptions, and user mapping should
be available but not required before the bot can answer or start work.

Acceptance criteria:

- Admins can configure default repo, branch, response visibility, allowed
  actions, routing mode, and notifications once for the Slackbot.
- New channels inherit Slackbot defaults without per-channel setup.
- Admins can override defaults for individual channels when needed.
- The default Slack settings page does not require admins to configure every
  channel one by one.
- The old monitoring flow is clearly labeled as context ingestion.
- Missing scopes and disconnected installs are visible with a reinstall action.

### Phase 4 - Context Resolution and Missing-Context Modals

Status: implemented.

Audit notes:

- No remaining phase 4 gaps found. Slack starts inherit effective context,
  routing overrides are honored, blocking preview/PR context prompts prevent
  vague work from starting, ack messages expose inferred context and correction
  actions, and missing-context modal submissions continue the original session
  with selected structured context.

Implemented:

- Slack starts inherit default repository, branch, and routing mode from
  effective Slack settings.
- `@143 ask ...` and `@143 start ...` override the default routing mode.
- The resolver classifies missing repository, preview target, and PR context,
  and blocking preview/PR missing context prevents vague agent work from
  starting until the user supplies the target.
- Slack acks show inferred repo, branch, PR, preview, and routing mode when
  known, with correction actions such as `Change repo`, `Start work`, `Choose
  preview target`, and `Choose PR`.
- Missing-context modals use structured Slack selects for preview, PR, and
  branch targets; submissions continue the original Slack-started session with
  the selected context.

Goal: the bot asks precise follow-up questions instead of starting vague work.

Add a resolver service:

```go
type SlackContextResolver interface {
    Resolve(ctx context.Context, input SlackContextResolveInput) (SlackContextResolveResult, error)
}

type SlackContextResolveInput struct {
    OrgID uuid.UUID
    Installation models.SlackInstallation
    ChannelSettings *models.SlackChannelSettings
    Text string
    ThreadMessages []ingestion.SlackMessage
    Files []slackContextFile
    TriggeringSlackUserID string
}

type SlackContextResolveResult struct {
    References []SlackContextReference
    RepositoryID *uuid.UUID
    Branch string
    PullRequestID *uuid.UUID
    PreviewID *uuid.UUID
    RoutingMode SlackRoutingMode
    ContextSummary SlackSessionContextSummary
    Missing []MissingSlackContext
}

type MissingSlackContext struct {
    Kind string `json:"kind"` // repository, branch, pull_request, preview_target
    Reason string `json:"reason"`
    Options []SlackContextOption `json:"options,omitempty"`
}
```

When missing context blocks a specific action, open a modal:

- repository selector
- branch or PR selector
- preview target selector
- account-link prompt

Acceptance criteria:

- `@143 create a preview` asks for a target.
- `@143 fix this PR` resolves or asks for the PR.
- `@143 ask ...` and `@143 start ...` override the default routing mode.
- Slack acks expose inferred repo, branch, PR, preview, and mode with correction
  actions when useful.
- Slack modal submissions continue the original session or start it with the
  selected structured context.

### Phase 5 - Direct Preview and PR Control Paths

Status: implemented.

Audit notes:

- `SlackPreviewControl` now supports session, pull request, branch, commit, and
  repository targets. Known branch-like targets route through the branch preview
  control plane instead of asking the agent to infer the operation.
- Slack preview creation action payloads can pass `session_id`,
  `pull_request_id`, `repository_id`, `branch`, `commit_sha`, and
  `config_name`; missing structured context still opens the context modal.
- Slack PR actions merge through the PR service, request PR creation through
  the durable `open_pr` job for authorized mapped users, and keep repair as a
  continuation prompt only for agent-owned repair work.
- Slack PR merge and create actions require Slack confirmation before mutating
  repository state.
- Team-session claiming is durable through `slack_session_claims` and updates
  the session link to the claiming mapped user.

Goal: Slack buttons use product control planes, not agent prompts, whenever the
target is known.

Implement direct preview actions:

```go
type SlackPreviewTarget struct {
    Kind string `json:"kind"` // session, pull_request, branch, commit, repository
    RepositoryID uuid.UUID `json:"repository_id"`
    SessionID *uuid.UUID `json:"session_id,omitempty"`
    PullRequestID *uuid.UUID `json:"pull_request_id,omitempty"`
    Branch string `json:"branch,omitempty"`
    CommitSHA string `json:"commit_sha,omitempty"`
    ConfigName string `json:"config_name,omitempty"`
}
```

Add or route through existing preview service methods:

```go
CreatePreviewForSlack(ctx context.Context, orgID uuid.UUID, target SlackPreviewTarget, actor Actor) (models.Preview, error)
OpenPreviewURL(ctx context.Context, orgID, previewID uuid.UUID, actor Actor) (string, error)
```

PR actions:

- repair PR through a continuation prompt only when repair truly requires agent
  work
- merge through the existing PR service
- request PR creation only for mapped, authorized users
- require confirmation for merge and user-authored PR creation

Acceptance criteria:

- Known preview targets create previews without asking the agent to infer the
  operation.
- Slack preview controls match web behavior.
- PR actions enforce the same role and repository checks as web.

### Phase 6 - Human Input and Approval Semantics

Status: implemented.

Audit notes:

- Durable human-input requests include assigned user, sensitivity, and
  preferred-channel metadata.
- Personal and sensitive requests are delivered only by Slack DM when a Slack
  user target is available; otherwise Slack channel delivery is skipped and the
  canonical web/session path remains the fallback.
- Slack approval and denial buttons are rendered with explicit action IDs and
  answered as selected-choice payloads instead of generic text answers.
- Multi-choice Slack answers use a Slack multi-select modal and persist all
  selected choice IDs.
- Continue, stop, and resume render as first-class Slack action IDs while still
  persisting canonical human-input answers.
- Team-session human-input answers continue to require a linked, authorized
  user in the originating Slack channel.

Goal: Slack becomes a safe delivery channel for agent questions and approvals.

Extend durable human-input requests with delivery metadata if not already
available:

```go
type HumanInputSensitivity string

const (
    HumanInputSensitivityTeam     HumanInputSensitivity = "team"
    HumanInputSensitivityPersonal HumanInputSensitivity = "personal"
    HumanInputSensitivitySensitive HumanInputSensitivity = "sensitive"
)

type HumanInputDeliveryTarget struct {
    AssignedUserID *uuid.UUID `json:"assigned_user_id,omitempty"`
    Sensitivity HumanInputSensitivity `json:"sensitivity"`
    PreferredChannel string `json:"preferred_channel"` // slack_thread, slack_dm, web
}
```

Render Slack interactions by request type:

- freeform answer
- multiple choice
- approve/deny
- tool or command approval
- continue/stop/resume

Acceptance criteria:

- Personal/sensitive requests do not post to team channels.
- Team requests show who answered and when.
- Approval actions are not represented as generic text answers.

### Phase 7 - Notification Templates and Subscriptions

Status: implemented.

Audit notes:

- Slack notification event kinds are typed in `internal/models`.
- Notification rendering derives default titles and bodies from event kind and
  adds session, preview, and PR actions when those IDs are available.
- PR notifications carry the GitHub PR URL when available, so `Review PR`
  opens the pull request directly instead of routing through the session page.
- Subscriptions support event-family wildcards, per-automation filters, channel
  destinations, and explicit user DM destinations.
- The settings UI exposes notification presets first, then reveals custom event
  subscriptions plus per-automation IDs and explicit Slack user DM destinations
  only when Custom is selected.
- Channel `response_visibility = dm` suppresses general channel fanout while
  preserving configured DM subscribers.

Goal: Slack notifications are actionable and low-noise.

Define notification event schemas:

```go
type SlackNotificationKind string

const (
    SlackNotificationSessionCompleted SlackNotificationKind = "session.completed"
    SlackNotificationSessionFailed SlackNotificationKind = "session.failed"
    SlackNotificationAutomationCompleted SlackNotificationKind = "automation.run.completed"
    SlackNotificationAutomationFailed SlackNotificationKind = "automation.run.failed"
    SlackNotificationAutomationFailureStreak SlackNotificationKind = "automation.run.failure_streak"
    SlackNotificationPROpened SlackNotificationKind = "pr.opened"
    SlackNotificationPreviewReady SlackNotificationKind = "preview.ready"
    SlackNotificationPreviewFailed SlackNotificationKind = "preview.failed"
    SlackNotificationPreviewStale SlackNotificationKind = "preview.stale"
    SlackNotificationHumanInputRequested SlackNotificationKind = "human_input.requested"
)

type SlackNotificationRenderInput struct {
    Kind SlackNotificationKind
    Preset SlackNotificationPreset
    Title string
    Body string
    SessionID *uuid.UUID
    AutomationID *uuid.UUID
    AutomationRunID *uuid.UUID
    PullRequestID *uuid.UUID
    PreviewID *uuid.UUID
    ActorUserID *uuid.UUID
}
```

Notification subscriptions should support:

- preset-based setup for quiet, balanced, verbose, and custom modes
- channel subscriptions
- per-automation subscriptions
- individual user DM subscriptions
- event-family wildcards such as `preview.*`

Acceptance criteria:

- Every notification says what happened, what changed, and what action is
  available.
- First-run notification setup starts with presets, not raw event checkboxes.
- Channels can subscribe narrowly enough to avoid noise.
- DM visibility settings are honored consistently.

### Phase 8 - Privacy, Retention, Multi-Org, and Operations

Status: implemented.

Audit notes:

- Stored Slack callback payloads redact transient Slack fields such as
  `trigger_id`, `response_url`, legacy tokens, authed users, and authorization
  envelopes.
- Raw DM event text is redacted from stored inbound payloads; the session
  prompt and attribution metadata remain the durable product record.
- `SlackInboundEventStore.RedactPayloadsOlderThan` provides a bounded,
  org-scoped retention cleanup path for raw payload JSON.
- The active Slack installation lookup continues to enforce one active
  team/app installation globally; Slack App Home org selection explains that
  retargeting is not automatic.
- Slackbot metrics cover inbound events, session starts, outbound messages,
  Slack API failures, interaction actions, rate limits, callback latency,
  dropped updates, dedupe hits, install health, missing scopes, signature
  failures, and message-update latency. Slack delivery paths include
  org/team/channel/action/session fields in operational logs where available.
- Slack install health reports missing scopes, token auth failures, event
  delivery state, and UI-visible symptom hints for installed workspaces that
  have not delivered events, which is the common signing-secret/event-
  subscription mismatch symptom.
- Slack settings, channel settings, and user-link handlers decode into typed
  request structs with directly tested validation/conversion helpers.
- Slack-detected context references are persisted into session message
  `references` as first-class session input references, while still being
  rendered in the Slack-origin prompt for agent readability.

Goal: Slackbot is supportable and trustworthy in production.

Privacy:

- Redact transient Slack fields from all stored payloads.
- Avoid storing raw DM text beyond what is necessary for retry/debug.
- Prefer derived context records and session prompts over indefinite raw Slack
  payload retention.
- Add a retention policy for `slack_inbound_events.payload`.

Multi-org:

- Keep the current single active org per Slack team/app behavior until a full
  retargeting model exists.
- Do not silently switch orgs based on Slack user mapping alone.
- Add explicit org selection only when the installation and authorization model
  can safely target another org.

Operations:

- Add metrics for install health, missing scopes, Slack API failures, callback
  latency, dropped updates, and dedupe rates.
- Add log fields: `org_id`, `team_id`, `channel_id`, `slack_event_id`,
  `slack_action_id`, `session_id`, `request_id`.
- Add admin-visible health for signing-secret mismatch symptoms, missing scopes,
  event delivery age, and token auth failures.

Acceptance criteria:

- Operators can diagnose Slack delivery without querying raw private content.
- Product surfaces show actionable remediation before users report failures.
- Multi-org behavior remains explicit and auditable.

## API Summary

Public Slack callbacks:

```http
POST /api/v1/webhooks/slack/events
POST /api/v1/webhooks/slack/commands
POST /api/v1/webhooks/slack/interactions
```

Authenticated settings APIs:

```http
GET    /api/v1/integrations/slack/health
GET    /api/v1/integrations/slack/bot
POST   /api/v1/integrations/slack/bot/reinstall
GET    /api/v1/integrations/slack/settings
PATCH  /api/v1/integrations/slack/settings
GET    /api/v1/integrations/slack/channels
PATCH  /api/v1/integrations/slack/channels/{slack_channel_id}
GET    /api/v1/integrations/slack/user-links
POST   /api/v1/integrations/slack/user-links
DELETE /api/v1/integrations/slack/user-links/{id}
POST   /api/v1/integrations/slack/user-links/me
DELETE /api/v1/integrations/slack/user-links/me
```

Internal worker jobs:

```text
slack_start_or_continue_session
slack_sync_app_home
slack_post_run_update
slack_post_final_response
slack_deliver_human_input
slack_send_notification
slack_handle_interaction
```

Slack action IDs handled by `slack_handle_interaction`:

```text
slack_open_session
slack_start_from_home
slack_link_account
slack_select_org
slack_select_repository
slack_configure_channel
slack_choose_preview_target
slack_choose_pull_request
slack_choose_branch
slack_create_preview
slack_open_preview
slack_refresh_preview
slack_restart_preview
slack_stop_preview
slack_extend_preview
slack_claim_team_session
slack_start_work
slack_create_pr
slack_merge_pr
slack_repair_pr
slack_answer_human_input
slack_approve_human_input
slack_deny_human_input
slack_continue_human_input
slack_resume_human_input
slack_stop_human_input
slack_answer_human_input_freeform
slack_answer_human_input_multi
slack_member_joined_channel
```

## Testing Strategy

Backend:

- Handler tests for signature verification, stale timestamps, URL verification,
  dedupe, install resolution, and job payloads.
- Store tests for every new org-scoped method with explicit `org_id` filtering.
- Authorization table tests for mapped users, unmapped team sessions, channel
  capability denial, viewer denial, and cross-channel team-session denial.
- Renderer golden/table tests for lifecycle, final, failure, notification, and
  human-input Block Kit payloads.
- Worker tests for context resolution, missing-context modal routing, preview
  direct control, notification fanout, progress debounce behavior, App Home
  personal defaults, and structured Slack context reference persistence.

Frontend:

- Settings page tests for install health, interactive channels, monitoring
  channels, notifications, and user links.
- API client tests for Slack health/channel/user-link endpoints.
- Error-state tests for disconnected install, missing scopes, and Slack API
  failures.

Operational:

- Smoke test using signed fixture payloads for Events API, command, and
  interaction callbacks.
- Slack API failure simulations for `not_in_channel`, `missing_scope`,
  `invalid_auth`, and rate-limit responses.

## Rollout Plan

1. Ship lifecycle renderer behind current job paths.
2. Add health/settings APIs and admin UI without changing Slack behavior.
3. Add context resolver in observe-only mode and log missing-context decisions.
4. Enable missing-context modals for preview and repository selection.
5. Move preview creation to direct control-plane calls.
6. Add specialized human-input and approval rendering.
7. Replace basic notification messages with typed templates.
8. Add retention controls and keep multi-org retargeting as an explicit
   follow-up before expanding enterprise workspace behavior.

Each phase should update [../future/92-slackbot-product-surface.md](../future/92-slackbot-product-surface.md)
when implementation status changes.
