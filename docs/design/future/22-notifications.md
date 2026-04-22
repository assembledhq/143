# Design: Notification System

> **Status:** Not Started | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** No `notifications` table, no notification handler, no `/api/v1/notifications` endpoints, no notification service or delivery channels, no SSE hub, no digest generation jobs.

This document describes how 143.dev notifies users about events that need their attention. The system generates value asynchronously — agents run, PRs open, fixes deploy, impact is measured — and users aren't watching the dashboard. The notification system pulls them back in at the right moment.

## Overview

The notification system has four layers:

1. **Event generation** — components across the system emit notification events
2. **Routing** — events are matched to recipients based on role, repo ownership, and preferences
3. **Delivery** — events are sent to one or more channels (in-app, email, Slack, GitHub)
4. **Escalation** — time-sensitive events that go unacknowledged are re-sent or escalated

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Event     │────▶│   Router    │────▶│  Delivery   │────▶│  Channels   │
│   Sources   │     │  (who gets  │     │  (format +  │     │             │
│             │     │   what)     │     │   send)     │     │  • In-app   │
│ • Agent run │     │             │     │             │     │  • Email    │
│ • PR status │     │ preferences │     │ templates   │     │  • Slack    │
│ • Deploy    │     │ role rules  │     │ rate limit  │     │  • GitHub   │
│ • Impact    │     │ repo scope  │     │ batching    │     │             │
└─────────────┘     └─────────────┘     └─────────────┘     └─────────────┘
                                                                   │
                                                                   ▼
                                                            ┌─────────────┐
                                                            │ Escalation  │
                                                            │ (if no ack) │
                                                            └─────────────┘
```

## Notification Events

### Event Taxonomy

Events are organized by urgency tier. Tiers control default delivery behavior — users can override per event type.

#### Tier 1: Action Required (Interrupt)

These events need a human response. Default: in-app + Slack DM (or email if no Slack).

| Event | Trigger | Recipient | Why It's Urgent |
|-------|---------|-----------|-----------------|
| `run.completed.pr_opened` | Agent run succeeds, PR created | Assigned reviewer(s) | PR needs review to ship |
| `run.failed.needs_input` | Agent run failed, needs guidance | User who triggered the run | Run is stuck without input |
| `run.question` | Agent asks a clarifying question (guided mode) | User who triggered the run | Agent is paused waiting for answer |
| `deploy.regression_detected` | Post-deploy metrics worsened | PR author + on-call (if configured) | Production impact, may need revert |

#### Tier 2: Informational (Next Check-In)

These events are useful but not time-sensitive. Default: in-app only. Optionally email/Slack.

| Event | Trigger | Recipient | Content |
|-------|---------|-----------|---------|
| `run.completed.no_fix` | Agent couldn't generate a fix | User who triggered the run | Failure reason + suggestions |
| `pr.merged` | 143-generated PR was merged | User who triggered the run | Confirmation + deploy tracking starts |
| `pr.changes_requested` | Reviewer requested changes on PR | User who triggered the run | Review feedback summary |
| `pr.revision_applied` | Auto-apply re-ran with reviewer feedback | PR reviewer | Revision ready for re-review |
| `deploy.impact_measured` | Post-deploy experiment completed | User who triggered the run | Impact summary (success/no change/inconclusive) |
| `context.quality_improved` | Repo context quality score increased | Repo admins | New quality score + what improved |
| `issue.auto_triggered` | System auto-triggered an agent run | Org admins | Which issue, why it was selected |

#### Tier 3: Digest Only

These events are aggregated into periodic summaries. Never sent individually.

| Event | Content | Frequency |
|-------|---------|-----------|
| `digest.weekly` | Fixes shipped, PRs merged, issues resolved, success rate, context improvements | Weekly (Monday 9am, user timezone) |
| `digest.monthly` | Aggregate impact: total issues fixed, customer pain reduced, cost per fix, review acceptance trends | Monthly (1st of month) |

### Event Schema

```go
type NotificationEvent struct {
    ID          uuid.UUID
    OrgID       uuid.UUID
    EventType   string                 // e.g. "run.completed.pr_opened"
    Tier        int                    // 1, 2, or 3
    SourceType  string                 // "agent_run", "pull_request", "deploy", "system"
    SourceID    uuid.UUID              // ID of the entity that generated the event
    RepoID      *uuid.UUID             // which repo, if applicable
    Payload     map[string]interface{} // event-specific data
    CreatedAt   time.Time
}
```

## Delivery Channels

### 1. In-App Notifications

Always enabled. The primary notification surface within 143.dev.

#### Data Model

```sql
CREATE TABLE notifications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    user_id         uuid NOT NULL REFERENCES users(id),
    event_type      text NOT NULL,
    tier            int NOT NULL,
    title           text NOT NULL,
    body            text NOT NULL,
    action_url      text,                    -- deep link into 143.dev UI
    source_type     text,                    -- "agent_run", "pull_request", etc.
    source_id       uuid,
    repo_id         uuid REFERENCES repositories(id),
    read_at         timestamptz,
    dismissed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user_unread ON notifications (org_id, user_id, created_at DESC)
    WHERE read_at IS NULL AND dismissed_at IS NULL;
