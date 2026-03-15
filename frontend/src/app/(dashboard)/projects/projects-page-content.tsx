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
import { ArrowUpDown, FolderKanban, Plus, Timer } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import { projectStatusConfig } from "@/lib/types";
import type { Project } from "@/lib/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const statusFilterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "scheduled", label: "Scheduled" },
  { value: "draft", label: "Draft" },
  { value: "completed", label: "Completed" },
  { value: "paused", label: "Paused" },
];

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

function priorityLabel(priority: number): { label: string; color: string } {
  if (priority <= 12) return { label: "Critical", color: "text-red-600 dark:text-red-400" };
  if (priority <= 37) return { label: "High", color: "text-orange-600 dark:text-orange-400" };
  if (priority <= 62) return { label: "Medium", color: "text-amber-600 dark:text-amber-400" };
  return { label: "Low", color: "text-muted-foreground" };
}

// ---------------------------------------------------------------------------
// Inline cell components
// ---------------------------------------------------------------------------

function StatusDot({ status }: { status: string }) {
  const isActive = status === "active";
  const cfg = projectStatusConfig[status] || projectStatusConfig.draft;

  if (isActive) {
    return (
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
      </span>
    );
  }

  // Derive dot color from the config color string
  const dotColorMap: Record<string, string> = {
    proposed: "bg-purple-500",
    draft: "bg-muted-foreground/50",
    planning: "bg-yellow-500",
    active: "bg-blue-500",
    paused: "bg-orange-500",
    completed: "bg-emerald-500",
    cancelled: "bg-red-500",
  };

  return <span className={`inline-flex rounded-full h-2 w-2 ${dotColorMap[status] || "bg-muted-foreground/50"}`} />;
}

function ProgressBar({ completed, total }: { completed: number; total: number }) {
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-16 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full rounded-full bg-[image:var(--gradient-primary)] transition-all"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-[12px] text-muted-foreground tabular-nums">
        {completed}/{total}
      </span>
    </div>
  );
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
// Column definitions
// ---------------------------------------------------------------------------

