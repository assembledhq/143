import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import type { CodingCredentialSummary, OpenCodeModelInfo } from "@/lib/types";

// Stable references for the empty case so consumers' useMemo deps don't churn
// every render (which would break parent memoization / inflate render counts).
const EMPTY_MODELS: OpenCodeModelInfo[] = [];
const EMPTY_AVAILABILITY: Map<string, OpenCodeModelAvailability> = new Map();

// useOpenCodeModels fetches the OpenCode logical-model registry (one entry per
// model with its ordered routes) from the backend — the single source of truth.
// The registry is effectively static, so it is cached aggressively. Returns a
// stable [] while loading or on error; route-dependent UI degrades gracefully
// (no badge, nothing disabled) rather than blocking the picker.
export function useOpenCodeModels(): OpenCodeModelInfo[] {
  const { data } = useQuery({
    queryKey: ["opencode-models"],
    queryFn: () => api.settings.getOpenCodeModels(),
    staleTime: 60 * 60 * 1000,
    gcTime: 60 * 60 * 1000,
  });
  return data?.data ?? EMPTY_MODELS;
}

// runnableOpenCodeBackings returns the set of OpenCode backing providers the
// user has a runnable (healthy or rate-limited) credential for. The unified
// coding-credential summary's `provider` field carries the backing provider for
// opencode rows (e.g. "openrouter", "opencode", "openai").
export function runnableOpenCodeBackings(
  codingCredentials: readonly CodingCredentialSummary[],
): Set<string> {
  const backings = new Set<string>();
  for (const row of codingCredentials) {
    if (row.agent !== "opencode") continue;
    if (row.status !== "healthy" && row.status !== "rate_limited") continue;
    if (row.provider) backings.add(row.provider);
  }
  return backings;
}

export interface OpenCodeModelAvailability {
  // hasRunnableRoute is false when none of the model's routes has a runnable
  // credential — the picker entry should be disabled.
  hasRunnableRoute: boolean;
  // transportLabel is the transport that would run given current keys (the
  // first route in priority order with a runnable credential), or null.
  transportLabel: string | null;
}

// openCodeAvailabilityById builds a lookup from logical/physical model id to its
// route availability, given the registry and the user's runnable backings. When
// the registry is empty (API still loading / failed) the map is empty and the
// caller treats every model as enabled with no badge.
//
// `requireOpenRouter` mirrors the org policy: when true, native routes are
// excluded from availability (they would be blocked at launch).
export function openCodeAvailabilityById(
  models: readonly OpenCodeModelInfo[],
  availableBackings: ReadonlySet<string>,
  requireOpenRouter: boolean,
): Map<string, OpenCodeModelAvailability> {
  const byId = new Map<string, OpenCodeModelAvailability>();
  for (const model of models) {
    let transportLabel: string | null = null;
    for (const route of model.routes) {
      if (requireOpenRouter && route.backing === "opencode") continue;
      if (availableBackings.has(route.backing)) {
        transportLabel = route.transport_label;
        break;
      }
    }
    const availability: OpenCodeModelAvailability = {
      hasRunnableRoute: transportLabel !== null,
      transportLabel,
    };
    byId.set(model.id, availability);
    // Index physical ids too so a pinned selection resolves the same way.
    for (const route of model.routes) {
      byId.set(route.physical_model_id, availability);
    }
  }
  return byId;
}

// useOpenCodeAvailability composes the registry + credentials into the per-model
// availability lookup used by the picker.
export function useOpenCodeAvailability(
  codingCredentials: readonly CodingCredentialSummary[],
  requireOpenRouter = false,
): Map<string, OpenCodeModelAvailability> {
  const models = useOpenCodeModels();
  return useMemo(() => {
    if (models.length === 0) return EMPTY_AVAILABILITY;
    const backings = runnableOpenCodeBackings(codingCredentials);
    return openCodeAvailabilityById(models, backings, requireOpenRouter);
  }, [models, codingCredentials, requireOpenRouter]);
}
