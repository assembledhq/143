"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Layers, Wrench, FileCode2 } from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { ContextStats } from "@/components/pm/context-stats";
import { api } from "@/lib/api";
import type { AgentSession, AgentSessionTask } from "@/lib/types";

const sessionStatusConfig: Record<string, { color: string; label: string }> = {
  active: { color: "bg-blue-100 text-blue-800", label: "Active" },
  completed: { color: "bg-green-100 text-green-800", label: "Completed" },
  failed: { color: "bg-red-100 text-red-800", label: "Failed" },
};

const runStatusConfig: Record<string, { color: string; label: string }> = {
  pending: { color: "bg-gray-100 text-gray-800", label: "Pending" },
  running: { color: "bg-blue-100 text-blue-800", label: "Running" },
  awaiting_input: { color: "bg-yellow-100 text-yellow-800", label: "Awaiting Input" },
  needs_human_guidance: { color: "bg-orange-100 text-orange-800", label: "Needs Guidance" },
  completed: { color: "bg-green-100 text-green-800", label: "Completed" },
  pr_created: { color: "bg-green-100 text-green-800", label: "PR Created" },
  failed: { color: "bg-red-100 text-red-800", label: "Failed" },
  cancelled: { color: "bg-gray-100 text-gray-700", label: "Cancelled" },
  skipped: { color: "bg-gray-100 text-gray-700", label: "Skipped" },
};

const triggeredByLabels: Record<string, string> = {
  scheduled: "Scheduled",
  manual: "Manual",
  fix_this: "Fix This",
};

function formatDuration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return "-";
  const start = new Date(startedAt);
  const end = completedAt ? new Date(completedAt) : new Date();
  const diffMs = end.getTime() - start.getTime();
  const diffSecs = Math.floor(diffMs / 1000);
  if (diffSecs < 60) return `${diffSecs}s`;
  const mins = Math.floor(diffSecs / 60);
  const secs = diffSecs % 60;
  return `${mins}m ${secs}s`;
}

function formatTimestamp(dateStr?: string): string {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
}

function confidenceColor(score: number): string {
  if (score > 0.8) return "text-green-700";
  if (score >= 0.5) return "text-yellow-700";
  return "text-red-700";
}

/** Parse `path:line` or `path` references from approach text. */
function parseFileRefs(text: string): { path: string; line?: number }[] {
  // Match patterns like `src/auth/token.go:142` or `auth/token_test.go`
  const regex = /(?:^|\s|`)([a-zA-Z0-9_\-./]+\.[a-zA-Z0-9]+(?::\d+)?)(?:`|\s|$|,|;)/g;
  const refs: { path: string; line?: number }[] = [];
  const seen = new Set<string>();
  let match;
  while ((match = regex.exec(text)) !== null) {
    const raw = match[1];
    // Filter out things that don't look like file paths
    if (!raw.includes("/") && !raw.includes(".go") && !raw.includes(".ts") && !raw.includes(".js") && !raw.includes(".py")) continue;
    if (seen.has(raw)) continue;
    seen.add(raw);
    const parts = raw.split(":");
    const path = parts[0];
    const line = parts[1] ? parseInt(parts[1], 10) : undefined;
    refs.push({ path, line: line && !isNaN(line) ? line : undefined });
  }
  return refs;
}

function FileRefs({ text }: { text: string }) {
  const refs = parseFileRefs(text);
  if (refs.length === 0) return null;

  return (
    <div className="flex items-center gap-1.5 flex-wrap">
      <FileCode2 className="h-3 w-3 text-muted-foreground shrink-0" />
      <span className="text-[11px] text-muted-foreground">Files:</span>
      {refs.map((ref) => (
        <code
          key={ref.path + (ref.line ?? "")}
          className="text-[11px] bg-muted px-1.5 py-0.5 rounded font-mono text-muted-foreground"
        >
          {ref.path}{ref.line ? `:${ref.line}` : ""}
        </code>
      ))}
    </div>
  );
}

