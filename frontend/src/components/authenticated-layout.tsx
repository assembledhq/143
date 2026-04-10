"use client";

import {
  Zap,
  Play,
  FolderKanban,
  LogOut,
  ChevronsUpDown,
  Search,
  PenSquare,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
import { queryKeys } from "@/lib/query-keys";
import { RepoContextSwitcher } from "@/components/repo-context-switcher";
import { CommandPalette } from "@/components/command-palette/command-palette";
import { SidebarSettingsSection } from "@/components/sidebar-settings-section";
import { CreateSessionDialog } from "@/components/create-session-dialog";
import type { Organization, SingleResponse } from "@/lib/types";

const navItems = [
  { label: "Autopilot", icon: Zap, href: "/autopilot", showProposalBadge: false },
  { label: "Sessions", icon: Play, href: "/sessions", showProposalBadge: false },
  { label: "Projects", icon: FolderKanban, href: "/projects", showProposalBadge: true },
];

export function AuthenticatedLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { user, isLoading, isAuthenticated, logout } = useAuth();

  const { data: proposalSummary } = useQuery({
    queryKey: ["proposalSummary"],
    queryFn: () => api.projects.proposalSummary(),
    refetchInterval: 30000,
    enabled: isAuthenticated,
  });
  const proposalCount = proposalSummary?.data?.count ?? 0;

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
    enabled: isAuthenticated,
  });
  const orgName = settingsResponse?.data?.name ?? "143.dev";

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);

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

  const handlePaletteOpen = useCallback(() => setPaletteOpen(true), []);

  useEffect(() => {
    if (!isLoading && !isAuthenticated) {
      router.replace("/login");
    }
  }, [isLoading, isAuthenticated, router]);

  if (isLoading) {
    return (
      <div className="flex h-screen">
        <aside className="w-[260px] border-r border-border bg-sidebar flex flex-col">
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
        <main className="flex-1 overflow-auto bg-background">
          <div className="max-w-none px-8 py-6 space-y-4">
            <div className="h-7 w-40 rounded bg-muted animate-pulse" />
            <div className="space-y-3">
              <div className="h-4 w-full rounded bg-muted animate-pulse" />
              <div className="h-4 w-3/4 rounded bg-muted animate-pulse" />
            </div>
            <div className="grid grid-cols-3 gap-4">
              {Array.from({ length: 3 }).map((_, i) => (
                <div key={i} className="h-24 rounded-lg border border-border bg-muted/30 animate-pulse" />
              ))}
            </div>
          </div>
        </main>
      </div>
    );
  }

  if (!isAuthenticated) {
    return null;
  }

  return (
    <div className="flex h-screen">
      <aside className="w-[260px] border-r border-border/50 bg-sidebar flex flex-col relative">
        {/* Header: org name + actions */}
        <div className="relative flex items-center justify-between px-4 py-3.5">
          <div className="flex items-center gap-1.5 min-w-0">
            <Link
              href="/autopilot"
              className="flex items-center gap-1.5 min-w-0 rounded-md px-1.5 py-1 -ml-1.5 hover:bg-sidebar-accent transition-colors"
            >
              <div className="flex h-5 w-5 items-center justify-center rounded bg-foreground text-background text-xs font-semibold shrink-0">
                {orgName[0]?.toUpperCase() ?? "1"}
              </div>
              <span className="text-sm font-semibold text-sidebar-foreground truncate">
                {orgName}
              </span>
            </Link>
            <RepoContextSwitcher />
          </div>
          <TooltipProvider>
            <div className="flex items-center gap-0.5">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={handlePaletteOpen}
                    className="h-7 w-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
                    aria-label="Search"
                  >
                    <Search className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="bottom" sideOffset={4}>Search</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => setCreateOpen(true)}
                    className="h-7 w-7 rounded-md text-muted-foreground hover:text-foreground hover:bg-sidebar-accent"
                    aria-label="New session"
                  >
                    <PenSquare className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="bottom" sideOffset={4}>New session</TooltipContent>
              </Tooltip>
            </div>
          </TooltipProvider>
        </div>

        {/* Navigation */}
        <nav className="relative flex-1 px-2 space-y-0.5 overflow-y-auto">
          {navItems.map((item) => {
            const isActive = pathname === item.href || pathname.startsWith(item.href + "/");
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "relative flex items-center gap-2.5 rounded-md px-2.5 py-[7px] text-[13px] font-medium transition-colors duration-150",
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
          <SidebarSettingsSection pathname={pathname} userRole={user?.role} />
        </nav>

        {/* User menu */}
        <div className="relative px-2 pb-3 border-t border-border/50 pt-2">
          {user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8 w-full justify-start gap-2 rounded-md px-2.5 text-[13px] font-medium transition-colors duration-150 text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
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
                <DropdownMenuItem onClick={logout}>
                  <LogOut className="h-4 w-4" />
                  Log out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </aside>
      <main className="flex-1 overflow-auto bg-background relative flex flex-col">
        <div className="relative max-w-none px-8 py-6 lg:px-10 flex-1 min-h-0">
          {children}
        </div>
      </main>
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
