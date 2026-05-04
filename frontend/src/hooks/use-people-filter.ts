"use client";

import { useMemo } from "react";
import { parseAsString, useQueryState } from "nuqs";
import { useAuth } from "@/hooks/use-auth";
import type { User } from "@/lib/types";

export type PeopleFilterMode = "mine" | "all" | "custom";
export type PeopleFilterParam = string | null;

function splitPeopleParam(value: string | null): string[] {
  if (!value || value === "all") return [];
  const seen = new Set<string>();
  const ids: string[] = [];
  for (const part of value.split(",")) {
    const id = part.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    ids.push(id);
  }
  return ids;
}

export function normalizePeopleFilter(
  peopleParam: string | null,
  currentUser: User | null,
  legacyUserParam?: string | null,
): { mode: PeopleFilterMode; selectedUserIDs: string[]; serialized: string | null } {
  const raw = peopleParam ?? legacyUserParam ?? null;
  if (!raw) {
    return { mode: "mine", selectedUserIDs: currentUser ? [currentUser.id] : [], serialized: null };
  }
  if (raw === "all") {
    return { mode: "all", selectedUserIDs: [], serialized: "all" };
  }

  const selectedUserIDs = splitPeopleParam(raw);
  if (selectedUserIDs.length === 0) {
    return { mode: "mine", selectedUserIDs: currentUser ? [currentUser.id] : [], serialized: null };
  }
  if (currentUser && selectedUserIDs.length === 1 && selectedUserIDs[0] === currentUser.id) {
    return { mode: "mine", selectedUserIDs, serialized: null };
  }
  return { mode: "custom", selectedUserIDs, serialized: selectedUserIDs.join(",") };
}

export function buildFilterSuffix(
  people: string | null,
  status: string | null,
  repo: string | null,
  search: string | null,
): string {
  const params = new URLSearchParams();
  if (people) params.set("people", people);
  if (status) params.set("status", status);
  if (repo) params.set("repo", repo);
  if (search) params.set("search", search);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export function peopleFilterLabel(
  mode: PeopleFilterMode,
  selectedUserIDs: string[],
  members: User[],
  currentUser: User | null,
): string {
  if (mode === "mine") return "Mine";
  if (mode === "all") return "Everyone";

  const names = selectedUserIDs.map((id) => {
    if (id === currentUser?.id) return "You";
    const member = members.find((item) => item.id === id);
    return member?.name.split(" ")[0] ?? "User";
  });

  if (names.length === 0) return "People";
  if (names.length === 1) return names[0];
  return `${names[0]} +${names.length - 1}`;
}

export function useFilterSuffix(
  people: string | null,
  status: string | null,
  repo: string | null,
  search: string | null,
): string {
  return useMemo(() => buildFilterSuffix(people, status, repo, search), [people, repo, search, status]);
}

export function usePeopleFilter() {
  const { user, isLoading } = useAuth();
  const [peopleParam, setPeopleParamState] = useQueryState("people", parseAsString);
  const [legacyUserParam, setLegacyUserParam] = useQueryState("user", parseAsString);

  const normalized = normalizePeopleFilter(peopleParam, user, legacyUserParam);
  const scopedUserIDs = normalized.mode === "all"
    ? undefined
    : normalized.mode === "mine"
      ? user ? [user.id] : undefined
      : normalized.selectedUserIDs;
  const isResolved = normalized.mode !== "mine" || !!user || !isLoading;

  async function setPeopleFilter(value: PeopleFilterParam) {
    await Promise.all([
      setPeopleParamState(value),
      setLegacyUserParam(null),
    ]);
  }

  return {
    mode: normalized.mode,
    selectedUserIDs: normalized.selectedUserIDs,
    serializedPeopleParam: normalized.serialized,
    currentPeopleFilter: normalized.mode === "mine" ? "mine" : normalized.serialized ?? "mine",
    scopedUserIDs,
    currentUser: user,
    isResolved,
    setPeopleFilter,
  };
}
