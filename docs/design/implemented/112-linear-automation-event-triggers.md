# Linear Automation Event Triggers

> **Status:** Implemented | **Last reviewed:** 2026-06-25

Linear is a first-class automation event provider. Linear `Issue` webhook `create` and `update` actions are normalized as generic automation events:

- `issue.created`
- `issue.updated`

Automation trigger rows live in `automation_event_triggers` with `provider='linear'`. Matching supports narrow filters so teams can start automations only for the Linear work they want agents to handle:

- `team_keys` / `team_ids`
- `labels` / `tags`
- `issue_types`
- `state_types` / `state_names`
- `priorities`
- `title_contains`
- `cooldown_minutes`

When a matching Linear issue event arrives, the webhook ingestion path first persists the normalized issue, then creates a provider-event `automation_runs` row with Linear issue context in `trigger_context` and `config_snapshot`. The automation run keeps `provider_event_id` for idempotency and is dispatched through the existing `automation_run` worker path. Repository context resolves from the trigger repository override, then the automation repository, then the shared org default work repository.
