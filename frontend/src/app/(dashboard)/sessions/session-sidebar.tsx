"use client";

import { useQuery } from "@tanstack/react-query";
import { Plus, Search, MessageSquare } from "lucide-react";
import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";

// ---------------------------------------------------------------------------
// Status config
// ---------------------------------------------------------------------------

const statusConfig: Record<string, { dot: string; label: string }> = {
  pending: { dot: "bg-muted-foreground/50", label: "Pending" },
  running: { dot: "bg-primary", label: "Running" },
  idle: { dot: "bg-sky-500", label: "Idle" },
  awaiting_input: { dot: "bg-amber-500", label: "Awaiting input" },
  needs_human_guidance: { dot: "bg-orange-500", label: "Needs guidance" },
  completed: { dot: "bg-emerald-500", label: "Completed" },
  pr_created: { dot: "bg-violet-500", label: "PR created" },
  failed: { dot: "bg-destructive", label: "Failed" },
  cancelled: { dot: "bg-muted-foreground/50", label: "Cancelled" },
  skipped: { dot: "bg-muted-foreground/30", label: "Skipped" },
};

const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "needs_human_guidance", label: "Guidance" },
  { value: "failed", label: "Failed" },
  { value: "done", label: "Done" },
];

const activeStatuses = new Set(["pending", "running", "awaiting_input"]);
const doneStatuses = new Set(["completed", "pr_created"]);

function isActive(s: Session): boolean {
  return activeStatuses.has(s.status);
}

function filterSessions(sessions: Session[], filter: string | null): Session[] {
  if (!filter || filter === "all") return sessions;
  if (filter === "active") return sessions.filter(isActive);
  if (filter === "done") return sessions.filter((s) => doneStatuses.has(s.status));
  return sessions.filter((s) => s.status === filter);
}

function sessionTitle(session: Session): string {
  if (session.result_summary) return session.result_summary;
  if (session.pm_approach) return session.pm_approach;
  return `Session ${session.id.slice(0, 8)}`;
}

function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}

// ---------------------------------------------------------------------------
// Sidebar component
// ---------------------------------------------------------------------------

export function SessionSidebar() {
  const params = useParams();
  const pathname = usePathname();
  const selectedId = params?.id as string | undefined;
  const [search, setSearch] = useState("");
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");

  const { data, isLoading } = useQuery({
    queryKey: ["sessions", repo],
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined }),
    refetchInterval: 10000,
  });

  const allSessions = data?.data ?? [];
  const currentFilter = activeFilter ?? "all";

  const activeSessions = allSessions.filter(isActive);
  const failedSessions = allSessions.filter((s) => s.status === "failed");
  const guidanceSessions = allSessions.filter((s) => s.status === "needs_human_guidance");

  const filteredSessions = useMemo(
    () => filterSessions(allSessions, activeFilter),
    [allSessions, activeFilter],
  );

  const displayedSessions = useMemo(() => {
    if (!search.trim()) return filteredSessions;
    const q = search.toLowerCase();
    return filteredSessions.filter((s) => sessionTitle(s).toLowerCase().includes(q));
  }, [filteredSessions, search]);

  return (
    <div className="w-80 border-r border-border bg-muted/30 flex flex-col shrink-0">
      {/* Header */}
      <div className="px-4 pt-4 pb-2 space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">Sessions</h2>
          <Button variant="ghost" size="icon" className="h-7 w-7" asChild>
            <Link href="/sessions/new">
              <Plus className="h-4 w-4" />
            </Link>
          </Button>
        </div>

        {/* Search */}
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/50" />
          <input
            type="text"
            placeholder="Search sessions..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="w-full h-8 pl-8 pr-3 rounded-md border border-border bg-background text-[13px] placeholder:text-muted-foreground/50 focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>

        {/* Filter tabs */}
        <div className="flex items-center gap-0.5 overflow-x-auto">
          {filterTabs.map((tab) => {
            const isSelected = currentFilter === tab.value;
            const count =
              tab.value === "active" ? activeSessions.length
              : tab.value === "failed" ? failedSessions.length
              : tab.value === "needs_human_guidance" ? guidanceSessions.length
              : 0;
            return (
              <button
                key={tab.value}
                className={cn(
                  "px-2 py-1 rounded-md text-[11px] font-medium transition-colors whitespace-nowrap",
                  isSelected
                    ? "bg-background text-foreground shadow-sm border border-border/50"
                    : "text-muted-foreground hover:text-foreground hover:bg-background/50"
                )}
                onClick={() => setActiveFilter(tab.value === "all" ? null : tab.value)}
              >
                {tab.label}
                {count > 0 && (
                  <span className={cn(
                    "ml-1 rounded-full text-white text-[9px] leading-none px-1.5 py-0.5",
                    tab.value === "failed" ? "bg-destructive"
                    : tab.value === "needs_human_guidance" ? "bg-orange-500"
                    : "bg-primary"
                  )}>{count}</span>
                )}
              </button>
            );
          })}
        </div>
      </div>

      {/* Session list */}
      <div className="flex-1 overflow-y-auto px-2 pb-2">
        {isLoading && (
          <div className="px-2 py-8 text-center text-[12px] text-muted-foreground">
            Loading...
          </div>
        )}

        {!isLoading && displayedSessions.length === 0 && (
          <div className="px-2 py-8 text-center text-[12px] text-muted-foreground">
            {allSessions.length === 0 ? "No sessions yet" : "No sessions match this filter."}
          </div>
        )}

        {displayedSessions.map((session) => {
          const isSelected = selectedId === session.id;
          const cfg = statusConfig[session.status] || statusConfig.pending;
          const isActiveSession = activeStatuses.has(session.status);
          const ts = session.completed_at || session.started_at || session.created_at;

          return (
            <Link
              key={session.id}
              href={`/sessions/${session.id}`}
              className={cn(
                "block rounded-lg px-3 py-2.5 mb-0.5 transition-all duration-150",
                isSelected
                  ? "bg-background shadow-sm border border-border/50"
                  : "hover:bg-background/60"
              )}
            >
              <div className="flex items-start gap-2.5 min-w-0">
                {/* Status dot */}
                <div className="mt-1.5 shrink-0">
                  {isActiveSession ? (
                    <span className="relative flex h-2 w-2">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
                      <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
                    </span>
                  ) : (
                    <span className={cn("inline-flex rounded-full h-2 w-2", cfg.dot)} />
                  )}
                </div>

                {/* Content */}
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium text-foreground truncate leading-snug">
                    {sessionTitle(session)}
                  </p>
                  <div className="flex items-center gap-2 mt-0.5">
                    <span className="text-[11px] text-muted-foreground">
                      {cfg.label}
                    </span>
                    <span className="text-[11px] text-muted-foreground/50">
                      {formatTimeAgo(ts)}
                    </span>
                  </div>
                  {session.status === "failed" && (session.failure_explanation || session.error) && (
                    <p className="text-[11px] text-destructive/70 truncate mt-0.5">
                      {session.failure_explanation || session.error}
                    </p>
                  )}
                </div>
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}
