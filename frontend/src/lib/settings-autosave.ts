import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

/**
 * Patch shape used by every `api.settings.update(...)` autosave call.
 */
export type SettingsPatch = {
  name?: string;
  settings?: Partial<OrgSettings>;
};

const NESTED_OBJECT_KEYS: ReadonlySet<keyof OrgSettings> = new Set([
  "agent_config",
  "product_context",
  "sandbox_network",
  "sandbox_lifecycle",
  "sandbox_resources",
  "session_automation",
]);

/**
 * Apply an `api.settings.update` patch to the React Query cache entry for
 * `queryKeys.settings.all`. Known nested object settings are deep-merged one
 * level to match the server's `mergeSettingsJSON` behavior.
 */
export function applyOrgSettingsPatch(prev: unknown, patch: SettingsPatch): unknown {
  const previous = prev as SingleResponse<Organization> | undefined;
  if (!previous?.data) {
    // The save still fires, but we have nothing to optimistically apply —
    // the user will see the indicator cycle without the value flipping
    // locally until the server responds. Most likely cause: a save
    // dispatched before the initial `settings.get` resolves, or after a
    // cache eviction. Flag it so this doesn't get mistaken for a bug in
    // the caller.
    if (process.env.NODE_ENV !== "production") {
      console.warn(
        "applyOrgSettingsPatch: cache entry is empty; optimistic write skipped. The save will still fire but the UI will lag one round-trip.",
      );
    }
    return previous;
  }
  return {
    ...previous,
    data: {
      ...previous.data,
      ...(patch.name !== undefined ? { name: patch.name } : {}),
      settings: mergeSettings(previous.data.settings, patch.settings),
    },
  };
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function mergeSettings(
  base: Organization["settings"] | Partial<OrgSettings> | undefined,
  patch: Partial<OrgSettings> | undefined,
): Organization["settings"] {
  if (!patch) return { ...(base ?? {}) };
  const merged = { ...(base ?? {}), ...patch };
  for (const key of Object.keys(patch) as (keyof OrgSettings)[]) {
    if (!NESTED_OBJECT_KEYS.has(key)) continue;
    const current = base?.[key];
    const incoming = patch[key];
    if (!isPlainObject(current) || !isPlainObject(incoming)) continue;
    merged[key] = mergePlainObjects(current, incoming) as never;
  }
  return merged;
}

function mergePlainObjects(
  base: Record<string, unknown>,
  patch: Record<string, unknown>,
): Record<string, unknown> {
  const merged = { ...base, ...patch };
  for (const key of Object.keys(patch)) {
    const current = base[key];
    const incoming = patch[key];
    if (!isPlainObject(current) || !isPlainObject(incoming)) continue;
    merged[key] = mergePlainObjects(current, incoming);
  }
  return merged;
}

/**
 * Coalesce two queued settings patches into one. Later keys win per the
 * spread operator. Use this as the `coalesce` option for `useAutosave`
 * when a single page may emit multiple in-flight saves.
 */
export function coalesceSettingsPatch(a: SettingsPatch, b: SettingsPatch): SettingsPatch {
  return {
    ...(a.name !== undefined ? { name: a.name } : {}),
    ...(b.name !== undefined ? { name: b.name } : {}),
    settings: mergeSettings(a.settings, b.settings),
  };
}
