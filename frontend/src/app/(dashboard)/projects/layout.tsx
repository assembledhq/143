"use client";

import { usePathname } from "next/navigation";
import { SidebarLayout } from "@/components/sidebar-layout";
import { ProjectSidebar } from "./project-sidebar";

export default function ProjectsLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const mobileShow = pathname === "/projects" ? "sidebar" : "content";

  return (
    <SidebarLayout sidebar={<ProjectSidebar />} mobileShow={mobileShow}>
      {children}
    </SidebarLayout>
  );
}
