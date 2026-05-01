"use client";

import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { AuditLog } from "@/lib/types";

interface UseAuditLogFeedOptions {
  filters?: Record<string, string>;
  pageSize?: number;
  enabled?: boolean;
}

export function useAuditLogFeed({
  filters = {},
  pageSize = 25,
  enabled = true,
}: UseAuditLogFeedOptions) {
  const query = useInfiniteQuery({
    queryKey: ["audit-logs", "feed", filters, pageSize],
    queryFn: ({ pageParam }) =>
      api.auditLogs.list({
        ...filters,
        cursor: pageParam as string | undefined,
        limit: pageSize,
      }),
    enabled,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.meta?.next_cursor || undefined,
  });

  const entries: AuditLog[] = query.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  const hasLoadedHistory = (query.data?.pages.length ?? 0) > 1;

  return {
    ...query,
    entries,
    hasLoadedHistory,
  };
}
