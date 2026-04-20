"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";

// Duck-typed 401 check. The backend's Auth middleware writes error.code =
// "UNAUTHORIZED" for every 401. Using a duck check (not `instanceof ApiError`)
// means component test mocks don't need to re-export ApiError to interop.
function isUnauthorizedError(err: unknown): boolean {
  return (
    typeof err === "object" &&
    err !== null &&
    (err as { code?: unknown }).code === "UNAUTHORIZED"
  );
}

export function useAuth() {
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => api.auth.me(),
    // Only treat a confirmed 401 as terminal. Network blips and 5xx
    // (e.g. during a rolling deploy) retry a few times instead of
    // instantly logging the user out.
    retry: (failureCount, err) => {
      if (isUnauthorizedError(err)) return false;
      return failureCount < 2;
    },
    // Short backoff so a transient deploy blip clears in under a second
    // rather than stalling on React Query's default exponential backoff.
    retryDelay: (attempt) => Math.min(200 * 2 ** attempt, 1000),
    staleTime: 5 * 60 * 1000,
  });

  const isUnauthorized = isUnauthorizedError(error);

  const logout = async () => {
    await api.auth.logout();
    queryClient.clear();
    window.location.href = "/login";
  };

  return {
    user: data?.data ?? null,
    isLoading,
    isAuthenticated: !!data?.data,
    isUnauthorized,
    logout,
  };
}

export function useAuthProviders() {
  const { data, isLoading } = useQuery({
    queryKey: ["auth", "providers"],
    queryFn: () => api.auth.providers(),
    staleTime: 60 * 60 * 1000,
  });

  return {
    providers: data?.data ?? null,
    isLoading,
  };
}
