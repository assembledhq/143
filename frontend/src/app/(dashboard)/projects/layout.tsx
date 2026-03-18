"use client";

import { SidebarLayout } from "@/components/sidebar-layout";
import { ProjectSidebar } from "./project-sidebar";

export default function ProjectsLayout({ children }: { children: React.ReactNode }) {
  return (
    <SidebarLayout sidebar={<ProjectSidebar />}>
      {children}
    </SidebarLayout>
  );
}
