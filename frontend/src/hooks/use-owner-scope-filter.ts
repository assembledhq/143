"use client";

import { parseAsString, useQueryState } from "nuqs";
import { useAuth } from "@/hooks/use-auth";
import type { User } from "@/lib/types";

export type OwnerScopePreset = "mine" | "all";
export type OwnerScopeParam = string | null;

export function resolveOwnerScope(param: string | null): OwnerScopePreset | string {
  return param ?? "mine";
}

export function deriveScopedUserId(
  filter: OwnerScopePreset | string,
  currentUser: User | null,
): string | undefined {
  if (filter === "all") return undefined;
  if (filter === "mine") return currentUser?.id;
  return filter;
}

export function ownerScopeLabel(
  filter: OwnerScopePreset | string,
  members: User[],
): string {
  if (filter === "mine") return "Mine";
  if (filter === "all") return "Everyone";
  const member = members.find((m) => m.id === filter);
  return member ? member.name.split(" ")[0] : "User";
}

export function ownerScopeParamForMember(
  memberId: string,
  currentUserId: string | undefined,
): OwnerScopeParam {
  return memberId === currentUserId ? null : memberId;
}

export function useOwnerScopeFilter() {
  const { user, isLoading } = useAuth();
  const [userFilter, setUserFilter] = useQueryState("user", parseAsString);

  const currentUserFilter = resolveOwnerScope(userFilter);
  const userId = deriveScopedUserId(currentUserFilter, user);
  const isResolved = currentUserFilter !== "mine" || !!user || !isLoading;

  return {
    currentUserFilter,
    currentUser: user,
    scopedUserId: userId,
    isResolved,
    setUserFilter,
  };
}
