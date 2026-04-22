"use client";

import { useQuery } from "@tanstack/react-query";
import { FolderKanban, Plus, Search } from "lucide-react";
import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { OwnerScopeToggle } from "@/components/owner-scope-toggle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn, formatTimeAgo } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useProjectUserFilter } from "@/hooks/use-project-user-filter";
import { projectStatusConfig, projectStatusDotColor } from "@/lib/types";
import type { Project } from "@/lib/types";
const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "draft", label: "Draft" },
  { value: "completed", label: "Done" },
];

function filterProjects(projects: Project[], filter: string | null): Project[] {
  if (!filter || filter === "all") return projects;
  return projects.filter((p) => p.status === filter);
}

// ---------------------------------------------------------------------------
// Sidebar component
// ---------------------------------------------------------------------------

export function ProjectSidebar() {
  const params = useParams();
  const pathname = usePathname();
  const selectedId = params?.id as string | undefined;
  const { currentUserFilter, createdByUserId, isResolved, setUserFilter } = useProjectUserFilter();
  const [search, setSearch] = useState("");
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");

  const { data, isLoading } = useQuery({
    queryKey: ["projects", activeFilter, repo, currentUserFilter, createdByUserId],
    queryFn: () => api.projects.list({ limit: 50, repository_id: repo ?? undefined, created_by: createdByUserId }),
    enabled: isResolved,
    refetchInterval: 10000,
  });

  const allProjects = useMemo(() => data?.data ?? [], [data?.data]);
  const currentFilter = activeFilter ?? "all";

  const activeCount = useMemo(
    () => allProjects.filter((p) => p.status === "active").length,
    [allProjects],
  );

  const filteredProjects = useMemo(
    () => filterProjects(allProjects, activeFilter),
    [allProjects, activeFilter],
  );

  const displayedProjects = useMemo(() => {
    if (!search.trim()) return filteredProjects;
    const q = search.toLowerCase();
    return filteredProjects.filter(
      (p) =>
        p.title.toLowerCase().includes(q) ||
        p.goal.toLowerCase().includes(q),
    );
  }, [filteredProjects, search]);

  const isNewProject = pathname === "/projects/new";
  const showMineEmptyState =
    currentUserFilter === "mine" &&
    currentFilter === "all" &&
    !search.trim() &&
    displayedProjects.length === 0;

  return (
    <div className="w-full h-full border-r border-border bg-muted/30 flex flex-col">
      {/* Header */}
      <div className="px-4 pt-3 pb-3 space-y-3">
        <div className="flex items-center gap-2">
          <div className="relative flex-1 min-w-0">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/50" />
          <Input
            placeholder="Search projects..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 pl-8 text-xs"
          />
        </div>
          <OwnerScopeToggle
            currentUserFilter={currentUserFilter}
            onFilterChange={setUserFilter}
            className="shrink-0"
          />
        </div>

        {/* New project button */}
        <Button asChild variant="outline" className="w-full gap-2 bg-background text-xs shadow-sm">
          <Link href="/projects/new">
            <Plus className="h-4 w-4" />
            New project
          </Link>
        </Button>

        {/* Filter tabs */}
        <Tabs
          value={currentFilter}
          onValueChange={(v) => setActiveFilter(v === "all" ? null : v)}
          className="gap-0"
        >
          <TabsList size="sm" className="overflow-x-auto overflow-y-hidden">
            {filterTabs.map((tab) => {
              const count = tab.value === "active" ? activeCount : 0;
              return (
                <TabsTrigger key={tab.value} value={tab.value}>
                  {tab.label}
                  {count > 0 && (
                    <span className="rounded-full text-white text-xs leading-none px-1.5 py-0.5 bg-primary">
                      {count}
                    </span>
                  )}
                </TabsTrigger>
              );
            })}
          </TabsList>
        </Tabs>
      </div>

      {/* Project list */}
      <div className="flex-1 overflow-y-auto px-2 pb-2">
        {/* Ghost "New project" entry when creating */}
        {isNewProject && (
          <Link
            href="/projects/new"
            className="block rounded-lg px-3 py-2.5 mb-0.5 bg-background shadow-sm border border-border/50"
          >
            <div className="flex items-center gap-2.5 min-w-0">
              <span className="inline-flex rounded-full h-2 w-2 border border-muted-foreground/30 shrink-0" />
              <p className="text-xs text-muted-foreground/50 italic">
                New project
              </p>
            </div>
          </Link>
        )}

        {(!isResolved || isLoading) && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            Loading...
          </div>
        )}

        {isResolved && !isLoading && displayedProjects.length === 0 && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            {allProjects.length === 0 && currentUserFilter === "all" ? (
              <div className="space-y-2">
                <FolderKanban className="h-5 w-5 mx-auto text-muted-foreground/40" />
                <p>No projects yet</p>
              </div>
            ) : showMineEmptyState ? (
              <div className="space-y-2">
                <FolderKanban className="h-5 w-5 mx-auto text-muted-foreground/40" />
                <p className="text-foreground">No projects in Mine yet</p>
                <p>Switch to Everyone to browse team projects, or create a new one.</p>
              </div>
            ) : (
              "No projects match this filter."
            )}
          </div>
        )}

        {displayedProjects.map((project) => {
          const isSelected = selectedId === project.id;
          const cfg = projectStatusConfig[project.status] || projectStatusConfig.draft;
          const isActiveProject = project.status === "active";
          const ts = project.completed_at || project.updated_at;
          const pct = project.total_tasks > 0
            ? Math.round((project.completed_tasks / project.total_tasks) * 100)
            : 0;

          return (
            <Link
              key={project.id}
              href={`/projects/${project.id}`}
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
                  {isActiveProject ? (
                    <StatusDot animate color="bg-blue-500" pingColor="bg-blue-400/60" />
                  ) : (
                    <StatusDot color={projectStatusDotColor[project.status] || "bg-muted-foreground/50"} />
                  )}
                </div>

                {/* Content */}
                <div className="min-w-0 flex-1">
                  <p className="text-xs font-medium text-foreground truncate leading-snug">
                    {project.title}
                  </p>

                  <div className="flex items-center gap-3 mt-0.5">
                    <span className="text-xs text-muted-foreground shrink-0">
                      {cfg.label}
                    </span>
                    <span className="text-xs text-muted-foreground/50 truncate">
                      {formatTimeAgo(ts)}
                    </span>
                  </div>

                  {/* Mini progress bar */}
                  {project.total_tasks > 0 && (
                    <div className="flex items-center gap-2 mt-1.5">
                      <div className="h-1 flex-1 rounded-full bg-muted overflow-hidden">
                        <div
                          className="h-full rounded-full bg-[image:var(--gradient-primary)] transition-all"
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                      <span className="text-xs text-muted-foreground/60 tabular-nums">
                        {project.completed_tasks}/{project.total_tasks}
                      </span>
                    </div>
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