CREATE INDEX idx_notifications_user_recent ON notifications (org_id, user_id, created_at DESC);
```

#### API

```
/api/v1/notifications
├── GET    /                    # list notifications (default: unread, supports ?status=all|unread|read)
├── POST   /:id/read           # mark as read
├── POST   /:id/dismiss        # dismiss (hide from list)
├── POST   /read-all           # mark all as read
└── GET    /unread-count        # badge count (lightweight, polled or via SSE)
```

#### Real-Time Delivery

Unread count and new notifications are pushed to the frontend via Server-Sent Events (SSE), reusing the same SSE infrastructure used for agent log streaming (doc 06).

```go
func (h *NotificationHandler) StreamNotifications(w http.ResponseWriter, r *http.Request) {
    userID := auth.UserIDFromContext(r.Context())

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming not supported", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    ch := h.hub.Subscribe(userID)
    defer h.hub.Unsubscribe(userID, ch)

    for {
        select {
        case event := <-ch:
            fmt.Fprintf(w, "event: notification\ndata: %s\n\n", event.JSON())
            flusher.Flush()
        case <-r.Context().Done():
            return
        }
    }
}
```

#### UI Component

The notification center is a bell icon in the top nav with an unread badge:

```
┌─ Notification Panel ─────────────────────────────┐
│                                                  │
│  Today                                           │
│  ┌────────────────────────────────────────────┐  │
│  │ 🟢 PR #42 opened: Fix null pointer in     │  │
│  │    user API handler                        │  │
│  │    my-org/backend · 5 min ago              │  │
│  │                           [View PR]        │  │
│  └────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────┐  │
│  │ 🔴 Regression detected after deploy of    │  │
│  │    PR #38                                  │  │
│  │    my-org/api · 2 hours ago                │  │
│  │                      [View Impact]         │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  Yesterday                                       │
│  ┌────────────────────────────────────────────┐  │
│  │ PR #41 merged · Impact: Error rate ↓ 73%   │  │
│  │    my-org/backend · yesterday              │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│                      [Mark all as read]          │
└──────────────────────────────────────────────────┘
```

### 2. Email

Opt-in per event type. Uses simple transactional email (no marketing fluff).

#### Provider

Use a simple SMTP integration or a transactional email service. Configuration via environment:

```
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USERNAME=notifications@143.dev
SMTP_PASSWORD=...
SMTP_FROM=143.dev <notifications@143.dev>
```

For self-hosted instances without SMTP configured, email notifications are silently disabled.

#### Email Templates

Emails are plain-text-first with optional HTML. Keep them short — the goal is to get the user back to the app, not to replicate the app in email.

**Example: PR Opened**

```
Subject: [143.dev] PR opened: Fix null pointer in user API handler

143.dev generated a fix for your repository.

  Repository: my-org/backend
  Issue: Null pointer exception in UserService.GetByID
  PR: #42 — Fix null pointer in user API handler
  Files changed: 1 (internal/api/users.go)
  Diff: +3 / -1

Review the PR:
https://github.com/my-org/backend/pull/42

---
View in 143.dev: https://your-instance.example.com/runs/abc-123
Notification settings: https://your-instance.example.com/settings/notifications
```

**Example: Regression Detected**

```
Subject: [143.dev] Regression detected — PR #38 may have caused increased errors

