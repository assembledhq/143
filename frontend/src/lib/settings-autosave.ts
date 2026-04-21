import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

/**
 * Patch shape used by every `api.settings.update(...)` autosave call.
 */
export type SettingsPatch = { settings: Partial<OrgSettings> };

/**
 * Apply an `api.settings.update` patch to the React Query cache entry for
 * `queryKeys.settings.all`. Merges at the `data.settings.<key>` level;
 * callers that need to update nested objects (e.g. `agent_config`) must
 * pre-merge the nested object before passing it in.
 */
export function applyOrgSettingsPatch(prev: unknown, patch: SettingsPatch): unknown {
  const previous = prev as SingleResponse<Organization> | undefined;
  if (!previous?.data) return previous;
  return {
    ...previous,
    data: {
      ...previous.data,
      settings: { ...previous.data.settings, ...patch.settings },
    },
  };
}

/**
 * Coalesce two queued settings patches into one. Later keys win per the
 * spread operator. Use this as the `coalesce` option for `useAutosave`
 * when a single page may emit multiple in-flight saves.
 */
export function coalesceSettingsPatch(a: SettingsPatch, b: SettingsPatch): SettingsPatch {
  return { settings: { ...a.settings, ...b.settings } };
}
