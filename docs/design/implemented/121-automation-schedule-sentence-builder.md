# 121 - Automation Schedule Sentence Builder

> **Status:** Implemented | **Last reviewed:** 2026-07-28

> **Depends on:** Existing automation interval and cron scheduling, timezone-aware next-run calculation, automation create/update APIs, and the goal-first automation UX.

## Problem

The automation editor currently describes scheduled work as an elapsed interval:

```text
Run every [7] [days] at [09]:[00]
```

That representation is adequate for duration-based work such as "every six
hours," but it is a poor fit for the calendar schedules users commonly intend.
"Every seven days" does not communicate which weekday owns the schedule, and it
is not equivalent to "every Monday" when a schedule is edited, paused, resumed,
or evaluated across daylight-saving changes.

Users should be able to choose the days that start an automation without
understanding cron expressions or translating a calendar schedule into elapsed
time. The schedule should remain readable after it is saved, using the same
sentence structure in the editor and in automation summaries.

The desired interaction follows the compact sentence-builder pattern used by
products such as Cursor and Codex:

```text
Every [week] on [Monday and Thursday] at [9:00 AM]
```

## Goals

- Replace the primary "Run every N units" editor with a natural-language
  sentence builder.
- Let weekly schedules select one or more weekdays explicitly.
- Use the same schedule wording in editable and read-only presentation.
- Make calendar schedules stable in the selected IANA timezone.
- Show the next concrete run before a user saves a schedule.
- Preserve existing interval and cron schedules without silently changing their
  behavior.
- Centralize schedule conversion, parsing, validation, and formatting so the
  create page, detail page, list page, API, and scheduler cannot develop
  independent interpretations.
- Keep advanced cron support available for API-created schedules that cannot be
  represented by the friendly controls.

## Non-Goals

- Supporting multiple independent schedule triggers on one automation.
- Replacing event triggers or changing how scheduled and event triggers coexist.
- Changing the scheduler claim loop, automation-run idempotency model, or
  scheduled-run execution pipeline.
- Building a general-purpose cron-expression designer.
- Converting existing schedules in a data migration.
- Changing automation permissions, ownership, identity, or publishing behavior.
- Adding second-level schedule precision. Friendly schedules use the existing
  five-minute time resolution.

## Product Direction

Use the sentence-builder interaction for both editing and presentation. Editing
uses inline shadcn controls embedded in the sentence; read-only surfaces render
the same sentence as text.

Editable example:

```text
+------------------------------------------------------------------+
| On a schedule                                                    |
|                                                                  |
| Every [ week v ] on [ Monday and Thursday v ] at [ 9:00 AM v ]  |
| [ America/Los_Angeles v ]                                        |
|                                                                  |
| Next run: Thursday, July 30 at 9:00 AM PDT                       |
+------------------------------------------------------------------+
```

Read-only example:

```text
Every week on Monday and Thursday at 9:00 AM PDT
Next run Thursday, July 30 at 9:00 AM
```

No-schedule example:

```text
[+] Add schedule
```

The first release should support:

- daily;
- weekdays;
- weekly on one or more selected weekdays;
- monthly on a numbered day;
- hourly or other elapsed intervals;
- custom existing intervals;
- advanced cron expressions.

If implementation is intentionally narrowed for the first delivery unit, Daily,
Weekdays, Weekly, and existing Intervals form the minimum useful release.
Monthly and Advanced cron editing may follow, but arbitrary stored cron
expressions must remain lossless even before an advanced editor ships.

## Schedule Sentences

Canonical presentation examples:

```text
Every day at 9:00 AM
Every weekday at 9:00 AM
Every week on Monday at 9:00 AM
Every week on Monday and Thursday at 9:00 AM
Every month on the 15th at 9:00 AM
Every 6 hours
Every 3 days at 9:00 AM
Custom schedule: 0 9 1,15 * *
```

Sentence output must use the browser locale for the time display while retaining
the stored IANA timezone as the execution contract. Compact surfaces may append
the localized timezone abbreviation. Expanded detail should also make the IANA
timezone discoverable so daylight-saving behavior is not hidden behind an
ambiguous abbreviation.

