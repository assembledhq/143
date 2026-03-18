"use client";

import { SidebarLayout } from "@/components/sidebar-layout";
import { SessionSidebar } from "./session-sidebar";
import { OptimisticSessionsProvider } from "@/contexts/optimistic-sessions";

export default function SessionsLayout({ children }: { children: React.ReactNode }) {
  return (
    <OptimisticSessionsProvider>
      <SidebarLayout sidebar={<SessionSidebar />}>
        {children}
      </SidebarLayout>
    </OptimisticSessionsProvider>
  );
}
