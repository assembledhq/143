"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
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
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { ArrowLeft, Play, Trash2, Loader2, AlertTriangle, ExternalLink, FileText, MonitorPlay } from "lucide-react";
import { usePageTitle } from "@/hooks/use-page-title";
import type { EvalRun, ScoringCriterion } from "@/lib/types";
import { evalComplexityConfig, evalRunStatusConfig, evalSourceConfig } from "@/lib/types";

export default function EvalTaskDetailPage() {
  const params = useParams();
  const router = useRouter();
  const queryClient = useQueryClient();
  const taskId = params.id as string;

  const { data: taskResponse, isLoading: taskLoading } = useQuery({
    queryKey: queryKeys.evals.taskDetail(taskId),
    queryFn: () => api.evals.getTask(taskId),
  });

  const { data: runsResponse } = useQuery({
    queryKey: queryKeys.evals.runs(taskId),
    queryFn: () => api.evals.listRuns(taskId),
    enabled: !!taskId,
  });

  const archiveMutation = useMutation({
    mutationFn: () => api.evals.archiveTask(taskId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.tasks() });
      router.push("/settings/evals");
    },
  });

  const task = taskResponse?.data;
  usePageTitle(task?.name, "Eval");
  const runs = runsResponse?.data ?? [];

  if (taskLoading) {
    return (
      <PageContainer size="default">
        <div className="flex items-center justify-center py-24">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      </PageContainer>
    );
  }

  if (!task) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader title="Eval task not found" description="This eval task does not exist or has been archived." />
        </div>
      </PageContainer>
    );
  }

  const criteria: ScoringCriterion[] = Array.isArray(task.scoring_criteria) ? task.scoring_criteria : [];
  const complexityStyle = evalComplexityConfig[task.complexity];

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        {/* Header */}
        <div>
          <Button variant="ghost" size="sm" className="mb-3 -ml-2 text-muted-foreground" asChild>
            <Link href="/settings/evals">
              <ArrowLeft className="mr-1 h-3.5 w-3.5" />
              Back to evals
            </Link>
          </Button>
          <PageHeader
            title={task.name}
            description={task.description}
            action={
              <div className="flex items-center gap-2">
                <RunEvalDialog taskId={taskId} />
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button variant="ghost" size="sm" className="text-destructive">
                      <Trash2 className="mr-1 h-3.5 w-3.5" />
                      Archive
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Archive eval task</AlertDialogTitle>
                      <AlertDialogDescription>
                        This will archive the eval task. It can still be viewed but won&apos;t appear in the default list.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    {archiveMutation.isError && (
                      <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
                        Failed to archive eval task. Please try again.
                      </div>
                    )}
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction
                        className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                        onClick={() => archiveMutation.mutate()}
                      >
                        Archive
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            }
          />
        </div>

        {/* Snapshot broken warning */}
        {task.snapshot_broken && (
          <div className="flex items-center gap-3 rounded-lg border border-attention/30 bg-attention/10 px-4 py-3">
            <AlertTriangle className="h-4 w-4 text-attention shrink-0" />
            <div>
              <p className="text-xs font-medium text-attention">Snapshot broken</p>
              <p className="text-xs text-attention">
                The base commit ({task.base_commit_sha.slice(0, 8)}) is no longer reachable. This usually happens after a force-push. Runs for this task will fail.
              </p>
            </div>
          </div>
        )}

        {/* Metadata */}
        <section className="space-y-3">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Details</h2>
          <Card>
            <CardContent className="space-y-3">
              <div className="grid gap-4 md:grid-cols-2">
                <div>
                  <span className="text-xs text-muted-foreground">Complexity</span>
                  <div className="mt-1">
                    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${complexityStyle.color}`}>
                      {complexityStyle.label}
                    </span>
                  </div>
                </div>
                <div>
                  <span className="text-xs text-muted-foreground">Source</span>
                  <p className="text-xs">
                    {evalSourceConfig[task.source].label}
                    {task.source_pr_number ? ` (PR #${task.source_pr_number})` : ""}
                  </p>
                </div>
                <div>
                  <span className="text-xs text-muted-foreground">Base commit</span>
                  <p className="text-xs font-mono">{task.base_commit_sha.slice(0, 8)}</p>
                </div>
                <div>
                  <span className="text-xs text-muted-foreground">Pass threshold</span>
                  <p className="text-xs">{(task.pass_threshold * 100).toFixed(0)}%</p>
                </div>
              </div>
              {task.tags && task.tags.length > 0 && (
                <div>
                  <span className="text-xs text-muted-foreground">Tags</span>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {task.tags.map((tag) => (
                      <Badge key={tag} variant="secondary" className="text-xs">{tag}</Badge>
                    ))}
                  </div>
                </div>
              )}
            </CardContent>
          </Card>
        </section>

        {/* Issue description */}
        <section className="space-y-3">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Issue description</h2>
          <Card>
            <CardContent>
              <p className="text-xs whitespace-pre-wrap">{task.issue_description}</p>
            </CardContent>
          </Card>
        </section>

        {/* Scoring criteria */}
        <section className="space-y-3">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Scoring criteria ({criteria.length})
          </h2>
          <Card>
            <CardContent className="p-0">
              {criteria.map((criterion, idx) => (
                <div
                  key={idx}
                  className="flex items-start justify-between py-3 px-4 border-b border-border last:border-b-0"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-medium">{criterion.name}</span>
                      <Badge variant="secondary" className="text-xs">
                        {criterion.grader_type === "code_check" ? "Code check" : "LLM judge"}
                      </Badge>
                      {criterion.required && (
                        <Badge variant="destructive" className="text-xs">Required</Badge>
                      )}
                    </div>
                    <p className="mt-0.5 text-xs text-muted-foreground">{criterion.notes}</p>
                  </div>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0 ml-4">
                    Weight: {criterion.weight}
                  </span>
                </div>
              ))}
            </CardContent>
          </Card>
        </section>

        {/* Runs */}
        <section className="space-y-3">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Runs ({runs.length})
          </h2>
          {runs.length === 0 ? (
            <Card>
              <CardContent className="py-8 text-center">
                <p className="text-sm text-muted-foreground">No runs yet. Click &quot;Run eval&quot; to start one.</p>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="p-0">
                <div className="flex items-center px-4 py-2 border-b border-border bg-muted/30 text-xs font-medium text-muted-foreground uppercase tracking-wider">
                  <span className="w-24">Status</span>
                  <span className="flex-1">Input</span>
                  <span className="w-24 text-right">Session</span>
                  <span className="w-20 text-right">Score</span>
                  <span className="w-16 text-right">Pass</span>
                  <span className="w-28 text-right">Artifacts</span>
                  <span className="w-24 text-right">Duration</span>
                </div>
                {runs.map((run) => (
                  <EvalRunRow key={run.id} run={run} />
                ))}
              </CardContent>
            </Card>
          )}
        </section>
      </div>
    </PageContainer>
  );
}

