"use client";

import {
  deriveScopedUserId,
  ownerScopeLabel,
  ownerScopeParamForMember,
  resolveOwnerScope,
  useOwnerScopeFilter,
  type OwnerScopeParam,
  type OwnerScopePreset,
} from "@/hooks/use-owner-scope-filter";

export type UserFilterPreset = OwnerScopePreset;
export type UserFilterParam = OwnerScopeParam;
export const resolveUserFilter = resolveOwnerScope;
export const deriveTriggeredByUserId = deriveScopedUserId;
export const userFilterLabel = ownerScopeLabel;
export const userFilterParamForMember = ownerScopeParamForMember;

export function useSessionUserFilter() {
  const { currentUserFilter, currentUser, scopedUserId, isResolved, setUserFilter } = useOwnerScopeFilter();

  return {
    currentUserFilter,
    triggeredByUserId: scopedUserId,
    isResolved,
    user: currentUser,
    setUserFilter,
  };
}
