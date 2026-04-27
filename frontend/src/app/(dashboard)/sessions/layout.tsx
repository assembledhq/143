"use client";

import { usePathname } from "next/navigation";
import { SidebarLayout } from "@/components/sidebar-layout";
import { SessionSidebar } from "./session-sidebar";
import { OptimisticSessionsProvider } from "@/contexts/optimistic-sessions";

export default function SessionsLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const mobileShow = pathname === "/sessions" ? "sidebar" : "content";

  return (
    <OptimisticSessionsProvider>
      <SidebarLayout sidebar={<SessionSidebar />} mobileShow={mobileShow}>
        {children}
      </SidebarLayout>
    </OptimisticSessionsProvider>
  );
}
