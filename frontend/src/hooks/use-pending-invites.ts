"use client";

import { useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { PendingInvitationForUser } from "@/lib/types";

// Polling cadence for the pending-invites surface. Five minutes is the
// sweet spot for a list that's empty for ~all users ~all the time:
// sub-minute polling burns cache + network for nothing, while waiting
// longer lets a user complete entire sessions before noticing they had
// access waiting. The dropdown also refetches on open and on window
// focus, so the worst-case discovery latency in practice is "next time
// the user clicks the org switcher" rather than the full poll interval.
const PENDING_INVITES_POLL_MS = 5 * 60 * 1000;

interface UsePendingInvitesResult {
  invites: PendingInvitationForUser[];
  count: number;
  isLoading: boolean;
  refetch: () => void;
  accept: (id: string) => Promise<{ org_id: string; role: string }>;
  decline: (id: string) => Promise<void>;
  // The id currently being accepted/declined (or null). Exposed so the UI can
  // disable the row whose mutation is in flight without freezing buttons on
  // every other row — several invites arriving together should still be
  // independently actionable.
  acceptingId: string | null;
  decliningId: string | null;
}

// usePendingInvites surfaces the invitations addressed to the authenticated
// user — the data behind the org-switcher dot and the "Pending invitations"
// section inside its dropdown.
//
// Pass `enabled: false` when the caller can render before the user is
// authenticated, to avoid generating 401s. The org-switcher does this by
// gating on whether userEmail has been resolved.
//
// Mutations invalidate both the pending-invites list (so the row disappears
// from the dropdown) and the auth.memberships query (so the new org appears
// in the switcher's main list). The hook deliberately does NOT switch the
// active org on accept — that decision belongs to the UI layer, where the
// user can be offered an explicit "Switch to it" affordance instead of being
// teleported out of whatever org they were working in.
export function usePendingInvites(opts: { enabled?: boolean } = {}): UsePendingInvitesResult {
  const enabled = opts.enabled ?? true;
  const queryClient = useQueryClient();

  const { data, isLoading, refetch } = useQuery({
    queryKey: queryKeys.invitations.pending,
    queryFn: () => api.invitations.listPending(),
    enabled,
    refetchInterval: enabled ? PENDING_INVITES_POLL_MS : false,
    refetchOnWindowFocus: true,
    // Don't retry on 401 — an unauthenticated user has nothing to recover.
    retry: (failureCount, err) => {
      if (
        typeof err === "object" &&
        err !== null &&
        (err as { code?: unknown }).code === "UNAUTHORIZED"
      ) {
        return false;
      }
      return failureCount < 2;
    },
  });

  const acceptMutation = useMutation({
    mutationFn: (id: string) => api.invitations.accept(id),
    onSuccess: () => {
      // Refresh both: the pending list (so the row disappears) and the
      // user's memberships (so the new org appears in the switcher).
      void queryClient.invalidateQueries({ queryKey: queryKeys.invitations.pending });
      void queryClient.invalidateQueries({ queryKey: queryKeys.auth.memberships });
    },
  });

  const declineMutation = useMutation({
    mutationFn: (id: string) => api.invitations.decline(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.invitations.pending });
    },
  });

  const accept = useCallback(
    async (id: string): Promise<{ org_id: string; role: string }> => {
      const result = await acceptMutation.mutateAsync(id);
      return result.data;
    },
    [acceptMutation],
  );

  const decline = useCallback(
    async (id: string): Promise<void> => {
      await declineMutation.mutateAsync(id);
    },
    [declineMutation],
  );

  const refetchCallback = useCallback(() => {
    void refetch();
  }, [refetch]);

  const invites = data?.data ?? [];
  return {
    invites,
    count: invites.length,
    isLoading,
    refetch: refetchCallback,
    accept,
    decline,
    // React Query exposes the in-flight argument via mutation.variables while
    // isPending is true, which gives us a free per-row pending indicator
    // without maintaining parallel state.
    acceptingId: acceptMutation.isPending ? acceptMutation.variables ?? null : null,
    decliningId: declineMutation.isPending ? declineMutation.variables ?? null : null,
  };
}
