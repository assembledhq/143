"use client";

import { useEffect } from "react";

import type { SessionStatus } from "@/lib/types";

export type FaviconOperationalState = "working" | "waiting" | "failed" | null;

export function sessionFaviconState(status?: SessionStatus | null): FaviconOperationalState {
  if (status === "pending" || status === "running") return "working";
  if (status === "awaiting_input" || status === "needs_human_guidance") return "waiting";
  if (status === "failed") return "failed";
  return null;
}

const stateColor: Record<Exclude<FaviconOperationalState, null>, string> = {
  working: "#3b82f6",
  waiting: "#f59e0b",
  failed: "#ef4444",
};

function faviconDataUrl(state: Exclude<FaviconOperationalState, null>): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect x="2" y="2" width="28" height="28" rx="8" fill="#101827"/><path d="M8 17.5 24 8l-6.2 16-2.7-6.1L8 17.5Z" fill="#f8fafc"/><circle cx="25" cy="25" r="5" fill="#101827"/><circle cx="25" cy="25" r="3.5" fill="${stateColor[state]}"/></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/** Adds a small lifecycle badge to the favicon while a session needs awareness. */
export function useOperationalFavicon(status?: SessionStatus | null): void {
  const state = sessionFaviconState(status);

  useEffect(() => {
    if (!state) return;

    const link = document.createElement("link");
    link.rel = "icon";
    link.type = "image/svg+xml";
    link.href = faviconDataUrl(state);
    link.dataset.operationalFavicon = state;
    document.head.append(link);

    return () => link.remove();
  }, [state]);
}
