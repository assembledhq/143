"use client";

import {
  LayoutDashboard,
  AlertCircle,
  Play,
  BarChart3,
  DollarSign,
  Settings,
  LogOut,
  ChevronsUpDown,
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
import { useAuth } from "@/hooks/use-auth";
import { useEffect } from "react";

const navItems = [
  { label: "Overview", icon: LayoutDashboard, href: "/overview" },
  { label: "Issues", icon: AlertCircle, href: "/issues" },
  { label: "Runs", icon: Play, href: "/runs" },
  { label: "Analytics", icon: BarChart3, href: "/analytics" },
  { label: "Costs", icon: DollarSign, href: "/costs" },
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
      <div className="flex h-screen items-center justify-center">
        <div className="text-sm text-muted-foreground">Loading...</div>
      </div>
    );
  }

  if (!isAuthenticated) {
    return null;
  }

  return (
    <div className="flex h-screen">
      <aside className="w-56 border-r border-border bg-sidebar flex flex-col">
        <div className="px-5 py-5">
          <Link href="/overview" className="text-sm font-semibold tracking-tight text-sidebar-foreground">
            143.dev
          </Link>
        </div>
        <nav className="flex-1 px-3 space-y-0.5">
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
        <div className="px-3 pb-4">
          {user && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] font-medium transition-colors",
                    pathname.startsWith("/settings")
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
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="top" className="w-48">
                <DropdownMenuItem onClick={() => router.push("/settings")}>
                  <Settings className="h-4 w-4" />
                  Settings
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
      <main className="flex-1 overflow-auto bg-background">
        <div className="mx-auto max-w-4xl px-8 py-8">
          {children}
        </div>
      </main>
    </div>
  );
}
