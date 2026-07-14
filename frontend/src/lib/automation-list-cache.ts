import type { QueryClient } from "@tanstack/react-query";
import { queryKeys } from "./query-keys";
import type { Automation, ListResponse, SingleResponse } from "./types";

export interface AutomationEnabledSnapshot {
  list?: ListResponse<Automation>;
  detail?: SingleResponse<Automation>;
}

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

/**
 * Applies the honest local intent for pause/resume without manufacturing a
 * server revision. The returned snapshot is scoped to one automation and can
 * be restored verbatim if the mutation fails.
 */
export function optimisticallySetAutomationEnabled(
  queryClient: QueryClient,
  automationID: string,
  enabled: boolean,
): AutomationEnabledSnapshot {
  const listKey = queryKeys.automations.all;
  const detailKey = queryKeys.automations.detail(automationID);
  const snapshot: AutomationEnabledSnapshot = {
    list: queryClient.getQueryData<ListResponse<Automation>>(listKey),
    detail: queryClient.getQueryData<SingleResponse<Automation>>(detailKey),
  };
  if (snapshot.list) {
    queryClient.setQueryData<ListResponse<Automation>>(listKey, {
      ...snapshot.list,
      data: snapshot.list.data.map((automation) =>
        automation.id === automationID ? { ...automation, enabled } : automation,
      ),
    });
  }
  if (snapshot.detail?.data.id === automationID) {
    queryClient.setQueryData<SingleResponse<Automation>>(detailKey, {
      ...snapshot.detail,
      data: { ...snapshot.detail.data, enabled },
    });
  }
  return snapshot;
}

export function restoreAutomationEnabledSnapshot(
  queryClient: QueryClient,
  automationID: string,
  snapshot: AutomationEnabledSnapshot | undefined,
): void {
  if (!snapshot) return;
  if (snapshot.list) queryClient.setQueryData(queryKeys.automations.all, snapshot.list);
  if (snapshot.detail) queryClient.setQueryData(queryKeys.automations.detail(automationID), snapshot.detail);
}
