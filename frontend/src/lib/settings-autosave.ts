import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

/**
 * Patch shape used by every `api.settings.update(...)` autosave call.
 */
export type SettingsPatch = { settings: Partial<OrgSettings> };

/**
 * Keys inside `OrgSettings` whose values are nested objects. Patching any of
 * these through `applyOrgSettingsPatch` REPLACES the entire nested object
 * (shallow spread), so callers must build the full merged nested value before
 * dispatching. In dev builds we warn when a partial nested object is passed,
 * since this is the most common autosave footgun.
 */
const NESTED_OBJECT_KEYS: ReadonlySet<keyof OrgSettings> = new Set([
  "agent_config",
  "product_context",
]);

/**
 * Apply an `api.settings.update` patch to the React Query cache entry for
 * `queryKeys.settings.all`. Merges at the `data.settings.<key>` level;
 * callers that need to update nested objects (e.g. `agent_config`) must
 * pre-merge the nested object before passing it in — see
 * `saveAgentConfigField` in `/settings/agent/page.tsx` for the canonical
 * pattern.
 */
export function applyOrgSettingsPatch(prev: unknown, patch: SettingsPatch): unknown {
  const previous = prev as SingleResponse<Organization> | undefined;
  if (process.env.NODE_ENV !== "production") {
    warnIfPartialNestedPatch(previous, patch);
  }
  if (!previous?.data) return previous;
  return {
    ...previous,
    data: {
      ...previous.data,
      settings: { ...previous.data.settings, ...patch.settings },
    },
  };
}

function warnIfPartialNestedPatch(
  previous: SingleResponse<Organization> | undefined,
  patch: SettingsPatch,
): void {
  const existing = previous?.data?.settings ?? ({} as Partial<OrgSettings>);
  for (const key of Object.keys(patch.settings) as (keyof OrgSettings)[]) {
    if (!NESTED_OBJECT_KEYS.has(key)) continue;
    const incoming = patch.settings[key];
    const current = existing[key];
    if (!isPlainObject(incoming) || !isPlainObject(current)) continue;
    const currentKeys = Object.keys(current);
    const incomingKeys = new Set(Object.keys(incoming));
    const missing = currentKeys.filter((k) => !incomingKeys.has(k));
    if (missing.length > 0) {
      console.warn(
        `applyOrgSettingsPatch: shallow-merging "${String(key)}" will drop existing keys [${missing.join(", ")}]. Pass the full merged object instead.`,
      );
    }
  }
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Coalesce two queued settings patches into one. Later keys win per the
 * spread operator. Use this as the `coalesce` option for `useAutosave`
 * when a single page may emit multiple in-flight saves.
 */
export function coalesceSettingsPatch(a: SettingsPatch, b: SettingsPatch): SettingsPatch {
  return { settings: { ...a.settings, ...b.settings } };
}
