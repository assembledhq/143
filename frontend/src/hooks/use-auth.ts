"use client";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { getActiveOrgId } from "@/lib/active-org";
import { clearCachedViewerScope, writeCachedViewerScope } from "@/lib/viewer-scope-cache";

// Duck-typed 401 check. The backend's `writeError` helper is the single
// source of truth for this contract: every 401 response carries
// error.code = "UNAUTHORIZED" (see internal/api/middleware/auth.go and
// callers of writeError with http.StatusUnauthorized). Using a duck
// check (not `instanceof ApiError`) means component test mocks don't
// need to re-export ApiError to interop.
function isUnauthorizedError(err: unknown): boolean {
  return (
    typeof err === "object" &&
    err !== null &&
    (err as { code?: unknown }).code === "UNAUTHORIZED"
  );
}

export function useAuth() {
  const queryClient = useQueryClient();

  const { data, isLoading, isFetching, error, refetch } = useQuery({
    queryKey: ["auth", "me"],
    queryFn: async () => {
      const response = await api.auth.me();
      // Persist the viewer identity so warm-start paths (prefetching
      // user/org-scoped state before the next cold load's /auth/me resolves)
      // can reconstruct localStorage keys. Same composition as the page-level
      // viewerScope: per-tab active org first, then the user's home org.
      const me = response.data;
      if (me && typeof window !== "undefined") {
        writeCachedViewerScope(window.localStorage, {
          userId: me.id,
          orgId: getActiveOrgId() ?? me.org_id ?? null,
        });
      }
      return response;
    },
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
  // Retries exhausted with a non-401 failure. Callers should render an
  // explicit error state (with a retry affordance) rather than hang on a
  // loading skeleton forever.
  const isTransientError = !!error && !isUnauthorized;

  const logout = async () => {
    await api.auth.logout();
    queryClient.clear();
    // Drop the warm-start identity hint so a different user on this browser
    // can't trigger prefetches keyed off the previous user's stored state.
    clearCachedViewerScope(window.localStorage);
    window.location.href = "/";
  };

  return {
    user: data?.data ?? null,
    isLoading,
    isFetching,
    isAuthenticated: !!data?.data,
    isUnauthorized,
    isTransientError,
    refetchUser: refetch,
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
