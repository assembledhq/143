-- Repair inbox entries that the transcript can no longer render.
--
-- The activity-capsule timeline hides a user message whose inbox entry has a
-- delivery_state but no applied_at, because an unapplied steering message must
-- not be attributed to work that has not happened yet. applied_at is only ever
-- written by the inbox-triggered branch of StartActivityPhase (migration
-- 000264 added the column with no backfill), which left several populations of
-- entries permanently invisible in the transcript.
--
-- The code paths that produced these rows are fixed alongside this migration:
-- the seed-message ack and the delivery-watermark commit now stamp applied_at,
-- and abandoning a batch now reopens its entries. This repairs the rows those
-- paths already wrote, so the affected set is bounded by history at deploy
-- time and does not grow afterwards.
--
-- OPERATIONAL NOTE: statement 2 scans thread_inbox_entries, which holds a row
-- per inbox-delivered message. golang-migrate runs a migration file as one
-- implicit transaction, so this cannot be split into committed batches from
-- here. Check the affected row count before deploying:
--
--   SELECT count(*) FROM thread_inbox_entries e
--   WHERE e.delivery_state = 'acked' AND e.applied_at IS NULL
--     AND NOT EXISTS (SELECT 1 FROM thread_inbox_delivery_batches b
--                     WHERE b.org_id = e.org_id AND b.thread_id = e.thread_id
--                       AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end);
--
-- If that count is large enough that a single rewrite is unacceptable, run the
-- same UPDATE as an out-of-band batched backfill keyed on org_id first; this
-- migration then becomes a cheap no-op because it skips rows already stamped.

-- 1. Entries stranded by a batch that was abandoned before it started.
--    Acknowledging a batch moves its entries to 'acked'; only starting one
--    sets applied_at. An abandoned batch therefore left its entries 'acked'
--    with a NULL applied_at forever - never applied, never retried, and below
--    the recoverable-inbox notice's threshold, which only counts
--    'dead_letter' and 'unknown_delivery'. The runtime confirmed receipt but
--    was lost before execution began, so 'unknown_delivery' is the accurate
--    state and is the one the recovery UI offers a replay action for.
--    Driven from the (small) batch table, so this scan is bounded.
UPDATE thread_inbox_entries e
SET delivery_state = 'unknown_delivery',
    last_error = COALESCE(e.last_error, 'delivery batch abandoned after acknowledgment before execution started'),
    updated_at = now()
FROM thread_inbox_delivery_batches b
WHERE b.status = 'abandoned'
  AND e.org_id = b.org_id
  AND e.session_id = b.session_id
  AND e.thread_id = b.thread_id
  AND e.runtime_id = b.runtime_id
  AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
  AND e.delivery_state = 'acked'
  AND e.applied_at IS NULL;

-- 2. Entries that predate the delivery-batch machinery, or that were acked by
--    the seed/watermark paths before those learned to stamp applied_at. An
--    entry that reached 'acked' with no covering batch row was delivered to
--    and executed by the runtime, so it is historical applied steering and
--    must render at its applied boundary. The NOT EXISTS guard keeps this
--    from touching anything the batch machinery owns: a live 'acknowledged'
--    batch still has its entries in flight, and a 'started' batch already set
--    applied_at in the same transaction.
UPDATE thread_inbox_entries e
SET applied_at = COALESCE(e.acked_at, e.delivered_at, e.updated_at),
    updated_at = now()
WHERE e.delivery_state = 'acked'
  AND e.applied_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM thread_inbox_delivery_batches b
      WHERE b.org_id = e.org_id
        AND b.thread_id = e.thread_id
        AND e.sequence_no BETWEEN b.sequence_start AND b.sequence_end
  );
