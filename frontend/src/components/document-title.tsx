"use client";

import { useEffect } from "react";
import { usePathname } from "next/navigation";
import { buildDocumentTitle, resolvePageTitle } from "@/lib/page-title";

export function DocumentTitle() {
  const pathname = usePathname();

  useEffect(() => {
    document.title = buildDocumentTitle(resolvePageTitle(pathname));
  }, [pathname]);

  return null;
}
