"use client";

import { useQuery } from "@tanstack/react-query";
import { CalendarClock, Layers, Wrench, FolderKanban } from "lucide-react";
import Link from "next/link";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { PMStatusBanner } from "@/components/pm/pm-status-banner";
import { DecisionsView } from "@/components/pm/decisions-view";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { AgentSession, Project } from "@/lib/types";

const sessionStatusConfig: Record<string, { dot: string; text: string; bg: string; label: string }> = {
  active: { dot: "bg-blue-500", text: "text-blue-700", bg: "bg-blue-50", label: "Active" },
  completed: { dot: "bg-emerald-500", text: "text-emerald-700", bg: "bg-emerald-50", label: "Completed" },
  failed: { dot: "bg-red-500", text: "text-red-700", bg: "bg-red-50", label: "Failed" },
};

const triggeredByLabels: Record<string, string> = {
  scheduled: "Scheduled",
  manual: "Manual",
  fix_this: "Fix This",
};

const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "completed", label: "Completed" },
  { value: "failed", label: "Failed" },
  { value: "decisions", label: "Decisions" },
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

function filterSessions(sessions: AgentSession[], filter: string | null): AgentSession[] {
  if (!filter || filter === "all") return sessions;
  return sessions.filter((s) => s.status === filter);
}

function SessionRow({ session }: { session: AgentSession }) {
  const status = sessionStatusConfig[session.status] || sessionStatusConfig.active;
  const isActive = session.status === "active";

  return (
    <Link
      href={`/sessions/${session.id}`}
      className="group flex items-center gap-4 py-3 px-4 hover:bg-muted/50 transition-colors cursor-pointer"
    >
      {/* Status dot */}
      <div className="flex-shrink-0">
        {isActive ? (
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
          </span>
        ) : (
          <span className={`inline-flex rounded-full h-2 w-2 ${status.dot}`} />
        )}
      </div>

      {/* Main content */}
      <div className="flex-1 min-w-0">
        <span className="text-[13px] font-medium text-foreground truncate block">
          {session.title}
        </span>
      </div>

      {/* Metadata pills */}
      <div className="flex items-center gap-2 flex-shrink-0">
        <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground bg-muted rounded-md px-2 py-0.5">
          {session.type === "plan" ? (
            <><Layers className="h-3 w-3" />PM Analysis</>
          ) : (
            <><Wrench className="h-3 w-3" />Manual</>
          )}
        </span>
        <span className="text-[11px] text-muted-foreground">
          {triggeredByLabels[session.triggered_by] || session.triggered_by}
        </span>
      </div>

      {/* Stats */}
      <div className="flex items-center gap-3 flex-shrink-0 text-[11px] tabular-nums">
        <span className="text-muted-foreground">{session.task_count} task{session.task_count !== 1 ? "s" : ""}</span>
        {session.active_run_count > 0 && (
          <span className="text-blue-600 font-medium">{session.active_run_count} running</span>
        )}
        {session.completed_run_count > 0 && (
          <span className="text-emerald-600">{session.completed_run_count} done</span>
        )}
        {session.failed_run_count > 0 && (
          <span className="text-red-500">{session.failed_run_count} failed</span>
        )}
      </div>

      {/* Timestamp */}
      <span className="text-[11px] text-muted-foreground flex-shrink-0 w-16 text-right">
        {formatTimeAgo(session.created_at)}
      </span>
    </Link>
  );
}

