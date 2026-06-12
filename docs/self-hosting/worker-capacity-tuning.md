# Worker Capacity Tuning Guide

This guide explains how to size worker nodes using:

- `WORKER_PROCESS_COUNT`
- `SANDBOX_CPU_LIMIT`
- `SANDBOX_MEMORY_LIMIT_MB`
- `SANDBOX_DISK_LIMIT_GB`

## Important: when changes take effect

Treat these as **startup-time settings**. In normal deployments, you change env vars and then restart/redeploy the server process/container. Runtime edits to env are not a supported operations model.

## What each knob controls

- **`WORKER_PROCESS_COUNT`**
  Number of worker loops in one server process. Higher = more jobs claimed/processed in parallel.
- **`SANDBOX_CPU_LIMIT`**
  CPU quota per sandbox container.
- **`SANDBOX_MEMORY_LIMIT_MB`**
  Memory limit per sandbox container.
- **`SANDBOX_DISK_LIMIT_GB`**
  Root filesystem size limit per sandbox container.

## Practical sizing heuristic

Pick target parallel sandboxes (`P`) for the node, then make sure:

- `P <= WORKER_PROCESS_COUNT`
- `P * SANDBOX_CPU_LIMIT <= 0.8 * node_vCPU`
- `P * SANDBOX_MEMORY_LIMIT_MB <= 0.75 * node_memory_mb`

Why 80% / 75%? The headroom covers the worker process itself, Docker/gVisor overhead, transient spikes, and OS background activity.

## Worker bucket defaults

Deploy/provision scripts now read bucket defaults from one shared file:
`deploy/scripts/worker_buckets.sh`.

How shared-CPU (CPX) counts were chosen:

- Base sandbox memory default is 3072 MB (3 GB cgroup limit). Tmpfs at /tmp
  (256 MiB) + /var/tmp (512 MiB) consumes up to ~768 MB of that, leaving
  ~2.25 GB for the agent's actual heap.
- Reserve host RAM first: `max(2 GB, 10% of node RAM)`.
- The first-order memory limit is roughly `floor((node_ram_gb - reserve_gb) / 3)`.
- We cap by available vCPU when that is lower. `SANDBOX_CPU_LIMIT=2` is a
  throttling ceiling, not a reservation, so this allows modest CPU overcommit
  for LLM/network-bound runs without letting memory overcommit be the default.
- In short: `WORKER_PROCESS_COUNT ~= max(1, min(vCPU, floor((RAM_GB - reserve_GB) / 3)))`.

This is intentionally not ultra-conservative: it targets high utilization at the default sandbox memory size while leaving room for the worker process, Docker/gVisor overhead, kernel memory, and page cache.

## Where to set these in production

For fleet-managed deploys, set these values in `.env.production.enc` in your private secrets checkout (`SECRETS_DIR` — see [docs/secrets/README.md](../secrets/README.md)), same pattern as other deploy-time env vars like `MODE`, `DB_HOST`, etc. Deploy/provision scripts write them into `/opt/143/.env` for worker nodes.

For fleet-managed deploys, either set `WORKER_PROCESS_COUNT` directly or map specific hosts in one env var using `WORKER_BUCKET_MAP`.

Example:

```dotenv
# map bucket to host/IP in one variable
WORKER_BUCKET_MAP=hcloud-cpx21:10.0.0.4,hcloud-cpx31:10.0.0.5,hcloud-ccx23:10.0.0.6
```

Built-in bucket presets used by deploy/provision scripts:

- Hetzner CPX (shared CPU):
  - `hcloud-cpx11` → `WORKER_PROCESS_COUNT=1`
  - `hcloud-cpx21` → `WORKER_PROCESS_COUNT=1`
  - `hcloud-cpx31` → `WORKER_PROCESS_COUNT=2`
  - `hcloud-cpx41` → `WORKER_PROCESS_COUNT=4`
  - `hcloud-cpx51` → `WORKER_PROCESS_COUNT=9`
