"use client";

import { SidebarLayout } from "@/components/sidebar-layout";
import { SessionSidebar } from "./session-sidebar";

export default function SessionsLayout({ children }: { children: React.ReactNode }) {
  return (
    <SidebarLayout sidebar={<SessionSidebar />}>
      {children}
    </SidebarLayout>
  );
}
