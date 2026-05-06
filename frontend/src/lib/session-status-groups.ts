// Status groups — keep in sync with models.ActiveStatuses / DoneStatuses
// in internal/models/session_enums.go.

export const activeStatuses = ["pending", "running", "idle", "awaiting_input", "needs_human_guidance"];
export const doneStatuses = ["completed", "pr_created", "failed", "cancelled", "skipped"];

// "Working" means the agent is actively processing or about to: skeleton
// shimmer, refetch polling, and "Agent is working..." indicators all key off
// this set. Distinct from `activeStatuses`, which also counts idle/awaiting
// states where the user holds the turn.
export const workingStatuses = ["pending", "running", "awaiting_input"];

export const activeSet = new Set(activeStatuses);
export const workingSet = new Set(["pending", "running"]);
export const workingStatusesSet = new Set(workingStatuses);

/** Map a filter tab value to the comma-separated status string for the API. */
export function filterToStatusParam(filter: string | null, extraPassthrough?: string[]): string | undefined {
  if (!filter || filter === "all") return undefined;
  if (extraPassthrough?.includes(filter)) return undefined;
  if (filter === "active") return activeStatuses.join(",");
  if (filter === "done") return doneStatuses.join(",");
  return filter;
}
