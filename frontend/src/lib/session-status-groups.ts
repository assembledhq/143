// Status groups — keep in sync with models.NeedsAttentionStatuses / WorkingStatuses / FailedStatuses / DoneStatuses
// in internal/models/session_enums.go.

export const needsAttentionStatuses = ["awaiting_input", "needs_human_guidance"];
export const workingStatuses = ["pending", "running"];
export const failedStatuses = ["failed"];
export const doneStatuses = ["completed", "pr_created", "cancelled", "skipped", "idle"];

export const needsAttentionSet = new Set(needsAttentionStatuses);
export const workingSet = new Set(workingStatuses);
export const failedSet = new Set(failedStatuses);

/** Map a filter tab value to the comma-separated status string for the API. */
export function filterToStatusParam(filter: string | null, extraPassthrough?: string[]): string | undefined {
  if (!filter || filter === "all") return undefined;
  if (extraPassthrough?.includes(filter)) return undefined;
  if (filter === "needs_attention") return needsAttentionStatuses.join(",");
  if (filter === "working") return workingStatuses.join(",");
  if (filter === "failed") return failedStatuses.join(",");
  if (filter === "done") return doneStatuses.join(",");
  return filter;
}
