"use client";

import { useCallback, useEffect, useState } from "react";

interface UsePersistedPanelWidthOptions {
  storageKey: string;
  defaultWidth: number;
  minWidth: number;
  maxWidth: number;
  /**
   * When set, calling `settle()` after a drag that ended below this width snaps
   * the panel to `minWidth` (the icon-rail width), so it never rests in the
   * dead zone between `minWidth` and the collapse threshold.
   */
  collapseBelow?: number;
}

function clampWidth(width: number, minWidth: number, maxWidth: number): number {
  return Math.min(maxWidth, Math.max(minWidth, width));
}

function readStoredWidth(
  storageKey: string,
  defaultWidth: number,
  minWidth: number,
  maxWidth: number,
): number {
  if (typeof window === "undefined") {
    return defaultWidth;
  }

  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(storageKey);
  } catch (error) {
    console.error("failed to read persisted panel width", { storageKey, error });
    return defaultWidth;
  }

  if (!raw) {
    return defaultWidth;
  }

  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed)) {
    return defaultWidth;
  }

  return clampWidth(parsed, minWidth, maxWidth);
}

export function usePersistedPanelWidth({
  storageKey,
  defaultWidth,
  minWidth,
  maxWidth,
  collapseBelow,
}: UsePersistedPanelWidthOptions) {
  const [width, setWidth] = useState(defaultWidth);

  const persistWidth = useCallback((nextWidth: number) => {
    try {
      window.localStorage.setItem(storageKey, String(nextWidth));
    } catch (error) {
      console.error("failed to persist panel width", { storageKey, width: nextWidth, error });
    }
  }, [storageKey]);

  useEffect(() => {
    const syncWidthFromStorage = () => {
      setWidth(readStoredWidth(storageKey, defaultWidth, minWidth, maxWidth));
    };

    syncWidthFromStorage();
    window.addEventListener("storage", syncWidthFromStorage);
    return () => window.removeEventListener("storage", syncWidthFromStorage);
  }, [defaultWidth, maxWidth, minWidth, storageKey]);

  const resizeBy = useCallback((delta: number) => {
    setWidth((current) => {
      const nextWidth = clampWidth(current + delta, minWidth, maxWidth);
      persistWidth(nextWidth);
      return nextWidth;
    });
  }, [maxWidth, minWidth, persistWidth]);

  const settle = useCallback(() => {
    if (collapseBelow === undefined) return;
    setWidth((current) => {
      if (current >= collapseBelow || current === minWidth) return current;
      persistWidth(minWidth);
      return minWidth;
    });
  }, [collapseBelow, minWidth, persistWidth]);

  return { width, resizeBy, settle };
}
