/**
 * UI bounds for org settings forms. Kept in one place so the agent and
 * autopilot pages can't drift apart while editing the same server field.
 *
 * Server-side values live in `internal/models/org_settings.go`:
 * - `DefaultMaxConcurrentRuns` / `OrgSize.MaxConcurrentRuns()` — the UI
 *   max mirrors the Enterprise tier's recommendation; any int is accepted
 *   by the server, so the UI cap is purely for sanity.
 * - `MinMaxSessionDurationSeconds` / `MaxMaxSessionDurationSeconds` are
 *   enforced by `ParseOrgSettings`, which clamps on save. These mirror
 *   those bounds so the number input matches server clamping.
 * - `DefaultPMScheduleHours` — the server has no hard bound on this field;
 *   the UI cap is a soft ceiling (a day's worth of hours).
 */

export const MIN_CONCURRENT_RUNS = 1;
export const MAX_CONCURRENT_RUNS = 25;

export const MIN_SESSION_DURATION_MINUTES = 2;
export const MAX_SESSION_DURATION_MINUTES = 120;

export const DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER = 4;
export const MIN_PREVIEW_MAX_PREVIEWS_PER_USER = 1;
export const MAX_PREVIEW_MAX_PREVIEWS_PER_USER = 20;

export const DEFAULT_PREVIEW_MAX_CPU_MILLIS = 2000;
export const MIN_PREVIEW_MAX_CPU_MILLIS = 250;
export const MAX_PREVIEW_MAX_CPU_MILLIS = 2000;

export const DEFAULT_PREVIEW_MAX_MEMORY_MIB = 8 * 1024;
export const MIN_PREVIEW_MAX_MEMORY_MIB = 512;
export const MAX_PREVIEW_MAX_MEMORY_MIB = 8 * 1024;

export const DEFAULT_PREVIEW_MAX_EPHEMERAL_DISK_MIB = 10 * 1024;
export const MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB = 1024;
export const MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB = 10 * 1024;

export const DEFAULT_COMPLETED_SESSION_RETENTION_MINUTES = 60;
export const MIN_COMPLETED_SESSION_RETENTION_MINUTES = 0;
export const MAX_COMPLETED_SESSION_RETENTION_MINUTES = 24 * 60;

export const DEFAULT_IDLE_PREVIEW_TTL_MINUTES = 4 * 60;
export const MIN_IDLE_PREVIEW_TTL_MINUTES = 15;
export const MAX_IDLE_PREVIEW_TTL_MINUTES = 24 * 60;

export const PM_SCHEDULE_MIN_HOURS = 1;
export const PM_SCHEDULE_MAX_HOURS = 24;

export function clampNumber(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
