"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { AuditLog, User } from "@/lib/types";
import { AuditLogEntry } from "./audit-log-entry";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { Clock } from "lucide-react";
import { useState } from "react";

interface AuditLogTimelineProps {
  /** Pre-set filters for scoped views (e.g., session_id, project_id). */
  filters?: Record<string, string>;
  /** Number of entries per page. */
  pageSize?: number;
  /** Team members for resolving actor names. */
  members: User[];
}

export function AuditLogTimeline({
  filters = {},
  pageSize = 10,
  members,
}: AuditLogTimelineProps) {
  const [cursors, setCursors] = useState<string[]>([]);
  const currentCursor = cursors[cursors.length - 1];

  const { data, isLoading, error } = useQuery({
    queryKey: ["audit-logs", filters, currentCursor, pageSize],
    queryFn: () =>
      api.auditLogs.list({
        ...filters,
        cursor: currentCursor,
        limit: pageSize,
      }),
  });

  const entries: AuditLog[] = data?.data ?? [];
  const nextCursor = data?.meta?.next_cursor;

  if (isLoading && cursors.length === 0) {
    return (
      <div className="py-6 text-center text-sm text-muted-foreground">
        Loading activity...
      </div>
    );
  }

  if (error) {
    return (
      <div className="py-6 text-center text-sm text-muted-foreground">
        Failed to load activity.
      </div>
    );
  }

  if (entries.length === 0 && cursors.length === 0) {
    return (
      <EmptyState
        icon={Clock}
        title="No activity yet"
        description="Activity will appear here as actions are performed."
      />
    );
  }

  return (
    <div>
      <div className="divide-y-0">
        {entries.map((entry) => (
          <AuditLogEntry key={entry.id} entry={entry} members={members} />
        ))}
      </div>
      <div className="flex items-center justify-between px-3 py-2">
        {cursors.length > 0 && (
          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={() => setCursors((prev) => prev.slice(0, -1))}
          >
            Newer
          </Button>
        )}
        <div className="flex-1" />
        {nextCursor && (
          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={() => setCursors((prev) => [...prev, nextCursor])}
            disabled={isLoading}
          >
            {isLoading ? "Loading..." : "Older"}
          </Button>
        )}
      </div>
    </div>
  );
}
