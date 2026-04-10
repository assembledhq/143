"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  Settings,
  Plug,
  Bot,
  Sparkles,
  Target,
  FlaskConical,
  Users,
  ScrollText,
  ChevronRight,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
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
}

interface SettingsGroup {
  label: string | null;
  items: SettingsItem[];
}

const settingsGroups: SettingsGroup[] = [
  {
    label: null,
    items: [
      { label: "General", icon: Settings, href: "/settings" },
    ],
  },
  {
    label: "PLATFORM",
    items: [
      { label: "Integrations", icon: Plug, href: "/settings/integrations" },
      { label: "Coding agents", icon: Bot, href: "/settings/agent" },
      { label: "LLM", icon: Sparkles, href: "/settings/llm" },
      { label: "Autopilot settings", icon: Target, href: "/settings/autopilot" },
      { label: "Evals", icon: FlaskConical, href: "/settings/evals" },
    ],
  },
  {
    label: "ORGANIZATION",
    items: [
      { label: "Team", icon: Users, href: "/settings/team" },
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
}: {
  pathname: string;
  userRole: string | undefined;
}) {
  const onSettingsPage = isSettingsPath(pathname);

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
      <div className="mt-4 mb-1 mx-0 border-t border-border/50" />
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className={cn(
            "flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-[13px] font-medium transition-colors duration-150",
            onSettingsPage
              ? "bg-sidebar-accent text-sidebar-accent-foreground"
              : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
          )}
        >
          <Settings className="h-4 w-4 shrink-0" />
          <span className="flex-1 text-left">Settings</span>
          <ChevronRight
            className={cn(
              "h-3.5 w-3.5 shrink-0 opacity-50 transition-transform duration-200",
              isOpen && "rotate-90"
            )}
          />
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent forceMount className={cn(
        "overflow-hidden transition-all duration-200",
        isOpen ? "grid grid-rows-[1fr] opacity-100" : "grid grid-rows-[0fr] opacity-0"
      )}>
        <div className="min-h-0">
          <div className="mt-0.5 space-y-0.5">
            {settingsGroups.map((group, groupIndex) => {
              const visibleItems = group.items.filter(
                (item) => !item.adminOnly || userRole === "admin"
              );
              if (visibleItems.length === 0) return null;

              return (
                <div key={groupIndex}>
                  {group.label && (
                    <div className="px-7 pt-3 pb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground">
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
                        className={cn(
                          "relative flex items-center gap-2 rounded-lg py-1.5 pl-7 pr-2.5 text-[13px] font-medium transition-colors duration-150",
                          active
                            ? "bg-sidebar-accent text-sidebar-accent-foreground before:absolute before:left-1.5 before:top-1/2 before:h-4 before:-translate-y-1/2 before:w-[3px] before:rounded-full before:bg-primary"
                            : "text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                        )}
                      >
                        <Icon className="h-3.5 w-3.5 shrink-0" />
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