Weekdays are ordered Monday through Sunday in controls and summaries regardless
of the order in which the user selected them. English sentence formatting uses
commas and "and" for multiple days. Localization of the sentence itself is not
part of this design, but schedule formatting must live behind one helper so it
can be localized later.

## Editor Behavior

### Frequency

The first inline select controls the schedule family:

```text
Every [ day | weekday | week | month | interval | advanced ]
```

The remainder of the sentence changes contextually:

| Frequency | Additional controls |
| --- | --- |
| Daily | Time |
| Weekdays | Time |
| Weekly | One or more weekdays, time |
| Monthly | Day of month, time |
| Interval | Positive value, hours/days/weeks, optional run-at time |
| Advanced | Cron expression |

The product-facing labels should favor natural singular wording in the sentence
even when internal enum names differ.

### Weekly Day Selection

Weekly schedules require at least one weekday. Use a popover containing checkbox
items rather than seven always-visible buttons:

```text
on [ Monday and Thursday v ]

+--------------------+
| [x] Monday         |
| [ ] Tuesday        |
| [ ] Wednesday      |
| [x] Thursday       |
| [ ] Friday         |
| [ ] Saturday       |
| [ ] Sunday         |
+--------------------+
```

The popover trigger displays the selected names when space permits and a compact
summary such as "3 days" at narrow widths. The accessible name must include all
selected days, not only the compact visual summary.

New weekly schedules default to the current weekday in the selected timezone.
Changing timezone does not change the selected weekday; it changes the wall-clock
interpretation of the schedule.

### Monthly Schedules

Monthly schedules select a day from 1 through 31. Months without the selected
date are skipped, matching cron semantics. When a user selects 29, 30, or 31,
the editor displays:

```text
Months without this date will be skipped.
```

"Last day of month" and ordinal weekday patterns such as "first Monday" are
outside the initial scope because they are not representable by a simple
five-field cron expression across all months without additional scheduler
semantics.

### Time

Time selection retains the existing five-minute resolution. The visual control
may be a single localized time select even though the stored and generated
values use 24-hour `HH:MM`.

Daily, weekday, weekly, monthly, and day/week interval schedules require a wall
clock time. Sub-day hourly intervals do not show a run-at control because they
are elapsed-duration schedules.

Existing interval rows may predate that requirement and carry no
`interval_run_at`. The editor must not fabricate one for them: showing a default
time the schedule does not actually have would contradict both the next-run
preview and the stored row. Day and week cadences instead offer an explicit
control to add a run time, which writes it to the draft on use.

Because `PATCH` treats an absent `interval_run_at` as "unchanged", an interval
payload always sends the field, using `""` to clear it. Omitting the key when a
schedule becomes unanchored would leave a previously stored run-at steering the
row behind the editor's back — the automation would keep firing against a wall
clock neither the sentence nor the preview shows.

### Timezone

The editor uses the existing IANA timezone selector and defaults new schedules
to the current automation-form default. Timezone must remain visible adjacent to
the sentence or immediately below it. It must not be hidden exclusively in
Advanced settings because it materially changes the schedule.

### Enabling and Removing a Schedule

Use `Add schedule` when no schedule is configured. Once added, the sentence
builder appears with conservative defaults. Removing the schedule sets
`schedule_type` to `none` and omits all cron and interval fields.

Event triggers remain peers of the schedule trigger. This design does not yet
introduce multiple schedule rows, but the interaction must not imply that event
triggers are subordinate to the schedule.

### Next-Run Preview

Every valid draft displays its next concrete occurrence:

```text
Next run: Thursday, July 30 at 9:00 AM PDT
```

The preview is computed by the backend using the same model functions used by
the production scheduler. The client debounces preview requests during editing.
While a request is in flight, the previous preview may remain visible with a
loading indicator, but it must not be presented as current after the draft
changes.

An invalid or unresolvable schedule displays the API validation message and
prevents saving. A network failure displays a retryable preview error but does
not prevent saving: the create and update endpoints validate the schedule again
on write, so an unreachable preview must not block the form — on the detail page
it would otherwise block saving unrelated fields as well. The editor must
distinguish validation failures from transport failures.

Preview validation must accept exactly what create accepts. In particular the
preview applies the same interval defaults (`interval_value` 1, `interval_unit`
days) rather than rejecting a payload the create path would have stored.

