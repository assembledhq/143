"use client";

import { useEffect } from "react";
import { SidebarLayout } from "@/components/sidebar-layout";
import { SessionSidebar } from "./session-sidebar";
import { OptimisticSessionsProvider } from "@/contexts/optimistic-sessions";
import { usePathname } from "next/navigation";
import { preloadSessionDetailContent } from "./[id]/session-detail-page-client";

export default function SessionsLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const mobileShow = pathname === "/sessions" ? "sidebar" : "content";

  // The detail view's heavy chunk sits behind a render-time dynamic import
  // that router.prefetch never touches, so the first session open would pay
  // its download/compile cost. Warm it during idle time from this layout,
  // which wraps every sessions surface — including /sessions/new and touch
  // devices that never fire the hover prefetches.
  useEffect(() => {
    if (typeof window.requestIdleCallback === "function") {
      const handle = window.requestIdleCallback(() => preloadSessionDetailContent());
      return () => window.cancelIdleCallback(handle);
    }
    const timeout = window.setTimeout(() => preloadSessionDetailContent(), 1000);
    return () => window.clearTimeout(timeout);
  }, []);

  return (
    <OptimisticSessionsProvider>
      <SidebarLayout sidebar={<SessionSidebar />} mobileShow={mobileShow}>
        {children}
      </SidebarLayout>
    </OptimisticSessionsProvider>
  );
}
