# Design: Automation Trigger Model

> **Status:** Implemented | **Last reviewed:** 2026-06-16

## Summary

Automations have one or more triggers. A trigger is an entry point that starts an automation run. Schedule-based starts and repository-event starts are peers; schedule is not the primary mechanism with GitHub events added as a secondary option.

The creation surface presents product-level trigger choices:

- On a schedule
- When a PR is opened
- When a PR is updated
- When there is new PR feedback
- When checks finish
- When a PR is merged

GitHub webhook event names remain backend implementation details. Clients can send product-level `triggers` to the API; the backend expands them to raw provider events and still accepts `github_event_triggers` for compatibility. The product-level `When there is new PR feedback` trigger expands to the existing GitHub comment/review event set:

- `github.issue_comment.created`
- `github.pull_request_review.submitted`
- `github.pull_request_review_comment.created`

`When checks finish` expands to both `github.check_suite.completed` and `github.check_run.completed` so automations work regardless of which GitHub checks webhook shape an installation emits.

Feedback events are debounced through the durable `automation_trigger_dedupes` table for a short window so one submitted review with inline comments produces one automation run instead of a burst of duplicate runs. The dedupe key prefers a GitHub review group id, falls back to a concrete comment/review event id, and only falls back to repository/PR when GitHub does not provide a better identity. This avoids suppressing distinct PR comments.

## Product Semantics

`On a schedule` controls cadence fields such as interval, run time, and timezone. If it is disabled, the automation is event-only and stores `schedule_type = none` with no `next_run_at`.

`When a PR is opened` starts a run from the `pull_request.opened` webhook.

`When a PR is updated` starts from meaningful pull request lifecycle updates such as new commits, edits, reopen, ready-for-review, or draft conversion.

`When there is new PR feedback` includes top-level PR conversation comments, submitted reviews, and inline review comments. The UI intentionally avoids exposing those three GitHub-specific mechanisms as separate first-level checkboxes because users experience them as one feedback moment.

`When checks finish` starts from completed GitHub check suites or check runs associated with a PR.

`When a PR is merged` starts from a `pull_request.closed` webhook where GitHub marks the PR as merged.

## Implementation Notes

The backend keeps using `automations.github_event_triggers text[]` for raw GitHub event subscriptions. Product-level API `triggers` are expanded server-side, and the frontend helper in `frontend/src/lib/automation-triggers.ts` coalesces raw events for display. The automation detail settings form uses the same grouped editor as creation.

Event-only automations use `schedule_type = none`. The scheduler continues to claim only rows with `next_run_at IS NOT NULL`, so event-only automations are ignored by scheduled ticks and can still be triggered by GitHub webhook paths.

Advanced trigger filters are stored in `automations.github_event_filters jsonb`. Implemented filters are target base branches, authors, changed/commented paths, feedback types, and review states. The trigger service applies filters only when the webhook payload supplies the corresponding context; label/team filters are intentionally left out until the webhook payload model includes reliable label or team data.

Audit snapshots and update diffs include a product-level trigger summary (`triggers`) alongside raw `github_event_triggers` and structured `github_event_filters`, so operators can understand trigger changes without reverse-mapping provider event names.

Future trigger additions should keep this layering:

- product-level trigger label in the UI,
- mapping to one or more provider event names,
- concise event context appended to the automation run snapshot.

Future trigger conditions should continue to live alongside the trigger model as filters, not as separate top-level triggers. Likely next filters include labels and teams once webhook payload capture supports them reliably.
