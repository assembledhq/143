"use client";

import { useSyncExternalStore } from "react";
import { useTheme } from "next-themes";

const subscribeToMount = () => () => {};
const getClientSnapshot = () => true;
const getServerSnapshot = () => false;

export function usePrefersDark() {
  const { resolvedTheme } = useTheme();
  const mounted = useSyncExternalStore(
    subscribeToMount,
    getClientSnapshot,
    getServerSnapshot,
  );

  // Keep the server and first client render identical. next-themes resolves
  // the system preference during hydration; reading it immediately causes
  // React to retain light inline backgrounds alongside dark text classes.
  return mounted && resolvedTheme === "dark";
}
