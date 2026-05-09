"use client";

import {
  Zap,
  Play,
  FolderKanban,
  RefreshCw,
  LogOut,
  ChevronsUpDown,
  Search,
  PenSquare,
  Info,
  Copy,
  Check,
  Menu,
  X,
  type LucideIcon,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useAuth } from "@/hooks/use-auth";
import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { RepoContextSwitcher } from "@/components/repo-context-switcher";
import { OrgSwitcher } from "@/components/org-switcher";
import { CommandPalette } from "@/components/command-palette/command-palette";
import { SidebarSettingsSection } from "@/components/sidebar-settings-section";
import { CreateSessionDialog } from "@/components/create-session-dialog";
import { ResizeHandle } from "@/components/resize-handle";
import { usePersistedPanelWidth } from "@/hooks/use-persisted-panel-width";

type SidebarUser = NonNullable<ReturnType<typeof useAuth>["user"]>;

// Skip drawer-close side effects when the user opens a link in a new tab/window
// (modifier or middle click). The current page hasn't changed for them, so the
// drawer should stay where it is.
function isPlainNavClick(e: React.MouseEvent): boolean {
  return (
    e.button === 0 &&
    !e.metaKey &&
    !e.ctrlKey &&
    !e.shiftKey &&
    !e.altKey
  );
}

const buildSha = process.env.NEXT_PUBLIC_BUILD_SHA || "dev";
const shortSha = buildSha === "dev" ? "dev" : buildSha.slice(0, 7);
const APP_SIDEBAR_DEFAULT_WIDTH = 236;
const APP_SIDEBAR_MIN_WIDTH = 200;
const APP_SIDEBAR_MAX_WIDTH = 300;
const APP_SIDEBAR_STORAGE_KEY = "143:app-sidebar-width";

function VersionMenuItem() {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback((e: Event) => {
    e.preventDefault();
    if (!navigator.clipboard) return;
    navigator.clipboard.writeText(buildSha).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      },
      () => {},
    );
  }, []);

  return (
    <DropdownMenuItem
      onSelect={handleCopy}
      aria-label="Copy version SHA"
      className="gap-2 text-xs text-muted-foreground focus:text-muted-foreground"
    >
      <Info className="h-3.5 w-3.5 opacity-60" />
      <span className="flex-1">
        Version <span className="font-mono">{shortSha}</span>
      </span>
      {copied ? (
        <Check className="h-3.5 w-3.5 text-green-500" />
      ) : (
        <Copy className="h-3.5 w-3.5 opacity-60" />
      )}
    </DropdownMenuItem>
  );
}

type NavItem = {
  label: string;
  icon: LucideIcon;
  href: string;
  showProposalBadge: boolean;
};

const navItems: NavItem[] = [
  { label: "Sessions", icon: Play, href: "/sessions", showProposalBadge: false },
  { label: "Automations", icon: RefreshCw, href: "/automations", showProposalBadge: false },
  { label: "Projects", icon: FolderKanban, href: "/projects", showProposalBadge: true },
  { label: "Autopilot", icon: Zap, href: "/autopilot", showProposalBadge: false },
];

type SidebarBodyProps = {
  variant: "desktop" | "mobile";
  user: SidebarUser;
  pathname: string;
  proposalCount: number;
  onPaletteOpen: () => void;
  onCreateSession: () => void;
  onNavigate?: () => void;
  onLogout: () => void;
};

