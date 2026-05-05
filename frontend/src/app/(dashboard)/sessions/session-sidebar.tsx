"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { notify as toast } from "@/lib/notify";
import { Archive, ArchiveRestore, Plus, Search } from "lucide-react";
import Link from "next/link";
import { usePathname, useSelectedLayoutSegment } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PeopleFilter } from "@/components/people-filter";
import { cn, formatTimeAgo, sessionTitle } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { AnimatedEllipsis } from "@/components/animated-ellipsis";
import { SwipeActionRow } from "@/components/swipe-action-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useFilterSuffix, usePeopleFilter } from "@/hooks/use-people-filter";
import { queryKeys } from "@/lib/query-keys";
import { useOptimisticSessions, type OptimisticSession } from "@/contexts/optimistic-sessions";
import { DiffStatsBadge } from "@/components/code-review/diff-stats-badge";
import { NoReposWarning } from "@/components/no-repos-warning";
import type { SessionListItem, User } from "@/lib/types";
import { prMergedAccent } from "@/lib/pr-status-styles";
import {
  workingSet,
  filterToStatusParam,
} from "@/lib/session-status-groups";
import { getCountForTab, renderCount } from "@/lib/session-counts";

// ---------------------------------------------------------------------------
// Status config
// ---------------------------------------------------------------------------

const statusConfig: Record<string, { dot: string; label: string }> = {
  pending: { dot: "bg-muted-foreground/50", label: "Pending" },
  running: { dot: "bg-primary", label: "Running" },
  idle: { dot: "bg-primary", label: "Idle" },
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
  { value: "archived", label: "Archived" },
];


// ---------------------------------------------------------------------------
// Unread indicator logic
// ---------------------------------------------------------------------------

/** Returns true if the session has activity the current user hasn't seen yet. */
function isUnread(session: SessionListItem): boolean {
  // Sessions that are actively working are always "live" — show an animated dot instead.
  if (workingSet.has(session.status)) return false;

  const lastActivity = session.last_activity_at;
  if (!lastActivity) return false;

  // Never viewed → unread if there's been any activity.
  if (!session.last_viewed_at) return true;

  return new Date(lastActivity) > new Date(session.last_viewed_at);
}

// ---------------------------------------------------------------------------
// PR status badge for sidebar rows
// ---------------------------------------------------------------------------

