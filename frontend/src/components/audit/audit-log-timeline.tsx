"use client";

import { useRef } from "react";
import type { User } from "@/lib/types";
import { AuditLogEntry } from "./audit-log-entry";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/empty-state";
import { Clock, ArrowUp, History } from "lucide-react";
import { useAuditLogFeed } from "./use-audit-log-feed";

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
  const topRef = useRef<HTMLDivElement | null>(null);
  const {
    entries,
    isLoading,
    error,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
    hasLoadedHistory,
  } = useAuditLogFeed({
    filters,
    pageSize,
  });

  if (isLoading && entries.length === 0) {
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

  if (entries.length === 0) {
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
      <div ref={topRef} className="flex items-center justify-between gap-3 border-b border-border/50 px-6 py-3">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <Badge variant="secondary" className="rounded-full bg-muted/70 px-2.5 py-0.5 text-xs font-medium text-foreground">
            Latest first
          </Badge>
          <span className="flex items-center gap-1.5">
            <History className="h-3.5 w-3.5" />
            {entries.length} event{entries.length === 1 ? "" : "s"} loaded
          </span>
        </div>
        {hasLoadedHistory && (
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 rounded-full px-2.5 text-xs"
            onClick={() => topRef.current?.scrollIntoView({ behavior: "smooth", block: "start" })}
          >
            <ArrowUp className="h-3.5 w-3.5" />
            Back to newest
          </Button>
        )}
      </div>
      <div className="divide-y-0">
        {entries.map((entry) => (
          <AuditLogEntry key={entry.id} entry={entry} members={members} />
        ))}
      </div>
      <div className="border-t border-border/50 px-6 py-3">
        {hasNextPage && (
          <Button
            variant="outline"
            size="sm"
            className="h-8 rounded-full px-3 text-xs"
            onClick={() => fetchNextPage()}
            disabled={isFetchingNextPage}
          >
            {isFetchingNextPage ? "Loading..." : "Load older"}
          </Button>
        )}
      </div>
    </div>
  );
}
