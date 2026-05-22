# Design: Preview Lifetime Controls

> **Status:** Implemented
> **Last reviewed:** 2026-05-19

Session previews are intentionally temporary. The current product default is a 15 minute idle timeout and a 30 minute hard TTL, with explicit extension capped at 2 hours from preview creation. This keeps unused preview containers from occupying scarce worker slots while still giving active reviewers enough time to inspect and iterate.

The product gap was control. Users sometimes need a preview to remain available while they write feedback, compare changes, or come back after a short interruption. Users may also want to shorten a preview when they know they are done but do not want to restart it just to change the lifetime. This control remains secondary because restarting the preview container is usually the cleaner default.

## Goals

- Let a user extend an active preview without restarting it.
- Let a user shorten an active preview or stop it sooner when they know it is no longer needed.
- Preserve the default bias toward short-lived previews and container conservation.
- Avoid turning preview lifetime into a prominent workflow for casual users.
- Make imminent shutdown understandable before the platform reclaims the container.

## Non-Goals

- Unlimited previews.
- A general-purpose always-on environment.
- Org-wide preview policy configuration in this design. Admin policy may later tune caps, but the session UI should first work well with the current defaults.

## Option A: Hidden Lifetime Menu In Preview Controls

Add a small clock or overflow-menu action beside `Stop` and `Restart` in the Preview tab controls. The menu shows:

- Current shutdown time: `Shuts off at 3:42 PM`
- Relative time remaining: `24 minutes left`
- Actions: `+15 min`, `+30 min`, `Keep for 1 hr`, `Stop in 5 min`, `Stop now`
- Disabled cap state: `Maximum lifetime reached`

This can use the existing `preview_instances.expires_at` value and the existing extend route as a starting point, but full support for choosing shorter or exact durations needs a new backend endpoint that sets `expires_at` within policy rather than always extending by the default 30 minutes.

Pros:

- Keeps the control discoverable for power users but out of the primary path.
- Fits the user's stated "somewhat hidden" requirement.
- Makes decrease and extend feel like the same lifecycle control instead of separate concepts.
- Easy to permission later by role, org policy, or preview type.

Cons:

- Users may not find it until they hit the warning banner.
- Requires more backend shape than the current one-click `ExtendTTL`.
- If too many presets are exposed, users may treat long previews as normal.

Best fit:

This should be the primary session-details control. It gives experienced users control without advertising long-running previews as a default behavior.

## Option B: Start Preview With Advanced Duration

Keep `Start Preview` as the main action, but add an advanced row in the start state, such as `Options` or a compact disclosure, with a duration selector:

- `Default`
- `15 min`
- `30 min`
- `1 hr`
- `2 hr max`

Pros:

- Lets users make the right choice before paying startup cost.
- Useful when the user already knows they need the preview for a meeting, handoff, or longer manual QA pass.
- Reduces post-start adjustment churn.

Cons:

- Puts lifetime choice in front of more users than necessary.
- Encourages longer starts preemptively, which is worse for utilization than just-in-time extension.
- Does not help much when the need appears after active development starts.

Best fit:

Use only behind a collapsed `Start options` disclosure, and default it to platform policy. This is a secondary enhancement after the active-preview menu.

## Option C: Imminent Shutdown Banner

When the preview is close to shutdown, show an inline message near the preview frame:

`Preview shuts off in 4 minutes. Extend 30 min`

This is close to the current `TTLWarning` behavior, but the message should make the platform intent clearer: the preview is being reclaimed because previews are temporary, not because something failed. If the preview has no recent app activity, the banner can say:

`No recent preview activity. Shuts off in 4 minutes. Extend`

Pros:

- Appears exactly when the decision is useful.
- Good for casual users who do not need to learn the hidden control.
- Strong resource conservation: users extend only when they are actually present near expiry.
- Reuses the existing warning surface and backend extension behavior.

Cons:

