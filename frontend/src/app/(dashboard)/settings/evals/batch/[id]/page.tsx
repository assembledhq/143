"use client";

import { useCallback, useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useParams } from "next/navigation";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { getActiveOrgId } from "@/lib/active-org";
import { buildEvalBatchStreamURL, SSE_EVENT } from "@/lib/sse";
import { shouldSubscribeToEvalBatchStream, useEvalSSE } from "@/lib/use-eval-sse";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { ArrowLeft, Loader2, RefreshCw } from "lucide-react";
import type { EvalBatchDetail, EvalRun, EvalTask, ListResponse, SingleResponse } from "@/lib/types";
import { evalRunStatusConfig } from "@/lib/types";

// Slow polling backstop while SSE is the primary update channel. Keeps the
// matrix correct in the rare event a Redis publish is dropped (network
// blip on the publish side, subscription disconnect that re-establishes
// after the missed event) without doing anything close to the prior 5s
// load on Postgres. When SSE itself is unavailable (Redis down) we drop
// back to the original 5s cadence — see streamHealthy from useEvalSSE.
const SSE_BACKSTOP_POLL_MS = 30_000;
const SSE_DOWN_POLL_MS = 5_000;

export default function BatchDetailPage() {
  const params = useParams();
  const batchId = params.id as string;
  const queryClient = useQueryClient();
  const cachedBatch = queryClient.getQueryData<SingleResponse<EvalBatchDetail>>(
    queryKeys.evals.batch(batchId),
  )?.data;

  // Per-batch SSE subscription. Invalidates the detail query on each event
  // so React Query refetches the full EvalBatchDetail (batch + runs). The
  // event itself carries only batch_id + status, but we don't try to merge
  // partial state into the cache because the matrix needs the full runs
  // array — a single GET keeps the rendering path simple.
  const sseURL = useMemo(() => {
    if (!batchId || !shouldSubscribeToEvalBatchStream(cachedBatch?.status)) return null;
    const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
    return buildEvalBatchStreamURL(apiBase, batchId, getActiveOrgId());
  }, [batchId, cachedBatch?.status]);
  const onBatchEvent = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: queryKeys.evals.batch(batchId) });
  }, [queryClient, batchId]);
  const { healthy: streamHealthy } = useEvalSSE({
    url: sseURL,
    event: SSE_EVENT.EVAL_BATCH_UPDATED,
    onEvent: onBatchEvent,
  });

  const { data: batchResponse, isLoading } = useQuery({
    queryKey: queryKeys.evals.batch(batchId),
    queryFn: () => api.evals.getBatch(batchId),
    refetchInterval: (query) => {
      const batch = query.state.data?.data;
      if (!batch || batch.status === "completed") return false;
      return streamHealthy ? SSE_BACKSTOP_POLL_MS : SSE_DOWN_POLL_MS;
    },
  });

  const batch = batchResponse?.data;

  // Fetch task details so we can show real names in the matrix
  const { data: tasksResponse } = useQuery<ListResponse<EvalTask>>({
    queryKey: queryKeys.evals.tasks(),
    queryFn: () => api.evals.listTasks({}),
    enabled: !!batch?.runs?.length,
  });
  const taskNameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const t of tasksResponse?.data ?? []) {
      map.set(t.id, t.name);
    }
    return map;
  }, [tasksResponse]);

  // Build comparison matrix: rows = tasks, columns = configs
  const { taskNames, configLabels, matrix } = useMemo(() => {
    if (!batch?.runs?.length) return { taskNames: [], configLabels: [], matrix: new Map() };
    return buildComparisonMatrix(batch, taskNameMap);
  }, [batch, taskNameMap]);

  if (isLoading) {
    return (
      <PageContainer size="default">
        <div className="flex items-center justify-center py-24">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      </PageContainer>
    );
  }

  if (!batch) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader title="Batch not found" description="This batch does not exist." />
        </div>
      </PageContainer>
    );
  }

  const isRunning = batch.status !== "completed";

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <div>
          <Button variant="ghost" size="sm" className="mb-3 -ml-2 text-muted-foreground" asChild>
            <Link href="/settings/evals">
              <ArrowLeft className="mr-1 h-3.5 w-3.5" />
              Back to evals
            </Link>
          </Button>
          <PageHeader
            title={batch.name || "Batch comparison"}
            description={`${batch.task_count} tasks × ${configLabels.length} configs = ${batch.run_count} runs`}
            action={
              isRunning ? (
                <div className="flex items-center gap-2 text-blue-600 dark:text-blue-400">
                  <RefreshCw className="h-3.5 w-3.5 animate-spin" />
                  <span className="text-xs">Running...</span>
                </div>
              ) : undefined
            }
          />
        </div>

        {/* Status summary */}
        <BatchSummary batch={batch} />

        {/* Comparison matrix */}
        {taskNames.length > 0 && configLabels.length > 0 && (
          <section className="space-y-3">
            <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Comparison matrix</h2>
            <Card>
              <CardContent className="p-0 overflow-x-auto">
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b border-border bg-muted/30">
                      <th className="text-left px-4 py-2 text-xs font-medium text-muted-foreground uppercase tracking-wider">Task</th>
                      {configLabels.map((label) => (
                        <th key={label} className="text-center px-4 py-2 text-xs font-medium text-muted-foreground uppercase tracking-wider min-w-[120px]">
                          {label}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {taskNames.map(({ taskId, name }) => (
                      <tr key={taskId} className="border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors">
                        <td className="px-4 py-3 font-medium">
                          <Link href={`/settings/evals/${taskId}`} className="text-primary hover:underline">
                            {name}
                          </Link>
                        </td>
                        {configLabels.map((label) => {
                          const run = matrix.get(`${taskId}:${label}`);
                          return (
                            <td key={label} className="text-center px-4 py-3">
                              {run ? <ScoreCell run={run} /> : <span className="text-muted-foreground">-</span>}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                    {/* Average row */}
                    <tr className="bg-muted/30 font-medium">
                      <td className="px-4 py-3">Average</td>
                      {configLabels.map((label) => {
                        const scores = taskNames
                          .map(({ taskId }) => matrix.get(`${taskId}:${label}`))
                          .filter((r): r is EvalRun => r != null && r.final_score != null)
                          .map((r) => r.final_score!);
                        const avg = scores.length > 0 ? scores.reduce((a, b) => a + b, 0) / scores.length : null;
                        return (
                          <td key={label} className="text-center px-4 py-3">
                            {avg != null ? `${(avg * 100).toFixed(0)}%` : "-"}
                          </td>
                        );
                      })}
                    </tr>
                    {/* Pass rate row */}
                    <tr className="bg-muted/30">
                      <td className="px-4 py-3 font-medium">Pass rate</td>
                      {configLabels.map((label) => {
                        const runs = taskNames
                          .map(({ taskId }) => matrix.get(`${taskId}:${label}`))
                          .filter((r): r is EvalRun => r != null && r.passed != null);
                        const passed = runs.filter((r) => r.passed).length;
                        return (
                          <td key={label} className="text-center px-4 py-3 text-xs">
                            {runs.length > 0 ? `${passed}/${runs.length}` : "-"}
                          </td>
                        );
                      })}
                    </tr>
                  </tbody>
                </table>
              </CardContent>
            </Card>
          </section>
        )}
      </div>
    </PageContainer>
  );
}

function ScoreCell({ run }: { run: EvalRun }) {
  if (run.status === "pending" || run.status === "running") {
    const statusStyle = evalRunStatusConfig[run.status];
    return (
      <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${statusStyle.color}`}>
        {statusStyle.label}
      </span>
    );
  }
  if (run.status === "failed") {
    return <span className="text-red-600 dark:text-red-400 text-xs">Error</span>;
  }
  if (run.final_score == null) return <span className="text-muted-foreground">-</span>;

  const pct = (run.final_score * 100).toFixed(0);
  const color = run.passed
    ? "text-emerald-600 dark:text-emerald-400"
    : "text-red-600 dark:text-red-400";

  return <span className={`font-medium ${color}`}>{pct}%</span>;
}

function BatchSummary({ batch }: { batch: EvalBatchDetail }) {
  let completedCount = 0;
  let failedCount = 0;
  let passedCount = 0;
  let scoreSum = 0;
  let scoreCount = 0;
  for (const r of batch.runs) {
    if (r.status === "completed") {
      completedCount++;
      if (r.passed) passedCount++;
      if (r.final_score != null) { scoreSum += r.final_score; scoreCount++; }
    } else if (r.status === "failed") {
      failedCount++;
    }
  }
  const avgScore = scoreCount > 0 ? scoreSum / scoreCount : null;

  return (
    <div className="grid gap-4 md:grid-cols-4">
      <Card>
        <CardContent className="py-3 text-center">
          <p className="text-2xl font-semibold">{completedCount}/{batch.run_count}</p>
          <p className="text-xs text-muted-foreground">Runs completed</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="py-3 text-center">
          <p className="text-2xl font-semibold">{avgScore != null ? `${(avgScore * 100).toFixed(0)}%` : "-"}</p>
          <p className="text-xs text-muted-foreground">Average score</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="py-3 text-center">
          <p className="text-2xl font-semibold text-emerald-600 dark:text-emerald-400">{passedCount}</p>
          <p className="text-xs text-muted-foreground">Passed</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="py-3 text-center">
          <p className="text-2xl font-semibold text-red-600 dark:text-red-400">{failedCount}</p>
          <p className="text-xs text-muted-foreground">Errors</p>
        </CardContent>
      </Card>
    </div>
  );
}

/** Build a lookup matrix from flat runs array. */
function buildComparisonMatrix(batch: EvalBatchDetail, taskNameMap: Map<string, string>) {
  const runs = batch.runs;

  // Identify unique task IDs and config labels
  const taskIdSet = new Set<string>();
  const configSet = new Set<string>();
  const matrix = new Map<string, EvalRun>();

  for (const run of runs) {
    const configLabel = run.config_ref || run.model;
    configSet.add(configLabel);
    taskIdSet.add(run.task_id);

    matrix.set(`${run.task_id}:${configLabel}`, run);
  }

  const taskNames = Array.from(taskIdSet).map((id) => ({
    taskId: id,
    name: taskNameMap.get(id) || id.slice(0, 8),
  }));
  const configLabels = Array.from(configSet);

  return { taskNames, configLabels, matrix };
}