For saved read-only schedules, `next_run_at` from the automation response remains
the authoritative presentation value. Paused automations may have no next run;
the schedule sentence still renders, with the next-run line replaced by the
existing paused state.

## Frontend Schedule Model

Introduce a UI-facing discriminated union separate from the wire/API model:

```ts
type Weekday =
  | "monday"
  | "tuesday"
  | "wednesday"
  | "thursday"
  | "friday"
  | "saturday"
  | "sunday";

type ScheduleDraft =
  | {
      frequency: "daily";
      time: string;
      timezone: string;
    }
  | {
      frequency: "weekdays";
      time: string;
      timezone: string;
    }
  | {
      frequency: "weekly";
      weekdays: Weekday[];
      time: string;
      timezone: string;
    }
  | {
      frequency: "monthly";
      dayOfMonth: number;
      time: string;
      timezone: string;
    }
  | {
      frequency: "interval";
      value: number;
      unit: "hours" | "days" | "weeks";
      time?: string;
      timezone: string;
    }
  | {
      frequency: "advanced";
      cronExpression: string;
      timezone: string;
    };
```

The exact file name is an implementation detail, but scheduling logic should be
co-located in one frontend module and exposed through pure functions:

- `scheduleDraftToAPI`
- `automationToScheduleDraft`
- `formatScheduleSentence`
- `parseFriendlyCron`
- weekday-number conversion and ordering;
- ordinal day formatting;
- time parsing and localized display.

Both create and edit pages must use one shared `AutomationScheduleEditor`.
Read-only surfaces must use one shared `AutomationScheduleSummary`. The
components consume the shared conversion and formatting helpers rather than
reimplementing schedule rules.

## Persistence Model

No database migration is required.

The existing automation fields remain the persistence contract:

- `schedule_type = cron` with `cron_expression` and `timezone` for
  calendar-anchored schedules;
- `schedule_type = interval` with `interval_value`, `interval_unit`, optional
  `interval_run_at`, and `timezone` for elapsed interval schedules;
- `schedule_type = none` with all schedule-specific fields unset.

Friendly calendar schedules map to standard five-field cron expressions:

| Friendly schedule | Stored cron expression |
| --- | --- |
| Every day at 09:00 | `0 9 * * *` |
| Every weekday at 09:00 | `0 9 * * 1-5` |
| Every Monday at 09:00 | `0 9 * * 1` |
| Every Monday and Thursday at 09:00 | `0 9 * * 1,4` |
| Every month on the 15th at 09:00 | `0 9 15 * *` |

Generated cron expressions use numeric weekdays with Sunday `0` and Monday `1`.
Generation emits weekdays in ascending cron order and never emits seconds,
aliases, names, or redundant syntax. Parsing must accept equivalent common forms
where doing so is unambiguous, but saving an unedited schedule must not
normalize its stored expression as a side effect.

An unedited schedule sends **no** schedule fields at all — not the stored ones
re-serialized. `PATCH` recomputes `next_run_at` from the current time whenever
any schedule field is present, `timezone` included, so echoing an unchanged
schedule back while saving an unrelated field would push an interval
automation's next run out by a full interval on every save.

Intervals remain appropriate for true elapsed-duration semantics:

| Friendly schedule | Stored representation |
| --- | --- |
| Every 6 hours | interval value `6`, unit `hours` |
| Every 3 days at 09:00 | interval value `3`, unit `days`, run at `09:00` |
| Every 2 weeks at 09:00 | interval value `2`, unit `weeks`, run at `09:00` |

The UI must not present "Every 7 days" as "Every week on Monday." Those are
different schedule contracts.

## Existing Schedule Compatibility

### Existing Intervals

Existing interval rows continue to execute and render without modification:

```text
Every 7 days at 9:00 AM
Every 2 weeks at 9:00 AM
Every 6 hours
```

Opening an automation does not convert it. If a user explicitly changes the
frequency to a calendar schedule and saves, the request switches the row to
`schedule_type = cron` and clears interval fields through the existing update
path.

### Friendly Cron Expressions

The frontend recognizes at least:

