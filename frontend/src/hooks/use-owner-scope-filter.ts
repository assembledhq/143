"use client";

import { useMemo } from "react";
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

// Builds the `?user=…&status=…&repo=…` suffix to append to links so scoped
// session/project views survive navigation. "mine" is the implicit default and
// is never serialized.
export function buildFilterSuffix(
  currentUserFilter: string,
  status: string | null,
  repo: string | null,
  search: string | null,
): string {
  const params = new URLSearchParams();
  if (currentUserFilter && currentUserFilter !== "mine") params.set("user", currentUserFilter);
  if (status) params.set("status", status);
  if (repo) params.set("repo", repo);
  if (search) params.set("search", search);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export function useFilterSuffix(
  currentUserFilter: string,
  status: string | null,
  repo: string | null,
  search: string | null,
): string {
  return useMemo(() => (
    buildFilterSuffix(currentUserFilter, status, repo, search)
  ), [currentUserFilter, status, repo, search]);
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
