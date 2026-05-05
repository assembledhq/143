-- Backfill: clear stale worker_node_id values left behind by ClearContainerID.
--
-- Before this migration, ClearContainerID nulled container_id but did not null
-- worker_node_id, so a session whose previous worker crashed mid-turn (and was
-- then reconciled at startup) ended up with container_id=NULL but worker_node_id
-- still pointing at the dead worker. The next ContinueSession on a different
-- worker would then fail SetWorkerNodeIDForContainer's CAS with
-- "session container ownership changed before worker ownership could be recorded"
-- and the session got stuck.
--
-- ClearContainerID now nulls both columns, so new occurrences are prevented.
-- This statement unsticks rows already in the bad state.
UPDATE sessions
SET worker_node_id = NULL
WHERE container_id IS NULL
  AND worker_node_id IS NOT NULL;
