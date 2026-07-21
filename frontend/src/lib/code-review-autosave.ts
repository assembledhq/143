import type { CodeReviewPolicyConfig, CodeReviewResolvedPolicy, SingleResponse } from "@/lib/types";

/**
 * Optimistic-update + coalesce helpers for the code-review policy autosave
 * organization policy (`queryKeys.codeReviews.policy`).
 *
 * The policy save is a single whole-config `PUT` (no partial patches), so the
 * cache entry's `config` is replaced wholesale and queued saves coalesce by
 * keeping the latest. See `frontend/src/app/(dashboard)/settings/AGENTS.md`.
 */
export function applyCodeReviewPolicyOptimistic(prev: unknown, config: CodeReviewPolicyConfig): unknown {
  const previous = prev as SingleResponse<CodeReviewResolvedPolicy> | undefined;
  if (!previous?.data) {
    if (process.env.NODE_ENV !== "production") {
      console.warn(
        "applyCodeReviewPolicyOptimistic: cache entry is empty; optimistic write skipped. The save will still fire but the UI will lag one round-trip.",
      );
    }
    return previous;
  }
  return {
    ...previous,
    data: { ...previous.data, config },
  };
}

/**
 * Coalesce two queued whole-config saves — the later payload already contains
 * every field, so the latest wins. Module-level export keeps it referentially
 * stable across callers of the same `queryKey` (required by `useAutosave`).
 */
export function coalesceCodeReviewPolicy(
  _a: CodeReviewPolicyConfig,
  b: CodeReviewPolicyConfig,
): CodeReviewPolicyConfig {
  return b;
}
