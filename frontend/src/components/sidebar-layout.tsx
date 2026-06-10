"use client";

import { ResizeHandle } from "@/components/resize-handle";
import { Button } from "@/components/ui/button";
import { useMediaQuery } from "@/hooks/use-media-query";
import { usePersistedPanelWidth } from "@/hooks/use-persisted-panel-width";
import { cn } from "@/lib/utils";
import { PanelLeftOpen } from "lucide-react";
import { usePathname } from "next/navigation";
import { useState, type MouseEvent } from "react";

const MIN_SIDEBAR = 240;
const MAX_SIDEBAR = 400;
const DEFAULT_SIDEBAR = 320;
const STORAGE_KEY = "143:sidebar-layout-width";

interface SidebarLayoutProps {
  sidebar: React.ReactNode;
  children: React.ReactNode;
  /**
   * Below the `md` breakpoint only one pane is visible. Above `xl` both panes
   * are rendered side-by-side regardless of this prop. Between `md` and `xl`,
   * compact desktop keeps the detail pane mounted while the session list can
   * expand into a full-height pane from the switcher rail.
   */
  mobileShow?: "sidebar" | "content";
}

export function SidebarLayout({ sidebar, children, mobileShow = "sidebar" }: SidebarLayoutProps) {
  const pathname = usePathname();
  const isCompactDesktop = useMediaQuery("(min-width: 768px) and (max-width: 1279px)");
  const [sessionSwitcherState, setSessionSwitcherState] = useState({
    open: false,
    pathname: "",
  });
  const { width: sidebarWidth, resizeBy: handleSidebarResize } = usePersistedPanelWidth({
    storageKey: STORAGE_KEY,
    defaultWidth: DEFAULT_SIDEBAR,
    minWidth: MIN_SIDEBAR,
    maxWidth: MAX_SIDEBAR,
  });

  const sidebarHiddenOnMobile = mobileShow === "content";
  const contentHiddenOnMobile = mobileShow === "sidebar";
  const sessionSwitcherOpen = sessionSwitcherState.pathname === pathname && sessionSwitcherState.open;
  const compactSidebarVisible = mobileShow === "sidebar" || sessionSwitcherOpen;
  const setSessionSwitcherOpen = (open: boolean) => {
    setSessionSwitcherState({ open, pathname });
  };

  const closeCompactSidebarOnLinkClick = (event: MouseEvent<HTMLDivElement>) => {
    if (!(event.target instanceof Element)) {
      return;
    }
    const link = event.target.closest("a[href]");
    if (link) {
      setSessionSwitcherOpen(false);
    }
  };

  return (
    <div className="absolute inset-0 flex overflow-hidden overscroll-none">
      <div
        data-testid="sidebar-pane"
        style={{ "--sidebar-w": `${sidebarWidth}px` } as React.CSSProperties}
        className={cn(
          "shrink-0 h-full w-full xl:w-[var(--sidebar-w)] xl:block",
          sidebarHiddenOnMobile ? "hidden" : "block md:hidden",
        )}
      >
        {!isCompactDesktop ? sidebar : null}
      </div>

      <div className="hidden xl:block">
        <ResizeHandle onResize={handleSidebarResize} testId="resize-handle" />
      </div>

      <div
        data-testid="session-switcher-rail"
        className={cn(
          "hidden md:flex xl:hidden h-full w-12 shrink-0 items-start justify-center border-r border-border bg-panel px-1 py-3",
          mobileShow === "sidebar" && "md:hidden",
        )}
      >
        <Button
          variant="ghost"
          size="icon"
          className="h-9 w-9 rounded-md text-muted-foreground hover:bg-muted/50 hover:text-foreground"
          aria-label="Open session switcher"
          aria-expanded={sessionSwitcherOpen}
          aria-controls="compact-session-sidebar"
          onClick={() => setSessionSwitcherOpen(!sessionSwitcherOpen)}
        >
          <PanelLeftOpen className="h-4 w-4" />
        </Button>
      </div>

      <div
        id="compact-session-sidebar"
        data-testid="compact-sidebar-pane"
        className={cn(
          "hidden md:block xl:hidden h-full shrink-0 overflow-hidden border-r border-border bg-panel transition-[width] duration-200 ease-out",
          compactSidebarVisible ? "w-[min(360px,42vw)]" : "w-0 border-r-0",
        )}
        onClickCapture={closeCompactSidebarOnLinkClick}
      >
        <div className="h-full w-[min(360px,42vw)] overflow-hidden">
          {isCompactDesktop && compactSidebarVisible ? sidebar : null}
        </div>
      </div>

      <div
        className={cn(
          "flex-1 min-w-0 overflow-auto overscroll-contain",
          contentHiddenOnMobile && "hidden xl:block",
        )}
      >
        {children}
      </div>
    </div>
  );
}
