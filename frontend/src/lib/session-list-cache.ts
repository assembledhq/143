import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { queryKeys } from "./query-keys";
import type { ListResponse, Session, SessionDetail, SessionListItem } from "./types";

function isSessionListResponse(value: unknown): value is ListResponse<SessionListItem> {
  return (
    typeof value === "object" &&
    value !== null &&
    "data" in value &&
    Array.isArray((value as { data?: unknown }).data)
  );
}

function isArchivedListKey(key: QueryKey): boolean {
  return key[0] === "sessions" && key[2] === "filtered" && key[3] === "archived";
}

function mergeSessionListItem(existing: SessionListItem, updated: SessionDetail): SessionListItem {
  const {
    changesets,
    threads,
    ...updatedListFields
  } = updated;
  void [changesets, threads];

  return {
    ...existing,
    ...updatedListFields,
    ...(Object.hasOwn(existing, "last_viewed_at") ? { last_viewed_at: existing.last_viewed_at } : {}),
    ...(Object.hasOwn(existing, "pr_summary") ? { pr_summary: existing.pr_summary } : {}),
    ...(Object.hasOwn(existing, "threads") ? { threads: existing.threads } : {}),
  };
}

export function applySessionDetailToSessionListCaches(queryClient: QueryClient, updated: SessionDetail): void {
  const cachedLists = queryClient.getQueriesData<ListResponse<SessionListItem>>({
    queryKey: queryKeys.sessions.all,
  });

  for (const [key, current] of cachedLists) {
    if (!isSessionListResponse(current)) {
      continue;
    }

    if (updated.archived_at && !isArchivedListKey(key)) {
      const nextData = current.data.filter((session) => session.id !== updated.id);
      if (nextData.length !== current.data.length) {
        queryClient.setQueryData(key, { ...current, data: nextData });
      }
      continue;
    }

    let changed = false;
    const nextData = current.data.map((session) => {
      if (session.id !== updated.id) {
        return session;
      }
      changed = true;
      return mergeSessionListItem(session, updated);
    });
    if (changed) {
      queryClient.setQueryData(key, { ...current, data: nextData });
    }
  }
}

export function applyCreatedSessionToSessionListCaches(queryClient: QueryClient, created: Session): void {
  const cachedLists = queryClient.getQueriesData<ListResponse<SessionListItem>>({
    queryKey: queryKeys.sessions.all,
  });

  for (const [key, current] of cachedLists) {
    if (!isSessionListResponse(current) || isArchivedListKey(key)) {
      continue;
    }

    if (current.data.some((session) => session.id === created.id)) {
      continue;
    }

    queryClient.setQueryData(key, {
      ...current,
      data: [created, ...current.data],
    });
  }
}
