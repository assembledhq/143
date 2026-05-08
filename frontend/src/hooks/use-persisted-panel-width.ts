"use client";

import { useCallback, useEffect, useState } from "react";

interface UsePersistedPanelWidthOptions {
  storageKey: string;
  defaultWidth: number;
  minWidth: number;
  maxWidth: number;
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
}: UsePersistedPanelWidthOptions) {
  const [width, setWidth] = useState(defaultWidth);

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

      try {
        window.localStorage.setItem(storageKey, String(nextWidth));
      } catch (error) {
        console.error("failed to persist panel width", { storageKey, width: nextWidth, error });
      }

      return nextWidth;
    });
  }, [maxWidth, minWidth, storageKey]);

  return { width, resizeBy };
}
