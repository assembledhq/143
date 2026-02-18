"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import {
  LayoutDashboard,
  AlertCircle,
  Play,
  BarChart3,
  DollarSign,
  Settings,
} from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import "./globals.css";
import { useState } from "react";
import { cn } from "@/lib/utils";
import { ErrorBoundary } from "@/components/error-boundary";

const navItems = [
  { label: "Overview", icon: LayoutDashboard, href: "/" },
  { label: "Issues", icon: AlertCircle, href: "/issues" },
  { label: "Runs", icon: Play, href: "/runs" },
  { label: "Analytics", icon: BarChart3, href: "/analytics" },
  { label: "Costs", icon: DollarSign, href: "/costs" },
  { label: "Settings", icon: Settings, href: "/settings" },
];

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const pathname = usePathname();
  const [queryClient] = useState(() => new QueryClient());

  return (
    <html lang="en">
      <body className="antialiased">
        <NuqsAdapter>
        <QueryClientProvider client={queryClient}>
          <ErrorBoundary>
          <div className="flex h-screen">
            <aside className="w-56 border-r border-border bg-sidebar flex flex-col">
              <div className="px-5 py-5">
                <Link href="/" className="text-sm font-semibold tracking-tight text-sidebar-foreground">
                  143.dev
                </Link>
              </div>
              <nav className="flex-1 px-3 space-y-0.5">
                {navItems.map((item) => {
                  const isActive =
                    item.href === "/"
                      ? pathname === "/"
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
            </aside>
            <main className="flex-1 overflow-auto bg-background">
              <div className="mx-auto max-w-4xl px-8 py-8">
                {children}
              </div>
            </main>
          </div>
          </ErrorBoundary>
        </QueryClientProvider>
        </NuqsAdapter>
      </body>
    </html>
  );
}
