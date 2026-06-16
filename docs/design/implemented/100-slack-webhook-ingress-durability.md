# Design: Slack Webhook Ingress Durability

> **Status:** Implemented | **Last reviewed:** 2026-06-16
>
> **Builds on:** [../future/101-slackbot-implementation-plan.md](../future/101-slackbot-implementation-plan.md)

Slack callbacks use the shared webhook-ingress durability pattern: verify the
external request, resolve it to an org installation, durably record the generic
delivery, persist provider-specific state, enqueue downstream work
idempotently, and make every retryable failure observable with the root error.

## Ingress Contract

The Events API, slash-command, and interaction routes verify Slack's signing
secret before any persistence. After installation lookup, they create or
rehydrate a `webhook_deliveries` row with provider `slack`, sanitized payload
and safe headers, the Slack delivery identity, event type, signature validity,
and status `received`.

Slack-specific state then lands in `slack_inbound_events`, which links back to
the generic row through `webhook_delivery_id`. This gives operators a stable
trace:

```text
Slack request -> webhook_deliveries -> slack_inbound_events -> jobs -> Slack response
```

## Retry Semantics

`webhook_deliveries` is the idempotency boundary. Duplicate Slack delivery IDs
with status `processed` or `ignored` are acknowledged with `200` and do not
dispatch work. Duplicate rows with status `received` or `failed` are
rehydrated so Slack retries can reuse the existing delivery and inbound event
IDs, then enqueue work again through the normal job dedupe keys.

Unknown, self, bot-authored, or otherwise non-actionable Slack events are
still persisted as ignored and acknowledged with `200`. Internal persistence or
enqueue failures mark the delivery `failed`, log the wrapped root error, and
return `500` so Slack retries only genuinely retryable failures.

## Observability

Request log context is installed for public routes, not only authenticated API
routes, so `writeError(..., err)` includes request ID, error code, response
message, and the wrapped root error for Slack webhook failures.

Slack health includes recent failed callback deliveries from the generic
ledger, and metrics classify callback outcomes with precise labels such as
`ok`, `ignored`, `duplicate`, `persist_failed`, `enqueue_failed`,
`invalid_signature`, and `installation_not_found`.
