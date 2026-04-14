// Status groups — keep in sync with models.ActiveStatuses / DoneStatuses
// in internal/models/session_enums.go.

export const activeStatuses = ["pending", "running", "idle", "awaiting_input", "needs_human_guidance"];
export const doneStatuses = ["completed", "pr_created", "failed", "cancelled", "skipped"];

export const activeSet = new Set(activeStatuses);
export const workingSet = new Set(["pending", "running"]);

/** Map a filter tab value to the comma-separated status string for the API. */
export function filterToStatusParam(filter: string | null, extraPassthrough?: string[]): string | undefined {
  if (!filter || filter === "all") return undefined;
  if (extraPassthrough?.includes(filter)) return undefined;
  if (filter === "active") return activeStatuses.join(",");
  if (filter === "done") return doneStatuses.join(",");
  return filter;
}
