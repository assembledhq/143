# Recovering code reviews after database or deployment failures

Last verified: 2026-09-25. This is an operator runbook; inspect current state before applying it. A deploy failure, a stopped review agent, and a completed review awaiting publication require different recovery actions.

## Establish what failed

1. Inspect the exact deploy run, target SHA, and per-host logs. The app deployment is a barrier before workers start rolling. Worker deployment can return before detached rollover finishes; inspect the host status files and logs as well as GitHub Actions.
2. Check each worker's running image, heartbeat, and drain state. A healthy container alone does not establish a healthy database connection or successful rollover.
3. On an affected host, inspect `df -h`, `df -i`, and `docker system df`. On the database host, also inspect `/proc/meminfo`, `vm.overcommit_memory`, `vm.overcommit_ratio`, and swap. Under strict overcommit accounting, `Committed_AS` can approach `CommitLimit` while `MemAvailable` remains positive.
4. Prefer a targeted retry of a failed host after checking that its detached deployment has finished. A full fleet retry creates extra worker generations and can increase database pressure.

Use the repository's documented read-only tools for production diagnosis:

```sh
make deploy-worker-status
make db-query Q='SELECT now(), current_user'
make logs-query Q='service:api AND level:error AND _time:[now-15m,now]' LIMIT=100
```

See [secrets management](../secrets/README.md) for the configured secrets checkout. Do not print decrypted bundles or copy credentials into incident notes. If Docker cannot start the `db-query` helper under memory pressure, use existing logs while arranging an approved database connection. The `readonly` role may be permitted only over the container's local socket; do not change authentication rules to make a diagnostic tunnel work.

## Distinguish resource pressure from lock contention

Read the blocking graph, rather than terminating every old or idle connection:

```sql
SELECT a.pid, a.backend_start, a.xact_start, a.state_change,
       a.state, a.wait_event_type, a.wait_event,
       pg_blocking_pids(a.pid) AS blocking_pids
FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND (cardinality(pg_blocking_pids(a.pid)) > 0
       OR a.state = 'idle in transaction')
ORDER BY a.xact_start;

SELECT pid, classid, objid, objsubid, mode, granted
FROM pg_locks
WHERE locktype = 'advisory'
ORDER BY pid;
```

Some activity fields require additional monitoring privileges. Missing visibility is not evidence that a connection is safe to terminate. PostgreSQL lock-wait logs can identify the holder and wait queue when a fresh diagnostic connection cannot start.

Code-review publication serializes work using this transaction-scoped advisory-lock key:

```sql
SELECT hashtextextended(
  'code_review_status_comment:' || '<org-uuid>' || ':' || '<pull-request-uuid>',
  0
);
```

For this bigint advisory lock, `pg_locks.classid` and `objid` contain the upper and lower 32 bits; `objsubid = 1`. Map the lock to a PR, then inspect that tenant's `code_review_session_metadata`, `session_threads`, `session_executors`, `thread_runtimes`, and controller jobs. Filter tenant-owned queries by `org_id`. Confirm GitHub publication directly when a review ID exists.

## Release only a confirmed stale holder

Before proposing termination, record the backend PID and start time, database/user, client address and port, exact lock identity, transaction age, affected PR, and current agent/runtime states. A merged PR or old connection alone is insufficient evidence.

Obtain authorization for the specific connection. Immediately recheck its identity and lock ownership, then guard `pg_terminate_backend(pid, timeout)` with those same conditions. If they no longer match, stop and investigate again. PostgreSQL's backend PID can differ from the host PID; do not substitute one for the other or use a broad process kill.

Termination rolls back that connection's open transaction and disconnects its caller. It does not delete previously committed review results, but the caller may need recovery or a retry. A full PostgreSQL restart affects all clients. Reviews use renewable leases, including 60-second thread-runtime leases, so a restart is not guaranteed to preserve running agent turns.

Verify recovery through several signals:

- The identified holder is gone and its blocking chain has cleared or shortened.
- Database memory headroom and bounded queries improve.
- Worker heartbeats and job leases recover.
- Reviews progress and expected GitHub reviews are actually delivered.

Do not terminate the remaining waiters automatically. They may finish once the root holder releases its lock. Rebuild the graph before selecting another target.

## Contain old worker generations before retrying reviews

Inspect both `nodes.status` and `nodes.drain_intent`. An old generation with `status = 'active'` and `drain_intent = 'planned_rollout'` needs attention, particularly if it is still claiming jobs. Reapply admission draining through the deployment control path after confirming the affected generation; preserve owned agents and previews until retirement checks pass. Do not force-stop workers solely because a replacement is healthy.

Drain intent is durable for a node generation. Heartbeats and re-registration preserve it, including after a stale node was marked dead. The drain watcher, queue admission, and placement queries also honor it when status is still active. Start a replacement with a new generation-specific `NODE_ID`; restarting the old ID does not undo an operator drain. Ordinary graceful process shutdown records transient draining without creating durable intent, so a normal same-ID restart can still resume admission. Existing old binaries still need operator containment until they retire. The September incident observed active old generations retaining drain intent and claiming new work; the complete production transition history was not captured.

