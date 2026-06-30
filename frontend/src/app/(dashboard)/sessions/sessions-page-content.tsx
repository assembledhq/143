"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from "@tanstack/react-table";
import { ArrowUpDown, CalendarClock, Plus } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { PeopleFilter } from "@/components/people-filter";
import { PMStatusBanner } from "@/components/pm/pm-status-banner";
import { DecisionsView } from "@/components/pm/decisions-view";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { formatTimeAgo, sessionTitle } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { AnimatedEllipsis } from "@/components/animated-ellipsis";
import { AgentBadge } from "@/components/agent-badge";
import { usePeopleFilter } from "@/hooks/use-people-filter";
import { provisionalSessionDetailFromListItem } from "@/lib/session-detail-cache";
import { preloadSessionDetailContent } from "./[id]/session-detail-page-client";
import { deriveSessionDisplayStatus, type SessionDisplayStatus } from "@/lib/session-display-status";
import type { Session, SessionDetail, SessionListItem, SingleResponse, User } from "@/lib/types";
import {
  filterToStatusParam as baseFilterToStatusParam,
} from "@/lib/session-status-groups";
import { getCountForTab, renderCount } from "@/lib/session-counts";

const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "decisions", label: "Decisions" },
];

/** Wrapper that also passes through "decisions" as a client-only filter. */
function filterToStatusParam(filter: string | null): string | undefined {
  return baseFilterToStatusParam(filter, ["decisions"]);
}

// ---------------------------------------------------------------------------
// Inline cell components
// ---------------------------------------------------------------------------

function SessionStatusDot({ displayStatus }: { displayStatus: SessionDisplayStatus }) {
  if (displayStatus.animated) {
    return <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />;
  }
  return <StatusDot color={displayStatus.dotClass} />;
}

function SortableHeader({ label, column }: { label: string; column: { toggleSorting: (desc?: boolean) => void; getIsSorted: () => false | "asc" | "desc" } }) {
  return (
    <button
      className="flex items-center gap-1 hover:text-foreground transition-colors -ml-1 px-1 py-0.5 rounded"
      onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
    >
      {label}
      <ArrowUpDown className="h-3 w-3 opacity-50" />
    </button>
  );
}

// ---------------------------------------------------------------------------
// Column definitions (members is captured via closure in the component)
// ---------------------------------------------------------------------------

