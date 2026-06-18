"use client";

import { useEffect } from "react";
import { SidebarLayout } from "@/components/sidebar-layout";
import { SessionSidebar } from "./session-sidebar";
import { OptimisticSessionsProvider } from "@/contexts/optimistic-sessions";
import { preloadSessionDetailContent } from "./[id]/session-detail-page-client";
import { SessionsShellContent } from "./sessions-shell-content";
import { useSessionsRouteState } from "./sessions-route-state";

export default function SessionsLayout({ children: _children }: { children: React.ReactNode }) {
  // Child pages are thin route markers; this persistent layout owns the visible
  // sessions content so the sidebar shell stays mounted across selection changes.
  const routeState = useSessionsRouteState();

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
      <SidebarLayout sidebar={<SessionSidebar />} mobileShow={routeState.mobileShow}>
        <SessionsShellContent routeState={routeState} />
      </SidebarLayout>
    </OptimisticSessionsProvider>
  );
}
