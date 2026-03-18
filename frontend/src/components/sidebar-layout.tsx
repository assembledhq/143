"use client";

import { useCallback, useState } from "react";
import { ResizeHandle } from "@/components/resize-handle";

const MIN_SIDEBAR = 240;
const MAX_SIDEBAR = 480;
const DEFAULT_SIDEBAR = 320;

interface SidebarLayoutProps {
  sidebar: React.ReactNode;
  children: React.ReactNode;
}

export function SidebarLayout({ sidebar, children }: SidebarLayoutProps) {
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR);

  const handleSidebarResize = useCallback((delta: number) => {
    setSidebarWidth((w) => Math.min(MAX_SIDEBAR, Math.max(MIN_SIDEBAR, w + delta)));
  }, []);

  return (
    <div className="flex h-[calc(100vh-theme(spacing.6)*2)] -mx-8 -my-6 lg:-mx-10">
      <div style={{ width: sidebarWidth }} className="shrink-0">
        {sidebar}
      </div>

      <ResizeHandle onResize={handleSidebarResize} />

      <div className="flex-1 min-w-0 overflow-auto">
        {children}
      </div>
    </div>
  );
}
