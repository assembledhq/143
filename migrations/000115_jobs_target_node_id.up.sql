-- Node-affinity job routing: pin sandbox-bound jobs (continue_session, open_pr,
-- run_agent for resume) to the worker node that owns the session's container.
--
-- Container ids are local to a docker daemon. When a worker on host A creates
-- a sandbox for a session, only host A's daemon can exec into it. Before this
-- column existed, any worker could claim any pending job from the queue (ORDER
-- BY priority, created_at), so a continue_session job for a session whose
-- container lives on host A could be picked up by host B's worker — which then
-- failed every Docker exec with "No such container: <id>" because B's daemon
-- has never seen that id. Symptom: session failed mid-turn for what looked like
-- an auth or sandbox-state issue but was actually a routing miss.
--
-- target_node_id is NULL by default — unpinned jobs (the vast majority: PM
-- analyses, Linear webhooks, post-PR snapshots, etc.) keep claiming on any
-- worker just like before. Pinning is opt-in at the enqueue site.
--
-- A pinned job becomes claimable by any worker if its target node is marked
-- dead in the `nodes` table — symmetrical to ReclaimLostRunningJobs. This
-- prevents starvation when a node is permanently lost (host failure, scaling
-- down) so an in-flight session's pending job doesn't sit forever.
ALTER TABLE jobs ADD COLUMN target_node_id text;

-- Partial index for the pinned-claim path. Most pending jobs at any moment
-- have target_node_id IS NULL (general queue), so indexing only the rows
-- where target_node_id is set keeps the index small while still letting the
-- claim query short-circuit "is there a pinned job for this worker?".
-- Ordered to match the ClaimNextRunnable ORDER BY (priority DESC, created_at
-- ASC) so an index scan can return rows in claim order without a sort.
CREATE INDEX idx_jobs_target_dequeue
  ON jobs (target_node_id, status, run_at, priority DESC, created_at ASC)
  WHERE target_node_id IS NOT NULL AND status = 'pending';
