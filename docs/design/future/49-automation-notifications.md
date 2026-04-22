# 49 — Automation Notifications & Failure Alerting

> **Status:** Not Started | **Last reviewed:** 2026-04-21
>
> **Depends on:** [48-automations-separation.md](../implemented/48-automations-separation.md) (automations + automation_runs data model), [22-notifications.md](22-notifications.md) (notification infrastructure — events, router, channels, preferences)

---

## 1. Problem

Automations in doc 48 fire silently. A run that lands at 9:00 AM on a Tuesday morning produces an `automation_runs` row, maybe a PR, maybe a failure — and unless a team member happens to open `/automations/:id` that day, nothing surfaces. Three concrete failure modes:

1. **Silent success.** An automation opens a PR; nobody notices; the PR rots.
2. **Silent failure.** An automation fails once (OOM, flaky dependency, model timeout) and nobody investigates.
3. **Silent decay.** An automation fails every run for a week. The metrics chart in `/automations/:id` trends to zero, but nobody's looking at it. The team only notices when someone finally asks "whatever happened to the flaky-test automation?"

Doc 48 §10 items 3 and §11 both name this gap and defer it. This doc closes it.

## 2. Scope

**In scope:**
- Per-automation notification preferences: who gets told about what, for *this* automation.
- Three automation-specific event types emitted by the orchestrator's existing completion hook (doc 48 §6.4): `automation.run.completed`, `automation.run.failed`, `automation.run.failure_streak`.
- Failure-streak detection: after N consecutive failed runs, emit an escalation event with a distinct urgency.
- UI: a Notifications card on the automation detail page's Settings tab.

**Explicitly out of scope (owned by doc 22):**
- The `notifications` table, delivery channels (in-app, email, Slack), SSE hub, preference storage, routing engine, digest generation, escalation policies, unread counts.
- User-level global preferences ("I never want email") — those live in doc 22's user preferences.

This doc defines *what* automations emit and *who for each automation* should receive it. Doc 22 defines *how it gets delivered*.

## 3. Dependency Status

Doc 22 is not yet implemented. This doc cannot ship before at least the minimum doc-22 surface lands:

