import type { QueryClient } from "@tanstack/react-query";
import { queryKeys } from "./query-keys";
import type { Automation, ListResponse } from "./types";

function isAutomationListResponse(value: unknown): value is ListResponse<Automation> {
  return (
    typeof value === "object" &&
    value !== null &&
    "data" in value &&
    Array.isArray((value as { data?: unknown }).data)
  );
}

export function upsertAutomationInListCaches(
  queryClient: QueryClient,
  automation: Automation,
  options: { prependIfMissing?: boolean } = {},
): void {
  const cachedLists = queryClient.getQueriesData<ListResponse<Automation>>({
    queryKey: queryKeys.automations.all,
    exact: true,
  });

  for (const [key, current] of cachedLists) {
    if (!isAutomationListResponse(current)) {
      continue;
    }

    let changed = false;
    const nextData = current.data.map((cachedAutomation) => {
      if (cachedAutomation.id !== automation.id) {
        return cachedAutomation;
      }
      changed = true;
      return automation;
    });

    if (!changed && options.prependIfMissing) {
      queryClient.setQueryData(key, {
        ...current,
        data: [automation, ...current.data],
      });
      continue;
    }

    if (changed) {
      queryClient.setQueryData(key, { ...current, data: nextData });
    }
  }
}

export function removeAutomationFromListCaches(
  queryClient: QueryClient,
  automationID: string,
): void {
  const cachedLists = queryClient.getQueriesData<ListResponse<Automation>>({
    queryKey: queryKeys.automations.all,
    exact: true,
  });

  for (const [key, current] of cachedLists) {
    if (!isAutomationListResponse(current)) {
      continue;
    }

    const nextData = current.data.filter(
      (automation) => automation.id !== automationID,
    );
    if (nextData.length !== current.data.length) {
      queryClient.setQueryData(key, { ...current, data: nextData });
    }
  }
}
