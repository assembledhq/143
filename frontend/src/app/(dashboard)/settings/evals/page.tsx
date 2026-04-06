"use client";

import { useState } from "react";
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
import { SettingsPageFrame } from "@/components/settings-page-frame";
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
import { FlaskConical, Plus, Loader2, GitPullRequest, AlertTriangle, Layers } from "lucide-react";
import type { EvalTask, EvalBatch, EvalTaskSource, ListResponse, Repository } from "@/lib/types";
import { evalComplexityConfig, evalSourceConfig } from "@/lib/types";

type SourceFilter = "all" | EvalTaskSource | "archived";

export default function EvalsSettingsPage() {
  const queryClient = useQueryClient();
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("all");

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

  const bootstrapMutation = useMutation({
    mutationFn: (repoId: string) => api.evals.bootstrap({ repo_id: repoId }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["evals"] });
    },
  });

  const [bootstrapRepoId, setBootstrapRepoId] = useState<string>("");
  const repos = reposResponse?.data ?? [];

  const filterTabs: { value: SourceFilter; label: string }[] = [
    { value: "all", label: "All" },
    { value: "manual", label: "Manual" },
    { value: "pr_bootstrap", label: "PR bootstrapped" },
    { value: "archived", label: "Archived" },
  ];

  return (
    <SettingsPageFrame
      title="Evals"
      description="Create, manage, and run eval tasks to measure agent quality."
    >
      {/* Actions */}
      <div className="flex items-center gap-3">
        <Button size="sm" asChild>
          <Link href="/settings/evals/new">
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Create eval task
          </Link>
        </Button>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm">
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
          title="No eval tasks yet"
          description="Create eval tasks manually or bootstrap them from your PR history to start measuring agent quality."
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

      {/* Bootstrap candidates banner */}
      <BootstrapCandidatesBanner />
    </SettingsPageFrame>
  );
}

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
          <span className="text-[13px] font-medium text-foreground truncate">{task.name}</span>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${complexityStyle.color}`}>
            {complexityStyle.label}
          </span>
          {task.snapshot_broken && (
            <span className="inline-flex items-center gap-1 rounded-full bg-orange-500/10 px-2 py-0.5 text-[11px] font-medium text-orange-700 dark:text-orange-400">
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
                  <span className="text-[13px] font-medium">{batch.name || "Unnamed batch"}</span>
                  <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${
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

function BootstrapCandidatesBanner() {
  const { data: bootstrapResponse } = useQuery({
    queryKey: queryKeys.evals.bootstrapCandidates,
    queryFn: () => api.evals.getBootstrapCandidates(),
    retry: false,
  });

  const bootstrap = bootstrapResponse?.data;
  if (!bootstrap || bootstrap.status !== "completed" || !bootstrap.candidates?.length) return null;

  return (
    <Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
      <CardContent className="flex items-center justify-between py-3">
        <div>
          <p className="text-[13px] font-medium text-blue-800 dark:text-blue-300">
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
