"use client";

import { useEffect, useState } from "react";

export function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState<boolean>(() =>
    typeof document === "undefined" ? true : document.visibilityState === "visible",
  );

  useEffect(() => {
    if (typeof document === "undefined") return;
    const onChange = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", onChange);
    return () => document.removeEventListener("visibilitychange", onChange);
  }, []);

  return visible;
}
