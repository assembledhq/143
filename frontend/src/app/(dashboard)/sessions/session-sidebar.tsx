"use client";

import { useQuery } from "@tanstack/react-query";
import { Plus, Search } from "lucide-react";
import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { cn, formatTimeAgo } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { useOptimisticSessions, type OptimisticSession } from "@/contexts/optimistic-sessions";
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
  { value: "needs_attention", label: "Needs attention" },
  { value: "working", label: "Working" },
  { value: "done", label: "Done" },
];

const needsAttentionStatuses = new Set(["awaiting_input", "needs_human_guidance", "failed"]);
const workingStatuses = new Set(["pending", "running"]);
const doneStatuses = new Set(["completed", "pr_created", "cancelled", "skipped", "idle"]);

function isWorking(s: Session): boolean {
  return workingStatuses.has(s.status);
}

function filterSessions(sessions: Session[], filter: string | null): Session[] {
  if (!filter || filter === "all") return sessions;
  if (filter === "needs_attention") return sessions.filter((s) => needsAttentionStatuses.has(s.status));
  if (filter === "working") return sessions.filter(isWorking);
  if (filter === "done") return sessions.filter((s) => doneStatuses.has(s.status));
  return sessions.filter((s) => s.status === filter);
}

function sessionTitle(session: Session): string {
  if (session.result_summary) return session.result_summary;
  if (session.pm_approach) return session.pm_approach;
  return `Session ${session.id.slice(0, 8)}`;
}

// ---------------------------------------------------------------------------
// Optimistic (unsaved) session row
// ---------------------------------------------------------------------------

function OptimisticSessionRow({ session }: { session: OptimisticSession }) {
  const cfg = statusConfig.pending;
  return (
    <div className="block rounded-lg px-3 py-2.5 mb-0.5">
      <div className="flex items-start gap-2.5 min-w-0">
        <div className="mt-1.5 shrink-0">
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
          </span>
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-medium text-foreground truncate leading-snug">
            {session.title}
          </p>
          <div className="flex items-center gap-3 mt-0.5">
            <span className="text-[11px] text-muted-foreground shrink-0">{cfg.label}</span>
            <span className="text-[11px] text-muted-foreground/50">just now</span>
          </div>
        </div>
      </div>
    </div>
  );
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

  const { optimisticSessions } = useOptimisticSessions();

  const { data, isLoading } = useQuery({
    queryKey: queryKeys.sessions.list(repo),
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined }),
    refetchInterval: 10000,
  });

  const allSessions = data?.data ?? [];
  const currentFilter = activeFilter ?? "all";

  const needsAttentionSessions = allSessions.filter((s) => needsAttentionStatuses.has(s.status));
  const workingSessions = allSessions.filter(isWorking);

  const filteredSessions = useMemo(
    () => filterSessions(allSessions, activeFilter),
    [allSessions, activeFilter],
  );

  const displayedSessions = useMemo(() => {
    if (!search.trim()) return filteredSessions;
    const q = search.toLowerCase();
    return filteredSessions.filter((s) => sessionTitle(s).toLowerCase().includes(q));
  }, [filteredSessions, search]);

  const isNewSession = pathname === "/sessions/new";

  return (
    <div className="w-full h-full border-r border-border bg-muted/30 flex flex-col">
      {/* New session button */}
      <Link
        href="/sessions/new"
        className="flex items-center gap-2.5 px-4 py-3 border-b border-border/50 text-[13px] font-medium text-muted-foreground hover:text-foreground transition-colors"
      >
        <Plus className="h-4 w-4" />
        New session
      </Link>

      {/* Header */}
      <div className="px-4 pt-3 pb-2 space-y-3">

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
        <Tabs
          value={currentFilter}
          onValueChange={(v) => setActiveFilter(v === "all" ? null : v)}
          className="gap-0"
        >
          <TabsList size="sm" className="overflow-x-auto">
            {filterTabs.map((tab) => {
              const count =
                tab.value === "needs_attention" ? needsAttentionSessions.length
                : tab.value === "working" ? workingSessions.length
                : 0;
              return (
                <TabsTrigger key={tab.value} value={tab.value}>
                  {tab.label}
                  {count > 0 && (
                    <span className={cn(
                      "rounded-full text-white text-[9px] leading-none px-1.5 py-0.5",
                      tab.value === "needs_attention" ? "bg-orange-500"
                      : "bg-primary"
                    )}>{count}</span>
                  )}
                </TabsTrigger>
              );
            })}
          </TabsList>
        </Tabs>
      </div>

      {/* Session list */}
      <div className="flex-1 overflow-y-auto px-2 pb-2">
        {/* Ghost "New session" entry when creating */}
        {isNewSession && (
          <Link
            href="/sessions/new"
            className="block rounded-lg px-3 py-2.5 mb-0.5 bg-background shadow-sm border border-border/50"
          >
            <div className="flex items-center gap-2.5 min-w-0">
              <span className="inline-flex rounded-full h-2 w-2 border border-muted-foreground/30 shrink-0" />
              <p className="text-[13px] text-muted-foreground/50 italic">
                New session
              </p>
            </div>
          </Link>
        )}

        {(currentFilter === "all" || currentFilter === "working") &&
          optimisticSessions.map((os) => (
            <OptimisticSessionRow key={os.id} session={os} />
          ))}

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
          const isWorkingSession = workingStatuses.has(session.status);
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
                  {isWorkingSession ? (
                    <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />
                  ) : (
                    <StatusDot color={cfg.dot} />
                  )}
                </div>

                {/* Content */}
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-medium text-foreground truncate leading-snug">
                    {sessionTitle(session)}
                  </p>
                  <div className="flex items-center gap-3 mt-0.5">
                    <span className="text-[11px] text-muted-foreground shrink-0">
                      {cfg.label}
                    </span>
                    <span className="text-[11px] text-muted-foreground/50 truncate">
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