function SessionSection({ title, sessions, badge }: {
  title: string;
  sessions: AgentSession[];
  badge?: React.ReactNode;
}) {
  if (sessions.length === 0) return null;
  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border bg-muted/30">
          <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
            {title}
          </span>
          {badge}
          <span className="text-xs text-muted-foreground">({sessions.length})</span>
        </div>
        <div className="divide-y divide-border">
          {sessions.map((session) => (
            <SessionRow key={session.id} session={session} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function ProjectGroup({ project, sessions }: { project: Project; sessions: AgentSession[] }) {
  if (sessions.length === 0) return null;

  const statusDot = project.status === "active" ? "bg-blue-500"
    : project.status === "completed" ? "bg-emerald-500"
    : "bg-muted-foreground";

  return (
    <div>
      <div className="flex items-center justify-between px-4 py-2">
        <div className="flex items-center gap-2">
          <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
          <Link
            href={`/projects/${project.id}`}
            className="text-[11px] font-semibold text-muted-foreground hover:text-foreground transition-colors"
          >
            {project.title}
          </Link>
          <span className={`inline-flex rounded-full h-1.5 w-1.5 ${statusDot}`} />
        </div>
        <div className="flex items-center gap-3 text-[11px] text-muted-foreground">
          <span>{project.total_tasks} task{project.total_tasks !== 1 ? "s" : ""}</span>
          {project.completed_tasks > 0 && (
            <span className="text-emerald-500">{project.completed_tasks} done</span>
          )}
          <span>{sessions.length} session{sessions.length !== 1 ? "s" : ""}</span>
        </div>
      </div>
      <Card>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            {sessions.map((session) => (
              <SessionRow key={session.id} session={session} />
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function groupSessionsByProject(sessions: AgentSession[], projects: Project[]) {
  const projectMap = new Map<string, Project>();
  for (const p of projects) {
    projectMap.set(p.id, p);
  }

  const grouped = new Map<string, AgentSession[]>();
  const ungrouped: AgentSession[] = [];

  for (const session of sessions) {
    if (session.project_id && projectMap.has(session.project_id)) {
      const list = grouped.get(session.project_id) ?? [];
      list.push(session);
      grouped.set(session.project_id, list);
    } else {
      ungrouped.push(session);
    }
  }

  // Sort project groups: active projects first, then by most recent session
  const sortedGroups = Array.from(grouped.entries())
    .map(([projectId, sessions]) => ({
      project: projectMap.get(projectId)!,
      sessions,
    }))
    .sort((a, b) => {
      if (a.project.status === "active" && b.project.status !== "active") return -1;
      if (a.project.status !== "active" && b.project.status === "active") return 1;
      const aTime = new Date(a.sessions[0]?.created_at ?? 0).getTime();
      const bTime = new Date(b.sessions[0]?.created_at ?? 0).getTime();
      return bTime - aTime;
    });

  return { sortedGroups, ungrouped };
}

export function SessionsPageContent() {
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);

  const { data, isLoading, error } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => api.sessions.list({ limit: 50 }),
    refetchInterval: 10000,
  });

  const allSessions = data?.data ?? [];
  const projects = data?.projects ?? [];
  const hasActivePlanSession = allSessions.some((s) => s.type === "plan" && s.status === "active");

  const currentFilter = activeFilter ?? "all";
  const showDecisions = currentFilter === "decisions";

  const activeSessions = allSessions.filter((s) => s.status === "active");
  const failedSessions = allSessions.filter((s) => s.status === "failed");

  // For non-decisions views, filter sessions
  const filteredSessions = showDecisions ? [] : filterSessions(allSessions, activeFilter);
  const showGrouped = currentFilter === "all";

  const { sortedGroups, ungrouped } = groupSessionsByProject(filteredSessions, projects);
  const hasProjects = sortedGroups.length > 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Sessions"
        description="Each PM analysis cycle or manual fix creates a session."
      />

      <PMStatusBanner hasActivePlanSession={hasActivePlanSession} />

      <div className="flex items-center gap-1">
        {filterTabs.map((tab) => (
          <Button
            key={tab.value}
            variant={currentFilter === tab.value ? "default" : "ghost"}
            size="sm"
            className="text-xs"
            onClick={() => setActiveFilter(tab.value === "all" ? null : tab.value)}
          >
            {tab.label}
            {tab.value === "active" && activeSessions.length > 0 && (
              <span className="ml-1.5 rounded-full bg-blue-500 text-white text-[10px] px-1.5 py-0.5 font-normal">{activeSessions.length}</span>
            )}
            {tab.value === "failed" && failedSessions.length > 0 && (
              <span className="ml-1.5 rounded-full bg-red-500 text-white text-[10px] px-1.5 py-0.5 font-normal">{failedSessions.length}</span>
            )}
          </Button>
        ))}
      </div>

      {showDecisions && <DecisionsView />}

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
        <EmptyState
          icon={CalendarClock}
          title="No sessions yet"
          description="Sessions are created when the PM agent runs an analysis or when you manually fix an issue."
        />
      )}

      {!showDecisions && !isLoading && !error && allSessions.length > 0 && showGrouped && (
        <div className="space-y-4">
          {hasProjects && sortedGroups.map(({ project, sessions }) => (
            <ProjectGroup key={project.id} project={project} sessions={sessions} />
          ))}

          {ungrouped.length > 0 && (
            <>
              {hasProjects && (
                <SessionSection
                  title="Ungrouped"
                  sessions={ungrouped}
                />
              )}
              {!hasProjects && (
                <>
                  <SessionSection
                    title="Active"
                    sessions={ungrouped.filter((s) => s.status === "active")}
                    badge={
                      activeSessions.length > 0 ? (
                        <span className="relative flex h-2 w-2">
                          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                          <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                        </span>
                      ) : undefined
                    }
                  />
                  <SessionSection title="Failed" sessions={ungrouped.filter((s) => s.status === "failed")} />
                  <SessionSection title="Completed" sessions={ungrouped.filter((s) => s.status === "completed")} />
                </>
              )}
            </>
          )}
        </div>
      )}

      {!showDecisions && !isLoading && !error && allSessions.length > 0 && !showGrouped && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {filteredSessions.length} session{filteredSessions.length !== 1 ? "s" : ""}
              </span>
            </div>
            {filteredSessions.length === 0 ? (
              <div className="py-12 text-center text-[13px] text-muted-foreground">
                No sessions match this filter.
              </div>
            ) : (
              <div className="divide-y divide-border">
                {filteredSessions.map((session) => (
                  <SessionRow key={session.id} session={session} />
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
