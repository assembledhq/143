"use client";

import { useSelectedLayoutSegments } from "next/navigation";

export type SessionsRouteState = {
  mode: "index" | "create" | "detail" | "unsupported";
  selectedSessionId: string | null;
  isCreatingSession: boolean;
  isUnsupportedRoute: boolean;
  mobileShow: "sidebar" | "content";
  routeKey: string;
};

export function deriveSessionsRouteState(selectedSegments: readonly string[]): SessionsRouteState {
  const [selectedSegment, ...nestedSegments] = selectedSegments;

  if (!selectedSegment) {
    return {
      mode: "index",
      selectedSessionId: null,
      isCreatingSession: false,
      isUnsupportedRoute: false,
      mobileShow: "sidebar",
      routeKey: "index",
    };
  }

  if (nestedSegments.length > 0) {
    // Nested session routes (e.g. /sessions/:id/diff) are intentionally not modeled:
    // the layout renders an "unsupported" fallback rather than guessing which session
    // id to highlight in the sidebar.
    return {
      mode: "unsupported",
      selectedSessionId: null,
      isCreatingSession: false,
      isUnsupportedRoute: true,
      mobileShow: "content",
      routeKey: `unsupported:${selectedSegments.join("/")}`,
    };
  }

  if (selectedSegment === "new") {
    return {
      mode: "create",
      selectedSessionId: null,
      isCreatingSession: true,
      isUnsupportedRoute: false,
      mobileShow: "content",
      routeKey: "new",
    };
  }

  return {
    mode: "detail",
    selectedSessionId: selectedSegment,
    isCreatingSession: false,
    isUnsupportedRoute: false,
    mobileShow: "content",
    routeKey: `session:${selectedSegment}`,
  };
}

export function useSessionsRouteState(): SessionsRouteState {
  return deriveSessionsRouteState(useSelectedLayoutSegments());
}
