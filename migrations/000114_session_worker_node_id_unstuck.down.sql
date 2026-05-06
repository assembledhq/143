-- No-op: the up migration is a backfill that nulls a column. There's no
-- meaningful rollback since the prior values were stale by definition (rows
-- with container_id IS NULL have no live container, so any worker_node_id
-- recorded against them was a leftover from a crashed turn).
SELECT 1;