function EvalRunRow({ run }: { run: EvalRun }) {
  const statusStyle = evalRunStatusConfig[run.status];
  const baseCommit = typeof run.input_manifest?.base_commit_sha === "string"
    ? run.input_manifest.base_commit_sha
    : undefined;
  const criterionCount = Array.isArray(run.criterion_results) ? run.criterion_results.length : 0;
  return (
    <div className="flex items-center py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors">
      <span className="w-24">
        <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${statusStyle.color}`}>
          {statusStyle.label}
        </span>
      </span>
      <span className="flex-1 min-w-0">
        <span className="block truncate text-xs font-mono">{run.model}</span>
        <span className="block truncate text-xs text-muted-foreground">
          {baseCommit ? `base ${baseCommit.slice(0, 8)}` : "base from task"}
          {run.config_ref ? ` - config ${run.config_ref}` : ""}
          {run.error_message ? ` - ${run.error_message}` : ""}
        </span>
      </span>
      <span className="w-24 text-right">
        {run.session_id ? (
          <span className="inline-flex justify-end gap-1">
            <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" asChild>
              <Link href={`/sessions/${run.session_id}`}>
                <ExternalLink className="mr-1 h-3 w-3" />
                Open
              </Link>
            </Button>
            <Button variant="ghost" size="icon" className="h-7 w-7" title="Open preview" asChild>
              <Link href={`/sessions/${run.session_id}?preview=1`}>
                <MonitorPlay className="h-3.5 w-3.5" />
              </Link>
            </Button>
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">-</span>
        )}
      </span>
      <span className="w-20 text-right text-xs tabular-nums">
        {run.final_score != null ? `${(run.final_score * 100).toFixed(0)}%` : "-"}
      </span>
      <span className="w-16 text-right text-xs">
        {run.passed != null ? (
          run.passed ? (
            <span className="text-success">Pass</span>
          ) : (
            <span className="text-destructive">Fail</span>
          )
        ) : "-"}
      </span>
      <span className="w-28 text-right text-xs text-muted-foreground">
        <span className="inline-flex items-center justify-end gap-1">
          <FileText className="h-3 w-3" />
          {run.agent_diff ? "diff" : "no diff"}
          {criterionCount > 0 ? ` - ${criterionCount} checks` : ""}
        </span>
      </span>
      <span className="w-24 text-right text-xs text-muted-foreground tabular-nums">
        {run.duration_seconds != null ? `${run.duration_seconds}s` : "-"}
      </span>
    </div>
  );
}

function RunEvalDialog({ taskId }: { taskId: string }) {
  const queryClient = useQueryClient();
  const [model, setModel] = useState("claude-sonnet-4-6");
  const [configRef, setConfigRef] = useState("");
  const [open, setOpen] = useState(false);

  const runMutation = useMutation({
    mutationFn: () =>
      api.evals.startRun(taskId, {
        model,
        config_ref: configRef || undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.evals.runs(taskId) });
      setOpen(false);
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Play className="mr-1 h-3.5 w-3.5" />
          Run eval
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Run eval</DialogTitle>
          <DialogDescription>
            Configure and start an eval run for this task.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="run-model">Model</Label>
            <Select value={model} onValueChange={setModel}>
              <SelectTrigger id="run-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="claude-opus-4-6">claude-opus-4-6</SelectItem>
                <SelectItem value="claude-sonnet-4-6">claude-sonnet-4-6</SelectItem>
                <SelectItem value="codex">codex</SelectItem>
                <SelectItem value="gemini-cli">gemini-cli</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="run-config-ref">Config overlay (optional)</Label>
            <Input
              id="run-config-ref"
              placeholder="Branch name or SHA"
              value={configRef}
              onChange={(e) => setConfigRef(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Overlay repo config files (AGENTS.md, CLAUDE.md, .claude/, .143/) from a different branch.
            </p>
          </div>
        </div>
        {runMutation.isError && (
          <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
            Failed to start eval run. Please try again.
          </div>
        )}
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
          <Button onClick={() => runMutation.mutate()} disabled={runMutation.isPending}>
            {runMutation.isPending ? "Starting..." : "Start run"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
