"use client";

import { useQuery } from "@tanstack/react-query";
import { FolderKanban, Plus } from "lucide-react";
import Link from "next/link";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import { projectStatusConfig } from "@/lib/types";
import type { Project } from "@/lib/types";

const statusFilterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
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

function ProgressBar({ completed, total }: { completed: number; total: number }) {
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-20 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full rounded-full bg-primary transition-all"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-xs text-muted-foreground">
        {completed}/{total}
      </span>
    </div>
  );
}

function ProjectRow({ project }: { project: Project }) {
  const status = projectStatusConfig[project.status] || projectStatusConfig.draft;
  const isActive = project.status === "active";

  return (
    <Link
      href={`/projects/${project.id}`}
      className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
    >
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
            {isActive && (
              <span className="relative mr-1.5 flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
            )}
            {status.label}
          </span>
          <span className="text-sm font-medium text-foreground truncate">
            {project.title}
          </span>
        </div>
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <span className="truncate max-w-[300px]">{project.goal}</span>
          <ProgressBar completed={project.completed_tasks} total={project.total_tasks} />
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {project.priority <= 12 ? "Critical" : project.priority <= 37 ? "High" : project.priority <= 62 ? "Medium" : "Low"}
          </Badge>
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {project.execution_mode}
          </Badge>
          <span>{formatTimeAgo(project.updated_at)}</span>
        </div>
      </div>
    </Link>
  );
}

export function ProjectsPageContent() {
  const [statusFilter, setStatusFilter] = useQueryState("status", parseAsString);

  const { data, isLoading, error } = useQuery({
    queryKey: ["projects", statusFilter],
    queryFn: () => api.projects.list({ status: statusFilter && statusFilter !== "all" ? statusFilter : undefined }),
    refetchInterval: 10000,
  });

  const projects = data?.data ?? [];

  const activeCount = projects.filter((p) => p.status === "active").length;
  const pausedCount = projects.filter((p) => p.status === "paused").length;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Projects"
        description="Multi-task projects managed by the PM agent."
        action={
          <Button size="sm" variant="outline" asChild>
            <Link href="/projects/new">
              <Plus className="mr-2 h-4 w-4" />
              New Project
            </Link>
          </Button>
        }
      />

      <div className="flex items-center gap-1">
        {statusFilterTabs.map((tab) => (
          <Button
            key={tab.value}
            variant={(statusFilter ?? "all") === tab.value ? "default" : "ghost"}
            size="sm"
            className="text-xs"
            onClick={() => setStatusFilter(tab.value === "all" ? null : tab.value)}
          >
            {tab.label}
            {tab.value === "active" && activeCount > 0 && (
              <span className="ml-1.5 rounded-full bg-blue-500 text-white text-[10px] px-1.5 py-0">{activeCount}</span>
            )}
            {tab.value === "paused" && pausedCount > 0 && (
              <span className="ml-1.5 rounded-full bg-orange-500 text-white text-[10px] px-1.5 py-0">{pausedCount}</span>
            )}
          </Button>
        ))}
      </div>

      {isLoading && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading projects...
          </CardContent>
        </Card>
      )}

      {error && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load projects. Make sure the backend is running.
          </CardContent>
        </Card>
      )}

      {!isLoading && !error && projects.length === 0 && (
        <EmptyState
          icon={FolderKanban}
          title="No projects yet"
          description="Projects are multi-task efforts managed by the PM agent or created manually."
          action={{ label: "Create Project", href: "/projects/new" }}
        />
      )}

      {!isLoading && !error && projects.length > 0 && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {projects.length} project{projects.length !== 1 ? "s" : ""}
              </span>
            </div>
            {projects.length === 0 ? (
              <div className="py-8 text-center text-sm text-muted-foreground">
                No projects match this filter.
              </div>
            ) : (
              projects.map((project) => (
                <ProjectRow key={project.id} project={project} />
              ))
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
