"use client";

import {
  Zap,
  Play,
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
  Settings,
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
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useAuth } from "@/hooks/use-auth";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { useCallback, useEffect, useRef, useState } from "react";
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
};

const navItems: NavItem[] = [
  { label: "Sessions", icon: Play, href: "/sessions" },
  { label: "Automations", icon: RefreshCw, href: "/automations" },
  { label: "Autopilot", icon: Zap, href: "/autopilot" },
];

type SidebarBodyProps = {
  variant: "desktop" | "mobile";
  user: SidebarUser;
  pathname: string;
  onPaletteOpen: () => void;
  onCreateSession: () => void;
  onNavigate?: () => void;
  onLogout: () => void;
};

function SidebarBody({
  variant,
  user,
  pathname,
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
            </Link>
          );
        })}
        <SidebarSettingsSection
          pathname={pathname}
          userRole={user?.role}
          onNavigate={onNavigate}
          variant={variant}
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

type CompactSidebarRailProps = {
  user: SidebarUser;
  pathname: string;
  onPaletteOpen: () => void;
  onCreateSession: () => void;
  onLogout: () => void;
};

function CompactSidebarRail({
  user,
  pathname,
  onPaletteOpen,
  onCreateSession,
  onLogout,
}: CompactSidebarRailProps) {
  return (
    <TooltipProvider>
      <aside
        data-testid="app-sidebar-rail"
        className="hidden md:flex xl:hidden h-full w-14 shrink-0 flex-col items-center border-r border-border/50 bg-sidebar py-2"
        aria-label="Primary navigation"
      >
        <div data-testid="app-sidebar-rail-quick-actions" className="flex flex-col items-center gap-0.5">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                onClick={onPaletteOpen}
                className="h-7 w-10 rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
                aria-label="Search"
              >
                <Search className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="right" sideOffset={8}>Search</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                onClick={onCreateSession}
                className="h-7 w-10 rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
                aria-label="New session"
              >
                <PenSquare className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="right" sideOffset={8}>New session</TooltipContent>
          </Tooltip>
        </div>

        <nav className="mt-2 flex flex-1 flex-col items-center gap-0.5">
          {navItems.map((item) => {
            const isActive = pathname === item.href || pathname.startsWith(item.href + "/");
            return (
              <Tooltip key={item.href}>
                <TooltipTrigger asChild>
                  <Link
                    href={item.href}
                    aria-label={item.label}
                    aria-current={isActive ? "page" : undefined}
                    className={cn(
                      "relative flex h-[30px] w-10 items-center justify-center rounded-md transition-colors duration-150",
                      isActive
                        ? "bg-sidebar-accent text-sidebar-accent-foreground"
                        : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground",
                    )}
                  >
                    <item.icon className="h-4 w-4" />
                  </Link>
                </TooltipTrigger>
                <TooltipContent side="right" sideOffset={8}>{item.label}</TooltipContent>
              </Tooltip>
            );
          })}
          <Tooltip>
            <TooltipTrigger asChild>
              <Link
                href="/settings"
                aria-label="Settings"
                aria-current={pathname.startsWith("/settings") ? "page" : undefined}
                className={cn(
                  "flex h-[30px] w-10 items-center justify-center rounded-md transition-colors duration-150",
                  pathname.startsWith("/settings")
                    ? "bg-sidebar-accent text-sidebar-accent-foreground"
                    : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground",
                )}
              >
                <Settings className="h-4 w-4" />
              </Link>
            </TooltipTrigger>
            <TooltipContent side="right" sideOffset={8}>Settings</TooltipContent>
          </Tooltip>
        </nav>

        <Popover>
          <Tooltip>
            <TooltipTrigger asChild>
              <PopoverTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="mt-1 h-10 w-10 rounded-md text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                  aria-label="Open workspace menu"
                >
                  {user?.avatar_url ? (
                    /* eslint-disable-next-line @next/next/no-img-element */
                    <img
                      src={user.avatar_url}
                      alt=""
                      className="h-5 w-5 rounded-full"
                    />
                  ) : (
                    <span className="flex h-5 w-5 items-center justify-center rounded-full bg-muted text-xs font-medium">
                      {user?.name?.[0]?.toUpperCase() ?? "?"}
                    </span>
                  )}
                </Button>
              </PopoverTrigger>
            </TooltipTrigger>
            <TooltipContent side="right" sideOffset={8}>Workspace</TooltipContent>
          </Tooltip>
          <PopoverContent
            side="right"
            align="end"
            sideOffset={8}
            className="w-72 p-0"
          >
            <div className="space-y-3 p-3">
              <div className="space-y-1">
                <p className="px-1 text-xs font-medium text-muted-foreground">Workspace</p>
                <div className="rounded-md border border-border/60 bg-background px-2 py-1.5">
                  <OrgSwitcher userEmail={user?.email} />
                </div>
              </div>
              <div className="rounded-md border border-border/60 bg-background p-1">
                <RepoContextSwitcher />
              </div>
              <div className="border-t border-border/60 pt-2">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="w-full justify-start gap-2 text-muted-foreground hover:text-foreground"
                  onClick={onLogout}
                >
                  <LogOut className="h-4 w-4" />
                  Log out
                </Button>
              </div>
            </div>
          </PopoverContent>
        </Popover>
      </aside>
    </TooltipProvider>
  );
}