function buildColumns(members: User[]): ColumnDef<Session>[] {
  return [
    {
      id: "status",
      accessorKey: "status",
      header: ({ column }) => <SortableHeader label="Status" column={column} />,
      size: 140,
      cell: ({ row }) => {
        const displayStatus = deriveSessionDisplayStatus(row.original);
        return (
          <div className="flex items-center gap-2">
            <SessionStatusDot displayStatus={displayStatus} />
            <span className={`text-xs font-medium ${displayStatus.textClass}`}>
              <span>{displayStatus.label}</span>
              {displayStatus.animated && <AnimatedEllipsis />}
            </span>
          </div>
        );
      },
    },
    {
      id: "title",
      accessorFn: (row) => sessionTitle(row),
      header: "Session",
      size: 999,
      cell: ({ row }) => {
        const session = row.original;
        const failed = session.status === "failed";
        return (
          <div className="min-w-0">
            <span className="text-xs font-medium text-foreground truncate block max-w-[480px]">
              {sessionTitle(session)}
            </span>
            {failed && (session.failure_explanation || session.error) && (
              <span className="text-xs text-destructive/80 truncate block max-w-[480px] mt-0.5">
                {session.failure_explanation || session.error}
              </span>
            )}
          </div>
        );
      },
    },
    {
      id: "agent_type",
      accessorKey: "agent_type",
      header: ({ column }) => <SortableHeader label="Agent" column={column} />,
      size: 140,
      cell: ({ row }) => (
        <AgentBadge agentType={row.original.agent_type} />
      ),
    },
    {
      id: "triggered_by",
      accessorKey: "triggered_by_user_id",
      header: "Triggered by",
      size: 120,
      cell: ({ row }) => {
        const userId = row.original.triggered_by_user_id;
        if (!userId) return <span className="text-xs text-muted-foreground/40">—</span>;
        const user = members.find((m) => m.id === userId);
        return (
          <span className="text-xs text-muted-foreground truncate block max-w-[100px]">
            {user ? user.name.split(" ")[0] : "Unknown"}
          </span>
        );
      },
    },
    {
      id: "last_modified",
      accessorKey: "last_activity_at",
      header: ({ column }) => <SortableHeader label="Last modified" column={column} />,
      size: 110,
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground tabular-nums">
          {formatTimeAgo(row.original.last_activity_at)}
        </span>
      ),
    },
  ];
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SessionsPageContent() {
  const router = useRouter();
  const queryClient = useQueryClient();

  // Seeding the detail cache with the list item lets the session detail view
  // render its header/skeleton instantly instead of waiting on the API fetch.
  const seedSessionDetailCache = useCallback((session: Session) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(
      queryKeys.sessions.detail(session.id),
      (current) => current ?? provisionalSessionDetailFromListItem(session),
    );
  }, [queryClient]);

  const navigateToSession = useCallback((session: Session) => {
    seedSessionDetailCache(session);
    router.push(`/sessions/${session.id}`);
  }, [router, seedSessionDetailCache]);
  const {
    mode,
    selectedUserIDs,
    scopedUserIDs,
    serializedPeopleParam,
    currentUser,
    isResolved,
    setPeopleFilter,
  } = usePeopleFilter();
  const canListTeamMembers = currentUser?.role === "admin" || currentUser?.role === "member";
  const canCreateSession = currentUser?.role !== "viewer";
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");
  const [sorting, setSorting] = useState<SortingState>([]);
  const tabsRef = useRef<HTMLDivElement>(null);
  const [tabsOverflow, setTabsOverflow] = useState(false);

  const checkOverflow = useCallback(() => {
    const el = tabsRef.current;
    if (el) setTabsOverflow(el.scrollWidth > el.clientWidth);
  }, []);

  useEffect(() => {
    checkOverflow();
    window.addEventListener("resize", checkOverflow);
    return () => window.removeEventListener("resize", checkOverflow);
  }, [checkOverflow]);

  const currentFilter = activeFilter ?? "all";
  const showDecisions = currentFilter === "decisions";
  const statusParam = filterToStatusParam(currentFilter);

  // Pagination state. Once the user clicks "Show more" we stop polling entirely
  // (page 0 included) — refreshing page 0 while extra pages are held locally
  // would invalidate the cursor that produced them. See automations/[id]/page.tsx.
  const [extraPages, setExtraPages] = useState<SessionListItem[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>(undefined);
  const isPaginated = extraPages.length > 0;

  // Pause list polling while the pointer is over the table. A poll response can
  // reorder rows mid-click, which either swallows the click or swaps a
  // different session under the cursor right before navigation.
  const [isTableHovered, setIsTableHovered] = useState(false);

  // Reset pagination when the effective query scope changes. Adjusting state
  // during render (rather than in an effect) avoids cascading renders.
  const scopeKey = `${repo ?? ""}|${serializedPeopleParam ?? "mine"}|${currentFilter}`;
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
      ...(statusParam ? { status: statusParam } : {}),
    }),
    [repo, scopedUserIDs, statusParam],
  );

  const { data: listData, isLoading, error } = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), "filtered", currentFilter, serializedPeopleParam],
    queryFn: () => api.sessions.list(listParams),
    refetchInterval: isPaginated || showDecisions || isTableHovered ? false : 10000,
    enabled: !showDecisions && isResolved,
  });

  // Tab badge counts.
  const { data: countsData } = useQuery({
    queryKey: queryKeys.sessions.counts(repo, serializedPeopleParam),
    queryFn: () =>
      api.sessions.counts({
        repository_id: repo ?? undefined,
        triggered_by_user_ids: scopedUserIDs,
      }),
    refetchInterval: showDecisions ? false : 10000,
    enabled: !showDecisions && isResolved,
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: canListTeamMembers,
  });

  const members = useMemo(() => membersData?.data ?? [], [membersData?.data]);
  const isPendingScope = !showDecisions && (!isResolved || isLoading);

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

  const filteredSessions = useMemo(() => {
    if (showDecisions) return [];
    return [firstPage, ...extraPages].flat();
  }, [firstPage, extraPages, showDecisions]);

  const counts = countsData?.data;
  const workingCount = counts?.active ?? 0;

  // Total for the currently-visible tab (used for "Showing N of M" footer).
  // Falls back to loaded length when counts haven't arrived yet or when the cap
  // is hit (the cap is represented by "M+" via renderCount).
  const currentTabTotal = useMemo(() => {
    if (!counts) return undefined;
    if (currentFilter === "active") return counts.active;
    return counts.all;
  }, [counts, currentFilter]);

  const columns = useMemo(() => buildColumns(members), [members]);

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Table exposes unstable functions by design; this component does not memoize the table instance across boundaries.
  const table = useReactTable({
    data: filteredSessions,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        description="Each agent execution creates a session."
      />

      <PMStatusBanner hasActivePlanSession={workingCount > 0} canMutate={canCreateSession} />

      {/* ── Tab filters ────────────────────────────────────────────── */}
      <div className="relative flex items-center border-b border-border">
        <div ref={tabsRef} className={`flex flex-nowrap items-center overflow-x-auto overflow-y-hidden scrollbar-hide min-w-0 ${tabsOverflow ? "mask-fade-r" : ""}`}>
          {filterTabs.map((tab) => {
            const isSelected = currentFilter === tab.value;
            const count = getCountForTab(tab.value, counts);
            const label = renderCount(count, counts);
            // Active uses the existing attention-grabbing pill; All gets a muted
            // inline number. Decisions is a client-only view with no count.
            // Zero buckets render nothing — a "0" badge is noise.
            const isActivePill = tab.value === "active" && count !== undefined && count > 0;
            const isMutedNumber = !isActivePill && label !== undefined && count !== undefined && count > 0;
            return (
              <button
                key={tab.value}
                className={`relative shrink-0 px-2.5 py-2.5 text-xs font-medium transition-colors duration-150 ${
                  isSelected
                    ? "text-foreground"
                    : "text-muted-foreground hover:text-foreground/80"
                }`}
                onClick={() => setActiveFilter(tab.value === "all" ? null : tab.value)}
              >
                <span className="flex items-center gap-1.5">
                  {tab.label}
                  {isActivePill && (
                    <span className="rounded-full text-white text-xs leading-none px-1.5 py-0.5 font-normal bg-primary">{label}</span>
                  )}
                  {isMutedNumber && (
                    <span className="text-xs leading-none text-muted-foreground/60 tabular-nums">{label}</span>
                  )}
                </span>
                {isSelected && (
                  <span className="absolute bottom-0 left-2.5 right-2.5 h-0.5 bg-[image:var(--gradient-primary)] rounded-full" />
                )}
              </button>
            );
          })}
        </div>

        {/* User filter toggle */}
        <PeopleFilter
          mode={mode}
          selectedUserIDs={selectedUserIDs}
          members={members}
          currentUser={currentUser}
          onFilterChange={setPeopleFilter}
          className="ml-auto shrink-0 mr-2"
        />
      </div>

      {/* ── Decisions tab ──────────────────────────────────────────── */}
      {showDecisions && <DecisionsView />}

      {/* ── Loading / error / empty states ─────────────────────────── */}
      {isPendingScope && (
        <div className="py-16 text-center text-xs text-muted-foreground">
          Loading sessions...
        </div>
      )}

      {!isPendingScope && !showDecisions && error && (
        <div className="py-16 text-center text-xs text-muted-foreground">
          Failed to load sessions. Make sure the backend is running.
        </div>
      )}

      {!isPendingScope && !showDecisions && !error && counts?.all === 0 && (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
              <CalendarClock className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="mt-4 text-xs font-medium text-foreground">No sessions yet</p>
            <p className="mt-1 max-w-sm text-center text-xs text-muted-foreground">
              The PM agent runs automatically on a schedule. Click <span className="font-medium text-foreground">Run now</span> above to start it immediately, or create a <span className="font-medium text-foreground">manual session</span> for a specific issue.
            </p>
            {canCreateSession && (
              <div className="flex items-center gap-2 mt-4">
                <Button variant="outline" size="sm" asChild>
                  <Link href="/sessions/new">
                    <Plus className="mr-1.5 h-3.5 w-3.5" />
                    Manual session
                  </Link>
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* ── Data table ─────────────────────────────────────────────── */}
      {!isPendingScope && !showDecisions && !error && counts?.all !== 0 && (
        <div
          className="rounded-lg border border-border bg-card overflow-hidden"
          onPointerEnter={() => setIsTableHovered(true)}
          onPointerLeave={() => setIsTableHovered(false)}
        >
          {filteredSessions.length === 0 ? (
            <div className="py-12 text-center text-xs text-muted-foreground">
              No sessions match this filter.
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  {table.getHeaderGroups().map((headerGroup) => (
                    <TableRow key={headerGroup.id} className="hover:bg-transparent border-border/50">
                      {headerGroup.headers.map((header) => (
                        <TableHead
                          key={header.id}
                          className="uppercase"
                          style={{ width: header.column.getSize() !== 150 ? header.column.getSize() : undefined }}
                        >
                          {header.isPlaceholder
                            ? null
                            : flexRender(header.column.columnDef.header, header.getContext())}
                        </TableHead>
                      ))}
                    </TableRow>
                  ))}
                </TableHeader>
                <TableBody>
                  {table.getRowModel().rows.map((row) => (
                    <TableRow
                      key={row.id}
                      className="cursor-pointer"
                      // Prefetch the route and warm the detail view's dynamic
                      // chunk on hover, and seed the detail cache on mousedown,
                      // so the click navigates instantly to a seeded skeleton
                      // instead of silently waiting on a server fetch.
                      onMouseEnter={() => {
                        router.prefetch(`/sessions/${row.original.id}`);
                        preloadSessionDetailContent();
                      }}
                      onMouseDown={() => seedSessionDetailCache(row.original)}
                      onClick={() => navigateToSession(row.original)}
                    >
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              {/* Footer: "Showing N of M · Show more" */}
              <div className="flex items-center justify-between gap-3 px-4 py-2.5 border-t border-border/50 bg-muted/20">
                <span className="text-xs text-muted-foreground/70 tabular-nums">
                  {currentTabTotal !== undefined
                    ? `Showing ${filteredSessions.length} of ${renderCount(currentTabTotal, counts)}`
                    : `${filteredSessions.length} session${filteredSessions.length !== 1 ? "s" : ""}`}
                </span>
                <div className="flex items-center gap-3">
                  {loadMoreMutation.isError && (
                    <span className="text-xs text-destructive/80">
                      Failed to load more. Try again.
                    </span>
                  )}
                  {hasMore && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs text-muted-foreground hover:text-foreground"
                      onClick={() => loadMoreMutation.mutate()}
                      disabled={loadMoreMutation.isPending}
                    >
                      {loadMoreMutation.isPending ? "Loading…" : "Show more"}
                    </Button>
                  )}
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}