```text
0 9 * * *       -> Every day at 9:00 AM
0 9 * * 1-5     -> Every weekday at 9:00 AM
0 9 * * 1,4     -> Every week on Monday and Thursday at 9:00 AM
0 9 15 * *      -> Every month on the 15th at 9:00 AM
```

Common equivalent weekday representations, including Sunday `0` or `7`, may be
parsed if tests demonstrate lossless meaning.

### Complex Cron Expressions

Cron expressions outside the friendly subset render as:

```text
Custom schedule: 0 9 1,15 * *
```

They open in Advanced mode and retain the original expression verbatim unless
the user edits the schedule. If Advanced editing is deferred, the editor must
show the expression read-only with a clear explanation; it must never coerce the
expression into the nearest friendly schedule.

Extended six-field cron expressions and aliases currently accepted by the API
remain supported by the backend. Friendly generation does not produce them.

## API Contract

### Existing Create and Update APIs

The existing automation create and update contracts remain valid. Friendly
calendar schedules submit:

```json
{
  "schedule_type": "cron",
  "cron_expression": "0 9 * * 1,4",
  "timezone": "America/Los_Angeles"
}
```

Interval schedules continue to submit:

```json
{
  "schedule_type": "interval",
  "interval_value": 6,
  "interval_unit": "hours",
  "timezone": "America/Los_Angeles"
}
```

The API continues to reject cross-typed fields rather than silently discarding
them. `none` requests omit all schedule-specific fields.

### Schedule Preview

Add an authenticated preview endpoint:

```http
POST /api/v1/automations/schedule-preview
```

Request:

```json
{
  "schedule_type": "cron",
  "cron_expression": "0 9 * * 1,4",
  "timezone": "America/Los_Angeles"
}
```

Response:

```json
{
  "data": {
    "next_run_at": "2026-07-30T16:00:00Z"
  }
}
```

The endpoint accepts the same schedule fields and applies the same validation as
automation create/update. It constructs an unsaved automation schedule and
calls the canonical `ComputeNextRunAt` path from the current server time. It
does not query or mutate tenant data and does not create an automation.

The route still requires authenticated organization membership and uses the
normal request body limit and per-org/per-IP rate limiting. The response should
not echo arbitrary cron input. Errors use the standard API envelope and the
same stable validation codes used by automation writes where practical.

The implementation should extract shared schedule-request validation rather
than maintaining a third independent validation branch across create, update,
and preview.

## Validation

- Weekly schedules require at least one weekday.
- Monthly days must be integers from 1 through 31.
- Interval values remain integers from 1 through 365.
- Times use valid `HH:MM` values aligned to five-minute increments.
- Timezones are valid IANA timezone identifiers.
- Generated and advanced cron expressions pass backend cron validation.
- Cron schedules do not contain interval fields.
- Interval schedules do not contain a cron expression.
- `none` schedules do not contain cron or interval fields.
- Friendly parsing is conservative: ambiguous or unsupported expressions fall
  back to Advanced.
- A next-run preview must exist for the schedule to be saveable. Cron
  expressions with no future occurrence are invalid.

Client validation provides immediate feedback, but server validation remains
authoritative.

## Accessibility and Responsive Behavior

The visual sentence must remain understandable to screen-reader users even
though it contains multiple controls. Each control has an explicit accessible
name:

- `Schedule frequency`
- `Days of week`
- `Run time`
- `Time zone`
- `Cron expression`
- `Remove schedule`

Validation and preview status use associated descriptions or a polite live
region. The weekday popover supports keyboard navigation, exposes checkbox
state, returns focus to its trigger on close, and announces every selected day.

On narrow layouts the sentence may wrap between semantic phrases:

```text
Every [week]
on [Monday and Thursday]
at [9:00 AM]
[America/Los_Angeles]
```

Control order must remain frequency, day selection, time, timezone in both the
DOM and visual layout. Compact trigger text may abbreviate the visual weekday
summary, but not its accessible name.

Use shadcn components for selects, popovers, checkboxes, buttons, inputs, and
structural cards. Use semantic design tokens rather than hardcoded colors.

## Loading, Error, and Permission States

- A new schedule starts with a valid default so the sentence and preview are
  immediately meaningful.
