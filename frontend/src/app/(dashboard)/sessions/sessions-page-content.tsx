"use client";

import { useQuery } from "@tanstack/react-query";
import { CalendarClock, Plus } from "lucide-react";
import Link from "next/link";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { PMStatusBanner } from "@/components/pm/pm-status-banner";
import { DecisionsView } from "@/components/pm/decisions-view";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";

const statusConfig: Record<string, { dot: string; text: string; bg: string; label: string }> = {
  pending: { dot: "bg-muted-foreground/50", text: "text-muted-foreground", bg: "bg-muted", label: "Pending" },
  running: { dot: "bg-primary", text: "text-primary", bg: "bg-primary/10", label: "Running" },
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
  { value: "active", label: "Active" },
  { value: "needs_human_guidance", label: "Needs guidance" },
  { value: "failed", label: "Failed" },
  { value: "done", label: "Done" },
  { value: "decisions", label: "Decisions" },
];

const activeStatuses = new Set(["pending", "running", "awaiting_input"]);
const doneStatuses = new Set(["completed", "pr_created"]);

function isActive(s: Session): boolean {
  return activeStatuses.has(s.status);
}

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

function filterSessions(sessions: Session[], filter: string | null): Session[] {
  if (!filter || filter === "all") return sessions;
  if (filter === "active") return sessions.filter(isActive);
  if (filter === "done") return sessions.filter((s) => doneStatuses.has(s.status));
  return sessions.filter((s) => s.status === filter);
}

function sessionTitle(session: Session): string {
  if (session.result_summary) return session.result_summary;
  if (session.pm_approach) return session.pm_approach;
  return `Session ${session.id.slice(0, 8)}`;
}

function SessionRow({ session }: { session: Session }) {
  const cfg = statusConfig[session.status] || statusConfig.pending;
  const active = isActive(session);

  return (
    <Link
      href={`/sessions/${session.id}`}
      className="group flex items-center gap-4 py-2.5 px-1 hover:bg-muted/40 dark:hover:bg-primary/[0.03] transition-all duration-150 cursor-pointer rounded-md -mx-1"
    >
      {/* Status dot */}
      <div className="flex-shrink-0 w-4 flex justify-center">
        {active ? (
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
          </span>
        ) : (
          <span className={`inline-flex rounded-full h-2 w-2 ${cfg.dot}`} />
        )}
      </div>

      {/* Main content */}
      <div className="flex-1 min-w-0">
        <span className="text-[13px] font-medium text-foreground truncate block">
          {sessionTitle(session)}
        </span>
        {session.status === "failed" && (session.failure_explanation || session.error) && (
          <span className="text-[11px] text-destructive truncate block mt-0.5">
            {session.failure_explanation || session.error}
          </span>
        )}
      </div>

      {/* Metadata */}
      <div className="flex items-center gap-2 flex-shrink-0">
        <span className="inline-flex items-center text-[11px] text-muted-foreground bg-muted rounded-md px-2 py-0.5">
          {session.agent_type.replace(/_/g, " ")}
        </span>
        <span className={`inline-flex items-center text-[11px] rounded-md px-2 py-0.5 ${cfg.text} ${cfg.bg}`}>
          {cfg.label}
        </span>
        {session.confidence_score != null && (
          <span className="text-[11px] text-muted-foreground">
            {Math.round(session.confidence_score * 100)}%
          </span>
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
  sessions: Session[];
  badge?: React.ReactNode;
}) {
  if (sessions.length === 0) return null;
  return (
    <div>
      <div className="flex items-center gap-2 py-2">
        <span className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">
          {title}
        </span>
        {badge}
        <span className="text-[11px] text-muted-foreground">({sessions.length})</span>
        <div className="flex-1 border-b border-border/50" />
      </div>
      <div className="divide-y divide-border/50">
        {sessions.map((session) => (
          <SessionRow key={session.id} session={session} />
        ))}
      </div>
    </div>
  );
}

export function SessionsPageContent() {
  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);

  const { data, isLoading, error } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => api.sessions.list({ limit: 50 }),
    refetchInterval: 10000,
  });

  const allSessions = data?.data ?? [];

  const currentFilter = activeFilter ?? "all";
  const showDecisions = currentFilter === "decisions";

  const activeSessions = allSessions.filter(isActive);
  const failedSessions = allSessions.filter((s) => s.status === "failed");
  const guidanceSessions = allSessions.filter((s) => s.status === "needs_human_guidance");

  const filteredSessions = showDecisions ? [] : filterSessions(allSessions, activeFilter);
  const showGrouped = currentFilter === "all";

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        description="Each agent execution creates a session."
      />

      <PMStatusBanner hasActivePlanSession={activeSessions.length > 0} />

      <div className="flex items-center gap-0 border-b border-border">
        {filterTabs.map((tab) => {
          const isSelected = currentFilter === tab.value;
          const count = tab.value === "active" ? activeSessions.length
            : tab.value === "failed" ? failedSessions.length
            : tab.value === "needs_human_guidance" ? guidanceSessions.length
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
                  <span className={`rounded-full text-white text-[10px] leading-none px-1.5 py-0.5 font-normal ${
                    tab.value === "failed" ? "bg-destructive"
                    : tab.value === "needs_human_guidance" ? "bg-orange-500"
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
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
              <CalendarClock className="h-5 w-5 text-muted-foreground" />
            </div>
            <p className="mt-4 text-[13px] font-medium text-foreground">No sessions yet</p>
            <p className="mt-1 max-w-sm text-center text-[13px] text-muted-foreground">
              Click <span className="font-medium text-foreground">Run PM agent</span> to review your issues and create sessions, or start a <span className="font-medium text-foreground">manual session</span> to fix a specific issue.
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

      {!showDecisions && !isLoading && !error && allSessions.length > 0 && showGrouped && (
        <div className="space-y-5">
          <SessionSection
            title="Active"
            sessions={activeSessions}
            badge={
              activeSessions.length > 0 ? (
                <span className="relative flex h-2 w-2">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
                  <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
                </span>
              ) : undefined
            }
          />
          <SessionSection title="Needs guidance" sessions={guidanceSessions} />
          <SessionSection title="Failed" sessions={failedSessions} />
          <SessionSection title="Completed" sessions={allSessions.filter((s) => doneStatuses.has(s.status))} />
        </div>
      )}

      {!showDecisions && !isLoading && !error && allSessions.length > 0 && !showGrouped && (
        <div>
          <div className="flex items-center justify-between py-2">
            <span className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">
              {filteredSessions.length} session{filteredSessions.length !== 1 ? "s" : ""}
            </span>
          </div>
          {filteredSessions.length === 0 ? (
            <div className="py-12 text-center text-[13px] text-muted-foreground">
              No sessions match this filter.
            </div>
          ) : (
            <div className="divide-y divide-border/50">
              {filteredSessions.map((session) => (
                <SessionRow key={session.id} session={session} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
