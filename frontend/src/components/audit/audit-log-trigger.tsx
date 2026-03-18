"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Clock } from "lucide-react";
import { api } from "@/lib/api";
import { formatTimeAgo } from "@/lib/utils";
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
}

export function AuditLogTrigger({ filters, members: membersProp, title }: AuditLogTriggerProps) {
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
    console.error("Failed to load latest audit log entry:", error);
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

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        className="inline-flex items-center gap-1.5 text-xs text-muted-foreground/70 hover:text-muted-foreground transition-all duration-150 h-auto px-1.5 py-0.5 -ml-1.5"
      >
        <Clock className="h-3 w-3" />
        <span>
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
