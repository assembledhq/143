"use client";

import { FileSearch, GitCommitHorizontal, Activity, Eye, History, Workflow } from "lucide-react";
import type { AgentSession } from "@/lib/types";

interface StatCardProps {
  value: number;
  label: string;
  icon: React.ElementType;
}

function StatCard({ value, label, icon: Icon }: StatCardProps) {
  if (value === 0) return null;
  return (
    <div className="flex items-center gap-2.5 rounded-lg border border-border bg-card px-3 py-2.5">
      <Icon className="h-4 w-4 text-muted-foreground shrink-0" />
      <div>
        <p className="text-sm font-semibold text-foreground">{value}</p>
        <p className="text-[11px] text-muted-foreground leading-tight">{label}</p>
      </div>
    </div>
  );
}

interface ContextStatsProps {
  session: AgentSession;
}

export function ContextStats({ session }: ContextStatsProps) {
  const stats = [
    { value: session.issues_reviewed ?? 0, label: "issues reviewed", icon: FileSearch },
    { value: session.in_flight_runs_checked ?? 0, label: "in-flight agent runs", icon: Workflow },
    { value: session.past_outcomes_reviewed ?? 0, label: "past runs learned from", icon: Activity },
    { value: session.recent_prs_checked ?? 0, label: "recent PRs checked", icon: Eye },
    { value: session.past_decisions_reviewed ?? 0, label: "past decisions reviewed", icon: History },
    { value: session.commits_analyzed ?? 0, label: "commits analyzed", icon: GitCommitHorizontal },
  ];

  const nonZeroStats = stats.filter((s) => s.value > 0);
  if (nonZeroStats.length === 0) return null;

  return (
    <div className="space-y-2">
      <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        Context considered
      </p>
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2">
        {nonZeroStats.map((stat) => (
          <StatCard key={stat.label} {...stat} />
        ))}
      </div>
    </div>
  );
}