Publication timeouts and nonblocking job recovery added in [PR #2195](https://github.com/assembledhq/143/pull/2195) protect the new code paths. They do not retroactively bound an old open transaction or update old worker processes that continue running.

### Resume a deliberately drained generation

Prefer deploying a new generation. If a replacement failed and an existing fixed node ID must be reused, use `worker-deployctl resume --node-id <exact-id> --reason '<rollback reason>' --requested-by '<operator>'` from a configured deploy-control environment. It atomically clears the durable intent and records a `node_drain_cleared` audit event. It **does not resume a live process**: status remains draining/dead until that generation next registers. The drain watcher latches both local worker queues and heartbeat state, so a heartbeat outage after clearing intent cannot advertise capacity while the queues remain stopped.

Before restarting, pause further deployments, stop the detached drain monitor for this exact container, and verify it exited. It owns `/var/log/143/drain-worker-<first-12-container-id>.lock` and writes the matching `.log`; inspect the lock owner and process command line before stopping that specific monitor. Never kill all deploy processes or delete a held lock file. An old monitor can reapply drain or stop the resumed container even after intent is cleared.

Use `worker-deployctl status --node-id <exact-id> --json` and `retire-ready --node-id <exact-id>` to verify owned work has finished before stopping/restarting that container. Run `resume` after stopping the generation, then start it with the verified image and configuration. This also supports intentionally drained single-node/local fixed IDs. Verify fresh heartbeats, `status=active`, `drain_intent=none`, and successful job admission afterward. The command never stops agents, restarts containers, or clears executor/runtime ownership itself.

## Choose which reviews to recover

For each affected PR, check the latest review attempt, current head SHA, saved reviewer/synthesis results, active executors/runtimes, controller state, and GitHub review receipt.

- Already published: verify delivery and finish state reconciliation; do not start another review solely because an earlier local status was stale.
- Completed agents awaiting publication: use the supported publication/recovery path where possible, preserving existing results.
- Failed review with no newer attempt: retry after the infrastructure is stable. Check whether partial results are reusable through the supported workflow.
- Changed head: request a fresh review of the current revision.
- Merged, closed, or superseded review: reconcile obsolete work instead of launching another review of the old revision.

Do not manually reset job or assessment rows to bypass publication fencing or deduplication.

### A recovery request remains blocked by an active review

The scheduler's `HasActiveCodeReview` check includes active thread records, even when their review metadata is already failed. A session page can therefore say "Running" or "Reconnecting after maintenance" after its controller, executor, and runtime have stopped. Check those records independently; the page alone does not establish a live agent.

Inspect the exact thread's status and activity timestamps, terminal controller metadata, executor states, runtime closure and lease, and pending/running jobs. The stuck-thread reaper normally waits at least about 2½ hours. A cancellation request can also leave an orphan unchanged when no live orchestrator owns it.

The scheduler now also reconciles failed controllers whose threads never received cancellation: it requires a terminal executor (including lost) joined to a terminal job and no active executor, runtime, queued execution, or recheck. A lost executor with a pending recovery job remains a blocker. This repair preserves saved outputs and publication receipts; it does not retry a review by itself. Request a review through the product and verify one replacement starts.

If immediate recovery still requires reconciling orphaned records, keep it within the specifically authorized incident scope. Recheck and lock the exact tenant/session/thread identities in a bounded transaction; require failed controller metadata, terminal executors, closed runtimes, no pending/running jobs, and unchanged activity timestamps. Fail only the verified orphan records with an explanatory failure category, preserving saved results. Abort if any guard or expected row count changes. Then let the supported review request and scheduler allocate the replacement; do not reopen the failed controller or alter publication receipts. Verify that each request resolves to one replacement session and that its executors start on healthy workers.

## September 25, 2026 incident evidence

The initial deploy failed because one worker's root filesystem was full. Writing the environment file failed and left it empty. Clearing unused image cache allowed the normal deploy process to restore the file and start the replacement. Docker volumes dominated disk usage and were preserved because they could contain workspace or preview state.

The retry then failed a worker verification query with PostgreSQL allocation errors. Those errors were present more than two hours before deployment and increased during the fleet retry. Strict memory commitment limits, blocked transactions, and overlapping workers were observed; their individual memory contributions were not measured.

After authorization, one precisely identified stale publication connection was terminated. Observed blocked connections fell from 53 to 13 and then 8, committed memory decreased by roughly 900 MB, and two delayed reviews published approvals. Other old lock holders cleared without intervention. These are incident observations, not a controlled performance measurement.

After further authorization, admission draining was restored on the affected old workers. A second precisely revalidated stale publication connection was terminated. No full database restart occurred, and no active agents or previews were force-stopped.

Three current-revision reviews were requested through the product. One replacement started directly; two were blocked by orphaned thread records despite failed controllers and terminal executors. A guarded transaction reconciled exactly those two threads to failed, preserving their saved output and leaving jobs and assessments untouched. All three requests then resolved to replacement sessions. At 21:57 UTC they were still running, including one in synthesis; final GitHub delivery was not yet verified for those replacements. The database had no blocked connections or idle transactions older than one minute; all six active workers ran the replacement build with fresh heartbeats, and the sampled organization's running jobs had no expired leases. A bounded API/worker log query found no allocation or review-publication errors since 21:50 UTC.

The drain-intent and terminal-thread protections above address the first two recovery gaps. Remaining operational follow-ups include preventing environment-file truncation on failed writes, investigating retained volume growth, and sizing database headroom for overlapping generations. Memory commitment remained close to its limit under resumed work, so cleared locks do not establish that capacity is sufficient.
