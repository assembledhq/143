import type { SessionCounts } from "./types";

// Server caps each bucket (see SessionStore.CountsByOrg). Anything at the cap
// is unknown-beyond-cap and renders as e.g. "99+".
export function renderCount(
  value: number | undefined,
  counts: SessionCounts | undefined,
): string | undefined {
  if (value === undefined || !counts) return undefined;
  return value >= counts.cap ? `${counts.cap - 1}+` : String(value);
}

export function getCountForTab(
  tabValue: string,
  counts: SessionCounts | undefined,
): number | undefined {
  if (!counts) return undefined;
  if (tabValue === "all") return counts.all;
  if (tabValue === "active") return counts.active;
  if (tabValue === "archived") return counts.archived;
  return undefined;
}
