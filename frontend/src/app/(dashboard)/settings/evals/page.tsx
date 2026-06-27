"use client";

import { useState, useEffect, useMemo, useRef, useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { ErrorText } from "@/components/ui/error-notice";
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
import { FlaskConical, Plus, Loader2, GitPullRequest, AlertTriangle, Layers, CheckCircle2, XCircle, Eye, RotateCw, ShieldCheck, Database } from "lucide-react";
import type { EvalTask, EvalBatch, EvalTaskSource, EvalBootstrapRun, EvalBootstrapStatus, EvalBootstrapCandidate, EvalBootstrapCandidateStatus, EvalDataset, EvalReleaseGate, ListResponse, Repository, SessionLog, SingleResponse } from "@/lib/types";
import { evalComplexityConfig, evalSourceConfig } from "@/lib/types";
import { formatDateTime } from "@/lib/utils";
import { addSSEListener, SSE_EVENT, buildEvalBootstrapStreamURL, buildSessionLogsStreamURL } from "@/lib/sse";
import { shouldSubscribeToEvalBootstrapStream } from "@/lib/eval-streams";
import { useResourceSSE } from "@/lib/use-resource-sse";
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
  const [selectedCandidate, setSelectedCandidate] = useState<EvalBootstrapCandidate | null>(null);
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
  const { data: datasetsResponse } = useQuery<ListResponse<EvalDataset>>({
    queryKey: ["evals", "datasets"],
    queryFn: () => api.evals.listDatasets(),
  });
  const { data: releaseGatesResponse } = useQuery<ListResponse<EvalReleaseGate>>({
    queryKey: ["evals", "release-gates"],
    queryFn: () => api.evals.listReleaseGates(),
  });
  const repoMap = new Map((reposResponse?.data ?? []).map((r) => [r.id, r]));

  const tasks = tasksResponse?.data ?? [];
  const repos = reposResponse?.data ?? [];
  const datasets = datasetsResponse?.data ?? [];
  const releaseGates = releaseGatesResponse?.data ?? [];

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
  const { healthy: bootstrapStreamHealthy } = useResourceSSE({
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
              <ErrorText className="rounded-md bg-destructive/10 px-3 py-2">
                Bootstrap scan failed. Please try again.
              </ErrorText>
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

      {activeBootstrap?.status === "completed" && activeBootstrap.candidates?.length ? (
        <BootstrapCandidateReview
          bootstrap={activeBootstrap}
          repoName={repoMap.get(activeBootstrap.repo_id)?.full_name}
          onOpenCandidate={setSelectedCandidate}
        />
      ) : null}

      <Sheet open={!!selectedCandidate} onOpenChange={(open) => { if (!open) setSelectedCandidate(null); }}>
        <SheetContent className="sm:max-w-2xl overflow-y-auto">
          {selectedCandidate ? (
            <CandidateDetailSheet candidate={selectedCandidate} repoName={repoMap.get(activeBootstrap?.repo_id ?? "")?.full_name} />
          ) : null}
        </SheetContent>
      </Sheet>

      <EvalQualityPanels datasets={datasets} releaseGates={releaseGates} />

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
// EvalQualityPanels
// ---------------------------------------------------------------------------

function EvalQualityPanels({ datasets, releaseGates }: { datasets: EvalDataset[]; releaseGates: EvalReleaseGate[] }) {
  const activeGateCount = releaseGates.filter((gate) => gate.enabled).length;
  const goldenTaskCount = datasets
    .filter((dataset) => dataset.dataset_type === "golden")
    .reduce((sum, dataset) => sum + dataset.task_count, 0);

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardContent className="space-y-3 p-4">
          <div className="flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <Database className="h-4 w-4 text-muted-foreground" />
              <h2 className="text-sm font-medium">Datasets</h2>
            </div>
            <Badge variant="outline">{goldenTaskCount} golden tasks</Badge>
          </div>
          <div className="grid gap-2">
            {datasets.slice(0, 4).map((dataset) => (
              <div key={dataset.id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{dataset.name}</span>
                    <Badge variant={dataset.dataset_type === "golden" ? "default" : "secondary"}>{dataset.dataset_type}</Badge>
                  </div>
                  <p className="truncate text-xs text-muted-foreground">{dataset.description || dataset.source_summary || "No description"}</p>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">{dataset.task_count} tasks</span>
              </div>
            ))}
            {datasets.length === 0 ? (
              <div className="rounded-md border border-dashed border-border px-3 py-4 text-sm text-muted-foreground">
                No datasets yet. Accepted candidates can now be organized into golden, shadow, or adversarial sets.
              </div>
            ) : null}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-3 p-4">
          <div className="flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <ShieldCheck className="h-4 w-4 text-muted-foreground" />
              <h2 className="text-sm font-medium">Release gates</h2>
            </div>
            <Badge variant={activeGateCount > 0 ? "default" : "outline"}>{activeGateCount} enabled</Badge>
          </div>
          <div className="grid gap-2">
            {releaseGates.slice(0, 4).map((gate) => (
              <div key={gate.id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{gate.gate_name}</span>
                    <Badge variant={gate.enabled ? "default" : "outline"}>{gate.enabled ? "enabled" : "disabled"}</Badge>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    pass@1 {">="} {Math.round(gate.min_pass_at_1 * 100)}% · pass@k {">="} {Math.round(gate.min_pass_at_k * 100)}% · max regression {Math.round(gate.max_regression_delta * 100)}%
                  </p>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">{gate.max_policy_violations} policy</span>
              </div>
            ))}
            {releaseGates.length === 0 ? (
              <div className="rounded-md border border-dashed border-border px-3 py-4 text-sm text-muted-foreground">
                No release gates yet. Gates can now bind a dataset to pass-rate and regression thresholds.
              </div>
            ) : null}
          </div>
        </CardContent>
      </Card>
    </div>
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
      border: "border-info/30",
      bg: "bg-info/10",
      icon: <Loader2 className="h-4 w-4 animate-spin text-info" />,
      text: "Scanning PR history...",
    },
    completed: {
      border: "border-success/30",
      bg: "bg-success/10",
      icon: <CheckCircle2 className="h-4 w-4 text-success" />,
      text: `Scan complete — ${bootstrap.candidates?.length ?? 0} candidates found`,
    },
    failed: {
      border: "border-destructive/30",
      bg: "bg-destructive/10",
      icon: <XCircle className="h-4 w-4 text-destructive" />,
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
              <span className="text-xs text-destructive truncate">
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
    running: { color: "bg-info/10 text-info", label: "Running" },
    completed: { color: "bg-success/10 text-success", label: "Completed" },
    failed: { color: "bg-destructive/10 text-destructive", label: "Failed" },
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
            <span>Started {formatDateTime(bootstrap.created_at)}</span>
          )}
        </SheetDescription>
      </SheetHeader>

      {/* Error banner */}
      {bootstrap.status === "failed" && bootstrap.error_message && (
        <div className="rounded-md bg-destructive/10 border border-destructive/30 px-3 py-2 mt-4">
          <p className="text-xs font-medium text-destructive mb-1">Error</p>
          <p className="text-xs text-destructive font-mono whitespace-pre-wrap break-all">
            {bootstrap.error_message}
          </p>
        </div>
      )}

      {/* Completed summary */}
      {bootstrap.status === "completed" && bootstrap.candidates && (
        <div className="rounded-md bg-success/10 border border-success/30 px-3 py-2 mt-4 flex items-center justify-between">
          <p className="text-xs font-medium text-success">
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
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-info opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-info" />
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
// BootstrapCandidateReview
// ---------------------------------------------------------------------------

function BootstrapCandidateReview({ bootstrap, repoName, onOpenCandidate }: { bootstrap: EvalBootstrapRun; repoName?: string; onOpenCandidate: (candidate: EvalBootstrapCandidate) => void }) {
  const queryClient = useQueryClient();
  const candidates = useMemo(() => bootstrap.candidates ?? [], [bootstrap.candidates]);
  const selectableCandidates = useMemo(
    () => candidates.filter((candidate) => candidate.id && (candidate.status ?? "proposed") === "proposed"),
    [candidates],
  );
  const [selectedIds, setSelectedIds] = useState<string[]>(() => selectableCandidates.map((candidate) => candidate.id!).slice(0, 5));
  const selectableIds = useMemo(() => selectableCandidates.map((candidate) => candidate.id!), [selectableCandidates]);
  const selectableIDSet = useMemo(() => new Set(selectableIds), [selectableIds]);
  const activeSelectedIds = useMemo(() => selectedIds.filter((id) => selectableIDSet.has(id)), [selectedIds, selectableIDSet]);

  const acceptMutation = useMutation({
    mutationFn: (candidateIds: string[]) => api.evals.acceptBootstrapCandidates({
      bootstrap_run_id: bootstrap.id,
      candidate_ids: candidateIds,
    }),
    onSuccess: () => {
      setSelectedIds([]);
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapRun(bootstrap.id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapCandidates });
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.tasks() });
    },
  });

  const reviewMutation = useMutation({
    mutationFn: ({ candidateId, status, rejectionReason }: { candidateId: string; status: EvalBootstrapCandidateStatus; rejectionReason?: string }) =>
      api.evals.reviewBootstrapCandidate(candidateId, {
        status,
        rejection_reason: rejectionReason,
      }),
    onSuccess: (_, variables) => {
      setSelectedIds((current) => current.filter((id) => id !== variables.candidateId));
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapRun(bootstrap.id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.bootstrapCandidates });
    },
  });

  const toggleCandidate = (candidate: EvalBootstrapCandidate, checked: boolean) => {
    if (!candidate.id) return;
    setSelectedIds((current) => (
      checked ? Array.from(new Set([...current, candidate.id!])) : current.filter((id) => id !== candidate.id)
    ));
  };

  const allSelected = selectableIds.length > 0 && selectableIds.every((id) => activeSelectedIds.includes(id));
  const selectedCount = activeSelectedIds.length;
  const reviewCandidate = (candidate: EvalBootstrapCandidate, status: EvalBootstrapCandidateStatus) => {
    if (!candidate.id) return;
    const rejectionReason = status === "needs_revision"
      ? "Needs reviewer revision before it can become an eval task."
      : status === "rejected"
        ? "Rejected during eval bootstrap review."
        : undefined;
    reviewMutation.mutate({ candidateId: candidate.id, status, rejectionReason });
  };

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Bootstrap candidates</h2>
          <p className="text-xs text-muted-foreground">
            {repoName ? `${repoName} · ` : ""}{candidates.length} candidates from session-backed bootstrap
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={selectableCandidates.length === 0}
            onClick={() => setSelectedIds(allSelected ? [] : selectableIds)}
          >
            {allSelected ? "Clear" : "Select all"}
          </Button>
          <Button
            size="sm"
            disabled={selectedCount === 0 || acceptMutation.isPending}
            onClick={() => acceptMutation.mutate(activeSelectedIds)}
          >
            {acceptMutation.isPending ? "Accepting..." : `Accept ${selectedCount || ""}`.trim()}
          </Button>
        </div>
      </div>

      {acceptMutation.isError ? (
        <ErrorText className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2">
          Failed to accept selected candidates.
        </ErrorText>
      ) : null}
      {reviewMutation.isError ? (
        <ErrorText className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2">
          Failed to update candidate review.
        </ErrorText>
      ) : null}

      <div className="grid gap-3">
        {candidates.map((candidate, index) => (
          <CandidateReviewCard
            key={candidate.id ?? `${candidate.pr_number}-${index}`}
            candidate={candidate}
            index={index}
            selected={!!candidate.id && selectedIds.includes(candidate.id)}
            onSelectedChange={(checked) => toggleCandidate(candidate, checked)}
            bootstrapSessionId={bootstrap.session_id}
            reviewPending={reviewMutation.isPending}
            onReview={(status) => reviewCandidate(candidate, status)}
            onOpenDetails={() => onOpenCandidate(candidate)}
          />
        ))}
      </div>
    </section>
  );
}

function CandidateReviewCard({
  candidate,
  index,
  selected,
  onSelectedChange,
  bootstrapSessionId,
  reviewPending,
  onReview,
  onOpenDetails,
}: {
  candidate: EvalBootstrapCandidate;
  index: number;
  selected: boolean;
  onSelectedChange: (checked: boolean) => void;
  bootstrapSessionId?: string;
  reviewPending: boolean;
  onReview: (status: EvalBootstrapCandidateStatus) => void;
  onOpenDetails: () => void;
}) {
  const status = candidate.status ?? "proposed";
  const accepted = status === "accepted";
  const actionable = !!candidate.id && status === "proposed";
  const acceptedTaskId = candidate.accepted_task_id ?? candidate.created_task_id;
  const criteria = Array.isArray(candidate.scoring_criteria) ? candidate.scoring_criteria : [];
  const warnings = candidate.warnings ?? [];
  const validationWarnings = candidate.validation_warnings ?? [];

  return (
    <Card className={selected ? "border-primary/60" : undefined}>
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start gap-3">
          <Checkbox
            checked={selected || accepted}
            disabled={!actionable}
            onCheckedChange={(value) => onSelectedChange(value === true)}
            aria-label={`Select candidate ${candidate.pr_title || index + 1}`}
            className="mt-1"
          />
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-foreground">{candidate.pr_title || `Candidate ${index + 1}`}</span>
              <Badge variant="secondary">PR #{candidate.pr_number}</Badge>
              <Badge variant={accepted ? "default" : "outline"}>{status}</Badge>
              <Badge variant="outline">{candidate.complexity}</Badge>
              <Badge variant="outline">{Math.round(candidate.fitness_score * 100)}% fit</Badge>
            </div>
            <p className="text-xs text-muted-foreground">{candidate.fitness_reasoning}</p>
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-xs text-muted-foreground">
            {candidate.rejection_reason ? candidate.rejection_reason : acceptedTaskId ? `Created task ${acceptedTaskId}` : "Review this candidate before adding it to the eval set."}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="outline" onClick={onOpenDetails}>
              <Eye className="mr-1.5 h-3.5 w-3.5" />
              Details
            </Button>
            {bootstrapSessionId ? (
              <Button size="sm" variant="outline" asChild>
                <Link href={`/sessions/${bootstrapSessionId}`}>
                  <Eye className="mr-1.5 h-3.5 w-3.5" />
                  Session
                </Link>
              </Button>
            ) : null}
            <Button
              size="sm"
              variant="outline"
              disabled={!actionable || reviewPending}
              onClick={() => onReview("needs_revision")}
            >
              <RotateCw className="mr-1.5 h-3.5 w-3.5" />
              Needs revision
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!actionable || reviewPending}
              onClick={() => onReview("rejected")}
            >
              <XCircle className="mr-1.5 h-3.5 w-3.5" />
              Reject
            </Button>
          </div>
        </div>

        <div className="grid gap-3 md:grid-cols-2">
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Task prompt</p>
            <p className="text-xs leading-relaxed text-foreground">{candidate.issue_description}</p>
          </div>
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Pinned commits</p>
            <div className="space-y-1 text-xs font-mono text-muted-foreground">
              <div>base {candidate.base_commit_sha}</div>
              <div>solution {candidate.solution_commit_sha}</div>
            </div>
          </div>
        </div>

        <div className="space-y-2">
          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Scoring</p>
          <div className="grid gap-2 md:grid-cols-2">
            {criteria.map((criterion) => (
              <div key={criterion.name} className="rounded-md border border-border bg-muted/30 p-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium">{criterion.name}</span>
                  <Badge variant="outline">{criterion.grader_type}</Badge>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">{criterion.notes || "No notes"}</p>
              </div>
            ))}
            {criteria.length === 0 ? (
              <div className="rounded-md border border-border bg-muted/30 p-2 text-xs text-muted-foreground">
                No scoring criteria were provided.
              </div>
            ) : null}
          </div>
        </div>

        {validationWarnings.length > 0 ? (
          <div className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 dark:border-amber-800 dark:bg-amber-950/30">
            <div className="mb-1 flex items-center gap-2 text-xs font-medium text-amber-800 dark:text-amber-300">
              <AlertTriangle className="h-3.5 w-3.5" />
              Validation warnings
            </div>
            <ul className="space-y-1 text-xs text-amber-800 dark:text-amber-300">
              {validationWarnings.map((warning) => (
                <li key={warning.code}>
                  <span className="font-medium">{warning.message}</span>
                  {warning.suggestion ? <span className="ml-1">{warning.suggestion}</span> : null}
                </li>
              ))}
            </ul>
          </div>
        ) : warnings.length > 0 ? (
          <div className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 dark:border-amber-800 dark:bg-amber-950/30">
            <div className="mb-1 flex items-center gap-2 text-xs font-medium text-amber-800 dark:text-amber-300">
              <AlertTriangle className="h-3.5 w-3.5" />
              Reviewer warnings
            </div>
            <ul className="space-y-1 text-xs text-amber-800 dark:text-amber-300">
              {warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function CandidateDetailSheet({ candidate, repoName }: { candidate: EvalBootstrapCandidate; repoName?: string }) {
  const criteria = Array.isArray(candidate.scoring_criteria) ? candidate.scoring_criteria : [];
  const validationWarnings = candidate.validation_warnings ?? [];
  const evidenceEntries = Object.entries(candidate.evidence ?? {});

  return (
    <div className="space-y-5">
      <SheetHeader>
        <SheetTitle>{candidate.pr_title || `PR #${candidate.pr_number}`}</SheetTitle>
        <SheetDescription>
          {repoName ? `${repoName} · ` : ""}PR #{candidate.pr_number} · {candidate.complexity} · {Math.round(candidate.fitness_score * 100)}% fit
        </SheetDescription>
      </SheetHeader>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">Task prompt</h3>
        <div className="rounded-md border border-border bg-muted/30 p-3 text-sm leading-relaxed">
          {candidate.issue_description}
        </div>
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">Evidence</h3>
        {evidenceEntries.length > 0 ? (
          <div className="grid gap-2">
            {evidenceEntries.map(([key, value]) => (
              <div key={key} className="rounded-md border border-border p-3">
                <div className="text-xs font-medium uppercase text-muted-foreground">{key.replaceAll("_", " ")}</div>
                <div className="mt-1 whitespace-pre-wrap break-words text-sm text-foreground">
                  {Array.isArray(value) ? value.join("\n") : typeof value === "object" ? JSON.stringify(value, null, 2) : String(value)}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground">No evidence payload was provided.</div>
        )}
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">Graders</h3>
        <div className="grid gap-2">
          {criteria.map((criterion) => (
            <div key={criterion.name} className="rounded-md border border-border p-3">
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm font-medium">{criterion.name}</span>
                <Badge variant={criterion.grader_type === "code_check" ? "default" : "outline"}>{criterion.grader_type}</Badge>
              </div>
              <p className="mt-1 text-sm text-muted-foreground">{criterion.notes || "No notes"}</p>
              {criterion.grader_config ? (
                <pre className="mt-2 max-h-32 overflow-auto rounded bg-muted p-2 text-xs">{JSON.stringify(criterion.grader_config, null, 2)}</pre>
              ) : null}
            </div>
          ))}
        </div>
      </section>

      {validationWarnings.length > 0 ? (
        <section className="space-y-2">
          <h3 className="text-sm font-medium">Validation warnings</h3>
          <div className="grid gap-2">
            {validationWarnings.map((warning) => (
              <div key={warning.code} className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
                <div className="font-medium">{warning.message}</div>
                {warning.suggestion ? <div className="mt-1">{warning.suggestion}</div> : null}
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <section className="space-y-2">
        <h3 className="text-sm font-medium">Solution diff</h3>
        <div className="rounded-md border border-border bg-muted/30 p-2">
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">{candidate.solution_diff || "No diff provided."}</pre>
        </div>
      </section>
    </div>
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
            <span className="inline-flex items-center gap-1 rounded-full bg-attention/10 px-2 py-0.5 text-xs font-medium text-attention">
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
      <div className="text-right text-xs text-muted-foreground tabular-nums shrink-0 ml-4">
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
                      ? "bg-success/10 text-success"
                      : batch.status === "running"
                      ? "bg-info/10 text-info"
                      : "bg-muted text-muted-foreground"
                  }`}>
                    {batch.status}
                  </span>
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground tabular-nums">
                  {batch.task_count} tasks, {batch.run_count} runs
                </div>
              </div>
              <span className="text-xs text-muted-foreground tabular-nums shrink-0 ml-4">
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
    <Card className="border-info/30 bg-info/10">
      <CardContent className="flex items-center justify-between py-3">
        <div>
          <p className="text-xs font-medium text-info">
            {bootstrap.candidates.length} bootstrap candidates ready for review
          </p>
          <p className="text-xs text-info">
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