- Preview loading does not block editing.
- Preview validation failure disables save and points to the relevant control.
- Preview transport failure exposes Retry and does not mislabel the schedule as
  invalid.
- Automation create/update failure preserves the draft.
- Users without automation-management permission see
  `AutomationScheduleSummary`, not disabled interactive controls.
- Unknown or malformed stored schedules render a safe "Invalid schedule"
  fallback with the raw expression available only where appropriate for
  diagnosis; list pages should not overflow with raw data.
- Paused automations retain their schedule sentence and show `Paused` instead of
  a next occurrence.

## Scheduler and Reliability Invariants

- `models.Automation.ComputeNextRunAt` remains the canonical next-run
  implementation for preview, create/update, resume, bulk resume, and the
  scheduler.
- Calendar schedules are always evaluated in their stored IANA timezone.
- The existing unique `(automation_id, scheduled_time)` index remains the
  scheduled-run idempotency boundary.
- A UI conversion must never write a different schedule merely because a user
  viewed or edited an unrelated field.
- Schedule changes recalculate `next_run_at` using existing update behavior.
- Pause/resume continues to recalculate the next occurrence instead of replaying
  missed occurrences.
- DST behavior remains delegated to the existing cron implementation. Tests
  document how nonexistent and ambiguous local times resolve.

## Observability and Audit

Existing automation audit snapshots continue to record the persisted schedule
type and its cron or interval fields. The feature does not add a second
human-readable schedule column.

The preview endpoint should log validation or computation failures at the
appropriate existing API level without logging unnecessary request bodies.
Metrics should distinguish preview success, validation failure, and internal
failure if the API already has a suitable automation metric family. UI product
analytics for selected frequency may be added later and are not required for
correctness.

## Testing Strategy

### Frontend Unit Tests

Use table-driven cases where practical for:

- each friendly draft to its exact API payload;
- API automation fields to the exact draft;
- daily, weekday, weekly, monthly, interval, and advanced sentences;
- Monday-through-Sunday ordering independent of selection order;
- ordinal day formatting;
- supported friendly cron parsing;
- conservative fallback for unsupported cron;
- Sunday `0`/`7` handling if supported;
- exact preservation of unedited complex cron expressions;
- localized time formatting with stable test locale/timezone setup.

### Frontend Component Tests

- Adding and removing a schedule.
- Changing frequency reveals only the relevant controls.
- Weekly schedules reject zero selected days.
- Selecting and removing multiple weekdays updates the sentence.
- Time and timezone edits request a new preview.
- Stale preview responses do not overwrite newer draft previews.
- Preview validation and transport errors render differently.
- Save payloads contain either cron or interval fields, never both.
- Existing intervals remain intervals after unrelated edits.
- Existing complex cron remains byte-for-byte unchanged after unrelated edits.
- Read-only users receive a summary without editable controls.
- Keyboard operation and accessible names work for all controls.
- Mobile wrapping preserves logical control order.

Use MSW for preview and automation API behavior. Per-test overrides use the
shared test server and do not start or stop their own server.

### Backend Tests

- Preview returns the exact result of `ComputeNextRunAt`.
- Valid cron, interval, and none requests follow the intended contract.
- Invalid cron, timezone, interval, time, and cross-typed fields return exact
  status codes and error envelopes.
- DST spring-forward and fall-back behavior is documented with exact expected
  timestamps.
- Expressions with no future occurrence are rejected.
- Authentication, body-size, and rate-limit middleware cover the route.
- Refactored schedule validation retains create and update behavior.
- Update, resume, and bulk-resume paths continue to compute next runs correctly.

Backend tests use `require` with descriptive messages and run in parallel where
repository test rules permit.

### Regression Verification

- Existing interval schedules execute unchanged.
- Existing friendly cron schedules render and round-trip correctly.
- Existing complex cron schedules render as Advanced and remain unchanged.
- List, creation, detail, pause/resume, and run-now flows still work.
- The scheduler does not double-fire a scheduled occurrence.

## Implementation Plan

### Phase 1: Scheduling Foundation

1. Add the frontend `ScheduleDraft` model and weekday types.
2. Add pure cron generation, friendly cron parsing, interval conversion, and
   sentence-formatting helpers.
