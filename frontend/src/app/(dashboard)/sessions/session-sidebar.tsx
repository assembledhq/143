"use client";

import { useQuery } from "@tanstack/react-query";
import { Plus, Search } from "lucide-react";
import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { cn, formatTimeAgo, sessionTitle } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useSessionUserFilter } from "@/hooks/use-session-user-filter";
import { SessionOwnerToggle } from "./session-owner-toggle";
import { queryKeys } from "@/lib/query-keys";
import { useOptimisticSessions, type OptimisticSession } from "@/contexts/optimistic-sessions";
import { DiffStatsBadge } from "@/components/code-review/diff-stats-badge";

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

// Status groups — keep in sync with models.NeedsAttentionStatuses / WorkingStatuses / DoneStatuses.
const needsAttentionStatuses = ["awaiting_input", "needs_human_guidance", "failed"];
const workingStatuses = ["pending", "running"];
const doneStatuses = ["completed", "pr_created", "cancelled", "skipped", "idle"];

const needsAttentionSet = new Set(needsAttentionStatuses);
const workingSet = new Set(workingStatuses);

/** Map a filter tab value to the comma-separated status string for the API. */
function filterToStatusParam(filter: string | null): string | undefined {
  if (!filter || filter === "all") return undefined;
  if (filter === "needs_attention") return needsAttentionStatuses.join(",");
  if (filter === "working") return workingStatuses.join(",");
  if (filter === "done") return doneStatuses.join(",");
  return filter;
}

// ---------------------------------------------------------------------------
// Lightweight diff stats badge for sidebar rows
// ---------------------------------------------------------------------------

function SessionDiffBadge({ diffStats }: { diffStats?: { added: number; removed: number; files_changed: number } }) {
  if (!diffStats) return null;
  if (diffStats.added === 0 && diffStats.removed === 0) return null;
  return (
    <span className="inline-flex shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5">
      <DiffStatsBadge added={diffStats.added} removed={diffStats.removed} className="text-xs" />
    </span>
  );
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
            <span className="text-xs text-muted-foreground shrink-0">{cfg.label}</span>
            <span className="text-xs text-muted-foreground/50">just now</span>
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
  const { currentUserFilter, triggeredByUserId, user, setUserFilter } = useSessionUserFilter();
  const selectedId = params?.id as string | undefined;
  const [search, setSearch] = useState("");

  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");

  const { optimisticSessions } = useOptimisticSessions();

  const currentFilter = activeFilter ?? "all";
  const statusParam = filterToStatusParam(currentFilter);

  // Fetch all sessions (for tab badge counts and the "all" view).
  // Also fetches a filtered query when a tab is active — see sessions-page-content
  // for the rationale on the double-fetch tradeoff.
  const { data: allData, isLoading } = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), triggeredByUserId],
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined, triggered_by_user_id: triggeredByUserId }),
    refetchInterval: 10000,
  });

  // Fetch filtered sessions from the backend when a specific tab is selected.
  const { data: filteredData } = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), statusParam, triggeredByUserId],
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined, status: statusParam, triggered_by_user_id: triggeredByUserId }),
    refetchInterval: 10000,
    enabled: !!statusParam,
  });

  const allSessions = useMemo(() => allData?.data ?? [], [allData?.data]);

  const needsAttentionSessions = allSessions.filter((s) => needsAttentionSet.has(s.status));
  const workingSessions = allSessions.filter((s) => workingSet.has(s.status));

  const filteredSessions = useMemo(
    () => {
      if (statusParam && filteredData) return filteredData.data;
      return allSessions;
    },
    [allSessions, filteredData, statusParam],
  );

  const displayedSessions = useMemo(() => {
    if (!search.trim()) return filteredSessions;
    const q = search.toLowerCase();
    return filteredSessions.filter((s) => sessionTitle(s).toLowerCase().includes(q));
  }, [filteredSessions, search]);

  const isNewSession = pathname === "/sessions/new";

  return (
    <div className="w-full h-full border-r border-border bg-muted/30 flex flex-col">
      {/* Header */}
      <div className="px-4 pt-3 pb-3 space-y-2.5">

        {/* Row 1: Search + Owner toggle */}
        <div className="flex items-center gap-2">
          <div className="relative flex-1 min-w-0">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/50" />
            <input
              type="text"
              placeholder="Search sessions..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setSearch("");
                  e.currentTarget.blur();
                }
              }}
              className="w-full h-7 pl-8 pr-2 rounded-md border border-border bg-background text-[13px] placeholder:text-muted-foreground/50 focus:outline-none focus:ring-1 focus:ring-ring"
            />
          </div>
          <SessionOwnerToggle
            currentUserFilter={currentUserFilter}
            onFilterChange={setUserFilter}
            className="shrink-0"
          />
        </div>

        {/* New session button */}
        <Link
          href="/sessions/new"
          className="flex items-center justify-center gap-2 w-full h-8 rounded-md border border-border bg-background text-[13px] font-medium text-foreground hover:bg-accent transition-colors shadow-sm"
        >
          <Plus className="h-3.5 w-3.5" />
          New session
        </Link>

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
                      "rounded-full text-white text-xs leading-none px-1.5 py-0.5",
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
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            Loading...
          </div>
        )}

        {!isLoading && displayedSessions.length === 0 && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            {allSessions.length === 0 ? "No sessions yet" : "No sessions match this filter."}
          </div>
        )}

        {displayedSessions.map((session) => {
          const isSelected = selectedId === session.id;
          const cfg = statusConfig[session.status] || statusConfig.pending;
          const isWorkingSession = workingSet.has(session.status);
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
                  <div className="flex items-start justify-between gap-2">
                    <p className="text-[13px] font-medium text-foreground truncate leading-snug">
                      {sessionTitle(session)}
                    </p>
                  </div>
                  <div className="flex items-center justify-between mt-0.5">
                    <div className="flex items-center gap-3 min-w-0">
                      <span className="text-xs text-muted-foreground shrink-0">
                        {cfg.label}
                      </span>
                      {session.pm_plan_id && !session.triggered_by_user_id && (
                        <span className="inline-flex items-center rounded-full bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary shrink-0">
                          PM
                        </span>
                      )}
                      <span className="text-xs text-muted-foreground/50 truncate">
                        {formatTimeAgo(ts)}
                      </span>
                    </div>
                    <SessionDiffBadge diffStats={session.diff_stats} />
                  </div>
                  {session.status === "failed" && (session.failure_explanation || session.error) && (
                    <p className="text-xs text-destructive/70 truncate mt-0.5">
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
