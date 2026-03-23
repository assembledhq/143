"use client";

import {
  Zap,
  Play,
  FolderKanban,
  Settings,
  Users,
  LogOut,
  ChevronsUpDown,
  Plug,
  Bot,
  Sparkles,
  ScrollText,
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
import { useAuth } from "@/hooks/use-auth";
import { useEffect } from "react";
import { RepoContextSwitcher } from "@/components/repo-context-switcher";

const navItems = [
  { label: "Autopilot", icon: Zap, href: "/autopilot" },
  { label: "Sessions", icon: Play, href: "/sessions" },
  { label: "Projects", icon: FolderKanban, href: "/projects" },
];

export function AuthenticatedLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { user, isLoading, isAuthenticated, logout } = useAuth();

  useEffect(() => {
    if (!isLoading && !isAuthenticated) {
      router.replace("/login");
    }
  }, [isLoading, isAuthenticated, router]);

  if (isLoading) {
    return (
      <div className="flex h-screen">
        <aside className="w-64 border-r border-border bg-sidebar flex flex-col">
          <div className="px-5 py-5">
            <div className="h-5 w-14 rounded bg-muted animate-pulse" />
          </div>
          <nav className="flex-1 px-2.5 space-y-0.5">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="flex items-center gap-2.5 px-2.5 py-2">
                <div className="h-4 w-4 rounded bg-muted animate-pulse" />
                <div className="h-3.5 rounded bg-muted animate-pulse" style={{ width: `${60 + i * 12}px` }} />
              </div>
            ))}
          </nav>
          <div className="px-2.5 pb-3.5">
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
      <aside className="w-64 border-r border-border bg-sidebar flex flex-col relative">
        <div className="absolute inset-0 bg-gradient-to-b from-primary/[0.03] to-transparent pointer-events-none" />
        <div className="relative px-5 py-5 flex items-center gap-2">
          <Link href="/autopilot" className="text-base font-bold tracking-tight text-sidebar-foreground">
            143.dev
          </Link>
          <RepoContextSwitcher />
        </div>
        <nav className="relative flex-1 px-2.5 space-y-0.5">
          {navItems.map((item) => {
            const isActive = pathname === item.href || pathname.startsWith(item.href + "/");
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "relative flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-[13px] font-medium transition-all duration-150",
                  isActive
                    ? "bg-sidebar-accent text-sidebar-accent-foreground shadow-[inset_0_0_0_1px_oklch(0.6_0.15_270_/_8%)] before:absolute before:left-0 before:top-1/2 before:-translate-y-1/2 before:h-4 before:w-[3px] before:rounded-full before:bg-gradient-to-b before:from-primary before:to-primary/50"
                    : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                )}
              >
                <item.icon className="h-4 w-4" />
                {item.label}
              </Link>
            );
          })}
        </nav>
        <div className="relative px-2.5 pb-3.5">
          {user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className={cn(
                    "h-8 w-full justify-start gap-2 rounded-lg px-2.5 text-[13px] font-medium transition-colors duration-150",
                    pathname.startsWith("/settings") ||
                    pathname.startsWith("/team") ||
                    pathname.startsWith("/integrations") ||
                    pathname.startsWith("/agent") ||
                    pathname.startsWith("/llm")
                      ? "bg-sidebar-accent text-sidebar-accent-foreground"
                      : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
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
                    <div className="flex h-5 w-5 items-center justify-center rounded-full bg-muted text-[10px] font-medium">
                      {user.name?.[0]?.toUpperCase() ?? "?"}
                    </div>
                  )}
                  <span className="truncate flex-1 text-left">{user.name}</span>
                  <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 opacity-50" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="top" className="w-48">
                <DropdownMenuItem onClick={() => router.push("/settings")}>
                  <Settings className="h-4 w-4" />
                  General
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/integrations")}>
                  <Plug className="h-4 w-4" />
                  Integrations
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/agent")}>
                  <Bot className="h-4 w-4" />
                  Coding agents
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/llm")}>
                  <Sparkles className="h-4 w-4" />
                  LLM
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/team")}>
                  <Users className="h-4 w-4" />
                  Team
                </DropdownMenuItem>
                {user.role === "admin" && (
                  <DropdownMenuItem onClick={() => router.push("/settings/audit-log")}>
                    <ScrollText className="h-4 w-4" />
                    Audit log
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
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
        <div className="pointer-events-none fixed inset-0 bg-[radial-gradient(ellipse_at_top_right,oklch(0.6_0.1_270_/_3%)_0%,transparent_50%)]" />
        <div className="relative max-w-none px-8 py-6 lg:px-10 flex-1">
          {children}
        </div>
      </main>
    </div>
  );
}