function SidebarBody({
  variant,
  user,
  pathname,
  proposalCount,
  onPaletteOpen,
  onCreateSession,
  onNavigate,
  onLogout,
}: SidebarBodyProps) {
  const isMobile = variant === "mobile";
  // Touch-friendly sizing on mobile: nav items ~44px tall, icon buttons 44×44.
  const navItemClasses = isMobile
    ? "py-3 text-sm"
    : "py-[7px] text-xs";
  const iconBtnClasses = isMobile ? "h-11 w-11" : "h-7 w-7";
  const iconSize = isMobile ? "h-5 w-5" : "h-4 w-4";

  return (
    <>
      {/* Header: org switcher + actions (+ close on mobile) */}
      <div
        className={cn(
          "relative flex items-center justify-between",
          isMobile ? "px-3 py-3" : "px-4 py-3.5"
        )}
      >
        <div className="flex items-center min-w-0 flex-1">
          <OrgSwitcher userEmail={user?.email} />
        </div>
        {isMobile ? (
          <SheetClose asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              className="h-9 w-9 rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
              aria-label="Close navigation menu"
            >
              <X className="h-4 w-4" />
            </Button>
          </SheetClose>
        ) : (
          <TooltipProvider>
            <div className="flex items-center gap-0.5">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={onPaletteOpen}
                    className={cn(
                      iconBtnClasses,
                      "rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
                    )}
                    aria-label="Search"
                  >
                    <Search className={iconSize} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="bottom" sideOffset={4}>Search</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={onCreateSession}
                    className={cn(
                      iconBtnClasses,
                      "rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
                    )}
                    aria-label="New session"
                  >
                    <PenSquare className={iconSize} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="bottom" sideOffset={4}>New session</TooltipContent>
              </Tooltip>
            </div>
          </TooltipProvider>
        )}
      </div>

      {/* Navigation */}
      <nav className="relative flex-1 px-2 space-y-0.5 overflow-y-auto">
        {navItems.map((item) => {
          const isActive = pathname === item.href || pathname.startsWith(item.href + "/");
          return (
            <Link
              key={item.href}
              href={item.href}
              onClick={onNavigate ? (e) => { if (isPlainNavClick(e)) onNavigate(); } : undefined}
              aria-current={isActive ? "page" : undefined}
              className={cn(
                "relative flex items-center gap-2.5 rounded-md px-2.5 font-medium transition-colors duration-150 active:bg-sidebar-accent",
                navItemClasses,
                isActive
                  ? "bg-sidebar-accent text-sidebar-accent-foreground"
                  : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground"
              )}
            >
              <item.icon className="h-4 w-4 shrink-0" />
              {item.label}
              {item.showProposalBadge && proposalCount > 0 && (
                <Badge variant="secondary" className="ml-auto text-xs px-1.5 py-0 h-5 bg-purple-100 text-purple-700 dark:bg-purple-900 dark:text-purple-300">
                  {proposalCount}
                </Badge>
              )}
            </Link>
          );
        })}
        <SidebarSettingsSection
          pathname={pathname}
          userRole={user?.role}
          onNavigate={onNavigate}
        />
      </nav>

      {/* Repo context switcher */}
      <div className="relative px-2 pb-1 border-t border-border/50 pt-2">
        <RepoContextSwitcher />
      </div>

      {/* User menu */}
      <div
        className={cn(
          "relative px-2 border-t border-border/50 pt-2",
          isMobile ? "pb-[max(0.5rem,env(safe-area-inset-bottom))]" : "pb-1"
        )}
      >
        {user && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                className={cn(
                  "w-full justify-start gap-2 rounded-md px-2.5 font-medium transition-colors duration-150 text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
                  isMobile ? "h-11 text-sm" : "h-8 text-xs"
                )}
              >
                {user.avatar_url ? (
                  /* eslint-disable-next-line @next/next/no-img-element */
                  <img
                    src={user.avatar_url}
                    alt=""
                    className="h-5 w-5 rounded-full"
                  />
                ) : (
                  <div className="flex h-5 w-5 items-center justify-center rounded-full bg-muted text-xs font-medium">
                    {user.name?.[0]?.toUpperCase() ?? "?"}
                  </div>
                )}
                <span className="truncate flex-1 text-left">{user.name}</span>
                <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 opacity-40" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" side="top" className="w-48">
              <DropdownMenuItem onClick={onLogout}>
                <LogOut className="h-4 w-4" />
                Log out
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <VersionMenuItem />
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </>
  );
}

type MobileTopBarProps = {
  onOpenMenu: () => void;
  onPaletteOpen: () => void;
  onCreateSession: () => void;
  menuOpen: boolean;
};

function isSessionDetailRoute(pathname: string): boolean {
  const segments = pathname.split("/").filter(Boolean);
  return segments.length === 2 && segments[0] === "sessions" && segments[1] !== "new";
}

function MobileTopBar({
  onOpenMenu,
  onPaletteOpen,
  onCreateSession,
  menuOpen,
}: MobileTopBarProps) {
  return (
    <header className="md:hidden flex h-14 shrink-0 items-center gap-1 border-b border-border/50 bg-background px-2">
      <Button
        variant="ghost"
        size="icon"
        onClick={onOpenMenu}
        aria-label="Open navigation menu"
        aria-expanded={menuOpen}
        className="h-11 w-11 rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
      >
        <Menu className="h-5 w-5" />
      </Button>
      <div className="flex-1" />
      <Button
        variant="ghost"
        size="icon"
        onClick={onPaletteOpen}
        aria-label="Search"
        className="h-11 w-11 rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
      >
        <Search className="h-5 w-5" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={onCreateSession}
        aria-label="New session"
        className="h-11 w-11 rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
      >
        <PenSquare className="h-5 w-5" />
      </Button>
    </header>
  );
}

export function AuthenticatedLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const hideMobileTopBar = isSessionDetailRoute(pathname);
  const router = useRouter();
  const {
    user,
    isLoading,
    isFetching,
    isAuthenticated,
    isUnauthorized,
    isTransientError,
    refetchUser,
    logout,
  } = useAuth();

  const { data: proposalSummary } = useQuery({
    queryKey: ["proposalSummary"],
    queryFn: () => api.projects.proposalSummary(),
    refetchInterval: 30000,
    enabled: isAuthenticated,
  });
  const proposalCount = proposalSummary?.data?.count ?? 0;

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const { width: appSidebarWidth, resizeBy: resizeAppSidebar } = usePersistedPanelWidth({
    storageKey: APP_SIDEBAR_STORAGE_KEY,
    defaultWidth: APP_SIDEBAR_DEFAULT_WIDTH,
    minWidth: APP_SIDEBAR_MIN_WIDTH,
    maxWidth: APP_SIDEBAR_MAX_WIDTH,
  });

  // Global Cmd+K / Ctrl+K shortcut
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setPaletteOpen((prev) => !prev);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  // Sync the drawer to the URL: any pathname change closes it. Direct nav-link
  // taps already call onNavigate for instant feedback, but indirect navigation
  // (org switcher, repo switcher, programmatic router.push) wouldn't otherwise
  // dismiss the drawer. The effect runs after a confirmed external (URL) state
  // change, which is the canonical use case the lint rule exempts.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { setMobileMenuOpen(false); }, [pathname]);

  const handlePaletteOpen = useCallback(() => {
    setPaletteOpen(true);
    setMobileMenuOpen(false);
  }, []);
  const handleCreateSessionOpen = useCallback(() => {
    setCreateOpen(true);
    setMobileMenuOpen(false);
  }, []);
  const handleOpenMobileMenu = useCallback(() => setMobileMenuOpen(true), []);
  const handleCloseMobileMenu = useCallback(() => setMobileMenuOpen(false), []);

  useEffect(() => {
    // Only redirect on a confirmed 401. Transient network errors (5xx during
    // a rolling deploy, offline blips) leave isAuthenticated false but must
    // not kick the user to /login — the query will retry and recover.
    if (!isLoading && isUnauthorized) {
      router.replace("/login");
    }
  }, [isLoading, isUnauthorized, router]);

  // Show the loading skeleton while the initial /me call is in flight OR
  // while retries are still in progress without a cached user. Confirmed
  // 401s fall through to the redirect path; exhausted non-401 retries fall
  // through to the error UI below.
  const showLoadingSkeleton =
    !user && !isUnauthorized && !isTransientError && isLoading;

  if (!user && isTransientError) {
    return (
      <div
        role="alert"
        aria-live="polite"
        className="flex h-dvh items-center justify-center bg-background px-6"
      >
        <div className="max-w-sm text-center space-y-4">
          <h2 className="text-base font-semibold text-foreground">
            Can&apos;t reach the server
          </h2>
          <p className="text-sm text-muted-foreground">
            We couldn&apos;t load your session. This usually clears up on its own
            during a deploy.
          </p>
          <Button
            variant="outline"
            size="sm"
            disabled={isFetching}
            onClick={() => {
              void refetchUser();
            }}
          >
            {isFetching ? "Retrying…" : "Try again"}
          </Button>
        </div>
      </div>
    );
  }

  if (showLoadingSkeleton) {
    return (
      <div className="flex h-dvh">
        <aside
          data-testid="app-sidebar"
          style={{ "--app-sidebar-w": `${appSidebarWidth}px` } as React.CSSProperties}
          className={cn(
            "hidden md:flex bg-sidebar flex-col w-[var(--app-sidebar-w)]"
          )}
        >
          <div className="px-4 py-4">
            <div className="h-5 w-20 rounded bg-muted animate-pulse" />
          </div>
          <nav className="flex-1 px-2 space-y-0.5">
            {Array.from({ length: 3 }).map((_, i) => (
              <div key={i} className="flex items-center gap-2.5 px-2.5 py-2">
                <div className="h-4 w-4 rounded bg-muted animate-pulse" />
                <div className="h-3.5 rounded bg-muted animate-pulse" style={{ width: `${60 + i * 12}px` }} />
              </div>
            ))}
          </nav>
          <div className="px-2 pb-3">
            <div className="flex items-center gap-2 px-2.5 py-2">
              <div className="h-5 w-5 rounded-full bg-muted animate-pulse" />
              <div className="h-3.5 w-20 rounded bg-muted animate-pulse" />
            </div>
          </div>
        </aside>
        <div className="hidden md:block">
          <ResizeHandle onResize={resizeAppSidebar} testId="app-sidebar-resize-handle" />
        </div>
        <div className="flex flex-1 min-w-0 flex-col">
          <header className="md:hidden flex h-14 items-center gap-2 border-b border-border/50 bg-background px-3">
            <div className="h-6 w-6 rounded bg-muted animate-pulse" />
            <div className="ml-auto h-6 w-6 rounded bg-muted animate-pulse" />
            <div className="h-6 w-6 rounded bg-muted animate-pulse" />
          </header>
          <main className="flex-1 overflow-auto bg-background">
            <div className="max-w-none px-4 sm:px-6 lg:px-10 py-5 sm:py-6 space-y-4">
              <div className="h-7 w-40 rounded bg-muted animate-pulse" />
              <div className="space-y-3">
                <div className="h-4 w-full rounded bg-muted animate-pulse" />
                <div className="h-4 w-3/4 rounded bg-muted animate-pulse" />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
                {Array.from({ length: 3 }).map((_, i) => (
                  <div key={i} className="h-24 rounded-lg border border-border bg-muted/30 animate-pulse" />
                ))}
              </div>
            </div>
          </main>
        </div>
      </div>
    );
  }

  if (isUnauthorized || !user) {
    return null;
  }

  return (
    <div className="flex h-dvh">
      {/* Desktop sidebar (md and up) */}
      <aside
        data-testid="app-sidebar"
        style={{ "--app-sidebar-w": `${appSidebarWidth}px` } as React.CSSProperties}
        className={cn(
          "hidden md:flex bg-sidebar flex-col relative w-[var(--app-sidebar-w)]"
        )}
      >
        <SidebarBody
          variant="desktop"
          user={user}
          pathname={pathname}
          proposalCount={proposalCount}
          onPaletteOpen={handlePaletteOpen}
          onCreateSession={handleCreateSessionOpen}
          onLogout={logout}
        />
      </aside>
      <div className="hidden md:block">
        <ResizeHandle onResize={resizeAppSidebar} testId="app-sidebar-resize-handle" />
      </div>

      {/* Mobile drawer (below md) */}
      <Sheet open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
        <SheetContent
          side="left"
          hideCloseButton
          aria-describedby={undefined}
          className="w-[min(85vw,320px)] p-0 bg-sidebar border-r border-border/50 flex flex-col gap-0"
        >
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <SidebarBody
            variant="mobile"
            user={user}
            pathname={pathname}
            proposalCount={proposalCount}
            onPaletteOpen={handlePaletteOpen}
            onCreateSession={handleCreateSessionOpen}
            onNavigate={handleCloseMobileMenu}
            onLogout={logout}
          />
        </SheetContent>
      </Sheet>

      <div className="flex min-w-0 flex-1 flex-col">
        {!hideMobileTopBar ? (
          <MobileTopBar
            menuOpen={mobileMenuOpen}
            onOpenMenu={handleOpenMobileMenu}
            onPaletteOpen={handlePaletteOpen}
            onCreateSession={handleCreateSessionOpen}
          />
        ) : null}
        <main className="flex-1 overflow-auto bg-background relative flex flex-col">
          <div className="relative max-w-none px-4 sm:px-6 lg:px-10 py-5 sm:py-6 flex-1 min-h-0">
            {children}
          </div>
        </main>
      </div>

      {user && (
        <CommandPalette
          open={paletteOpen}
          onOpenChange={setPaletteOpen}
          userRole={user.role}
          logout={logout}
        />
      )}
      <CreateSessionDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
