"use client";

import {
  LayoutDashboard,
  Play,
  Settings,
  Users,
  LogOut,
  ChevronsUpDown,
  Plug,
  Bot,
  Target,
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

const navItems = [
  { label: "Overview", icon: LayoutDashboard, href: "/overview" },
  { label: "Sessions", icon: Play, href: "/sessions" },
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
        <aside className="w-64 border-r border-border/80 bg-sidebar flex flex-col">
          <div className="px-4 py-4">
            <div className="h-4 w-14 rounded bg-muted animate-pulse" />
          </div>
          <nav className="flex-1 px-2.5 space-y-0.5">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="flex items-center gap-2.5 px-2.5 py-1.5">
                <div className="h-4 w-4 rounded bg-muted animate-pulse" />
                <div className="h-3.5 rounded bg-muted animate-pulse" style={{ width: `${60 + i * 12}px` }} />
              </div>
            ))}
          </nav>
          <div className="px-2.5 pb-3.5">
            <div className="flex items-center gap-2 px-2.5 py-1.5">
              <div className="h-5 w-5 rounded-full bg-muted animate-pulse" />
              <div className="h-3.5 w-20 rounded bg-muted animate-pulse" />
            </div>
          </div>
        </aside>
        <main className="flex-1 overflow-auto bg-card">
          <div className="max-w-none px-4 py-4 space-y-4">
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
      <aside className="w-64 border-r border-border/80 bg-sidebar flex flex-col">
        <div className="px-4 py-4">
          <Link href="/overview" className="text-sm font-semibold tracking-tight text-sidebar-foreground">
            143.dev
          </Link>
        </div>
        <nav className="flex-1 px-2.5 space-y-0.5">
          {navItems.map((item) => {
            const isActive =
              item.href === "/overview"
                ? pathname === "/overview"
                : pathname.startsWith(item.href);
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[13px] font-medium transition-colors",
                  isActive
                    ? "bg-sidebar-accent text-sidebar-accent-foreground"
                    : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                )}
              >
                <item.icon className="h-4 w-4" />
                {item.label}
              </Link>
            );
          })}
        </nav>
        <div className="px-2.5 pb-3.5">
          {user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className={cn(
                    "h-8 w-full justify-start gap-2 rounded-md px-2.5 text-[13px] font-medium transition-colors",
                    pathname.startsWith("/settings") || pathname.startsWith("/team") || pathname.startsWith("/integrations") || pathname.startsWith("/agent") || pathname.startsWith("/prioritization")
                      ? "bg-sidebar-accent text-sidebar-accent-foreground"
                      : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                  )}
                >
                  {user.avatar_url ? (
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
                  Agent
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/prioritization")}>
                  <Target className="h-4 w-4" />
                  Prioritization
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => router.push("/team")}>
                  <Users className="h-4 w-4" />
                  Team
                </DropdownMenuItem>
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
      <main className="flex-1 overflow-auto bg-card">
        <div className="max-w-none px-4 py-4 lg:px-5">
          {children}
        </div>
      </main>
    </div>
  );
}
