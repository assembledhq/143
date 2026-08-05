-- Repair inbox entries that the transcript can no longer render.
--
-- The activity-capsule timeline hides a user message whose inbox entry has a
-- delivery_state but no applied_at, because an unapplied steering message must
-- not be attributed to work that has not happened yet. applied_at is only ever
-- written by the inbox-triggered branch of StartActivityPhase (migration
-- 000264 added the column with no backfill), which leaves two populations of
-- entries permanently invisible in the transcript.

-- 1. Entries stranded by a batch that was abandoned before it started.
--    Acknowledging a batch moves its entries to 'acked'; only starting one
--    sets applied_at. An abandoned batch therefore left its entries 'acked'
--    with a NULL applied_at forever - never applied, never retried, and below
--    the recoverable-inbox notice's threshold, which only counts
--    'dead_letter' and 'unknown_delivery'. The runtime confirmed receipt but
--    was lost before execution began, so 'unknown_delivery' is the accurate
--    state and is the one the recovery UI offers a replay action for.
UPDATE thread_inbox_entries e
SET delivery_state = 'unknown_delivery', updated_at = now()
FROM thread_inbox_delivery_batches b
WHERE b.status = 'abandoned'
  AND e.org_id = b.org_id
  AND e.session_id = b.session_id
  AND e.thread_id = b.thread_id
  AND e.runtime_id = b.runtime_id
  AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
  AND e.delivery_state = 'acked'
  AND e.applied_at IS NULL;

-- 2. Entries that predate the delivery-batch machinery entirely. Before
--    migration 000264 there were no batches, so an entry that reached 'acked'
--    had already been delivered to and executed by the runtime. Those
--    messages are historical applied steering and must render at their
--    applied boundary. Restricting to entries with no covering batch row
--    keeps this from touching anything the current machinery owns: a live
--    'acknowledged' batch still has its entries in flight, and a 'started'
--    batch already set applied_at in the same transaction.
UPDATE thread_inbox_entries e
SET applied_at = COALESCE(e.acked_at, e.delivered_at, e.updated_at),
    updated_at = now()
WHERE e.applied_at IS NULL
  AND e.delivery_state = 'acked'
  AND NOT EXISTS (
      SELECT 1
      FROM thread_inbox_delivery_batches b
      WHERE b.org_id = e.org_id
        AND b.thread_id = e.thread_id
        AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
  );
