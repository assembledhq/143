import { useDeferredValue } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

const MIN_SEARCH_LENGTH = 2;
const SEARCH_LIMIT = 5;

export function useCommandPaletteSearch(query: string, repo: string | null) {
  const deferredQuery = useDeferredValue(query);
  const enabled = deferredQuery.length >= MIN_SEARCH_LENGTH;

  const sessions = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), "search", deferredQuery],
    queryFn: () =>
      api.sessions.list({
        search: deferredQuery,
        limit: SEARCH_LIMIT,
        ...(repo ? { repository_id: repo } : {}),
      }),
    enabled,
    staleTime: 10_000,
  });

  return {
    sessions: sessions.data?.data ?? [],
    isLoading: enabled && sessions.isLoading,
  };
}
