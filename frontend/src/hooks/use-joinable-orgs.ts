"use client";

import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { JoinableOrganization, MembershipSummary } from "@/lib/types";

// Same cadence rationale as usePendingInvites: the list is empty for ~all
// users ~all the time, and the org-switcher dropdown refetches on open, so
// worst-case discovery latency is "next time the user looks", not the poll.
const JOINABLE_ORGS_POLL_MS = 5 * 60 * 1000;

interface UseJoinableOrgsResult {
  orgs: JoinableOrganization[];
  count: number;
  isLoading: boolean;
  refetch: () => void;
  join: (orgId: string) => Promise<MembershipSummary>;
  // The org_id currently being joined (or null), so the UI can disable only
  // the in-flight row.
  joiningOrgId: string | null;
  // True when the user's email domain is captured by some org but their
  // address isn't verified yet — verifying would unlock a join. The org's
  // identity is deliberately not included.
  emailVerificationRequired: boolean;
}

// useJoinableOrgs surfaces workspaces the authenticated user can join via a
// verified email domain — the domain-capture sibling of usePendingInvites.
// The server only returns orgs whose DNS-verified auto-join domain matches
// the user's provider-verified email domain, minus orgs they already belong
// to, so an entry here is always actionable.
//
// Pass `enabled: false` when the caller can render before the user is
// authenticated, to avoid generating 401s.
export function useJoinableOrgs(opts: { enabled?: boolean } = {}): UseJoinableOrgsResult {
  const enabled = opts.enabled ?? true;
  const queryClient = useQueryClient();

  const { data, isLoading, refetch } = useQuery({
    queryKey: queryKeys.organizations.joinable,
    queryFn: () => api.organizations.listJoinable(),
    enabled,
    refetchInterval: enabled ? JOINABLE_ORGS_POLL_MS : false,
    refetchOnWindowFocus: true,
    retry: (failureCount, err) => {
      if (err instanceof ApiError && err.code === "UNAUTHORIZED") {
        return false;
      }
      return failureCount < 2;
    },
  });

  const joinMutation = useMutation({
    mutationFn: (orgId: string) => api.organizations.join(orgId),
    onSuccess: () => {
      // Refresh both: the joinable list (so the row disappears) and the
      // user's memberships (so the new org appears in the switcher).
      void queryClient.invalidateQueries({ queryKey: queryKeys.organizations.joinable });
      void queryClient.invalidateQueries({ queryKey: queryKeys.auth.memberships });
    },
  });

  const join = useCallback(
    async (orgId: string): Promise<MembershipSummary> => {
      const result = await joinMutation.mutateAsync(orgId);
      return result.data;
    },
    [joinMutation],
  );

  const refetchCallback = useCallback(() => {
    void refetch();
  }, [refetch]);

  const orgs = useMemo(() => data?.data ?? [], [data]);
  return {
    orgs,
    count: orgs.length,
    isLoading,
    refetch: refetchCallback,
    join,
    joiningOrgId: joinMutation.isPending ? joinMutation.variables ?? null : null,
    emailVerificationRequired: data?.email_verification_required ?? false,
  };
}