function TaskRow({ task }: { task: AgentSessionTask }) {
  const taskStatus = task.status ?? "pending";
  const runStatus = task.run_status ? runStatusConfig[task.run_status] : null;
  const isActive = task.run_status === "running" || task.run_status === "awaiting_input";

  return (
    <Card>
      <CardHeader className="space-y-2">
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm">
            #{task.rank} · {task.title}
          </CardTitle>
          <div className="flex items-center gap-2">
            {runStatus && (
              <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${runStatus.color}`}>
                {isActive && (
                  <span className="relative mr-1.5 flex h-2 w-2">
                    <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                    <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                  </span>
                )}
                {runStatus.label}
              </span>
            )}
            {!runStatus && (
              <Badge className={taskStatus === "delegated" ? "bg-green-100 text-green-800" : "bg-gray-100 text-gray-700"}>
                {taskStatus.replace("_", " ")}
              </Badge>
            )}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {task.complexity && (
            <Badge variant="outline" className="text-[11px]">{task.complexity}</Badge>
          )}
          {task.confidence && (
            <Badge variant="outline" className="text-[11px]">{task.confidence} confidence</Badge>
          )}
          {task.run_confidence_score != null && (
            <span className={`text-xs font-medium ${confidenceColor(task.run_confidence_score)}`}>
              {(task.run_confidence_score * 100).toFixed(0)}% confidence
            </span>
          )}
          {task.issue_ids.map((id) => (
            <Badge key={id} variant="secondary" className="text-[11px]">
              {id.slice(0, 8)}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {task.run_result_summary && (
          <div>
            <p className="text-xs font-medium text-muted-foreground">Result</p>
            <p>{task.run_result_summary}</p>
          </div>
        )}
        {task.reasoning && (
          <div>
            <p className="text-xs font-medium text-muted-foreground">Reasoning</p>
            <p>{task.reasoning}</p>
          </div>
        )}
        {task.approach && (
          <div>
            <p className="text-xs font-medium text-muted-foreground">Approach</p>
            <p>{task.approach}</p>
            <div className="mt-1.5">
              <FileRefs text={task.approach} />
            </div>
          </div>
        )}
        {task.risk && (
          <div>
            <p className="text-xs font-medium text-muted-foreground">Risk</p>
            <p>{task.risk}</p>
          </div>
        )}
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          {task.run_started_at && (
            <span>Duration: {formatDuration(task.run_started_at, task.run_completed_at)}</span>
          )}
          {task.agent_run_id && (
            <Link href={`/runs/${task.agent_run_id}`} className="text-primary underline">
              View run details
            </Link>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function PlanSessionDetail({ session }: { session: AgentSession }) {
  return (
    <div className="space-y-6">
      {session.type === "plan" && <ContextStats session={session} />}

      {session.analysis && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Situation Analysis</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            {session.analysis}
          </CardContent>
        </Card>
      )}

      {session.tasks.length > 0 && (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Tasks</h3>
            <Badge variant="secondary" className="text-[11px]">
              {session.tasks.length} task{session.tasks.length !== 1 ? "s" : ""}
            </Badge>
          </div>
          <div className="space-y-4">
            {session.tasks.map((task) => (
              <TaskRow key={`${task.rank}-${task.title}`} task={task} />
            ))}
          </div>
        </div>
      )}

      {session.clusters && session.clusters.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Issue Clusters</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            {session.clusters.map((cluster, index) => (
              <div key={`${cluster.root_cause}-${index}`} className="space-y-2">
                <div className="flex flex-wrap gap-2">
                  {cluster.issue_ids.map((id) => (
                    <Badge key={id} variant="secondary" className="text-[11px]">
                      {id.slice(0, 8)}
                    </Badge>
                  ))}
                </div>
                <p>
                  <span className="font-medium">Root cause:</span> {cluster.root_cause}
                </p>
                <p>
                  <span className="font-medium">Strategy:</span> {cluster.strategy}
                </p>
                {index < session.clusters!.length - 1 && <Separator />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {session.skipped_issues && session.skipped_issues.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Skipped Issues</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            {session.skipped_issues.map((skip, index) => (
              <div key={`${skip.issue_id}-${index}`} className="space-y-2">
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="text-[11px]">
                    {skip.issue_id.slice(0, 8)}
                  </Badge>
                  <Badge variant="secondary" className="text-[11px]">
                    {skip.reason.replace("_", " ")}
                  </Badge>
                </div>
                <p>{skip.detail}</p>
                {index < session.skipped_issues!.length - 1 && <Separator />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function ManualSessionDetail({ session }: { session: AgentSession }) {
  const task = session.tasks[0];
  if (!task) return null;

  return (
    <div className="space-y-4">
      <TaskRow task={task} />
    </div>
  );
}

export function SessionDetailContent({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["session", id],
    queryFn: () => api.sessions.get(id),
    refetchInterval: (query) => {
      const session = query.state.data?.data;
      if (session && session.status === "active") {
        return 5000;
      }
      return false;
    },
  });

  const session = data?.data;

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Link href="/sessions" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to sessions
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading session...
          </CardContent>
        </Card>
      </div>
    );
  }

  if (error || !session) {
    return (
      <div className="space-y-6">
        <Link href="/sessions" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to sessions
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load session details.
          </CardContent>
        </Card>
      </div>
    );
  }

  const status = sessionStatusConfig[session.status] || sessionStatusConfig.active;

  return (
    <div className="space-y-6">
      <Link href="/sessions" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Back to sessions
      </Link>

      <div>
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold text-foreground">
            {session.title}
          </h1>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
            {status.label}
          </span>
          {session.project_title && (
            <Link
              href={`/projects/${session.project_id}`}
              className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
            >
              <Badge variant="outline" className="text-[11px] px-1.5 py-0">
                {session.project_title}
              </Badge>
            </Link>
          )}
        </div>
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {session.type === "plan" ? (
              <><Layers className="mr-1 h-3 w-3 inline" />PM Analysis</>
            ) : (
              <><Wrench className="mr-1 h-3 w-3 inline" />Manual</>
            )}
          </Badge>
          <span>{triggeredByLabels[session.triggered_by]}</span>
          <span>{formatTimestamp(session.created_at)}</span>
          {session.issues_reviewed != null && (
            <span>{session.issues_reviewed} issues reviewed</span>
          )}
        </div>
      </div>

      {session.type === "plan" ? (
        <PlanSessionDetail session={session} />
      ) : (
        <ManualSessionDetail session={session} />
      )}
    </div>
  );
}
