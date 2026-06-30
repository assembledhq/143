# Demo Seed Fragments

The demo seed is applied by reading the numbered `.sql` files in lexical order.
Keep dependencies in earlier fragments: identity rows first, then repositories and
projects, product data, session data, preview data, and pull request state.

Fragment map:

- `00_preamble.sql` - shared safety/idempotency notes.
- `10_identity.sql` - demo organization, users, and memberships.
- `20_sources_and_projects.sql` - integrations, repositories, PR templates, and projects.
- `30_issues.sql` - issues plus priority and complexity sidecars.
- `40_sessions_base.sql` - seeded session rows, session updates, issue links, and issue snapshots.
- `41_session_artifacts.sql` - session threads, file events, diffs, reviews, questions, and validations.
- `42_session_conversation.sql` - session diff body, messages, and logs.
- `50_preview_targets.sql` - preview natural-key cleanup, groups, targets, and links.
- `51_preview_runtime.sql` - preview instances, services, infrastructure, runtime, snapshots, and logs.
- `60_pull_requests.sql` - pull requests, PR health, and PR preview state.
- `70_product_surface.sql` - organization and repository product-surface settings.
- `71_integration_accounts.sql` - synthetic provider accounts, user links, and repository mappings.
- `72_provider_issues.sql` - provider-sourced issues, priority scores, complexity estimates, and incidents.
- `73_automation_runs.sql` - automations, triggers, runs, and capability policies.
- `74_high_impact_sessions.sql` - high-impact sessions plus Slack and Linear surfaces linked to them.
- `75_pm_projects.sql` - PM documents, plans, project tasks, specs, attachments, cycles, and improvements.
- `76_code_review.sql` - code review policy, sessions, reviewer output, findings, and prompt artifacts.
- `77_usage_rollups.sql` - seeded usage rollups for dashboards.
