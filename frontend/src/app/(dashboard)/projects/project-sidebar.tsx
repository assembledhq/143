"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { notify as toast } from "@/lib/notify";
import { Archive, ArchiveRestore, FolderKanban, Plus, Search } from "lucide-react";
import Link from "next/link";
import { usePathname, useSelectedLayoutSegment } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PeopleFilter } from "@/components/people-filter";
import { SwipeActionRow } from "@/components/swipe-action-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn, formatTimeAgo } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useFilterSuffix, usePeopleFilter } from "@/hooks/use-people-filter";
import { projectStatusConfig, projectStatusDotColor } from "@/lib/types";
import type { Project } from "@/lib/types";
import { hoverSurface, paneSurface, raisedSurface, strongBoundary } from "@/lib/surfaces";
const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "draft", label: "Draft" },
  { value: "completed", label: "Done" },
  { value: "archived", label: "Archived" },
];

function filterProjects(projects: Project[], filter: string | null): Project[] {
  if (!filter || filter === "all" || filter === "archived") return projects;
  return projects.filter((p) => p.status === filter);
}

// ---------------------------------------------------------------------------
// Sidebar component
// ---------------------------------------------------------------------------

export function ProjectSidebar() {
  const pathname = usePathname();
  const selectedSegment = useSelectedLayoutSegment();
  const selectedId = selectedSegment && selectedSegment !== "new" ? selectedSegment : undefined;
  const {
    mode,
    selectedUserIDs,
    scopedUserIDs,
    serializedPeopleParam,
    currentUser,
    isResolved,
    setPeopleFilter,
  } = usePeopleFilter();
  const [searchParam, setSearchParam] = useQueryState("search", parseAsString);
  const [search, setSearch] = useState(searchParam ?? "");
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");
  const queryClient = useQueryClient();
  useEffect(() => {
    const nextSearch = searchParam ?? "";
    // Sync the local input from the URL when navigation/back/forward changes
    // the search param outside this component.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSearch((current) => (current === nextSearch ? current : nextSearch));
  }, [searchParam]);
  useEffect(() => {
    const currentParam = searchParam ?? "";
    if (search === currentParam) {
      return;
    }
    void setSearchParam(search || null);
  }, [search, searchParam, setSearchParam]);

  const canListTeamMembers = currentUser?.role === "admin" || currentUser?.role === "member";
  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: canListTeamMembers,
  });

  const members = useMemo(() => membersData?.data ?? [], [membersData?.data]);

  const { data, isLoading } = useQuery({
    queryKey: ["projects", activeFilter, repo, serializedPeopleParam],
    queryFn: () => api.projects.list({
      limit: 50,
      repository_id: repo ?? undefined,
      created_by_ids: scopedUserIDs,
      ...(activeFilter === "archived" ? { only_archived: true } : {}),
    }),
    enabled: isResolved,
    refetchInterval: 10000,
  });

  const invalidateProjects = () => {
    queryClient.invalidateQueries({ queryKey: ["projects"] });
  };

  const archiveMutation = useMutation({
    mutationFn: (projectId: string) => api.projects.archive(projectId),
    onSuccess: invalidateProjects,
    onError: () => {
      toast.error("Failed to archive project");
    },
  });

  const unarchiveMutation = useMutation({
    mutationFn: (projectId: string) => api.projects.unarchive(projectId),
    onSuccess: invalidateProjects,
    onError: () => {
      toast.error("Failed to unarchive project");
    },
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

  // Carry the sidebar's filters into detail-page links so opening a project
  // doesn't reset the scope back to "Mine".
  const filterSuffix = useFilterSuffix(serializedPeopleParam, activeFilter, repo, search || null);

  const isNewProject = pathname === "/projects/new";
  const showMineEmptyState =
    mode === "mine" &&
    currentFilter === "all" &&
    !search.trim() &&
    displayedProjects.length === 0;
  const canManage = canListTeamMembers;

  return (
    <div className={cn("w-full h-full border-r flex flex-col", paneSurface, strongBoundary)}>
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
          <PeopleFilter
            mode={mode}
            selectedUserIDs={selectedUserIDs}
            members={members}
            currentUser={currentUser}
            onFilterChange={setPeopleFilter}
            className="shrink-0"
          />
        </div>

        {/* New project button */}
        {canManage && (
          <Button asChild variant="outline" className={cn("w-full gap-2 text-xs shadow-sm", raisedSurface)}>
            <Link href="/projects/new">
              <Plus className="h-4 w-4" />
              New project
            </Link>
          </Button>
        )}

        {/* Filter tabs */}
        <Tabs
          value={currentFilter}
          onValueChange={(v) => setActiveFilter(v === "all" ? null : v)}
          className="gap-0"
        >
          <div className="overflow-x-auto overflow-y-hidden pb-1">
            <TabsList variant="line" size="sm">
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
          </div>
        </Tabs>
      </div>

      {/* Project list */}
      <div className="flex-1 overflow-y-auto px-2 pb-2">
        {/* Ghost "New project" entry when creating */}
        {canManage && isNewProject && (
          <Link
            href="/projects/new"
            className="block rounded-lg px-3 py-2.5 mb-0.5 bg-surface-raised shadow-sm border border-border/60"
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
            {allProjects.length === 0 && mode === "all" ? (
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
          const isArchived = !!project.archived_at;

          return (
            <SwipeActionRow
              key={project.id}
              className="mb-0.5"
              actionLabel={isArchived ? "Unarchive project" : "Archive project"}
              actionText={isArchived ? "Restore" : "Archive"}
              desktopActionVisibility="hover"
              actionIcon={isArchived ? <ArchiveRestore className="h-4 w-4" /> : <Archive className="h-4 w-4" />}
              onAction={() => {
                if (isArchived) {
                  return unarchiveMutation.mutateAsync(project.id);
                } else {
                  return archiveMutation.mutateAsync(project.id);
                }
              }}
            >
              <Link
                href={`/projects/${project.id}${filterSuffix}`}
                className={cn(
                  "block rounded-lg px-3 py-2.5 transition-all duration-150",
                  isSelected
                    ? "bg-surface-selected shadow-sm border border-primary/25 ring-1 ring-primary/20"
                    : cn("bg-transparent", hoverSurface)
                )}
              >
                <div className="flex items-start gap-2.5 min-w-0">
                  <div className="mt-1.5 shrink-0">
                    {isActiveProject ? (
                      <StatusDot animate color="bg-blue-500" pingColor="bg-blue-400/60" />
                    ) : (
                      <StatusDot color={projectStatusDotColor[project.status] || "bg-muted-foreground/50"} />
                    )}
                  </div>

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

                    {project.total_tasks > 0 && (
                      <div className="mt-1.5 flex items-center gap-2">
                        <div className="h-1 flex-1 overflow-hidden rounded-full bg-muted">
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
            </SwipeActionRow>
          );
        })}
      </div>
    </div>
  );
}