- Hetzner CCX (dedicated CPU):
  - `hcloud-ccx13` → `WORKER_PROCESS_COUNT=2`
  - `hcloud-ccx23` → `WORKER_PROCESS_COUNT=4`
  - `hcloud-ccx33` → `WORKER_PROCESS_COUNT=8`
  - `hcloud-ccx43` → `WORKER_PROCESS_COUNT=16`
  - `hcloud-ccx53` → `WORKER_PROCESS_COUNT=32`
  - `hcloud-ccx63` → `WORKER_PROCESS_COUNT=48`
- `ec2-t3.xlarge` → `WORKER_PROCESS_COUNT=4`
- `ec2-c6i.2xlarge` → `WORKER_PROCESS_COUNT=4`
- `ec2-c6i.4xlarge` → `WORKER_PROCESS_COUNT=9`

Per-sandbox defaults are intentionally consistent across buckets unless you explicitly override:

- `SANDBOX_CPU_LIMIT=2`
- `SANDBOX_MEMORY_LIMIT_MB=3072`
- `SANDBOX_DISK_LIMIT_GB=10`

`max_concurrent_runs` is a separate org-level execution policy in app settings, not a host-capacity bucket knob.

If a knob is omitted, runtime defaults still apply.

## Scale strategy

1. **Scale up vertically first** (raise `WORKER_PROCESS_COUNT` on a bigger node) until queue delay is acceptable.
2. **Scale out horizontally** (add worker nodes) when one node is near safe limits.
3. Keep per-sandbox limits stable unless workloads actually need larger CPU/memory envelopes.

## Failure signals that mean “dial back”

- Frequent OOM kills in sandbox/worker logs
- Sustained host memory pressure/swap
- Queue claim failures or high job retry rates due infrastructure errors
- Increased p95/p99 run duration after increasing process count

If you see these, lower `WORKER_PROCESS_COUNT` first, then lower per-sandbox CPU/memory limits if needed.

## Verifying density with runtime metrics

Workers emit OTel histograms (`container.memory.used`, `container.cpu.used`, `container.memory.utilization`, `container.cpu.utilization`) sampled every `RUNTIME_STATS_INTERVAL` (default 30s, set to `0` to disable). After bumping `WORKER_PROCESS_COUNT` or lowering `SANDBOX_MEMORY_LIMIT_MB`, watch p95 of `container.memory.utilization` for at least a week of real workload before treating the new size as proven. Sustained p95 above ~0.85 means you're one bad turn away from OOM kills; pull back.

**gVisor (runsc) caveat.** Production workers run gVisor by default. gVisor's stat surface is partial: `container.memory.used` is reported but with coarse granularity, and CPU throttling stats are zero. The histograms are still useful for relative comparison ("did p95 mem go up after the bucket bump?"), but treat absolute numbers as approximate. When in doubt, double-check on a runc dev worker.

**Tick-budget caveat at high density.** The sampler fans out at most 8 concurrent stats calls per tick with a 5s per-call timeout, so a tick can take up to `ceil(WORKER_PROCESS_COUNT / 8) * 5s` worst-case (typical Docker `stats?stream=false` calls return in ~1s, well under that ceiling). On the largest bucket (`hcloud-ccx63`, 48 processes) the ceiling is exactly 30s — the same as the default `RUNTIME_STATS_INTERVAL`. If you provision dense nodes with `WORKER_PROCESS_COUNT > 40`, raise `RUNTIME_STATS_INTERVAL` to `60s` so a slow tick can't overrun the next one and silently drop samples.

## Migration notes

The default `SANDBOX_MEMORY_LIMIT_MB` was lowered from `4096` to `3072` (paired with smaller tmpfs sizes so the agent's actual usable RAM went up, not down). Operators upgrading from earlier versions:

- If you run workloads that genuinely need >3 GB cgroup headroom (large PM bootstraps on monorepos, agents that load big indices into memory), set `SANDBOX_MEMORY_LIMIT_MB=4096` explicitly to preserve old behavior.
- If you also run with the old `WORKER_PROCESS_COUNT` and bump it per the new bucket presets, do it in one step per node — don't increase density and lower the per-sandbox limit at the same time on a node serving live traffic.
