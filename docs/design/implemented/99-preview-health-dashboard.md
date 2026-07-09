# Design: Preview Health Dashboard

> **Status:** Implemented | **Last reviewed:** 2026-06-13

The preview health dashboard is a compact internal Grafana surface for the
preview subsystem's most important operational signals: active preview count,
startup latency, ready vs failed/unavailable previews, cache restore hit rate,
and recent preview-related errors.

The dashboard stays on the existing VictoriaLogs path. Workers emit a
low-volume `preview health: lifecycle sample` structured log once per minute,
backed by a platform-wide aggregate over `preview_instances`. Preview cache
restore observers emit `preview health: cache event` logs with `cache_kind`,
`operation`, `status`, and a boolean `cache_hit`, so Grafana can show restore
hit rate without adding a Prometheus/VictoriaMetrics datasource.

Workers also persist per-preview resource samples in
`preview_resource_samples` when the sandbox provider exposes runtime stats.
Each sample is scoped by `org_id`, references `preview_instances`, records the
current launch/runtime phase, memory bytes, memory limit, CPU usage, worker
node, and a best-effort JSON process snapshot. `preview_instances` keeps
`peak_memory_bytes`, `peak_memory_sampled_at`, and `peak_memory_phase` so
failure triage can quickly distinguish install/build/startup/runtime pressure
without replaying all samples. Detailed samples are retained for the recent
triage window only; the preview cleanup worker prunes
`preview_resource_samples` after 24 hours in bounded batches.

The provisioned dashboard lives at
`deploy/grafana/provisioning/dashboards/preview-health.json` and intentionally
keeps only five panels so it remains an operator triage view rather than a deep
analysis workspace.
