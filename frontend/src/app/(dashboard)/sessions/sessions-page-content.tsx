"use client";

import { useQuery } from "@tanstack/react-query";
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
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
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
import { formatTimeAgo, sessionTitle } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { useSessionUserFilter } from "@/hooks/use-session-user-filter";
import { SessionOwnerToggle } from "./session-owner-toggle";
import type { Session, User } from "@/lib/types";

// ---------------------------------------------------------------------------
// Status config
// ---------------------------------------------------------------------------

const statusConfig: Record<string, { dot: string; text: string; bg: string; label: string }> = {
  pending: { dot: "bg-muted-foreground/50", text: "text-muted-foreground", bg: "bg-muted", label: "Pending" },
  running: { dot: "bg-primary", text: "text-primary", bg: "bg-primary/10", label: "Running" },
  idle: { dot: "bg-sky-500", text: "text-sky-700 dark:text-sky-400", bg: "bg-sky-50 dark:bg-sky-950/30", label: "Idle" },
  awaiting_input: { dot: "bg-amber-500", text: "text-amber-700 dark:text-amber-400", bg: "bg-amber-50 dark:bg-amber-950/30", label: "Awaiting input" },
  needs_human_guidance: { dot: "bg-orange-500", text: "text-orange-700 dark:text-orange-400", bg: "bg-orange-50 dark:bg-orange-950/30", label: "Needs guidance" },
  completed: { dot: "bg-emerald-500", text: "text-emerald-700 dark:text-emerald-400", bg: "bg-emerald-50 dark:bg-emerald-950/30", label: "Completed" },
  pr_created: { dot: "bg-violet-500", text: "text-violet-700 dark:text-violet-400", bg: "bg-violet-50 dark:bg-violet-950/30", label: "PR created" },
  failed: { dot: "bg-destructive", text: "text-destructive", bg: "bg-destructive/10", label: "Failed" },
  cancelled: { dot: "bg-muted-foreground/50", text: "text-muted-foreground", bg: "bg-muted", label: "Cancelled" },
  skipped: { dot: "bg-muted-foreground/30", text: "text-muted-foreground", bg: "bg-muted", label: "Skipped" },
};

const filterTabs = [
  { value: "all", label: "All" },
  { value: "needs_attention", label: "Needs attention" },
  { value: "working", label: "Working" },
  { value: "done", label: "Done" },
  { value: "decisions", label: "Decisions" },
];

// Status groups — keep in sync with models.NeedsAttentionStatuses / WorkingStatuses / DoneStatuses.
const needsAttentionStatuses = ["awaiting_input", "needs_human_guidance", "failed"];
const workingStatuses = ["pending", "running"];
const doneStatuses = ["completed", "pr_created", "cancelled", "skipped", "idle"];

const needsAttentionSet = new Set(needsAttentionStatuses);
const workingSet = new Set(workingStatuses);

/** Map a filter tab value to the comma-separated status string for the API. */
function filterToStatusParam(filter: string | null): string | undefined {
  if (!filter || filter === "all" || filter === "decisions") return undefined;
  if (filter === "needs_attention") return needsAttentionStatuses.join(",");
  if (filter === "working") return workingStatuses.join(",");
  if (filter === "done") return doneStatuses.join(",");
  return filter;
}

// ---------------------------------------------------------------------------
// Inline cell components
// ---------------------------------------------------------------------------

