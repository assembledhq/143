# Worker database connection pools

Workers and session executors use small pools of reusable PostgreSQL connections. The worker Compose configuration sets `DATABASE_MAX_CONN_IDLE_TIME` to five minutes through `WORKER_DATABASE_MAX_CONN_IDLE_TIME`. New executors inherit the worker's effective environment. Without an explicit idle setting, the pool library defaults to 30 minutes.

The shorter lifetime lets unused connections opened during bursts close sooner. It does not change the connection ceiling (four per worker or executor by default), cancel active queries or transactions, or limit how long an agent can run. Regular heartbeats keep connections in use. Reopening a retired connection adds connection setup work; measure behavior under your workload before expanding a rollout. Existing worker and executor processes keep their startup configuration.

This setting does not change PostgreSQL query memory, temporary-file limits, maintenance budgets or parallelism. Those require a separate database-capacity review.

## Override the worker setting

For workers managed by `deploy/scripts/deploy.sh`, the persistent host override belongs in `/opt/143/.env.local`. Deployment rebuilds `/opt/143/.env` from its supported shared values and appends `.env.local`. Setting `WORKER_DATABASE_MAX_CONN_IDLE_TIME` only in the deploying machine's shell does not forward it to the host. Editing `/opt/143/.env` directly is temporary and will be overwritten on a secrets refresh.

To retain the previous 30-minute lifetime on one worker host, back up `.env.local` privately and edit only this entry, preserving its existing identity values, ownership and restrictive permissions. Keep exactly one assignment:

```dotenv
WORKER_DATABASE_MAX_CONN_IDLE_TIME=30m
```

The same mechanism accepts a positive Go duration such as `5m`. Apply it through a coordinated worker deployment; editing the file alone does not change a running process. Provisioning can regenerate `.env.local`, so retain the override in your private host configuration record and recheck it after reprovisioning. Do not copy the complete environment file into logs or public documentation.

Compose translates that worker-specific value into `DATABASE_MAX_CONN_IDLE_TIME`. The container entrypoint subsequently loads the encrypted environment bundle, which can override `DATABASE_MAX_CONN_IDLE_TIME` for both workers and API processes. Check for an existing bundle override before relying on the host setting. Changing the shared bundle is a separate configuration change; the worker-specific host override cannot supersede a value loaded by the entrypoint. Verify the actual process environment and a new executor's `database_max_conn_idle_time` startup log field, inspecting only this setting rather than printing secret-bearing environments.

## Coordinate a controlled rollout

Manual foreground deployments do not share GitHub Actions' deployment concurrency group. Only detached worker rollovers take the script's host rollover lock, and staging configuration happens before that lock. Watching for an idle deploy run is not mutual exclusion.

Before staging any files, designate one maintenance owner and establish a deployment hold for the entire canary and any rollback. For a deployment driven by the repository's **Build & Deploy** GitHub Actions workflow, disable that workflow through the repository's workflow controls (or `gh workflow disable deploy.yml` with the correct repository selected). Disabling new triggers does not finish existing runs: wait for queued/running deployment jobs and every already-launched detached host rollover to settle. Use `make deploy-worker-status` and the host rollout status/logs to confirm completion. Coordinate a freeze on manual deployments and any other schedulers too. If these sources cannot be held, postpone the manual canary. Keep the hold until the final configuration is verified, then restore the workflow's previous enabled state and resume the agreed deployment sources.

Within that hold:

1. Select one worker and record its configuration revision, host override, server and sandbox image revisions and digests, and effective idle setting in private operations notes. Pin compatible images; a configuration checkout and an image tag are separate rollback inputs. Worker-only deployment does not run migrations, so verify compatibility with the deployed application and schema before proceeding.
2. Establish a baseline over a representative workload: database connection counts, worker heartbeats, job/executor leases, queue delay, review completion and verified GitHub publication. Record observation timestamps and monitoring visibility limits. Database activity statistics may require monitoring privileges.
3. Use the ordinary worker preflight and routine blue/green deployment for that single explicit host. Keep pruning, volume pruning, daemon restarts and forced runtime interruption disabled. Let existing owned jobs, executors and previews drain through the supported deployment controls; a healthy replacement alone does not justify stopping an old generation. Leave `WORKER_DEPLOY_DETACH` empty for foreground feedback; any nonempty value, including `0`, enables detachment.
4. Verify successful rollover, the new worker's image identity, fresh database heartbeat and preview RPC authentication, and the effective idle setting in both the worker process and a newly launched executor. These checks establish configuration and connectivity, not completed work.
5. Observe at least 15 minutes after those checks, including completed workload through the new generation. Compare lease renewal, queue delay, review completion and confirmed publication with the baseline. Investigate errors or material latency changes before continuing to more workers. Verify old generations retire only after their owned work drains.

The application does not currently report pool acquisition waits, idle-close counts or new-connection counts. Startup configuration proves which value was loaded; it does not prove connections retired or establish memory savings. A successful workload window establishes functional compatibility for the observed scope. Fleet behavior, reconnect latency and capacity improvements require additional measurements.

## Roll back

Keep the deployment hold active. Restore the recorded host override and reviewed worker configuration, then use the same routine deployment path on the same host with the recorded compatible server and sandbox images. Restoring an image tag alone leaves the Compose setting in place. Conversely, restoring the configuration does not change images already running. Recheck application/schema compatibility before any image rollback.

For a setting-only rollback that retains current compatible images, use the explicit `30m` host override above in a reviewed configuration, after excluding an encrypted-bundle override. Verify the replacement worker and a new executor read back the intended value, complete work and publish successfully. Preserve owned work during draining; do not bypass lease checks or force-stop an old generation to finish rollback. See [code-review recovery](code-review-recovery.md) for diagnosing stalled work and [secrets management](../secrets/README.md) for environment-bundle setup.
