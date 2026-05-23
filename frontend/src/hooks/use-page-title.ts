"use client";

import { useEffect } from "react";
import { buildDocumentTitle } from "@/lib/page-title";

export function usePageTitle(
  pageTitle: string | null | undefined,
  fallbackTitle?: string,
): void {
  useEffect(() => {
    const nextTitle = pageTitle || fallbackTitle;
    if (!nextTitle) {
      return;
    }

    document.title = buildDocumentTitle(nextTitle);
  }, [fallbackTitle, pageTitle]);
}
