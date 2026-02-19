"use client";

import {
  LayoutDashboard,
  AlertCircle,
  Play,
  BarChart3,
  DollarSign,
  Settings,
  LogOut,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { cn } from "@/lib/utils";
import { useAuth } from "@/hooks/use-auth";
import { useEffect } from "react";

const navItems = [
  { label: "Overview", icon: LayoutDashboard, href: "/overview" },
  { label: "Issues", icon: AlertCircle, href: "/issues" },
  { label: "Runs", icon: Play, href: "/runs" },
  { label: "Analytics", icon: BarChart3, href: "/analytics" },
  { label: "Costs", icon: DollarSign, href: "/costs" },
  { label: "Settings", icon: Settings, href: "/settings" },
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
            <div className="flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] text-muted-foreground">
              <span className="truncate flex-1">{user.name}</span>
              <button
                onClick={logout}
                className="hover:text-foreground transition-colors"
                aria-label="Log out"
              >
                <LogOut className="h-3.5 w-3.5" />
              </button>
            </div>
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