3. Add table-driven unit coverage for conversions and compatibility.
4. Confirm that unsupported cron expressions always fall back losslessly.

This phase has no intended visible behavior change.

### Phase 2: Shared Sentence Editor

1. Add `AutomationScheduleEditor` using shadcn controls.
2. Add the weekly weekday popover and monthly day selector.
3. Integrate the component into `/automations/new`.
4. Integrate the same component into `/automations/:id`.
5. Remove the duplicated inline interval editor logic from both pages.
6. Preserve current form-draft behavior and unsaved-change handling.
7. Add focused component tests.

### Phase 3: Preview and Presentation

1. Extract shared backend schedule-input validation from create/update.
2. Add `POST /api/v1/automations/schedule-preview`.
3. Add a debounced TanStack Query preview hook with stale-response protection.
4. Add `AutomationScheduleSummary`.
5. Replace schedule formatting on automation list, detail, and trigger-summary
   surfaces with the shared presenter.
6. Add backend, frontend, and DST regression tests.

### Phase 4: Optional Polish

- Recommended presets such as weekday mornings.
- Richer advanced-cron explanation.
- Ordinal weekday monthly schedules if backed by explicit scheduler semantics.
- Product analytics for frequency choice and validation abandonment.
- Multiple independent schedule triggers, which requires a separate data-model
  design.

## Rollout and Rollback

No data migration or backfill is needed. Deploy backend preview support before
or with the frontend that calls it. Existing clients remain compatible because
create/update fields are unchanged.

The frontend rollout must be safe in the presence of every existing schedule:

- supported cron opens in the friendly editor;
- unsupported cron opens in Advanced;
- intervals remain intervals;
- malformed legacy state renders a non-destructive error fallback.

Rollback consists of reverting the frontend editor and preview route. Persisted
calendar schedules remain valid cron rows understood by the existing scheduler
and API, so schedules created through the new UI continue to run after a
frontend rollback.

No feature flag is required if compatibility tests cover all stored schedule
types. A flag may still be used if product rollout policy requires staged
exposure.

## Documentation Impact

This design is a durable user-facing scheduling contract and should be linked
from the automation area of `docs/design/overall.md` when implementation starts
or completes. Public Fumadocs should be updated when the sentence builder ships
because the schedule creation workflow and supported recurrence options are
user-facing behavior.

The existing goal-first automation design remains valid. This feature replaces
its compact schedule control with the sentence builder; it does not change the
goal-first hierarchy.

## Acceptance Criteria

- A user can create and edit a weekly automation that runs on multiple explicit
  weekdays.
- The editor and all read-only surfaces use the same natural-language schedule
  sentence.
- Calendar schedules persist as timezone-aware cron expressions.
- True interval schedules preserve elapsed-time semantics.
- The editor shows a canonical backend-computed next run before save.
- Existing interval and complex cron schedules do not change merely by being
  viewed or by saving unrelated settings.
- Invalid schedules cannot be saved and produce actionable accessible errors.
- Schedule create, update, pause/resume, list, and scheduler regression tests
  pass.
- No database migration is required, and rollback leaves newly created
  schedules executable by the existing scheduler.

## Decisions

1. **Use cron for calendar schedules.** The existing scheduler already provides
   timezone-aware cron evaluation, so a parallel recurrence data model would add
   drift without improving the product contract.
2. **Keep intervals for elapsed schedules.** "Every N units" remains useful but
   is no longer the primary representation for weekday-based work.
3. **Use the sentence builder for editing and presentation.** One mental model
   reduces the gap between configured and displayed behavior.
4. **Preview on the backend.** The preview and scheduler must share parsing,
   timezone, and DST semantics.
5. **Parse cron conservatively.** Lossless fallback is more important than
   presenting every expression as friendly.
6. **Skip missing monthly dates.** This matches cron behavior and is explained
   before save.

## Implementation Notes

- Monthly and editable Advanced cron shipped with the initial sentence builder.
- The time control is one localized selector backed by canonical five-minute
  `HH:MM` values.
- Compact schedule sentences omit the timezone abbreviation; expanded editor
  and next-run presentation expose it directly.
- Product analytics were not added because they are optional polish rather than
  part of the scheduling correctness contract.
