"use client";

import { useQueryState, parseAsString } from "nuqs";
import { useAuth } from "@/hooks/use-auth";
import type { User } from "@/lib/types";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Well-known user filter presets. Any other string value is a specific user ID. */
export type UserFilterPreset = "mine" | "all";

/** The resolved value stored in the URL query param. `null` means "mine" (default). */
export type UserFilterParam = string | null;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Resolve the URL-level filter value to the effective filter key.
 * `null` (no param) → "mine".
 */
export function resolveUserFilter(param: string | null): UserFilterPreset | string {
  return param ?? "mine";
}

/**
 * Derive the `triggered_by_user_id` API param from the resolved filter and
 * the current user.
 *
 * - `"all"`          → `undefined` (no server filter)
 * - `"mine"`         → current user's ID (or `undefined` if not yet loaded)
 * - specific user ID → that ID
 */
export function deriveTriggeredByUserId(
  filter: UserFilterPreset | string,
  currentUser: User | null,
): string | undefined {
  if (filter === "all") return undefined;
  if (filter === "mine") return currentUser?.id;
  return filter; // specific user ID
}

/**
 * Compute the display label for the user filter trigger button.
 */
export function userFilterLabel(
  filter: UserFilterPreset | string,
  members: User[],
): string {
  if (filter === "mine") return "Mine";
  if (filter === "all") return "Everyone";
  const member = members.find((m) => m.id === filter);
  return member ? member.name.split(" ")[0] : "User";
}

/**
 * Determine the URL param value to set for a given member selection.
 * Selecting yourself maps to `null` ("mine") for a clean URL.
 */
export function userFilterParamForMember(
  memberId: string,
  currentUserId: string | undefined,
): UserFilterParam {
  return memberId === currentUserId ? null : memberId;
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function useSessionUserFilter() {
  const { user } = useAuth();
  const [userFilter, setUserFilter] = useQueryState("user", parseAsString);

  const currentUserFilter = resolveUserFilter(userFilter);
  const triggeredByUserId = deriveTriggeredByUserId(currentUserFilter, user);

  return {
    /** The resolved filter value: "mine", "all", or a user ID. */
    currentUserFilter,
    /** The user ID to pass to the API, or undefined for no filter. */
    triggeredByUserId,
    /** The current authenticated user. */
    user,
    /** Set the user filter. Pass `null` for "mine", `"all"` for everyone, or a user ID. */
    setUserFilter,
  };
}
