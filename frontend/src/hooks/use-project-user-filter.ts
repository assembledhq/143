"use client";

import { useOwnerScopeFilter } from "@/hooks/use-owner-scope-filter";

export function useProjectUserFilter() {
  const { currentUserFilter, currentUser, scopedUserId, setUserFilter } = useOwnerScopeFilter();

  return {
    currentUserFilter,
    createdByUserId: scopedUserId,
    user: currentUser,
    setUserFilter,
  };
}
