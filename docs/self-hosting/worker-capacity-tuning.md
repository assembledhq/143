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

- Base sandbox memory default is 4096 MB.
- The first-order limit is roughly `floor(node_ram_gb / 4)`.
- We cap by available vCPU when that is lower.
- In short: `WORKER_PROCESS_COUNT ~= min(vCPU, floor(RAM_GB / 4))`.

This is intentionally not ultra-conservative: it targets full utilization at the default sandbox memory size.

## Where to set these in production

For fleet-managed deploys, set these values in `.env.production.enc` (same pattern as other deploy-time env vars like `MODE`, `DB_HOST`, etc.). Deploy/provision scripts write them into `/opt/143/.env` for worker nodes.

For fleet-managed deploys, either set `WORKER_PROCESS_COUNT` directly or map specific hosts in one env var using `WORKER_BUCKET_MAP`.

Example:

```dotenv
# map host/IP to bucket in one variable
WORKER_BUCKET_MAP=10.0.0.4=hcloud-cpx21,10.0.0.5=hcloud-cpx31,10.0.0.6=hcloud-ccx23
```

Built-in bucket presets used by deploy/provision scripts:

- Hetzner CPX (shared CPU):
  - `hcloud-cpx11` → `WORKER_PROCESS_COUNT=1`
  - `hcloud-cpx21` → `WORKER_PROCESS_COUNT=1`
  - `hcloud-cpx31` → `WORKER_PROCESS_COUNT=2`
  - `hcloud-cpx41` → `WORKER_PROCESS_COUNT=4`
  - `hcloud-cpx51` → `WORKER_PROCESS_COUNT=8`
- Hetzner CCX (dedicated CPU):
  - `hcloud-ccx13` → `WORKER_PROCESS_COUNT=2`
  - `hcloud-ccx23` → `WORKER_PROCESS_COUNT=4`
  - `hcloud-ccx33` → `WORKER_PROCESS_COUNT=8`
  - `hcloud-ccx43` → `WORKER_PROCESS_COUNT=16`
  - `hcloud-ccx53` → `WORKER_PROCESS_COUNT=32`
  - `hcloud-ccx63` → `WORKER_PROCESS_COUNT=48`
- `ec2-t3.xlarge` → `WORKER_PROCESS_COUNT=4`
- `ec2-c6i.2xlarge` → `WORKER_PROCESS_COUNT=6`
- `ec2-c6i.4xlarge` → `WORKER_PROCESS_COUNT=10`

Per-sandbox defaults are intentionally consistent across buckets unless you explicitly override:

- `SANDBOX_CPU_LIMIT=2`
- `SANDBOX_MEMORY_LIMIT_MB=4096`
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