- `notifications` table
- Event dispatch entrypoint (a function that takes a `NotificationEvent` and routes it)
- At least one delivery channel (in-app is the obvious v0 target)
- Per-user preferences table (so doc 22's routing engine has somewhere to look)

If doc 22 stalls, a pragmatic bridge is to ship this doc with *direct* email/Slack delivery inside the automation hook and retrofit into doc 22's router later. That trades architectural cleanliness for shipping velocity and is called out explicitly in §7.

## 4. Event Taxonomy

Three new events, to be added to doc 22's Tier table:

| Event | Tier | Trigger | Default Recipients | Payload |
|-------|------|---------|---------------------|---------|
| `automation.run.completed` | 2 (informational) | `automation_runs.status` → `completed` | `created_by` of the automation, plus anyone subscribed to this automation | `{automation_id, run_id, result_summary, session_ids, pr_urls}` |
| `automation.run.failed` | 2 (informational) | `automation_runs.status` → `failed` | `created_by` + subscribers | `{automation_id, run_id, error_message, session_id, retry_url}` |
| `automation.run.failure_streak` | 1 (action required) | N consecutive failures detected | `created_by` + org admins (configurable) | `{automation_id, streak_length, first_failure_at, most_recent_run_id}` |

Rationale for tiering:

- **Completed** is Tier 2. Useful to know but not urgent — the PR flow (`pr.opened` in doc 22) already handles the "review me" nudge.
- **Failed** is Tier 2 by default. A single failure is often transient (rate limit, flaky infra). Users can opt to bump it to Tier 1 per-automation if they want louder signal.
- **Failure streak** is Tier 1. If an automation has failed N times in a row, the automation itself is broken — somebody needs to look. Defaults to N=3.

## 5. Data Model

One new table, scoped to the automation. All notification *delivery* state lives in doc 22's `notifications` table.

### 5.1 `automation_notification_preferences`

```sql
CREATE TABLE automation_notification_preferences (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    automation_id   UUID NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Event opt-ins. NULL = use tier default from doc 22 user prefs.
    on_completed    BOOLEAN,     -- NULL = default (off for Tier 2), true = opt-in, false = opt-out
    on_failed       BOOLEAN,
    on_streak       BOOLEAN,     -- almost always NULL/true; opt-out is unusual

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (automation_id, user_id)
);

CREATE INDEX idx_automation_notif_prefs_automation
    ON automation_notification_preferences (automation_id);
```

Design notes:

- **Tri-state booleans** (`NULL`/`true`/`false`): lets users explicitly opt out of a specific automation even if their global tier-2 default is "notify me." Essential for noisy-automation cases.
- **`created_by` is the implicit default recipient** — no row needed for the creator to get defaults. A row only needs to exist when someone wants to override.
- **Org admins always get `on_streak`** regardless of rows in this table (hardcoded in the router — analogous to doc 22's "admin escalation" rules).
- **No channel column.** Channel selection is a *user-level* preference in doc 22. Per-automation preferences only toggle *whether to notify*, not *how*.

### 5.2 Failure streak tracking

No new table. Streak length is computed on demand from `automation_runs`:

```sql
-- Simplified: pseudo-SQL for "how many consecutive failures has this automation had?"
SELECT COUNT(*)
FROM automation_runs
WHERE automation_id = $1
  AND triggered_at > (
      SELECT COALESCE(MAX(triggered_at), 'epoch')
      FROM automation_runs
      WHERE automation_id = $1
        AND status IN ('completed', 'completed_noop')
  )
  AND status = 'failed';
```

This runs inside the orchestrator's automation completion hook (doc 48 §6.4 — the same hook that updates the run row), so it adds one query per run completion, not a separate sweeper.

Threshold (default 3, configurable later) is a constant for v1.

## 6. Event Emission

### 6.1 Where events fire

Doc 48's `AutomationHooks.OnSessionComplete` (in `internal/services/automations/hooks.go`) is already the only code path that transitions a run to its terminal status. Extend it to emit events:

```
OnSessionComplete(ctx, session, status):
    map session status → run status (existing)
    UpdateStatus(run) (existing)

    // NEW:
    if run status == "completed":
        emit automation.run.completed event
    if run status == "failed":
        emit automation.run.failed event
        streak := count_consecutive_failures(automation_id)
        if streak >= threshold:
            emit automation.run.failure_streak event
```

The emission is a single call to doc 22's dispatcher. If that dispatcher is down or unavailable, we log and continue — notifications are best-effort, never blocking. The `automation_runs` row transition must not fail because notification dispatch failed.

### 6.2 Event payload assembly

The hook already has the `models.Session`, so assembling the payload (session IDs, error message, result summary) is local. The `pr_urls` field requires a read from the session's PRs — cheap since sessions→PRs is indexed and most runs have 0–1 PRs.

### 6.3 Streak reset

A streak resets when the *next* run lands as `completed` or `completed_noop`. No explicit reset event — the absence of further `failure_streak` events is the signal.

## 7. Implementation Phases

### Phase 1 (after doc 22 ships): Event emission

1. Add event dispatch to `AutomationHooks.OnSessionComplete`. Use doc 22's dispatcher interface.
2. Wire `automation.run.completed` and `automation.run.failed` event types into doc 22's taxonomy table.
3. Smoke-test: run an automation, verify an in-app notification appears.

No UI changes, no new table. The automation creator gets default notifications based on their doc-22 tier-2 preferences.

### Phase 2: Per-automation preferences

1. Create `automation_notification_preferences` table.
2. Add `/api/v1/automations/:id/notification-preferences` CRUD endpoints.
3. Build the Notifications card on the automation Settings tab. Lists org members, lets each user toggle `completed`/`failed` notifications for this automation.
4. Router in doc 22 needs a hook: "for `automation.*` events, consult `automation_notification_preferences` as an override layer before user-global defaults."

### Phase 3: Failure-streak alerting

1. Add consecutive-failure count query to the hook.
2. Emit `automation.run.failure_streak` when threshold hit.
3. Register it as a Tier 1 event in doc 22.
4. Add a "failure streak banner" to the automation detail page: "⚠ 4 consecutive failures — investigate."

### Bridge option: direct delivery before doc 22 lands

If doc 22 is not ready and we need to ship this sooner, Phase 1 can degrade to direct email/Slack delivery from inside the hook. Interface-wrap the dispatcher so swapping to doc 22's router later is a constructor change, not a rewrite. Accept the trade-off: no preferences, no batching, no in-app UI — just "we'll email the creator on failure." This is genuinely useful on its own and buys us time.

## 8. UI

### 8.1 Automation settings tab: Notifications card

```
┌─ Notifications ─────────────────────────────────────────────┐
│                                                             │
│  Who gets notified about this automation?                   │
│                                                             │
│  ┌──────────────┬──────────┬──────────┬──────────────────┐ │
│  │  Member      │ Run OK   │ Failures │ Failure streak   │ │
│  ├──────────────┼──────────┼──────────┼──────────────────┤ │
│  │  @john       │  [ ]     │  [x]     │  [x]             │ │
│  │  @sarah      │  [x]     │  [x]     │  [x]             │ │
│  │  @mike       │  [ ]     │  [ ]     │  [x] (admin)     │ │
│  └──────────────┴──────────┴──────────┴──────────────────┘ │
│                                                             │
│  Delivery channel is controlled in your account settings.   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

Key choices:

- **Org-wide visibility with per-user opt-in.** Anyone in the org can see the table and toggle their own row. Editing someone else's row requires org admin.
- **Failure streak toggle is greyed for admins** — it's hardcoded on for them (per §5.1).
- **Channel choice is elsewhere.** This card is purely "whether," not "how." Clicking "account settings" goes to the doc 22 preference page.

### 8.2 Runs tab: streak banner

When the current streak ≥ threshold, show a banner above the runs list:

```
⚠  This automation has failed 4 runs in a row.
   Most recent failure: "Session crashed: OOM after scanning 12k files"
   [Investigate latest run]  [Pause automation]
```

Dismisses itself once a run lands `completed`/`completed_noop`.

## 9. What This Doesn't Do (Yet)

- **Custom thresholds per-automation.** N=3 is global. Some automations (known flaky external APIs) might want N=10. Future.
- **Custom event types per-automation.** "Notify me if this automation takes > 10 minutes" or "notify me if it opens a PR with > 500 lines changed" — interesting but complex. Out of scope.
- **Digest integration.** Doc 22's weekly digest should include automation summaries (X runs, Y completed, Z failed, N PRs opened). Called out but not specified here — the digest generator owns it once we have one.
- **Notification muting.** Users can opt out per automation but can't "snooze this one for a week." If needed later, add a `muted_until` column.
- **Per-repo admin rules.** Right now `on_streak` escalates to *org* admins. Repo-admin escalation is a doc 22 extension and should happen there, not here.
