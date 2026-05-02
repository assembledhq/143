"use client";

import { useState, useEffect, useMemo, useRef, useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { FlaskConical, Plus, Loader2, GitPullRequest, AlertTriangle, Layers, CheckCircle2, XCircle, Eye, RotateCw } from "lucide-react";
import type { EvalTask, EvalBatch, EvalTaskSource, EvalBootstrapRun, EvalBootstrapStatus, ListResponse, Repository, SessionLog, SingleResponse } from "@/lib/types";
import { evalComplexityConfig, evalSourceConfig } from "@/lib/types";
import { addSSEListener, SSE_EVENT, buildEvalBootstrapStreamURL, buildSessionLogsStreamURL } from "@/lib/sse";
import { shouldSubscribeToEvalBootstrapStream, useEvalSSE } from "@/lib/use-eval-sse";
import { getActiveOrgId } from "@/lib/active-org";

type SourceFilter = "all" | EvalTaskSource | "archived";

function formatElapsed(startTime: string): string {
  const seconds = Math.floor((Date.now() - new Date(startTime).getTime()) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

function formatDuration(startTime: string, endTime: string): string {
  const seconds = Math.floor((new Date(endTime).getTime() - new Date(startTime).getTime()) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

function isBootstrapActive(status: EvalBootstrapStatus): boolean {
  return status === "pending" || status === "running";
}

export default function EvalsSettingsPage() {
  const queryClient = useQueryClient();
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("all");
  const [activeBootstrapRunId, setActiveBootstrapRunId] = useState<string | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [dialogOpen, setDialogOpen] = useState(false);

  const filterParams = {
    source: sourceFilter !== "all" && sourceFilter !== "archived" ? sourceFilter : undefined,
    archived: sourceFilter === "archived" ? "true" : undefined,
  };

  const { data: tasksResponse, isLoading } = useQuery({
    queryKey: queryKeys.evals.tasks(filterParams),
    queryFn: () => api.evals.listTasks(filterParams),
  });

  const { data: reposResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const repoMap = new Map((reposResponse?.data ?? []).map((r) => [r.id, r]));

  const tasks = tasksResponse?.data ?? [];
  const repos = reposResponse?.data ?? [];

  // On mount, detect any in-progress bootstrap run from the latest run query.
  const firstRepoId = repos[0]?.id;
  const { data: latestBootstrapResponse } = useQuery({
    queryKey: queryKeys.evals.bootstrapCandidates,
    queryFn: () => api.evals.getBootstrapCandidates({ repo_id: firstRepoId }),
    enabled: !!firstRepoId,
    retry: false,
  });

  // Derive the effective bootstrap run ID: prefer explicitly started run, fall back to
  // an in-progress run detected from the latest query (avoids setState-in-effect).
  const latestActiveId = (() => {
    const latest = latestBootstrapResponse?.data;
    return latest && isBootstrapActive(latest.status) ? latest.id : null;
  })();
  const effectiveBootstrapRunId = activeBootstrapRunId ?? latestActiveId;
  const cachedBootstrap = queryClient.getQueryData<SingleResponse<EvalBootstrapRun>>(
    queryKeys.evals.bootstrapRun(effectiveBootstrapRunId ?? ""),
  )?.data;

  // SSE-driven bootstrap status with a polling backstop. The SSE wakes the
  // page on every state transition so the user sees progress within ms; the
  // backstop only fires when SSE itself is unavailable (Redis down) or has
  // briefly disconnected, in which case we fall back to the original 3s
  // cadence so the UI still updates while Redis is recovering.
  const bootstrapSSEURL = useMemo(() => {
    if (
      !effectiveBootstrapRunId ||
      !shouldSubscribeToEvalBootstrapStream(cachedBootstrap?.status)
    ) {
      return null;
    }
    const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
    return buildEvalBootstrapStreamURL(apiBase, effectiveBootstrapRunId, getActiveOrgId());
  }, [effectiveBootstrapRunId, cachedBootstrap?.status]);
  const onBootstrapEvent = useCallback(() => {
    if (!effectiveBootstrapRunId) return;
    queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapRun(effectiveBootstrapRunId) });
  }, [queryClient, effectiveBootstrapRunId]);
  const { healthy: bootstrapStreamHealthy } = useEvalSSE({
    url: bootstrapSSEURL,
    event: SSE_EVENT.EVAL_BOOTSTRAP_UPDATED,
    onEvent: onBootstrapEvent,
  });

  const { data: activeBootstrapResponse } = useQuery({
    queryKey: queryKeys.evals.bootstrapRun(effectiveBootstrapRunId ?? ""),
    queryFn: () => api.evals.getBootstrapCandidates({ bootstrap_run_id: effectiveBootstrapRunId! }),
    enabled: !!effectiveBootstrapRunId,
    refetchInterval: (query) => {
      const status = query.state.data?.data?.status;
      if (!status || !isBootstrapActive(status)) return false;
      // 30s backstop while SSE is healthy; 3s when SSE is down.
      return bootstrapStreamHealthy ? 30_000 : 3_000;
    },
  });
  const activeBootstrap = activeBootstrapResponse?.data;

  // When a polled bootstrap reaches a terminal state, refresh related queries.
  const prevBootstrapStatus = useRef(activeBootstrap?.status);
  useEffect(() => {
    const prev = prevBootstrapStatus.current;
    const curr = activeBootstrap?.status;
    prevBootstrapStatus.current = curr;
    // Only invalidate on transition from active → terminal.
    if (prev && isBootstrapActive(prev) && curr && !isBootstrapActive(curr)) {
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapCandidates });
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.tasks() });
    }
  }, [activeBootstrap?.status, queryClient]);

  const bootstrapMutation = useMutation({
    mutationFn: (repoId: string) => api.evals.bootstrap({ repo_id: repoId }),
    onSuccess: (response) => {
      setActiveBootstrapRunId(response.data.id);
      setDialogOpen(false);
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapCandidates });
    },
  });

  const [bootstrapRepoId, setBootstrapRepoId] = useState<string>("");

  const filterTabs: { value: SourceFilter; label: string }[] = [
    { value: "all", label: "All" },
    { value: "manual", label: "Manual" },
    { value: "pr_bootstrap", label: "PR bootstrapped" },
    { value: "archived", label: "Archived" },
  ];

  const hasActiveBootstrap = activeBootstrap && isBootstrapActive(activeBootstrap.status);

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Coding agent evals"
          description="Benchmark coding agents on real tasks from your repo. Each eval pins a base commit and task setup to measure end-to-end coding ability."
        />
      {/* Actions */}
      <div className="flex items-center gap-3">
        <Button size="sm" asChild>
          <Link href="/settings/evals/new">
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Create eval task
          </Link>
        </Button>
        <AlertDialog open={dialogOpen} onOpenChange={setDialogOpen}>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm" disabled={!!hasActiveBootstrap} title={hasActiveBootstrap ? "A scan is already in progress" : undefined}>
              <GitPullRequest className="mr-1.5 h-3.5 w-3.5" />
              Bootstrap from PR history
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Bootstrap from PR history</AlertDialogTitle>
              <AlertDialogDescription>
                Scan merged PRs to automatically discover eval task candidates. Select a repository to scan.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="space-y-2">
              <Select value={bootstrapRepoId} onValueChange={setBootstrapRepoId}>
                <SelectTrigger>
                  <SelectValue placeholder="Select a repository" />
                </SelectTrigger>
                <SelectContent>
                  {repos.map((repo) => (
                    <SelectItem key={repo.id} value={repo.id}>
                      {repo.full_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {bootstrapMutation.isError && (
              <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
                Bootstrap scan failed. Please try again.
              </div>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={!bootstrapRepoId || bootstrapMutation.isPending}
                onClick={() => bootstrapMutation.mutate(bootstrapRepoId)}
              >
                {bootstrapMutation.isPending ? "Scanning..." : "Start scan"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>

      {/* Bootstrap progress banner */}
      {activeBootstrap && (
        <BootstrapProgressBanner
          bootstrap={activeBootstrap}
          repoName={repoMap.get(activeBootstrap.repo_id)?.full_name}
          onViewDetails={() => setSheetOpen(true)}
          onRetry={() => {
            setActiveBootstrapRunId(null);
            setDialogOpen(true);
          }}
          onDismiss={() => setActiveBootstrapRunId(null)}
        />
      )}

      {/* Bootstrap detail sidesheet */}
      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent className="sm:max-w-lg overflow-hidden flex flex-col">
          {activeBootstrap && (
            <BootstrapDetailSheet
              bootstrap={activeBootstrap}
              repoName={repoMap.get(activeBootstrap.repo_id)?.full_name}
            />
          )}
        </SheetContent>
      </Sheet>

      {/* Filter tabs */}
      <div className="flex items-center gap-1">
        {filterTabs.map((tab) => (
          <Button
            key={tab.value}
            variant={sourceFilter === tab.value ? "default" : "ghost"}
            size="sm"
            className="text-xs"
            onClick={() => setSourceFilter(tab.value)}
          >
            {tab.label}
          </Button>
        ))}
      </div>

      {/* Task list */}
      {isLoading ? (
        <Card>
          <CardContent className="flex items-center justify-center py-12">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </CardContent>
        </Card>
      ) : tasks.length === 0 ? (
        <EmptyState
          icon={FlaskConical}
          title="No coding agent evals yet"
          description="Create eval tasks manually or bootstrap from your PR history. Each pins a base commit and setup to benchmark different coding agents."
          action={{ label: "Create eval task", href: "/settings/evals/new" }}
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {tasks.length} {tasks.length === 1 ? "task" : "tasks"}
              </span>
            </div>
            {tasks.map((task) => (
              <EvalTaskRow key={task.id} task={task} repoName={repoMap.get(task.repo_id)?.full_name} />
            ))}
          </CardContent>
        </Card>
      )}

      {/* Batch history */}
      <BatchHistory />

      {/* Bootstrap candidates banner (shown when completed and not actively tracking) */}
      {(!activeBootstrap || activeBootstrap.status !== "completed") && (
        <BootstrapCandidatesBanner repoIds={repos.map((r) => r.id)} />
      )}
      </div>
    </PageContainer>
  );
}

// ---------------------------------------------------------------------------
// BootstrapProgressBanner
// ---------------------------------------------------------------------------

function BootstrapProgressBanner({
  bootstrap,
  repoName,
  onViewDetails,
  onRetry,
  onDismiss,
}: {
  bootstrap: EvalBootstrapRun;
  repoName?: string;
  onViewDetails: () => void;
  onRetry: () => void;
  onDismiss: () => void;
}) {
  const [, setTick] = useState(0);
  const isActive = isBootstrapActive(bootstrap.status);

  // Tick every second to update elapsed time while active.
  useEffect(() => {
    if (!isActive) return;
    const interval = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(interval);
  }, [isActive]);

  const statusConfig: Record<EvalBootstrapStatus, { border: string; bg: string; icon: React.ReactNode; text: string }> = {
    pending: {
      border: "border-muted-foreground/30",
      bg: "bg-muted/30",
      icon: <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />,
      text: "Bootstrap scan queued...",
    },
    running: {
      border: "border-blue-300 dark:border-blue-700",
      bg: "bg-blue-50 dark:bg-blue-950/30",
      icon: <Loader2 className="h-4 w-4 animate-spin text-blue-600 dark:text-blue-400" />,
      text: "Scanning PR history...",
    },
    completed: {
      border: "border-emerald-300 dark:border-emerald-700",
      bg: "bg-emerald-50 dark:bg-emerald-950/30",
      icon: <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" />,
      text: `Scan complete — ${bootstrap.candidates?.length ?? 0} candidates found`,
    },
    failed: {
      border: "border-red-300 dark:border-red-700",
      bg: "bg-red-50 dark:bg-red-950/30",
      icon: <XCircle className="h-4 w-4 text-red-600 dark:text-red-400" />,
      text: "Bootstrap scan failed",
    },
  };

  const config = statusConfig[bootstrap.status];

  return (
    <Card className={`${config.border} ${config.bg}`}>
      <CardContent className="flex items-center gap-3 py-3">
        {config.icon}
        <div className="min-w-0 flex-1">
          <p className="text-xs font-medium text-foreground">{config.text}</p>
          <div className="flex items-center gap-2 mt-0.5">
            {repoName && <span className="text-xs text-muted-foreground">{repoName}</span>}
            {isActive && (
              <span className="text-xs text-muted-foreground">
                {formatElapsed(bootstrap.created_at)}
              </span>
            )}
            {bootstrap.status === "failed" && bootstrap.error_message && (
              <span className="text-xs text-red-600 dark:text-red-400 truncate">
                {bootstrap.error_message}
              </span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Button variant="ghost" size="sm" onClick={onViewDetails}>
            <Eye className="mr-1.5 h-3.5 w-3.5" />
            View details
          </Button>
          {bootstrap.status === "failed" && (
            <Button variant="ghost" size="sm" onClick={onRetry}>
              <RotateCw className="mr-1.5 h-3.5 w-3.5" />
              Retry
            </Button>
          )}
          {bootstrap.status === "completed" && (
            <>
              <Button size="sm" variant="outline" asChild>
                <Link href={`/settings/evals?bootstrap=${bootstrap.id}`}>Review candidates</Link>
              </Button>
              <Button variant="ghost" size="sm" className="text-xs text-muted-foreground" onClick={onDismiss}>
                Dismiss
              </Button>
            </>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// BootstrapDetailSheet
// ---------------------------------------------------------------------------

function BootstrapDetailSheet({
  bootstrap,
  repoName,
}: {
  bootstrap: EvalBootstrapRun;
  repoName?: string;
}) {
  const [logs, setLogs] = useState<SessionLog[]>([]);
  const [, setTick] = useState(0);
  const logContainerRef = useRef<HTMLDivElement>(null);
  const isActive = isBootstrapActive(bootstrap.status);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";

  // Tick every second to update elapsed time.
  useEffect(() => {
    if (!isActive) return;
    const interval = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(interval);
  }, [isActive]);

  // SSE log streaming.
  const mergeLogs = useCallback((newLogs: SessionLog[]) => {
    setLogs((prev) => {
      const existingIds = new Set(prev.map((l) => l.id));
      const unique = newLogs.filter((l) => !existingIds.has(l.id));
      if (unique.length === 0) return prev;
      return [...prev, ...unique];
    });
  }, []);

  const MAX_SSE_RECONNECT_ATTEMPTS = 3;

  useEffect(() => {
    const sessionId = bootstrap.session_id;
    if (!sessionId) return;

    // Clear stale logs when session changes (e.g. retry with a new run).
    setLogs([]);

    // Fetch existing logs first.
    let cancelled = false;
    api.sessions.getLogs(sessionId).then((response) => {
      if (!cancelled && response?.data) {
        mergeLogs(response.data);
      }
    }).catch(() => {});

    // Only open SSE for active sessions.
    if (!isActive) {
      return () => { cancelled = true; };
    }

    let eventSource: EventSource | null = null;
    let reconnectAttempts = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    const connect = () => {
      if (cancelled) return;

      eventSource = new EventSource(
        buildSessionLogsStreamURL(apiBase, sessionId, getActiveOrgId()),
        { withCredentials: true }
      );

      eventSource.onopen = () => {
        reconnectAttempts = 0;
      };

      addSSEListener(eventSource, SSE_EVENT.LOG, (log) => {
        mergeLogs([log]);
      });

      addSSEListener(eventSource, SSE_EVENT.DONE, () => {
        eventSource?.close();
      });

      eventSource.onerror = () => {
        eventSource?.close();
        if (cancelled) return;
        reconnectAttempts++;
        if (reconnectAttempts <= MAX_SSE_RECONNECT_ATTEMPTS) {
          const delay = Math.min(1000 * Math.pow(2, reconnectAttempts - 1), 15000);
          reconnectTimer = setTimeout(connect, delay);
        }
      };
    };

    connect();

    return () => {
      cancelled = true;
      eventSource?.close();
      if (reconnectTimer) clearTimeout(reconnectTimer);
    };
  }, [bootstrap.session_id, isActive, apiBase, mergeLogs]);

  // Auto-scroll to bottom when new logs arrive.
  useEffect(() => {
    const container = logContainerRef.current;
    if (container) {
      container.scrollTop = container.scrollHeight;
    }
  }, [logs]);

  const statusBadge: Record<EvalBootstrapStatus, { color: string; label: string }> = {
    pending: { color: "bg-muted text-muted-foreground", label: "Pending" },
    running: { color: "bg-blue-500/10 text-blue-700 dark:text-blue-400", label: "Running" },
    completed: { color: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400", label: "Completed" },
    failed: { color: "bg-red-500/10 text-red-700 dark:text-red-400", label: "Failed" },
  };

  const badge = statusBadge[bootstrap.status];

  return (
    <>
      <SheetHeader>
        <div className="flex items-center gap-2">
          <SheetTitle>Bootstrap Scan</SheetTitle>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${badge.color}`}>
            {badge.label}
          </span>
        </div>
        <SheetDescription>
          {repoName && <span>{repoName}</span>}
          {repoName && " — "}
          {isActive ? (
            <span>Started {formatElapsed(bootstrap.created_at)} ago</span>
          ) : bootstrap.completed_at ? (
            <span>Completed in {formatDuration(bootstrap.created_at, bootstrap.completed_at)}</span>
          ) : (
            <span>Started {new Date(bootstrap.created_at).toLocaleString()}</span>
          )}
        </SheetDescription>
      </SheetHeader>

      {/* Error banner */}
      {bootstrap.status === "failed" && bootstrap.error_message && (
        <div className="rounded-md bg-red-500/10 border border-red-200 dark:border-red-800 px-3 py-2 mt-4">
          <p className="text-xs font-medium text-red-700 dark:text-red-400 mb-1">Error</p>
          <p className="text-xs text-red-600 dark:text-red-400 font-mono whitespace-pre-wrap break-all">
            {bootstrap.error_message}
          </p>
        </div>
      )}

      {/* Completed summary */}
      {bootstrap.status === "completed" && bootstrap.candidates && (
        <div className="rounded-md bg-emerald-500/10 border border-emerald-200 dark:border-emerald-800 px-3 py-2 mt-4 flex items-center justify-between">
          <p className="text-xs font-medium text-emerald-700 dark:text-emerald-400">
            {bootstrap.candidates.length} candidates ready for review
          </p>
          <Button size="sm" variant="outline" className="h-7 text-xs" asChild>
            <Link href={`/settings/evals?bootstrap=${bootstrap.id}`}>Review candidates</Link>
          </Button>
        </div>
      )}

      {/* Log stream */}
      <div className="flex-1 min-h-0 mt-4 flex flex-col">
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Logs</span>
          {isActive && (
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="relative flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
              Live
            </span>
          )}
        </div>
        <div
          ref={logContainerRef}
          className="flex-1 min-h-0 overflow-y-auto rounded-md bg-zinc-950 border border-zinc-800 p-3 font-mono text-xs"
        >
          {logs.length === 0 && isActive && (
            <div className="flex items-center gap-2 text-zinc-500">
              <Loader2 className="h-3 w-3 animate-spin" />
              <span>Waiting for logs...</span>
            </div>
          )}
          {logs.length === 0 && !isActive && !bootstrap.session_id && (
            <div className="text-zinc-500">No logs available for this run.</div>
          )}
          {logs.map((log) => (
            <div key={log.id} className="py-0.5 flex gap-2 leading-relaxed">
              <span className="text-zinc-600 shrink-0 select-none">
                {new Date(log.created_at).toLocaleTimeString()}
              </span>
              <span className={
                log.level === "error" ? "text-red-400" :
                log.level === "assistant" ? "text-zinc-300" :
                "text-zinc-400"
              }>
                {log.message}
              </span>
            </div>
          ))}
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// EvalTaskRow
// ---------------------------------------------------------------------------

function EvalTaskRow({ task, repoName }: { task: EvalTask; repoName?: string }) {
  const criteria: unknown[] = Array.isArray(task.scoring_criteria) ? task.scoring_criteria : [];
  const complexityStyle = evalComplexityConfig[task.complexity];
  const sourceStyle = evalSourceConfig[task.source];

  return (
    <Link
      href={`/settings/evals/${task.id}`}
      className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-xs font-medium text-foreground truncate">{task.name}</span>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${complexityStyle.color}`}>
            {complexityStyle.label}
          </span>
          {task.snapshot_broken && (
            <span className="inline-flex items-center gap-1 rounded-full bg-orange-500/10 px-2 py-0.5 text-xs font-medium text-orange-700 dark:text-orange-400">
              <AlertTriangle className="h-3 w-3" />
              Snapshot broken
            </span>
          )}
        </div>
        <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
          {repoName && <span>{repoName}</span>}
          {task.source_pr_number && <span>PR #{task.source_pr_number}</span>}
          <span>{criteria.length} {criteria.length === 1 ? "criterion" : "criteria"}</span>
          <span>{sourceStyle.label}</span>
        </div>
      </div>
      <div className="text-right text-xs text-muted-foreground shrink-0 ml-4">
        {new Date(task.created_at).toLocaleDateString()}
      </div>
    </Link>
  );
}

// ---------------------------------------------------------------------------
// BatchHistory
// ---------------------------------------------------------------------------

function BatchHistory() {
  const { data: batchesResponse } = useQuery({
    queryKey: queryKeys.evals.batches,
    queryFn: () => api.evals.listBatches(),
  });

  const batches = batchesResponse?.data ?? [];
  if (batches.length === 0) return null;

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Batch runs
        </h2>
      </div>
      <Card>
        <CardContent className="p-0">
          {batches.map((batch: EvalBatch) => (
            <Link
              key={batch.id}
              href={`/settings/evals/batch/${batch.id}`}
              className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <Layers className="h-3.5 w-3.5 text-muted-foreground" />
                  <span className="text-xs font-medium">{batch.name || "Unnamed batch"}</span>
                  <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                    batch.status === "completed"
                      ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
                      : batch.status === "running"
                      ? "bg-blue-500/10 text-blue-700 dark:text-blue-400"
                      : "bg-muted text-muted-foreground"
                  }`}>
                    {batch.status}
                  </span>
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {batch.task_count} tasks, {batch.run_count} runs
                </div>
              </div>
              <span className="text-xs text-muted-foreground shrink-0 ml-4">
                {new Date(batch.created_at).toLocaleDateString()}
              </span>
            </Link>
          ))}
        </CardContent>
      </Card>
    </section>
  );
}

// ---------------------------------------------------------------------------
// BootstrapCandidatesBanner
// ---------------------------------------------------------------------------

function BootstrapCandidatesBanner({ repoIds }: { repoIds: string[] }) {
  const firstRepoId = repoIds[0];
  const { data: bootstrapResponse } = useQuery({
    queryKey: queryKeys.evals.bootstrapCandidates,
    queryFn: () => api.evals.getBootstrapCandidates({ repo_id: firstRepoId }),
    enabled: !!firstRepoId,
    retry: false,
  });

  const bootstrap = bootstrapResponse?.data;
  if (!bootstrap || bootstrap.status !== "completed" || !bootstrap.candidates?.length) return null;

  return (
    <Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
      <CardContent className="flex items-center justify-between py-3">
        <div>
          <p className="text-xs font-medium text-blue-800 dark:text-blue-300">
            {bootstrap.candidates.length} bootstrap candidates ready for review
          </p>
          <p className="text-xs text-blue-700 dark:text-blue-400">
            Review and accept candidates to create eval tasks.
          </p>
        </div>
        <Button size="sm" variant="outline" asChild>
          <Link href={`/settings/evals?bootstrap=${bootstrap.id}`}>Review candidates</Link>
        </Button>
      </CardContent>
    </Card>
  );
}