- Only helps users who are watching the session near expiry.
- Does not support planned longer usage unless paired with another control.
- Can become noisy if shown too early or too persistently.

Best fit:

Keep this as the safety net. Show only inside the Preview tab and only near expiry, with thresholds such as 5 minutes for hard TTL and 2 minutes for scheduled recycle.

## Option D: Passive Session Detail Status Row

Add a low-emphasis row in the Overview or Preview details metadata:

`Preview running · shuts off in 22 min · Manage`

The `Manage` affordance opens the same lifetime menu from Option A.

Pros:

- Makes lifetime visible without making it the headline action.
- Helps users understand why a preview later stopped.
- Works well when the session details panel is open but the Preview tab is not active.

Cons:

- Adds another piece of session metadata to an already dense details surface.
- If always visible, it may over-normalize manual lifetime tuning.
- It is less actionable than an expiry banner when the preview is actively being used.

Best fit:

Use as a quiet readout in session details after Option A exists. Do not add separate controls here; link to the shared menu.

## Option E: Activity-Aware Auto Extension

Automatically extend active previews while the user is interacting with the preview iframe or the agent is actively using the session sandbox, still capped by the max lifetime. Manual controls remain only for exceptional cases.

Pros:

- Lowest user burden for normal active development.
- Aligns platform cost with real activity rather than wall-clock alone.
- The existing idle sweeper already treats recent access and active turns differently, so this follows the current model.

Cons:

- Users may not understand why some previews live longer than others.
- Browser activity signals from isolated preview origins need careful handling.
- Can mask resource cost if "activity" is too broad, such as background HMR or polling.

Best fit:

Use cautiously as a backend lifecycle improvement, not as the primary product affordance. Count explicit user presence and active session turns, not arbitrary network chatter from the preview app.

## Recommendation

Ship Option A plus Option C first.

The experience should be:

1. Starting a preview keeps the current default lifetime.
2. Active previews show a small, secondary lifetime affordance in the Preview controls, probably an icon-only clock/menu with tooltip text like `Preview lifetime`.
3. The menu exposes bounded presets for extend and shorten actions.
4. When the preview is within the warning threshold, the existing inline warning becomes more direct: `Preview shuts off in N minutes. Extend 30 min`.
5. When the cap is reached, both the menu and warning explain that the user must restart the preview to continue.

This combination gives power users the hidden control they need, while the broader user base only sees the extension prompt when it matters. It also keeps the platform's cost posture intact: default short, extend on intent, hard cap unchanged.

## Backend Shape

The current `ExtendTTL` endpoint only extends by `DefaultHardTTL` from now, capped by `CreatedAt + DefaultMaxTTL`. To support the full UX, add a policy-bound adjustment endpoint:

`PATCH /api/v1/sessions/{id}/preview/lifetime`

Request:

```json
{
  "expires_at": "2026-05-19T16:30:00Z"
}
```

or:

```json
{
  "duration_seconds": 1800
}
```

Rules:

- The active preview must belong to the request org.
- The resulting expiry must be after `now` unless the user chooses stop-now, which should continue to use the stop endpoint.
- The resulting expiry must not exceed `created_at + DefaultMaxTTL`.
- Shortening is allowed down to a small floor such as 2 minutes, so users can recover from accidental clicks.
- Every adjustment should emit an audit event with previous expiry, new expiry, actor, preview id, session id, and whether the action shortened or extended the preview.

The existing one-click extend endpoint can remain as a compatibility helper, but new UI should call the more explicit adjustment endpoint.

## Product Guardrails

- Preserve the 2 hour max lifetime unless an admin policy explicitly changes it.
- Keep long-duration choices out of the default start button.
- Do not show lifetime controls for stopped, failed, or expired previews except as historical metadata.
- Consider showing stronger copy for connected previews because they may hold managed credentials or private network access.
- Track usage: extension rate, max-cap hit rate, shorten rate, idle-stop rate, and average extra container-minutes from manual extension.
