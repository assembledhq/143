"use client";

import { ResizeHandle } from "@/components/resize-handle";
import { usePersistedPanelWidth } from "@/hooks/use-persisted-panel-width";
import { cn } from "@/lib/utils";

const MIN_SIDEBAR = 240;
const MAX_SIDEBAR = 400;
const DEFAULT_SIDEBAR = 320;
const STORAGE_KEY = "143:sidebar-layout-width";

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
  const { width: sidebarWidth, resizeBy: handleSidebarResize } = usePersistedPanelWidth({
    storageKey: STORAGE_KEY,
    defaultWidth: DEFAULT_SIDEBAR,
    minWidth: MIN_SIDEBAR,
    maxWidth: MAX_SIDEBAR,
  });

  const sidebarHiddenOnMobile = mobileShow === "content";
  const contentHiddenOnMobile = mobileShow === "sidebar";

  return (
    <div className="absolute inset-0 flex overflow-hidden">
      <div
        data-testid="sidebar-pane"
        style={{ "--sidebar-w": `${sidebarWidth}px` } as React.CSSProperties}
        className={cn(
          "shrink-0 h-full w-full md:w-[var(--sidebar-w)]",
          sidebarHiddenOnMobile && "hidden md:block",
        )}
      >
        {sidebar}
      </div>

      <div className="hidden md:block">
        <ResizeHandle onResize={handleSidebarResize} testId="resize-handle" />
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
