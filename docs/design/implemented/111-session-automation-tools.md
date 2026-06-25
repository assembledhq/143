# 111 - Session Automation Tools

> **Status:** Implemented | **Last reviewed:** 2026-06-24

## Summary

Sandbox coding agents can manage repo-scoped automations through `143-tools automation`.
The tool surface is backed by the same internal-token pattern used by session tabs,
PR creation, issue creation, and project proposals:

```bash
143-tools automation create --payload '{"name":"Weekly cleanup","goal":"Clean stale state","repository_id":"<repo-uuid>","schedule_type":"cron","cron_expression":"0 9 * * 1"}'
143-tools automation update --automation-id <automation-uuid> --payload '{"goal":"..."}'
143-tools automation run --automation-id <automation-uuid>
143-tools automation pause --automation-id <automation-uuid>
143-tools automation resume --automation-id <automation-uuid>
```

The internal route forwards create/update payloads to the existing automation
handlers instead of reimplementing validation. This keeps session-created
automations aligned with UI-created automations for schedule parsing,
repository validation, agent/model validation, GitHub triggers, generic event
triggers, capability overrides, audit emission, and run dispatch.

## Authorization Model

Session automation tools are intentionally narrower than browser-authenticated
or external API automation management:

- The caller must use a valid session-scoped internal token.
- Automation goal-improvement sessions cannot use the general automation
  management tool.
- `create` requires `repository_id`.
- `create`, `update`, `run`, `pause`, and `resume` are limited to automations
  whose `repository_id` matches the session token repository.
- Org-wide automations remain managed through browser auth or scoped external
  API tokens, not through repo-scoped sandbox tokens.

This preserves the existing invariant that sandbox tools use platform paths
while avoiding cross-repo or org-wide mutations from a repository-scoped coding
session.

## Goal Improvement Placement

`automation-goal-improvement complete` remains a separate namespace for now.
That tool has a different security contract from general automation
management:

- It is available only to sessions with `automation_goal_improvement` origin.
- It requires the scoped `automation-goal-improvement:complete` internal tool
  grant.
- It writes a structured proposal for human review rather than mutating the
  automation directly.

If the CLI grows a richer automation namespace, the goal-improvement command
can move to `143-tools automation goal-improvement complete` as a namespacing
cleanup. The backend authorization should stay separate unless deep
goal-improvement sessions are deliberately allowed to use broader automation
management tools.

## Complex Rule Coverage

The session automation tool accepts raw JSON payloads so newly added automation
fields do not need a CLI flag every time the product adds a rule. The initial
payload contract is the existing flat automation handler shape, including:

- schedule fields: `schedule_type`, `interval_value`, `interval_unit`,
  `interval_run_at`, `cron_expression`, `timezone`
- GitHub triggers: `github_event_triggers`, `github_event_filters`
- generic event triggers: `event_triggers`
- execution policy: `execution_mode`, `max_concurrent`, `priority`
- agent policy: `agent_type`, `model`, `reasoning_effort`
- PR policy: `base_branch`, `pre_pr_review_loops`
- identity and capability policy: `identity_scope`, `capabilities`

The external API still has a split contract: creation uses the grouped public
shape while update accepts the product handler's flat shape. To make complex
rules such as Slack and recently added capability surfaces first-class outside
the UI, the external API should add:

- grouped `event_triggers` in create/update responses and requests, not only
  UI-flat fields
- provider-specific filter schemas for GitHub, PagerDuty, Slack, and future
  providers
- route-to-scope coverage for event-trigger mutation endpoints such as
  `PUT /api/v1/automations/{id}/event-triggers`
- explicit scopes if notification/rule management becomes separable from
  execution, for example `automations:triggers:write` or
  `automations:notifications:write`
- response examples for `capabilities`, Slack notification subscriptions, and
  provider event filters

Until that public contract is widened, agents inside 143 sessions can configure
the richer flat payload through `143-tools automation`, while external callers
should use the documented external API shape plus follow-up `PATCH`/rule routes
where available.
