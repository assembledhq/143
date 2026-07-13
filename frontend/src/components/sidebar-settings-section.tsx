"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  Settings,
  CircleUser,
  Plug,
  Bot,
  Sparkles,
  Target,
  Activity,
  Users,
  ScrollText,
  BarChart3,
  KeyRound,
  Globe,
  ChevronRight,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleTrigger,
  CollapsibleContent,
} from "@/components/ui/collapsible";

interface SettingsItem {
  label: string;
  icon: LucideIcon;
  href: string;
  adminOnly?: boolean;
  // Hides the entry from selected roles. Backend rejects the underlying reads,
  // so showing the link would land them on an empty/failed page.
  hideForRoles?: string[];
}

interface SettingsGroup {
  label: string | null;
  items: SettingsItem[];
}

const settingsGroups: SettingsGroup[] = [
  {
    label: "PERSONAL",
    items: [
      { label: "Account", icon: CircleUser, href: "/settings/account" },
    ],
  },
  {
    label: "CONNECTIONS",
    items: [
      { label: "Integrations", icon: Plug, href: "/settings/integrations", hideForRoles: ["viewer", "builder"] },
    ],
  },
  {
    label: "AGENTS",
    items: [
      { label: "Coding agents", icon: Bot, href: "/settings/agent", hideForRoles: ["viewer"] },
      { label: "App LLM", icon: Sparkles, href: "/settings/llm", adminOnly: true },
      { label: "Autopilot", icon: Target, href: "/settings/autopilot", adminOnly: true },
    ],
  },
  {
    label: "RUNTIME",
    items: [
      { label: "Sandboxes", icon: Activity, href: "/settings/runtime", adminOnly: true },
      { label: "Previews", icon: Globe, href: "/settings/previews", adminOnly: true },
    ],
  },
  {
    label: "SECURITY & ADMIN",
    items: [
      { label: "Organization", icon: Settings, href: "/settings", adminOnly: true },
      { label: "Team", icon: Users, href: "/settings/team", hideForRoles: ["viewer", "builder"] },
      { label: "API keys", icon: KeyRound, href: "/settings/api-keys", adminOnly: true },
    ],
  },
  {
    label: "OPERATIONS",
    items: [
      { label: "Usage", icon: BarChart3, href: "/settings/usage", adminOnly: true },
      { label: "Audit log", icon: ScrollText, href: "/settings/audit-log", adminOnly: true },
    ],
  },
];

const STORAGE_KEY = "sidebar-settings-expanded";

function isSettingsPath(pathname: string): boolean {
  return pathname === "/settings" || pathname.startsWith("/settings/");
}

function isItemActive(pathname: string, href: string): boolean {
  if (href === "/settings") {
    return pathname === "/settings";
  }
  return pathname === href || pathname.startsWith(href + "/");
}

export function SidebarSettingsSection({
  pathname,
  userRole,
  onNavigate,
  variant = "desktop",
}: {
  pathname: string;
  userRole: string | undefined;
  onNavigate?: () => void;
  variant?: "desktop" | "mobile";
}) {
  const onSettingsPage = isSettingsPath(pathname);
  const isMobile = variant === "mobile";

  // Default to expanded if on a settings page to avoid flicker; otherwise
  // start collapsed and let the mount effect restore the persisted value.
  const [isOpen, setIsOpen] = useState(onSettingsPage);

  // On mount, restore persisted state from localStorage (only when not on a
  // settings page, since that always forces open).
  const didMount = useRef(false);
  useEffect(() => {
    if (!onSettingsPage) {
      const stored = localStorage.getItem(STORAGE_KEY);
      if (stored === "true") setIsOpen(true);
    }
    didMount.current = true;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Auto-expand when navigating to a settings page
  useEffect(() => {
    if (onSettingsPage && !isOpen) {
      setIsOpen(true);
    }
  }, [onSettingsPage]); // eslint-disable-line react-hooks/exhaustive-deps

  // Persist open/close to localStorage (skip initial mount)
  useEffect(() => {
    if (!didMount.current) return;
    localStorage.setItem(STORAGE_KEY, String(isOpen));
  }, [isOpen]);

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <div data-testid="sidebar-settings-divider" className="mx-0 my-1 border-t border-sidebar-border/70" />
      <CollapsibleTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          className={cn(
            "relative flex h-auto w-full items-center rounded-md px-2.5 font-medium transition-colors duration-[175ms]",
            isMobile ? "gap-2.5 py-3 text-sm" : "gap-2.5 py-[7px] type-dense",
            onSettingsPage
              ? "bg-accent/65 text-foreground before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-primary"
              : "text-sidebar-foreground/70 hover:bg-sidebar-accent hover:text-sidebar-foreground"
          )}
        >
          <Settings className="h-4 w-4 shrink-0" />
          <span className="flex-1 text-left">Settings</span>
          <ChevronRight
            className={cn(
              "shrink-0 opacity-50 transition-transform duration-200",
              isMobile ? "h-4 w-4" : "h-3.5 w-3.5",
              isOpen && "rotate-90"
            )}
          />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent forceMount className={cn(
        "overflow-hidden transition-all duration-200",
        isOpen ? "grid grid-rows-[1fr] opacity-100" : "grid grid-rows-[0fr] opacity-0"
      )}>
        <div className="min-h-0">
          <div className="mt-0.5 space-y-0.5">
            {settingsGroups.map((group, groupIndex) => {
              const visibleItems = group.items.filter((item) => {
                if (item.adminOnly && userRole !== "admin") return false;
                if (item.hideForRoles?.includes(userRole ?? "")) return false;
                return true;
              });
              if (visibleItems.length === 0) return null;

              return (
                <div key={groupIndex}>
                  {group.label && (
                    <div
                      className={cn(
                        "pt-3 pb-1 font-medium uppercase tracking-wider text-muted-foreground",
                        isMobile ? "px-8 text-xs" : "px-7 text-xs",
                      )}
                    >
                      {group.label}
                    </div>
                  )}
                  {visibleItems.map((item) => {
                    const active = isItemActive(pathname, item.href);
                    const Icon = item.icon;
                    return (
                      <Link
                        key={item.href}
                        href={item.href}
                        onClick={
                          onNavigate
                            ? (e) => {
                                // Skip on modifier/middle clicks — the user is
                                // opening in a new tab and the current page
                                // hasn't changed for them.
                                if (
                                  e.button === 0 &&
                                  !e.metaKey &&
                                  !e.ctrlKey &&
                                  !e.shiftKey &&
                                  !e.altKey
                                ) {
                                  onNavigate();
                                }
                              }
                            : undefined
                        }
                        className={cn(
                          "relative flex items-center gap-2 rounded-lg pr-2.5 font-medium transition-colors duration-150",
                          isMobile ? "py-2.5 pl-8 text-sm" : "py-1.5 pl-7 type-dense",
                          active
                            ? "bg-accent/65 text-foreground before:absolute before:left-1.5 before:top-1/2 before:h-4 before:-translate-y-1/2 before:w-0.5 before:rounded-full before:bg-primary"
                            : "text-sidebar-foreground/70 hover:bg-sidebar-accent hover:text-sidebar-foreground"
                        )}
                      >
                        <Icon className={cn("shrink-0", isMobile ? "h-4 w-4" : "h-3.5 w-3.5")} />
                        {item.label}
                      </Link>
                    );
                  })}
                </div>
              );
            })}
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}