function PRStatusBadge({ prSummary }: { prSummary?: SessionListItem["pr_summary"] }) {
  if (!prSummary) return null;

  let dotColor: string;
  let label: string;

  if (prSummary.status === "merged") {
    dotColor = prMergedAccent.dot;
    label = "Merged";
  } else if (prSummary.status === "closed") {
    dotColor = "bg-muted-foreground";
    label = "Closed";
  } else if (prSummary.ci_status === "success") {
    dotColor = "bg-emerald-500";
    label = "CI passed";
  } else if (prSummary.ci_status === "failure") {
    dotColor = "bg-destructive";
    label = "CI failed";
  } else {
    // pending / unknown CI status
    dotColor = "bg-muted-foreground/40";
    label = "CI pending";
  }

  return (
    <span className="inline-flex items-center gap-1 shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5" title={label}>
      <span className={cn("inline-flex rounded-full h-1.5 w-1.5", dotColor)} />
      <span className="text-xs text-muted-foreground">PR</span>
    </span>
  );
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
          <p className="text-xs font-medium text-foreground truncate leading-snug">
            {session.title}
          </p>
          <div className="flex items-center gap-3 mt-0.5">
            <span className="text-xs text-muted-foreground shrink-0">
              <span>{cfg.label}</span>
              <AnimatedEllipsis />
            </span>
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
  const pathname = usePathname();
  const selectedSegment = useSelectedLayoutSegment();
  const queryClient = useQueryClient();
  const {
    mode,
    selectedUserIDs,
    scopedUserIDs,
    serializedPeopleParam,
    currentUser,
    isResolved,
    setPeopleFilter,
  } = usePeopleFilter();
  const selectedId = selectedSegment && selectedSegment !== "new" ? selectedSegment : undefined;
  const [searchParam, setSearchParam] = useQueryState("search", parseAsString);
  const [search, setSearch] = useState(searchParam ?? "");
  const searchRef = useRef(search);
  const skipNextSearchParamWriteRef = useRef(false);
  // Debounce the search query so rapid typing doesn't fire a request per
  // keystroke. useDeferredValue only lowers render priority — it does not
  // throttle network calls.
  const [debouncedSearch, setDebouncedSearch] = useState("");
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedSearch(search.trim()), 200);
    return () => clearTimeout(handle);
  }, [search]);
  useEffect(() => {
    searchRef.current = search;
  }, [search]);
  useEffect(() => {
    const nextSearch = searchParam ?? "";
    if (searchRef.current === nextSearch) {
      return;
    }
    // Sync the local input from the URL when navigation/back/forward changes
    // the search param outside this component.
    // When that external navigation cleared/changed the param, the URL is the
    // source of truth. Skip the next write-back so we do not restore stale
    // input text into the URL and thrash the router state.
    skipNextSearchParamWriteRef.current = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSearch(nextSearch);
  }, [searchParam]);
  useEffect(() => {
    const currentParam = searchParam ?? "";
    if (search === currentParam) {
      skipNextSearchParamWriteRef.current = false;
      return;
    }
    if (skipNextSearchParamWriteRef.current) {
      skipNextSearchParamWriteRef.current = false;
      return;
    }
    void setSearchParam(search || null);
  }, [search, searchParam, setSearchParam]);

  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");
  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });
  const members = useMemo<User[]>(() => membersData?.data ?? [], [membersData?.data]);

  const { optimisticSessions, removeOptimisticSession } = useOptimisticSessions();

  const currentFilter = activeFilter ?? "all";
  const isArchivedView = currentFilter === "archived";
  const statusParam = filterToStatusParam(currentFilter);

  // Pagination state. Once the user clicks "Show more" we stop polling entirely
  // (page 0 included) — refreshing page 0 while extra pages are held locally
  // would invalidate the cursor that produced them. Pages re-hydrate on scope
  // change via the scopeKey reset below. See automations/[id]/page.tsx for the
  // same pattern.
  const [extraPages, setExtraPages] = useState<SessionListItem[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>(undefined);
  const isPaginated = extraPages.length > 0;

  const trimmedSearch = debouncedSearch;

  // Reset pagination when the effective query scope changes. Adjusting state
  // during render (rather than in an effect) avoids cascading renders — see
  // https://react.dev/reference/react/useState#storing-information-from-previous-renders.
  const scopeKey = `${repo ?? ""}|${serializedPeopleParam ?? "mine"}|${currentFilter}|${trimmedSearch}`;
  const [prevScopeKey, setPrevScopeKey] = useState(scopeKey);
  if (prevScopeKey !== scopeKey) {
    setPrevScopeKey(scopeKey);
    setExtraPages([]);
    setLoadMoreCursor(undefined);
  }
  const listParams = useMemo(
    () => ({
      limit: 50,
      repository_id: repo ?? undefined,
      triggered_by_user_ids: scopedUserIDs,
      search: trimmedSearch || undefined,
      ...(isArchivedView ? { only_archived: true } : {}),
      ...(!isArchivedView && statusParam ? { status: statusParam } : {}),
    }),
    [repo, scopedUserIDs, trimmedSearch, isArchivedView, statusParam],
  );

  const { data: listData, isLoading } = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), "filtered", currentFilter, serializedPeopleParam, trimmedSearch],
    queryFn: () => api.sessions.list(listParams),
    enabled: isResolved,
    refetchInterval: isPaginated ? false : 10000,
  });

  // Tab badge counts. Search-independent so tabs reflect the scope totals, not
  // the current search result size.
  const { data: countsData } = useQuery({
    queryKey: queryKeys.sessions.counts(repo, serializedPeopleParam),
    queryFn: () =>
      api.sessions.counts({
        repository_id: repo ?? undefined,
        triggered_by_user_ids: scopedUserIDs,
      }),
    enabled: isResolved,
    refetchInterval: 10000,
  });

  const firstPage = useMemo(() => listData?.data ?? [], [listData?.data]);
  const firstPageCursor = listData?.meta?.next_cursor || undefined;
  const cursor = isPaginated ? loadMoreCursor : firstPageCursor;
  const hasMore = !!cursor;

  const loadMoreMutation = useMutation({
    mutationFn: () => api.sessions.list({ ...listParams, cursor }),
    onSuccess: (res) => {
      setExtraPages((prev) => [...prev, res.data ?? []]);
      setLoadMoreCursor(res.meta?.next_cursor || undefined);
    },
  });

  const invalidateSessions = () => {
    queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
  };

  const archiveMutation = useMutation({
    mutationFn: (sessionId: string) => api.sessions.archive(sessionId),
    onSuccess: invalidateSessions,
    onError: () => {
      toast.error("Failed to archive session");
    },
  });

  const unarchiveMutation = useMutation({
    mutationFn: (sessionId: string) => api.sessions.unarchive(sessionId),
    onSuccess: invalidateSessions,
    onError: () => {
      toast.error("Failed to unarchive session");
    },
  });

  const displayedSessions = useMemo(() => {
    const merged = [firstPage, ...extraPages].flat();
    const q = search.trim().toLowerCase();
    if (!q) return merged;
    return merged.filter((s) => sessionTitle(s).toLowerCase().includes(q));
  }, [firstPage, extraPages, search]);

  // Hide optimistic rows whose real session is already in the list — prevents
  // the double-render flash between "optimistic added" and "server refetch
  // lands". Resolved-but-not-yet-visible rows stay until the real row arrives.
  const realIds = useMemo(() => new Set(displayedSessions.map((s) => s.id)), [displayedSessions]);
  const visibleOptimistic = useMemo(
    () => optimisticSessions.filter((os) => !(os.resolvedId && realIds.has(os.resolvedId))),
    [optimisticSessions, realIds],
  );

  // Garbage-collect resolved optimistic rows once we've observed their real
  // counterpart in the list. Done in an effect so state updates happen after
  // render. This also handles the failure case: if the real session later
  // changes status (e.g. to "failed"), it's still in the list, so the
  // optimistic stays hidden and gets cleaned up here.
  useEffect(() => {
    for (const os of optimisticSessions) {
      if (os.resolvedId && realIds.has(os.resolvedId)) {
        removeOptimisticSession(os.id);
      }
    }
  }, [optimisticSessions, realIds, removeOptimisticSession]);

  const counts = countsData?.data;

  // Carry the sidebar's filters into detail-page links so opening a session
  // doesn't reset the scope back to "Mine".
  const filterSuffix = useFilterSuffix(serializedPeopleParam, activeFilter, repo, search || null);

  const isNewSession = pathname === "/sessions/new";
  const showDefaultEmptyState =
    currentFilter === "all" && !trimmedSearch && (!counts || counts.all === 0);

  return (
    <div className="w-full h-full border-r border-border bg-muted/30 flex flex-col">
      {/* Header */}
      <div className="px-4 pt-3 pb-3 space-y-3">

        {/* Row 1: Search + Owner toggle */}
        <div className="flex items-center gap-2">
          <div className="relative flex-1 min-w-0">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/50" />
            <Input
              placeholder="Search sessions..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setSearch("");
                  e.currentTarget.blur();
                }
              }}
              className="h-8 pl-8 text-xs"
            />
          </div>
          <PeopleFilter
            mode={mode}
            selectedUserIDs={selectedUserIDs}
            members={members}
            currentUser={currentUser}
            onFilterChange={setPeopleFilter}
            className="shrink-0"
          />
        </div>

        {/* New session button */}
        <Link
          href={`/sessions/new${filterSuffix}`}
          className="flex items-center justify-center gap-2 w-full h-9 rounded-md border border-border bg-background text-xs font-medium text-foreground hover:bg-accent transition-colors shadow-sm"
        >
          <Plus className="h-4 w-4" />
          New session
        </Link>

        {/* Filter tabs */}
        <Tabs
          value={currentFilter}
          onValueChange={(v) => setActiveFilter(v === "all" ? null : v)}
          className="gap-0"
        >
          <TabsList size="sm" className="overflow-x-auto overflow-y-hidden">
            {filterTabs.map((tab) => {
              const count = getCountForTab(tab.value, counts);
              const label = renderCount(count, counts);
              // Active uses the existing attention-grabbing pill; All/Archived get a
              // muted inline number so totals are visible without being loud.
              // Zero buckets render nothing — a "0" badge is noise.
              const isActivePill = tab.value === "active" && count !== undefined && count > 0;
              const isMutedNumber = !isActivePill && label !== undefined && count !== undefined && count > 0;
              return (
                <TabsTrigger key={tab.value} value={tab.value}>
                  {tab.label}
                  {isActivePill && (
                    <span className="rounded-full text-white text-xs leading-none px-1.5 py-0.5 bg-primary">{label}</span>
                  )}
                  {isMutedNumber && (
                    <span className="text-xs leading-none text-muted-foreground/60 tabular-nums">{label}</span>
                  )}
                </TabsTrigger>
              );
            })}
          </TabsList>
        </Tabs>
      </div>

      {/* Session list */}
      <div className="flex-1 overflow-y-auto px-2 pt-1 pb-2">
        {/* Ghost "New session" entry when creating */}
        {isNewSession && (
          <Link
            href={`/sessions/new${filterSuffix}`}
            className="block rounded-lg px-3 py-2.5 mb-0.5 bg-background shadow-sm border border-border/50"
          >
            <div className="flex items-center gap-2.5 min-w-0">
              <span className="inline-flex rounded-full h-2 w-2 border border-muted-foreground/30 shrink-0" />
              <p className="text-xs text-muted-foreground/50 italic">
                New session
              </p>
            </div>
          </Link>
        )}

        {(currentFilter === "all" || currentFilter === "active") &&
          visibleOptimistic.map((os) => (
            <OptimisticSessionRow key={os.id} session={os} />
          ))}

        {(!isResolved || isLoading) && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            Loading...
          </div>
        )}

        {isResolved && !isLoading && displayedSessions.length === 0 && showDefaultEmptyState && (
          <div className="space-y-3 px-2 py-3">
            <NoReposWarning showDisconnectedState compact />
            <div className="px-2 py-5 text-center text-xs text-muted-foreground">
              No sessions yet
            </div>
          </div>
        )}

        {isResolved && !isLoading && displayedSessions.length === 0 && !showDefaultEmptyState && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            No sessions match this filter.
          </div>
        )}

        {displayedSessions.map((session) => {
          const isSelected = selectedId === session.id;
          const cfg = statusConfig[session.status] || statusConfig.pending;
          const isWorkingSession = workingSet.has(session.status);
          const hasUnread = isUnread(session);
          const ts = session.completed_at || session.started_at || session.created_at;
          const isArchived = !!session.archived_at;

          return (
            <SwipeActionRow
              key={session.id}
              className="mb-0.5"
              actionLabel={isArchived ? "Unarchive session" : "Archive session"}
              actionText={isArchived ? "Restore" : "Archive"}
              actionIcon={isArchived ? <ArchiveRestore className="h-4 w-4" /> : <Archive className="h-4 w-4" />}
              onAction={() => {
                if (isArchived) {
                  unarchiveMutation.mutate(session.id);
                } else {
                  archiveMutation.mutate(session.id);
                }
              }}
            >
              <Link
                href={`/sessions/${session.id}${filterSuffix}`}
                aria-current={isSelected ? "page" : undefined}
                className={cn(
                  "block rounded-lg border border-border/50 bg-background px-3 py-2.5 shadow-sm transition-all duration-150 md:border-transparent md:bg-muted/30 md:shadow-none",
                  isSelected
                    ? "border-border/60 bg-background shadow-sm md:border-border/60 md:bg-background md:shadow-sm"
                    : "hover:border-border/60 hover:bg-background md:hover:border-transparent md:hover:bg-background/60"
                )}
              >
                <div className="flex items-start gap-2.5 min-w-0">
                  {/* Unread / working indicator */}
                  <div className="mt-1.5 shrink-0">
                    {isWorkingSession ? (
                      <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />
                    ) : hasUnread ? (
                      <StatusDot color="bg-primary" />
                    ) : (
                      <span className="inline-flex rounded-full h-2 w-2" />
                    )}
                  </div>

                {/* Content */}
                <div className="min-w-0 flex-1">
                  <div className="flex items-start justify-between gap-2">
                    <p className={cn(
                      "text-xs font-medium truncate leading-snug",
                      hasUnread || isWorkingSession ? "text-foreground" : "text-muted-foreground"
                    )}>
                      {sessionTitle(session)}
                    </p>
                  </div>
                  <div className="flex items-center justify-between mt-0.5">
                    <div className="flex items-center gap-3 min-w-0">
                      <span className="text-xs text-muted-foreground shrink-0">
                        <span>{cfg.label}</span>
                        {isWorkingSession && <AnimatedEllipsis />}
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
                    <div className="flex items-center gap-1.5 shrink-0">
                      <PRStatusBadge prSummary={session.pr_summary} />
                      <SessionDiffBadge diffStats={session.diff_stats} />
                    </div>
                  </div>
                  {session.status === "failed" && (session.failure_explanation || session.error) && (
                    <p className="text-xs text-destructive/70 truncate mt-0.5">
                      {session.failure_explanation || session.error}
                    </p>
                  )}
                </div>
                </div>
              </Link>
            </SwipeActionRow>
          );
        })}

        {loadMoreMutation.isError && (
          <p className="px-2 py-2 text-center text-xs text-destructive/80">
            Failed to load more. Try again.
          </p>
        )}

        {hasMore && (
          <Button
            variant="ghost"
            size="sm"
            className="mx-2 mt-1 mb-2 h-7 w-[calc(100%-1rem)] text-xs text-muted-foreground hover:text-foreground"
            onClick={() => loadMoreMutation.mutate()}
            disabled={loadMoreMutation.isPending}
          >
            {loadMoreMutation.isPending ? "Loading…" : "Show more"}
          </Button>
        )}
      </div>
    </div>
  );
}