const columns: ColumnDef<Project>[] = [
  {
    id: "status",
    accessorKey: "status",
    header: ({ column }) => <SortableHeader label="Status" column={column} />,
    size: 130,
    cell: ({ row }) => {
      const status = row.original.status;
      const cfg = projectStatusConfig[status] || projectStatusConfig.draft;
      return (
        <div className="flex items-center gap-2">
          <StatusDot status={status} />
          <span className={`text-[12px] font-medium ${cfg.color.split(" ").filter(c => c.startsWith("text-")).join(" ")}`}>
            {cfg.label}
          </span>
        </div>
      );
    },
  },
  {
    id: "title",
    accessorKey: "title",
    header: "Project",
    size: 999,
    cell: ({ row }) => {
      const project = row.original;
      return (
        <div className="min-w-0">
          <span className="text-[13px] font-medium text-foreground truncate block max-w-[400px]">
            {project.title}
          </span>
          {project.goal && (
            <span className="text-[11px] text-muted-foreground truncate block max-w-[400px] mt-0.5">
              {project.goal}
            </span>
          )}
        </div>
      );
    },
  },
  {
    id: "progress",
    accessorFn: (row) => (row.total_tasks > 0 ? row.completed_tasks / row.total_tasks : 0),
    header: ({ column }) => <SortableHeader label="Progress" column={column} />,
    size: 130,
    cell: ({ row }) => (
      <ProgressBar completed={row.original.completed_tasks} total={row.original.total_tasks} />
    ),
  },
  {
    id: "priority",
    accessorKey: "priority",
    header: ({ column }) => <SortableHeader label="Priority" column={column} />,
    size: 90,
    cell: ({ row }) => {
      const p = priorityLabel(row.original.priority);
      return <span className={`text-[12px] font-medium ${p.color}`}>{p.label}</span>;
    },
  },
  {
    id: "schedule",
    accessorFn: (row) => row.schedule_enabled,
    header: "Schedule",
    size: 90,
    cell: ({ row }) => {
      const project = row.original;
      if (!project.schedule_enabled) {
        return <span className="text-[12px] text-muted-foreground/40">—</span>;
      }
      return (
        <span className="inline-flex items-center gap-1 text-[12px] text-muted-foreground">
          <Timer className="h-3 w-3" />
          {project.schedule_interval}{project.schedule_unit.charAt(0)}
        </span>
      );
    },
  },
  {
    id: "last_modified",
    accessorKey: "updated_at",
    header: ({ column }) => <SortableHeader label="Last modified" column={column} />,
    size: 110,
    cell: ({ row }) => (
      <span className="text-[12px] text-muted-foreground tabular-nums">
        {formatTimeAgo(row.original.updated_at)}
      </span>
    ),
  },
];

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function ProjectsPageContent() {
  const router = useRouter();
  const [statusFilter, setStatusFilter] = useQueryState("status", parseAsString);
  const [sorting, setSorting] = useState<SortingState>([]);

  const isScheduledFilter = statusFilter === "scheduled";
  const apiStatus = statusFilter && statusFilter !== "all" && !isScheduledFilter ? statusFilter : undefined;

  const { data, isLoading, error } = useQuery({
    queryKey: ["projects", apiStatus],
    queryFn: () => api.projects.list({ status: apiStatus }),
    refetchInterval: 10000,
  });

  const allProjects = data?.data ?? [];
  const projects = useMemo(
    () => isScheduledFilter ? allProjects.filter((p) => p.schedule_enabled) : allProjects,
    [allProjects, isScheduledFilter],
  );

  const activeCount = allProjects.filter((p) => p.status === "active").length;
  const pausedCount = allProjects.filter((p) => p.status === "paused").length;
  const scheduledCount = allProjects.filter((p) => p.schedule_enabled).length;

  const table = useReactTable({
    data: projects,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  return (
    <div className="space-y-5">
      <PageHeader
        title="Projects"
        description="Multi-task projects managed by the PM agent."
        action={
          <Button size="sm" variant="outline" asChild>
            <Link href="/projects/new">
              <Plus className="mr-2 h-4 w-4" />
              New project
            </Link>
          </Button>
        }
      />

      {/* ── Tab filters ────────────────────────────────────────────── */}
      <div className="flex items-center gap-0 border-b border-border">
        {statusFilterTabs.map((tab) => {
          const isSelected = (statusFilter ?? "all") === tab.value;
          const count =
            tab.value === "active" ? activeCount
            : tab.value === "scheduled" ? scheduledCount
            : tab.value === "paused" ? pausedCount
            : 0;
          return (
            <button
              key={tab.value}
              className={`relative px-3 py-2.5 text-[13px] font-medium transition-colors duration-150 ${
                isSelected
                  ? "text-foreground"
                  : "text-muted-foreground hover:text-foreground/80"
              }`}
              onClick={() => setStatusFilter(tab.value === "all" ? null : tab.value)}
            >
              <span className="flex items-center gap-1.5">
                {tab.label}
                {count > 0 && (
                  <span className={`rounded-full text-white text-[10px] leading-none px-1.5 py-0.5 font-normal ${
                    tab.value === "active" ? "bg-primary"
                    : tab.value === "scheduled" ? "bg-purple-500"
                    : tab.value === "paused" ? "bg-orange-500"
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

      {/* ── Loading / error / empty ────────────────────────────────── */}
      {isLoading && (
        <div className="py-16 text-center text-[13px] text-muted-foreground">
          Loading projects...
        </div>
      )}

      {error && (
        <div className="py-16 text-center text-[13px] text-muted-foreground">
          Failed to load projects. Make sure the backend is running.
        </div>
      )}

      {!isLoading && !error && projects.length === 0 && !statusFilter && (
        <EmptyState
          icon={FolderKanban}
          title="No projects yet"
          description="Projects are multi-task efforts managed by the PM agent or created manually."
          action={{ label: "Create project", href: "/projects/new" }}
        />
      )}

      {!isLoading && !error && projects.length === 0 && statusFilter && (
        <div className="py-12 text-center text-[13px] text-muted-foreground">
          No projects match this filter.
        </div>
      )}

      {/* ── Data table ─────────────────────────────────────────────── */}
      {!isLoading && !error && projects.length > 0 && (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          {/* Count header */}
          <div className="flex items-center justify-between px-4 py-2.5 border-b border-border/50 bg-muted/30">
            <span className="text-[11px] font-semibold text-muted-foreground/70 uppercase tracking-widest">
              {projects.length} project{projects.length !== 1 ? "s" : ""}
            </span>
          </div>

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
                  onClick={() => router.push(`/projects/${row.original.id}`)}
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
        </div>
      )}
    </div>
  );
}
