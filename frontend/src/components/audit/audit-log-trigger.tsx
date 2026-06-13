"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Clock } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { cn, formatTimeAgo } from "@/lib/utils";
import { useAuth } from "@/hooks/use-auth";
import type { User } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { AuditLogSidesheet } from "./audit-log-sidesheet";

interface AuditLogTriggerProps {
  /** Filters to scope the audit log query (e.g., { session_id: "..." }). */
  filters: Record<string, string>;
  /** Team members for resolving actor names. If omitted, fetched internally. */
  members?: User[];
  /** Sidesheet title. */
  title?: string;
  /**
   * Visual variant.
   * - `default`: standalone row with a leading Clock icon.
   * - `inline`: drops the icon, adds a leading middle-dot separator, and removes
   *   horizontal padding so the trigger reads as part of a surrounding sentence.
   * - `footer`: muted page footer for low-priority settings activity metadata.
   */
  variant?: "default" | "inline" | "footer";
}

export function AuditLogTrigger({ filters, members: membersProp, title, variant = "default" }: AuditLogTriggerProps) {
  const [open, setOpen] = useState(false);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  // Fetch members internally when not provided by parent
  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: isAdmin && !membersProp,
  });
  const members = membersProp ?? membersData?.data ?? [];

  // Fetch just the latest entry to show "Updated X ago by Y"
  const { data, error } = useQuery({
    queryKey: ["audit-logs", "latest", filters],
    queryFn: () => api.auditLogs.list({ ...filters, limit: 1 }),
    enabled: isAdmin,
  });

  if (error) {
    captureError(error, { feature: "audit-log" });
  }

  const latestEntry = data?.data?.[0];

  // Don't render anything if there's no audit history
  if (!latestEntry) return null;

  const actorName = (() => {
    if (latestEntry.actor_type === "user" && latestEntry.user_id) {
      const member = members.find((m) => m.id === latestEntry.user_id);
      if (member) return member.name;
    }
    if (latestEntry.actor_type !== "user") {
      const labels: Record<string, string> = { agent: "Agent", system: "System", webhook: "Webhook" };
      return labels[latestEntry.actor_type] || latestEntry.actor_type;
    }
    return latestEntry.actor_id;
  })();

  const isInline = variant === "inline";
  const isFooter = variant === "footer";

  if (isFooter) {
    return (
      <footer className="flex border-t border-border/60 pt-4 text-xs text-muted-foreground">
        <Button
          variant="ghost"
          size="xs"
          onClick={() => setOpen(true)}
          className="inline-flex h-auto items-center gap-1.5 px-1 py-0.5 text-xs font-normal text-muted-foreground transition-colors hover:text-foreground"
        >
          <Clock className="h-3 w-3" />
          <span className="text-xs">Last activity:</span>
          <span className="text-xs">
            Updated {formatTimeAgo(latestEntry.created_at)} by {actorName}
          </span>
        </Button>
        <AuditLogSidesheet
          open={open}
          onOpenChange={setOpen}
          filters={filters}
          title={title}
          members={members}
        />
      </footer>
    );
  }

  return (
    <>
      {isInline && (
        <span aria-hidden="true" className="text-muted-foreground/50 text-xs">·</span>
      )}
      <Button
        variant="ghost"
        size="xs"
        onClick={() => setOpen(true)}
        className={cn(
          "inline-flex h-auto items-center py-0.5 text-xs font-normal text-muted-foreground transition-colors hover:text-foreground",
          isInline ? "px-0" : "gap-1.5 px-1",
        )}
      >
        {!isInline && <Clock className="h-3 w-3" />}
        <span className="text-xs">
          Updated {formatTimeAgo(latestEntry.created_at)} by {actorName}
        </span>
      </Button>
      <AuditLogSidesheet
        open={open}
        onOpenChange={setOpen}
        filters={filters}
        title={title}
        members={members}
      />
    </>
  );
}
