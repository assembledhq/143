"use client";

import { useTheme } from "next-themes";

export function usePrefersDark() {
  const { resolvedTheme } = useTheme();
  return resolvedTheme === "dark";
}