type MobileTopBarProps = {
  onOpenMenu: () => void;
  onPaletteOpen: () => void;
  onCreateSession: () => void;
  menuOpen: boolean;
};

// Returns the second path segment of a /sessions/<segment> route, or null
// for anything else (including /sessions/new). Shared by the loose UI check
// below and the strict prefetch guard so the route shape is parsed once.
function sessionDetailRouteSegment(pathname: string): string | null {
  const segments = pathname.split("/").filter(Boolean);
  if (segments.length !== 2 || segments[0] !== "sessions" || segments[1] === "new") return null;
  return segments[1];
}

function isSessionDetailRoute(pathname: string): boolean {
  return sessionDetailRouteSegment(pathname) !== null;
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// Strict variant for the auth-gate prefetch: only a UUID segment is worth a
// speculative request, so non-id paths never hit the API.
export function sessionDetailRouteId(pathname: string): string | null {
  const segment = sessionDetailRouteSegment(pathname);
  return segment !== null && UUID_RE.test(segment) ? segment : null;
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
    isUnauthorized,
    isTransientError,
    refetchUser,
    logout,
  } = useAuth();

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  // On a cold load this layout renders only a skeleton until /auth/me
  // resolves, so the page behind it can't start its own data fetches — every
  // round trip serializes behind auth. Warm the session detail cache for
  // /sessions/[id] routes in parallel with /auth/me; when the page mounts,
  // React Query dedupes against the in-flight prefetch (or serves the settled
  // payload) instead of paying a fresh round trip. prefetchQuery swallows
  // errors by design: a 401 here just means the auth gate is about to
  // redirect to /login anyway.
  const queryClient = useQueryClient();
  const didPrefetchRouteDataRef = useRef(false);
  useEffect(() => {
    if (didPrefetchRouteDataRef.current) return;
    didPrefetchRouteDataRef.current = true;
    // Auth already settled (warm navigation) — the page mounts immediately
    // and owns its fetches; prefetching would only duplicate work.
    if (user || !isLoading) return;
    const sessionId = sessionDetailRouteId(pathname);
    if (!sessionId) return;
    void queryClient.prefetchQuery({
      queryKey: queryKeys.sessions.detail(sessionId),
      queryFn: () => api.sessions.get(sessionId),
    });
  }, [isLoading, pathname, queryClient, user]);
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
      <div className="fixed inset-0 flex h-dvh overflow-hidden overscroll-none bg-background">
        {/* Compact rail placeholder — holds space between md and xl so no layout shift on load */}
        <div className="hidden md:flex xl:hidden h-full w-14 shrink-0 flex-col items-center border-r border-border/50 bg-sidebar py-2" />
        <aside
          data-testid="app-sidebar"
          style={{ "--app-sidebar-w": `${appSidebarWidth}px` } as React.CSSProperties}
          className={cn(
            "hidden xl:flex bg-sidebar flex-col w-[var(--app-sidebar-w)]"
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
        <div className="hidden xl:block">
          <ResizeHandle onResize={resizeAppSidebar} testId="app-sidebar-resize-handle" />
        </div>
        <div className="flex flex-1 min-w-0 flex-col">
          <header className="md:hidden flex h-14 items-center gap-2 border-b border-border/50 bg-background px-3">
            <div className="h-6 w-6 rounded bg-muted animate-pulse" />
            <div className="ml-auto h-6 w-6 rounded bg-muted animate-pulse" />
            <div className="h-6 w-6 rounded bg-muted animate-pulse" />
          </header>
          <main className="flex-1 overflow-auto overscroll-contain bg-background">
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
    <div className="fixed inset-0 flex h-dvh overflow-hidden overscroll-none bg-background">
      {/* Desktop sidebar (md and up) */}
      <CompactSidebarRail
        user={user}
        pathname={pathname}
        onPaletteOpen={handlePaletteOpen}
        onCreateSession={handleCreateSessionOpen}
        onLogout={logout}
      />
      <aside
        data-testid="app-sidebar"
        style={{ "--app-sidebar-w": `${appSidebarWidth}px` } as React.CSSProperties}
        className={cn(
          "hidden xl:flex bg-sidebar flex-col relative w-[var(--app-sidebar-w)]"
        )}
      >
        <SidebarBody
          variant="desktop"
          user={user}
          pathname={pathname}
          onPaletteOpen={handlePaletteOpen}
          onCreateSession={handleCreateSessionOpen}
          onLogout={logout}
        />
      </aside>
      <div className="hidden xl:block">
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
        <main className="flex-1 overflow-auto overscroll-contain bg-background relative flex flex-col">
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
