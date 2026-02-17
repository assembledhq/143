# Design: Ingestion Pipeline

This document describes how 143.dev ingests issues from external sources (Sentry, Linear, support tools), normalizes them into a unified format, deduplicates, and stores them for prioritization.

## Overview

Ingestion happens two ways:

1. **Push (Webhooks)** — real-time. Sentry, Linear, and GitHub send webhooks when events occur.
2. **Pull (Polling)** — periodic. A scheduled job polls each integration's API to catch anything webhooks may have missed.

Both paths converge into the same normalization and deduplication pipeline.

## Architecture

```
                    ┌─────────────┐
                    │   Webhook   │
  Sentry ──────────▶│  Handlers   │──┐
  Linear ──────────▶│  /webhooks  │  │
  GitHub ──────────▶│             │  │
                    └─────────────┘  │     ┌──────────────┐     ┌─────────────┐
                                     ├────▶│  Normalizer  │────▶│  Dedup +    │──▶ issues table
                    ┌─────────────┐  │     │              │     │  Upsert     │
                    │   Polling   │  │     └──────────────┘     └─────────────┘
  Sentry API ◀─────│   Workers   │──┘
  Linear API ◀─────│  (cron)     │
  Support API◀─────│             │
                    └─────────────┘
```

## Source Adapters

Each integration source implements a common adapter interface:

```go
type SourceAdapter interface {
    // FetchNewIssues polls the source API for issues since the last sync.
    FetchNewIssues(ctx context.Context, integration *models.Integration, since time.Time) ([]RawIssue, error)

    // ParseWebhook parses an inbound webhook payload into a RawIssue.
    ParseWebhook(ctx context.Context, headers http.Header, body []byte) (*RawIssue, error)

    // ValidateWebhook verifies the webhook signature.
    ValidateWebhook(ctx context.Context, integration *models.Integration, headers http.Header, body []byte) error
}

type RawIssue struct {
    ExternalID   string
    Source       string          // "sentry", "linear", "support"
    Title        string
    Description  string
    Severity     string          // normalized to: critical, high, medium, low
    Tags         []string
    FirstSeenAt  time.Time
    LastSeenAt   time.Time
    OccurrenceCount int
    AffectedCustomers []string   // external customer identifiers
    RawData      json.RawMessage // original payload preserved
}
```

### Sentry Adapter

**Webhook events handled:**
- `issue` (action: `created`, `resolved`, `regression`)
- `event_alert` (fired when alert rule triggers)

**Polling:**
- Uses Sentry's `GET /api/0/projects/{org}/{project}/issues/` with `query=is:unresolved&sort=date`
- Fetches issue details including event count, user count
- Respects `last_synced_at` to only fetch new/updated issues

**Normalization:**
- `level` -> `severity` mapping: `fatal`/`error` -> `critical`/`high`, `warning` -> `medium`, `info` -> `low`
- `userCount` -> `affected_customer_count`
- `count` -> `occurrence_count`
- Stack trace summary extracted from latest event for `description`

### Linear Adapter

**Webhook events handled:**
- `Issue` (action: `create`, `update`)

**Polling:**
- Uses Linear GraphQL API to query issues updated since `last_synced_at`
- Fetches assignee, labels, priority, project

**Normalization:**
- Linear priority (0-4) -> `severity`: 0 (none) -> `low`, 1 (urgent) -> `critical`, 2 (high) -> `high`, 3 (medium) -> `medium`, 4 (low) -> `low`
- Labels -> `tags`
- Issue description (markdown) preserved

### Support Adapter

Supports pluggable sub-adapters for different support tools:

**Zendesk:**
- Webhook: trigger-based webhooks for new/updated tickets
- Polling: Incremental ticket export API
- Maps priority field to severity

**Intercom:**
- Webhook: `conversation.created`, `conversation.updated`
- Polling: Search conversations API
- Uses tags and custom attributes for severity

**Generic (future):**
- CSV import
- API endpoint for custom integrations

## Normalization Pipeline

After the source adapter produces a `RawIssue`, the normalizer:

1. **Validates** required fields (external_id, source, title)
2. **Cleans** text (strip HTML from descriptions, truncate overly long fields)
3. **Normalizes severity** to the canonical set: `critical`, `high`, `medium`, `low`
4. **Computes fingerprint** for deduplication (see below)
5. **Extracts customer IDs** from event data for impact counting

## Deduplication

Issues are deduplicated using a fingerprint. The fingerprint is computed differently per source:

- **Sentry**: Use Sentry's `fingerprint` or `groupID` (already deduplicated by Sentry)
- **Linear**: Use Linear issue ID (each issue is unique)
- **Support**: Hash of `(normalized_title, source)` — groups related support tickets by title similarity

The `issues` table has a unique constraint on `(org_id, fingerprint)`.

### Upsert Logic

```sql
INSERT INTO issues (id, org_id, external_id, source, ..., fingerprint)
VALUES (...)
ON CONFLICT (org_id, fingerprint) DO UPDATE SET
    last_seen_at = GREATEST(issues.last_seen_at, EXCLUDED.last_seen_at),
    occurrence_count = issues.occurrence_count + EXCLUDED.occurrence_count,
    affected_customer_count = GREATEST(issues.affected_customer_count, EXCLUDED.affected_customer_count),
    updated_at = now();
```

When an issue is upserted (existing fingerprint), the new event data is also inserted into `issue_events` for full history.

## Webhook Handling

### Security

Each webhook endpoint validates the request signature using the integration's stored webhook secret:

- **Sentry**: `sentry-hook-signature` header, HMAC-SHA256
- **Linear**: `linear-signature` header, HMAC-SHA256
- **GitHub**: `x-hub-signature-256` header, HMAC-SHA256

Invalid signatures return `401 Unauthorized`.

### Processing

Webhooks are processed asynchronously:

1. Webhook handler validates signature and returns `200 OK` immediately
2. Payload is enqueued as an `ingest_webhook` job
3. Worker picks up the job, runs it through the adapter -> normalizer -> dedup pipeline

This ensures webhook endpoints respond quickly (avoiding timeouts) and processing is retryable.

## Polling Schedule

The `ingest_sync` job runs every 5 minutes (configurable per org). For each active integration:

1. Read `last_synced_at` from the integration record
2. Call `FetchNewIssues(since: last_synced_at)`
3. Process each result through normalize -> dedup -> store
4. Update `last_synced_at`

Polling acts as a catch-all to ensure no issues are missed if a webhook fails.

## Post-Ingestion

After new issues are ingested (or existing issues updated), the system enqueues a `prioritize` job to recompute priority scores. This happens automatically — the ingestion pipeline does not need to know about prioritization logic.

## Rate Limiting & Error Handling

- Source API polling respects rate limits (back off on 429 responses).
- Failed webhook processing retries up to 3 times with exponential backoff.
- If an integration is persistently failing, its status is set to `error` and an alert is shown in the admin UI.
- Individual event failures do not block processing of other events in a batch.
