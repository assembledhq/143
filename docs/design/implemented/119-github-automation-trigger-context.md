# GitHub Automation Trigger Context

> **Status:** Implemented | **Last reviewed:** 2026-07-14

## Problem

GitHub-triggered automation runs previously recorded the product event and a small amount of context only inside `config_snapshot`. The automation page's polling endpoint intentionally omits that large snapshot, so clients could not reliably answer which pull request and revision a run evaluated, whether several runs came from the same delivery, or whether the event actor was a bot. This also made an execution result easy to mistake for a review decision about a PR.

## Contract

Webhook handlers copy GitHub's `X-GitHub-Delivery` header into the typed event passed to `PRService`. `PRService` forwards the delivery ID and the available target metadata to `GitHubEventTriggerService`:

- repository, pull request number, URL, and title;
- evaluated head SHA;
- actor login and GitHub actor type;
- logical event and feedback-dedupe identifiers.

Before creating a run, the trigger service trims the fields, constructs a canonical PR URL when GitHub omitted one, infers `Bot` for logins ending in `[bot]`, and scopes the provider event key to the pull request as `<delivery-id>:pr:<number>`. The suffix is required because one check delivery can reference multiple pull requests while the run idempotency index is keyed by automation and provider event ID.

The normalized fields are stored in the existing run columns and snapshots:

- `provider = github` and `provider_event_id` identify the delivery/target pair;
- `trigger_context` carries provider, event, provider event ID, logical event ID, and feedback-dedupe group;
- `config_snapshot.github` carries the target and actor metadata used to build the run;
- `goal_snapshot` includes the PR title and head SHA so the agent sees the same revision context users see.

No new table or migration is required.

## Run API Projection

`AutomationRunStore.ListByAutomation` projects two compact optional objects from the snapshot while continuing to omit the full `config_snapshot` from the polling payload:

- `trigger_target`: repository, PR number, canonical URL, optional title, and optional head SHA;
- `trigger_details`: GitHub event, provider event ID, logical event ID, dedupe group, actor, actor type, and a normalized `bot_triggered` flag.

The SQL projection also constructs canonical PR URLs for historical GitHub runs that have a repository and PR number but no stored URL. Scheduled, manual, and non-GitHub runs leave both objects absent, preserving the existing additive API shape.

## Boundaries

This context describes what caused a run and what it evaluated. It does not claim that the run approved, rejected, or advised on the PR. Review decisions are a separate structured outcome contract, and raw execution status remains a statement about whether the automation infrastructure completed successfully.

