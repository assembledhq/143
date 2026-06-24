# Design: Slack Automation Notification Capability

> **Status:** Implemented | **Last reviewed:** 2026-06-23

## Summary

Automations can grant coding agents a Slack notification capability. When the capability is present, the sandbox `143-tools` surface exposes:

```bash
143-tools slack send --channel-id C123 --text "Automation completed successfully."
```

The command posts through a 143 internal API endpoint rather than exposing Slack bot credentials inside the sandbox. The result returns delivery state and Slack message coordinates:

```json
{"status":"sent","channel_id":"C123","message_ts":"1700000000.000100"}
```

This mirrors the first-class PR creation path: agents request a platform-managed action, the backend enforces session-scoped internal-token authorization, and the platform owns credential use and observability.

## Capability Model

Slack notification sending is represented by `slack_notifications`, a write-level integration capability. It is intentionally separate from `team_docs`, which allows read-only Slack message search/thread context, and from `external_comments`, which covers task-manager or incident writebacks.

This keeps automation policies explicit:

- Read Slack context: grant `team_docs`.
- Send a completion/status notification: grant `slack_notifications`.
- Open or update a PR: grant `publishing`.

## Runtime Path

The sandbox CLI registers a Slack message sender when `INTERNAL_API_TOKEN` and `INTERNAL_API_URL` are available. The tool calls:

```text
POST /api/v1/internal/slack/messages
```

The internal route requires a session-scoped token, resolves the active Slack install and Slack credential server-side, sends the plain-text message through `chat.postMessage`, and records the attempt in `slack_outbound_messages` for delivery observability.

Agents should use this near the end of an automation run when the automation owner has configured a target Slack channel/thread in the automation instructions or capability config.
