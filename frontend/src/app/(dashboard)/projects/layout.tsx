"use client";

import { useCallback, useState } from "react";
import { ProjectSidebar } from "./project-sidebar";
import { ResizeHandle } from "@/components/resize-handle";

const MIN_SIDEBAR = 240;
const MAX_SIDEBAR = 480;
const DEFAULT_SIDEBAR = 320;

export default function ProjectsLayout({ children }: { children: React.ReactNode }) {
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR);

  const handleSidebarResize = useCallback((delta: number) => {
    setSidebarWidth((w) => Math.min(MAX_SIDEBAR, Math.max(MIN_SIDEBAR, w + delta)));
  }, []);

  return (
    <div className="flex h-[calc(100vh-theme(spacing.6)*2)] -mx-8 -my-6 lg:-mx-10">
      {/* Project list sidebar */}
      <div style={{ width: sidebarWidth }} className="shrink-0">
        <ProjectSidebar />
      </div>

      <ResizeHandle onResize={handleSidebarResize} />

      {/* Main content area */}
      <div className="flex-1 min-w-0 overflow-auto">
        {children}
      </div>
    </div>
  );
}