function SessionStatusDot({ status }: { status: string }) {
  const working = workingSet.has(status);
  const cfg = statusConfig[status] || statusConfig.pending;
  if (working) {
    return <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />;
  }
  return <StatusDot color={cfg.dot} />;
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
        const status = row.original.status;
        const cfg = statusConfig[status] || statusConfig.pending;
        return (
          <div className="flex items-center gap-2">
            <SessionStatusDot status={status} />
            <span className={`text-xs font-medium ${cfg.text}`}>{cfg.label}</span>
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
            <span className="text-[13px] font-medium text-foreground truncate block max-w-[480px]">
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
      size: 120,
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {row.original.agent_type.replace(/_/g, " ")}
        </span>
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
      id: "confidence",
      accessorKey: "confidence_score",
      header: ({ column }) => <SortableHeader label="Confidence" column={column} />,
      size: 100,
      cell: ({ row }) => {
        const score = row.original.confidence_score;
        if (score == null) return <span className="text-xs text-muted-foreground/40">—</span>;
        const pct = Math.round(score * 100);
        const color = pct >= 80 ? "text-emerald-600 dark:text-emerald-400" : pct >= 50 ? "text-amber-600 dark:text-amber-400" : "text-destructive";
        return <span className={`text-xs font-medium tabular-nums ${color}`}>{pct}%</span>;
      },
    },
    {
      id: "last_modified",
      accessorFn: (row) => row.completed_at || row.started_at || row.created_at,
      header: ({ column }) => <SortableHeader label="Last modified" column={column} />,
      size: 110,
      cell: ({ row }) => {
        const ts = row.original.completed_at || row.original.started_at || row.original.created_at;
        return (
          <span className="text-xs text-muted-foreground tabular-nums">
            {formatTimeAgo(ts)}
          </span>
        );
      },
    },
  ];
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function SessionsPageContent() {
  const router = useRouter();
  const { currentUserFilter, triggeredByUserId, user, setUserFilter } = useSessionUserFilter();
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");
  const [sorting, setSorting] = useState<SortingState>([]);

  const currentFilter = activeFilter ?? "all";
  const showDecisions = currentFilter === "decisions";
  const statusParam = filterToStatusParam(currentFilter);

  // Fetch all sessions (for tab badge counts and the "all" view).
  // We also fetch a filtered query below when a tab is active. The two queries
  // run in parallel on a 10s interval. The "all" query is cheap (limit 50) and
  // needed for accurate badge counts; the filtered query ensures the displayed
  // list reflects the correct server-side results even when total sessions exceed
  // the limit. If polling cost becomes a concern, consider a dedicated
  // /sessions/counts endpoint.
  const { data: allData, isLoading, error } = useQuery({
    queryKey: ["sessions", repo, triggeredByUserId],
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined, triggered_by_user_id: triggeredByUserId }),
    refetchInterval: 10000,
  });

  // Fetch filtered sessions from the backend when a specific tab is selected.
  const { data: filteredData } = useQuery({
    queryKey: ["sessions", repo, statusParam, triggeredByUserId],
    queryFn: () => api.sessions.list({ limit: 50, repository_id: repo ?? undefined, status: statusParam, triggered_by_user_id: triggeredByUserId }),
    refetchInterval: 10000,
    enabled: !!statusParam,
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const allSessions = useMemo(() => allData?.data ?? [], [allData?.data]);
  const members = useMemo(() => membersData?.data ?? [], [membersData?.data]);

  const needsAttentionSessions = allSessions.filter((s) => needsAttentionSet.has(s.status));
  const workingSessions = allSessions.filter((s) => workingSet.has(s.status));

  const filteredSessions = useMemo(
    () => {
      if (showDecisions) return [];
      if (statusParam && filteredData) return filteredData.data;
      return allSessions;
    },
    [allSessions, filteredData, statusParam, showDecisions],
  );

  const columns = useMemo(() => buildColumns(members), [members]);

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

      <PMStatusBanner hasActivePlanSession={workingSessions.length > 0} />

      {/* ── Tab filters ────────────────────────────────────────────── */}
      <div className="flex items-center justify-between border-b border-border">
        <div className="flex items-center gap-0">
          {filterTabs.map((tab) => {
            const isSelected = currentFilter === tab.value;
            const count =
              tab.value === "needs_attention" ? needsAttentionSessions.length
              : tab.value === "working" ? workingSessions.length
              : 0;
            return (
              <button
                key={tab.value}
                className={`relative px-3 py-2.5 text-[13px] font-medium transition-colors duration-150 ${
                  isSelected
                    ? "text-foreground"
                    : "text-muted-foreground hover:text-foreground/80"
                }`}
                onClick={() => setActiveFilter(tab.value === "all" ? null : tab.value)}
              >
                <span className="flex items-center gap-1.5">
                  {tab.label}
                  {count > 0 && (
                    <span className={`rounded-full text-white text-xs leading-none px-1.5 py-0.5 font-normal ${
                      tab.value === "needs_attention" ? "bg-orange-500"
                      : "bg-primary"
                    }`}>{count}</span>
                  )}
                </span>
                {isSelected && (
                  <span className="absolute bottom-0 left-3 right-3 h-0.5 bg-[image:var(--gradient-primary)] rounded-full" />
                )}
              </button>
            );
          })}
        </div>

        {/* User filter toggle */}
        <SessionOwnerToggle
          currentUserFilter={currentUserFilter}
          onFilterChange={setUserFilter}
          className="mr-2"
        />
      </div>

      {/* ── Decisions tab ──────────────────────────────────────────── */}
      {showDecisions && <DecisionsView />}

      {/* ── Loading / error / empty states ─────────────────────────── */}
      {!showDecisions && isLoading && (
        <div className="py-16 text-center text-[13px] text-muted-foreground">
          Loading sessions...
        </div>
      )}

      {!showDecisions && error && (
        <div className="py-16 text-center text-[13px] text-muted-foreground">
          Failed to load sessions. Make sure the backend is running.
        </div>
      )}

      {!showDecisions && !isLoading && !error && allSessions.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
              <CalendarClock className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="mt-4 text-[13px] font-medium text-foreground">No sessions yet</p>
            <p className="mt-1 max-w-sm text-center text-[13px] text-muted-foreground">
              The PM agent runs automatically on a schedule. Click <span className="font-medium text-foreground">Run now</span> above to start it immediately, or create a <span className="font-medium text-foreground">manual session</span> for a specific issue.
            </p>
            <div className="flex items-center gap-2 mt-4">
              <Button variant="outline" size="sm" asChild>
                <Link href="/sessions/new">
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  Manual session
                </Link>
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* ── Data table ─────────────────────────────────────────────── */}
      {!showDecisions && !isLoading && !error && allSessions.length > 0 && (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          {/* Count header */}
          <div className="flex items-center justify-between px-4 py-2.5 border-b border-border/50 bg-muted/30">
            <span className="text-xs font-semibold text-muted-foreground/70 tracking-wider">
              {filteredSessions.length} session{filteredSessions.length !== 1 ? "s" : ""}
            </span>
          </div>

          {filteredSessions.length === 0 ? (
            <div className="py-12 text-center text-[13px] text-muted-foreground">
              No sessions match this filter.
            </div>
          ) : (
            <Table>
              <TableHeader>
                {table.getHeaderGroups().map((headerGroup) => (
                  <TableRow key={headerGroup.id} className="hover:bg-transparent border-border/50">
                    {headerGroup.headers.map((header) => (
                      <TableHead
                        key={header.id}
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
                    onClick={() => router.push(`/sessions/${row.original.id}`)}
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
          )}
        </div>
      )}
    </div>
  );
}
