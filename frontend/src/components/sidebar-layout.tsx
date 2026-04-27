"use client";

import { useCallback, useState } from "react";
import { ResizeHandle } from "@/components/resize-handle";
import { cn } from "@/lib/utils";

const MIN_SIDEBAR = 240;
const MAX_SIDEBAR = 480;
const DEFAULT_SIDEBAR = 320;

interface SidebarLayoutProps {
  sidebar: React.ReactNode;
  children: React.ReactNode;
  /**
   * Below the `md` breakpoint only one pane is visible. Above `md` both panes
   * are always rendered side-by-side regardless of this prop.
   */
  mobileShow?: "sidebar" | "content";
}

export function SidebarLayout({ sidebar, children, mobileShow = "sidebar" }: SidebarLayoutProps) {
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR);

  const handleSidebarResize = useCallback((delta: number) => {
    setSidebarWidth((w) => Math.min(MAX_SIDEBAR, Math.max(MIN_SIDEBAR, w + delta)));
  }, []);

  const sidebarHiddenOnMobile = mobileShow === "content";
  const contentHiddenOnMobile = mobileShow === "sidebar";

  return (
    <div className="absolute inset-0 flex overflow-hidden">
      <div
        style={{ "--sidebar-w": `${sidebarWidth}px` } as React.CSSProperties}
        className={cn(
          "shrink-0 h-full w-full md:w-[var(--sidebar-w)]",
          sidebarHiddenOnMobile && "hidden md:block",
        )}
      >
        {sidebar}
      </div>

      <div className="hidden md:block">
        <ResizeHandle onResize={handleSidebarResize} />
      </div>

      <div
        className={cn(
          "flex-1 min-w-0 overflow-auto",
          contentHiddenOnMobile && "hidden md:block",
        )}
      >
        {children}
      </div>
    </div>
  );
}
