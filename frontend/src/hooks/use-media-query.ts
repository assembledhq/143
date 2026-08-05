"use client";

import { useCallback, useSyncExternalStore } from "react";

function getMatch(query: string): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }
  return window.matchMedia(query).matches;
}

function getServerMatch(): boolean {
  return false;
}

export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback((onChange: () => void) => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return () => undefined;
    }

    const mediaQuery = window.matchMedia(query);
    const supportsEventListener =
      typeof mediaQuery.addEventListener === "function" &&
      typeof mediaQuery.removeEventListener === "function";

    if (supportsEventListener) {
      mediaQuery.addEventListener("change", onChange);
    } else {
      mediaQuery.addListener(onChange);
    }
    return () => {
      if (supportsEventListener) {
        mediaQuery.removeEventListener("change", onChange);
      } else {
        mediaQuery.removeListener(onChange);
      }
    };
  }, [query]);
  const getSnapshot = useCallback(() => getMatch(query), [query]);

  return useSyncExternalStore(subscribe, getSnapshot, getServerMatch);
}
