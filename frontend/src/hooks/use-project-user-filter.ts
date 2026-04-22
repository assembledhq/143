"use client";

import { useOwnerScopeFilter } from "@/hooks/use-owner-scope-filter";

export function useProjectUserFilter() {
  const { currentUserFilter, currentUser, scopedUserId, isResolved, setUserFilter } = useOwnerScopeFilter();

  return {
    currentUserFilter,
    createdByUserId: scopedUserId,
    isResolved,
    user: currentUser,
    setUserFilter,
  };
}