A fix deployed from PR #38 may have introduced a regression.

  Repository: my-org/api
  PR: #38 — Fix timeout in payment handler
  Deployed: 2 hours ago
  Impact: Error rate increased 15% (baseline: 2.1/hr → current: 2.4/hr)

Review the impact:
https://your-instance.example.com/runs/def-456/impact

---
Notification settings: https://your-instance.example.com/settings/notifications
```

#### Rate Limiting

Email notifications are rate-limited per user to prevent inbox flooding:

- Maximum 10 individual emails per user per hour
- If limit is reached, remaining events are held and delivered in a single batch email at the next hour boundary
- Tier 1 events (action required) are exempt from rate limiting — they always send immediately

### 3. Slack

Optional integration via Slack incoming webhook or Slack App.

#### Setup

**Simple (webhook)**: User provides a Slack incoming webhook URL in settings. 143.dev posts notifications to that channel.

**Full (Slack App)**: Install the 143.dev Slack App for richer features: DMs, thread replies, inline actions.

Configuration stored in `integrations` table (provider = `slack`):

```json
{
  "mode": "webhook",
  "webhook_url": "https://hooks.slack.com/services/T.../B.../xxx",
  "default_channel": "#engineering"
}
```

Or for Slack App mode:

```json
{
  "mode": "app",
  "bot_token": "xoxb-...",
  "team_id": "T...",
  "user_mappings": {
    "user-uuid-1": "U12345",
    "user-uuid-2": "U67890"
  }
}
```

#### Message Formatting

Slack messages use Block Kit for structured display:

**PR Opened (channel notification)**:

```json
{
  "blocks": [
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "*PR opened:* <https://github.com/my-org/backend/pull/42|Fix null pointer in user API handler>\n`my-org/backend` · 1 file changed · +3 / -1"
      }
    },
    {
      "type": "actions",
      "elements": [
        {
          "type": "button",
          "text": { "type": "plain_text", "text": "View PR" },
          "url": "https://github.com/my-org/backend/pull/42"
        },
        {
          "type": "button",
          "text": { "type": "plain_text", "text": "View in 143.dev" },
          "url": "https://your-instance.example.com/runs/abc-123"
        }
      ]
    }
  ]
}
```

**Regression Detected (DM to PR author)**:

```json
{
  "channel": "U12345",
  "blocks": [
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": ":warning: *Regression detected* after deploy of <https://github.com/my-org/api/pull/38|PR #38>\n\nError rate increased 15% since deploy 2 hours ago.\n\n<https://your-instance.example.com/runs/def-456/impact|View impact details>"
      }
    }
  ]
}
```

#### Slack User Mapping

In Slack App mode, 143.dev maps users by matching GitHub email to Slack email:

```go
func (s *SlackService) ResolveSlackUser(ctx context.Context, user *models.User) (string, error) {
    // 1. Check explicit mapping in integration config
    if slackID, ok := s.config.UserMappings[user.ID.String()]; ok {
        return slackID, nil
    }

    // 2. Look up by email via Slack API
    resp, err := s.client.GetUserByEmail(user.Email)
    if err != nil {
        return "", fmt.Errorf("slack user lookup: %w", err)
    }

    // 3. Cache the mapping
    s.config.UserMappings[user.ID.String()] = resp.User.ID
    s.db.UpdateIntegrationConfig(ctx, s.integrationID, s.config)

    return resp.User.ID, nil
}
```

### 4. GitHub

Some events naturally belong on GitHub as PR comments or issue comments. These are not configurable — they happen automatically as part of the PR lifecycle.

| Event | GitHub Action |
|-------|--------------|
| `run.completed.pr_opened` | PR is created (existing behavior from doc 08) |
| `deploy.impact_measured` | Comment on the PR with impact summary |
| `deploy.regression_detected` | Comment on the PR with regression warning |
| `pr.revision_applied` | Comment on PR noting the revision push |

These are handled by the existing `GitHubService` (doc 08) and are not routed through the notification system — they're direct GitHub API calls from the relevant services.

## User Preferences

### Data Model

Notification preferences are stored per-user in the `users` table via a `notification_preferences` JSONB column:

```sql
ALTER TABLE users ADD COLUMN notification_preferences jsonb NOT NULL DEFAULT '{}';
```

Structure:

```json
{
  "channels": {
    "email": {
      "enabled": true,
      "address": "dev@example.com"
    },
    "slack": {
      "enabled": true
    }
  },
  "event_overrides": {
    "run.completed.pr_opened": {
      "email": true,
      "slack": true
    },
    "run.completed.no_fix": {
      "email": false,
      "slack": false
    },
    "digest.weekly": {
      "email": true,
      "slack": false
    }
  },
  "quiet_hours": {
    "enabled": false,
    "start": "22:00",
    "end": "08:00",
    "timezone": "America/New_York"
  },
  "repo_filter": null
}
```

### Preference Resolution

When deciding how to deliver an event, preferences are resolved in order:

1. **User event override** — if the user has an explicit setting for this event type + channel, use it
2. **Tier default** — if no override, use the tier's default behavior
3. **Channel availability** — if the channel isn't configured (no SMTP, no Slack), skip it

```go
func (r *NotificationRouter) ShouldDeliver(user *models.User, event *NotificationEvent, channel string) bool {
    prefs := user.NotificationPreferences

    // 1. Check explicit override
    if override, ok := prefs.EventOverrides[event.EventType]; ok {
        if channelPref, ok := override[channel]; ok {
            return channelPref
        }
    }

    // 2. Fall back to tier defaults
    switch event.Tier {
    case 1:
        // Action required: in-app always, Slack DM if available, email if no Slack
        return channel == "in_app" || channel == "slack" || (channel == "email" && !prefs.Channels["slack"].Enabled)
    case 2:
        // Informational: in-app only by default
        return channel == "in_app"
    case 3:
        // Digest: email only by default
        return channel == "email"
    }

    return false
}
```

### Quiet Hours

During quiet hours, Tier 2 and Tier 3 events are held and delivered when quiet hours end. Tier 1 events (action required) are always delivered immediately — if you have a production regression, you want to know.

```go
func (r *NotificationRouter) IsQuietHours(user *models.User) bool {
    prefs := user.NotificationPreferences
    if !prefs.QuietHours.Enabled {
        return false
    }

    loc, err := time.LoadLocation(prefs.QuietHours.Timezone)
    if err != nil {
        return false
    }

    now := time.Now().In(loc)
    currentTime := now.Format("15:04")

    start := prefs.QuietHours.Start
    end := prefs.QuietHours.End

    // Handle overnight ranges (e.g., 22:00 - 08:00)
    if start > end {
        return currentTime >= start || currentTime < end
    }
    return currentTime >= start && currentTime < end
}
```

### Settings UI

```
┌── Notification Settings ─────────────────────────┐
│                                                  │
│  Channels                                        │
│  ┌────────────────────────────────────────────┐  │
│  │ In-app         Always on                   │  │
│  │ Email          [✓] dev@example.com         │  │
│  │ Slack          [✓] Connected               │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  Event Preferences                               │
│  ┌─────────────────────────────────────────────┐ │
│  │ Event                  In-app Email  Slack  │ │
│  │ ─────────────────────────────────────────── │ │
│  │ PR opened (review)     ✓      ✓      ✓     │ │
│  │ Agent needs input      ✓      ✓      ✓     │ │
│  │ Regression detected    ✓      ✓      ✓     │ │
│  │ PR merged              ✓      ○      ○     │ │
│  │ Impact measured        ✓      ○      ○     │ │
│  │ Agent run failed       ✓      ○      ○     │ │
│  │ Weekly digest          ○      ✓      ○     │ │
│  │ Monthly digest         ○      ✓      ○     │ │
│  └─────────────────────────────────────────────┘ │
│                                                  │
│  Quiet Hours                                     │
│  ┌────────────────────────────────────────────┐  │
│  │ [○] Enable quiet hours                     │  │
│  │ From: [22:00] To: [08:00]                  │  │
│  │ Timezone: [America/New_York ▼]             │  │
│  │ (Urgent alerts always delivered)           │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│                              [Save preferences]  │
└──────────────────────────────────────────────────┘
```

## Escalation

Some events need a response. If the initial notification goes unacknowledged, the system escalates.

### Escalation Rules

| Event | Escalation Trigger | Escalation Action |
|-------|-------------------|-------------------|
| `run.completed.pr_opened` | No review activity in 24 hours | Re-send notification with "Reminder: PR awaiting review" |
| `run.completed.pr_opened` | No review activity in 72 hours | Notify org admins: "PR #42 has been waiting 3 days" |
| `run.question` | No answer in 1 hour | Re-send to user + notify the configured Slack channel |
| `run.question` | No answer in 4 hours | Auto-skip the question and continue the run in best-effort mode |
| `deploy.regression_detected` | No acknowledgment in 30 minutes | Notify all org admins |

### Implementation

Escalation is handled by a periodic job that checks for unacknowledged events:

```go
func (e *EscalationWorker) Run(ctx context.Context) error {
    // Find Tier 1 events that haven't been acknowledged within their escalation window
    rules := []EscalationRule{
        {
            EventType:   "run.completed.pr_opened",
            Window:      24 * time.Hour,
            AckCheck:    e.hasPRReviewActivity,
            Action:      e.sendReminder,
        },
        {
            EventType:   "run.question",
            Window:      1 * time.Hour,
            AckCheck:    e.hasQuestionResponse,
            Action:      e.escalateToChannel,
        },
        {
            EventType:   "deploy.regression_detected",
            Window:      30 * time.Minute,
            AckCheck:    e.hasRegressionAck,
            Action:      e.notifyAllAdmins,
        },
    }

    for _, rule := range rules {
        events, err := e.db.GetUnacknowledgedEvents(ctx, rule.EventType, rule.Window)
        if err != nil {
            continue
        }
        for _, event := range events {
            acked, err := rule.AckCheck(ctx, &event)
            if err != nil || acked {
                continue
            }
            rule.Action(ctx, &event)
        }
    }
    return nil
}
```

### Escalation Tracking

```sql
CREATE TABLE notification_escalations (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid NOT NULL REFERENCES organizations(id),
    notification_id     uuid NOT NULL REFERENCES notifications(id),
    escalation_level    int NOT NULL DEFAULT 1,     -- 1 = first reminder, 2 = admin escalation
    escalated_to        uuid[] NOT NULL,            -- user IDs who received the escalation
    escalated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_escalations_notification ON notification_escalations (notification_id);
```

## Notification Service

The central service that all other components call to emit events:

```go
type NotificationService struct {
    db       *db.NotificationStore
    router   *NotificationRouter
    channels map[string]DeliveryChannel // "in_app", "email", "slack"
    hub      *SSEHub                    // for real-time in-app push
}

// DeliveryChannel is implemented by each channel (email, Slack, etc.)
type DeliveryChannel interface {
    Send(ctx context.Context, recipient *models.User, notification *Notification) error
}

// Notify is the single entry point for all notification events.
// Other services call this — they don't interact with channels directly.
func (s *NotificationService) Notify(ctx context.Context, event *NotificationEvent) error {
    // 1. Determine recipients
    recipients, err := s.router.GetRecipients(ctx, event)
    if err != nil {
        return fmt.Errorf("get recipients: %w", err)
    }

    for _, user := range recipients {
        // 2. Create in-app notification (always)
        notification, err := s.db.CreateNotification(ctx, &Notification{
            OrgID:      event.OrgID,
            UserID:     user.ID,
            EventType:  event.EventType,
            Tier:       event.Tier,
            Title:      s.formatTitle(event),
            Body:       s.formatBody(event),
            ActionURL:  s.actionURL(event),
            SourceType: event.SourceType,
            SourceID:   event.SourceID,
            RepoID:     event.RepoID,
        })
        if err != nil {
            return fmt.Errorf("create notification: %w", err)
        }

        // 3. Push to SSE for real-time in-app delivery
        s.hub.Publish(user.ID, notification)

        // 4. Deliver to external channels based on preferences
        for channelName, channel := range s.channels {
            if s.router.ShouldDeliver(user, event, channelName) {
                if err := channel.Send(ctx, user, notification); err != nil {
                    zerolog.Ctx(ctx).Err(err).
                        Str("channel", channelName).
                        Str("event", event.EventType).
                        Msg("notification delivery failed")
                    // Don't fail the whole flow — log and continue
                }
            }
        }
    }

    return nil
}
```

## Recipient Resolution

Who gets notified depends on the event type and org configuration:

```go
func (r *NotificationRouter) GetRecipients(ctx context.Context, event *NotificationEvent) ([]*models.User, error) {
    switch event.EventType {
    case "run.completed.pr_opened":
        // The user who triggered the run + configured reviewers for this repo
        return r.getRunTriggerAndReviewers(ctx, event)

    case "run.failed.needs_input", "run.question":
        // Only the user who triggered the run
        return r.getRunTrigger(ctx, event)

    case "deploy.regression_detected":
        // PR author + on-call if configured
        return r.getPRAuthorAndOnCall(ctx, event)

    case "pr.merged", "deploy.impact_measured":
        // The user who triggered the original run
        return r.getRunTrigger(ctx, event)

    case "issue.auto_triggered":
        // Org admins only
        return r.getOrgAdmins(ctx, event)

    default:
        return r.getOrgAdmins(ctx, event)
    }
}
```

## Digest Generation

Weekly and monthly digests are generated by a scheduled job and delivered as a single notification.

```go
func (d *DigestWorker) GenerateWeeklyDigest(ctx context.Context, org *models.Organization) (*DigestContent, error) {
    now := time.Now()
    weekAgo := now.AddDate(0, 0, -7)

    stats, err := d.db.GetOrgStatsForPeriod(ctx, org.ID, weekAgo, now)
    if err != nil {
        return nil, err
    }

    return &DigestContent{
        Period:          "weekly",
        StartDate:       weekAgo,
        EndDate:         now,
        RunsCompleted:   stats.RunsCompleted,
        RunsSuccessful:  stats.RunsSuccessful,
        PRsOpened:       stats.PRsOpened,
        PRsMerged:       stats.PRsMerged,
        IssuesResolved:  stats.IssuesResolved,
        SuccessRate:     stats.SuccessRate(),
        TopFixedIssues:  stats.TopFixedIssues,     // top 5 most impactful fixes
        ContextChanges:  stats.ContextQualityDelta, // repos where context improved
    }, nil
}
```

## API Endpoints

```
/api/v1/
├── /notifications
│   ├── GET    /                      # list notifications (paginated, filterable)
│   ├── GET    /unread-count          # lightweight badge count
│   ├── POST   /:id/read             # mark as read
│   ├── POST   /:id/dismiss          # dismiss
│   ├── POST   /read-all             # mark all as read
│   └── GET    /stream               # SSE endpoint for real-time delivery
│
├── /settings/notifications
│   ├── GET    /preferences           # get current user's notification preferences
│   └── PUT    /preferences           # update preferences
```

## Connection with Other Design Docs

**Agent Orchestrator (doc 06)**:
- Emits `run.completed.pr_opened`, `run.completed.no_fix`, `run.failed.needs_input` events
- The orchestrator calls `NotificationService.Notify()` at the end of each run

**PR & Ship (doc 08)**:
- Emits `pr.merged`, `pr.changes_requested`, `pr.revision_applied` events
- GitHub PR comments (impact summaries, regression warnings) are handled directly by `GitHubService`, not through the notification system

**Observability (doc 09)**:
- Emits `deploy.impact_measured` and `deploy.regression_detected` events
- The experiment evaluator calls `NotificationService.Notify()` after classification

**Review Feedback Loop (doc 11)**:
- `pr.revision_applied` notifies the original reviewer that the auto-apply revision is ready

**Interactive Sessions (doc 18)**:
- `run.question` events are emitted when an agent pauses for input in guided mode
- Escalation rule auto-skips unanswered questions after 4 hours

**Onboarding / activation flows**:
- Initial success milestones can trigger `run.completed.pr_opened` with onboarding-specific formatting
- The "What's next?" prompt in an initial success state is handled by the onboarding/product UX, not notifications

**Database Schema (doc 01)**:
- New `notifications` table
- New `notification_escalations` table
- New `notification_preferences` JSONB column on `users`

**Infrastructure (doc 10)**:
- SMTP configuration for email delivery
- Slack webhook/app configuration
- SSE infrastructure reused from agent log streaming
