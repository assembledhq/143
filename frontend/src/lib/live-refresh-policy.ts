import type { QueryKey } from "@tanstack/react-query";
import type { LiveHealth } from "@/lib/live-events";

export type LiveRefreshSurface =
  | "list"
  | "active-detail"
  | "terminal-detail"
  | "converging";

const ranges: Record<LiveRefreshSurface, { healthy: [number, number] | null; degraded: [number, number]; sustained: [number, number] }> = {
  list: { healthy: [120_000, 300_000], degraded: [10_000, 30_000], sustained: [30_000, 90_000] },
  "active-detail": { healthy: [30_000, 60_000], degraded: [5_000, 15_000], sustained: [15_000, 30_000] },
  "terminal-detail": { healthy: null, degraded: [30_000, 90_000], sustained: [30_000, 90_000] },
  converging: { healthy: [2_000, 5_000], degraded: [2_000, 5_000], sustained: [2_000, 5_000] },
};
const fallbackTelemetrySeen = new Set<string>();

const browserClientID = (): string => {
  const key = "143:browser-client-id";
  const current = localStorage.getItem(key);
  if (current) return current;
  const created = crypto.randomUUID();
  localStorage.setItem(key, created);
  return created;
};

function hash(value: string): number {
  let result = 2166136261;
  for (let index = 0; index < value.length; index++) {
    result ^= value.charCodeAt(index);
    result = Math.imul(result, 16777619);
  }
  return result >>> 0;
}

/** Stable per browser/query/state jitter prevents synchronized fleet polling. */
export function liveRefreshInterval(
  queryKey: QueryKey,
  surface: LiveRefreshSurface,
  health: LiveHealth,
  visible = typeof document === "undefined" || document.visibilityState === "visible",
): number | false {
  if (typeof window === "undefined") return false;
  if (!visible || health === "offline") return false;
  const range = health === "healthy"
    ? ranges[surface].healthy
    : health === "degraded-sustained"
      ? ranges[surface].sustained
      : ranges[surface].degraded;
  if (!range) return false;
  const [minimum, maximum] = range;
  const seed = `${browserClientID()}:${JSON.stringify(queryKey)}:${surface}:${health}`;
  if (health !== "healthy") {
    const telemetryKey = `${surface}:${health}`;
    if (!fallbackTelemetrySeen.has(telemetryKey)) {
      fallbackTelemetrySeen.add(telemetryKey);
      window.dispatchEvent(new CustomEvent("143:live-telemetry", { detail: { name: "fallback_poll", surface, health } }));
    }
  }
  return minimum + (hash(seed) % (maximum - minimum + 1));
}
