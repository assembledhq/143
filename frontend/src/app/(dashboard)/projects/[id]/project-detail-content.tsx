"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Clock,
  FileText,
  GitPullRequest,
  Settings,
  Target,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import { projectStatusConfig } from "@/lib/types";
import { BranchPicker } from "@/components/branch-picker";
import { ProgressBar } from "./shared";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { PlanTab } from "./plan-tab";
import { WorkTab } from "./work-tab";
import { MobileBackButton } from "@/components/mobile-back-button";
import { usePageTitle } from "@/hooks/use-page-title";

const PRIORITY_OPTIONS = [
  { value: "low", label: "Low", numeric: 75 },
  { value: "medium", label: "Medium", numeric: 50 },
  { value: "high", label: "High", numeric: 25 },
  { value: "critical", label: "Critical", numeric: 0 },
] as const;

type PriorityLevel = (typeof PRIORITY_OPTIONS)[number]["value"];

function numericToPriorityLevel(n: number): PriorityLevel {
  if (n <= 12) return "critical";
  if (n <= 37) return "high";
  if (n <= 62) return "medium";
  return "low";
}

function priorityLevelToNumeric(level: PriorityLevel): number {
  return PRIORITY_OPTIONS.find((o) => o.value === level)!.numeric;
}

// ─── Settings Tab ────────────────────────────────────────────────────────────

function SettingsTab({ project }: { project: import("@/lib/types").Project }) {
  const queryClient = useQueryClient();
  const [goal, setGoal] = useState(project.goal);
  const [scope, setScope] = useState(project.scope ?? "");
  const [completionCriteria, setCompletionCriteria] = useState(project.completion_criteria ?? "");
  const [executionMode, setExecutionMode] = useState(project.execution_mode);
  const [maxConcurrent, setMaxConcurrent] = useState(project.max_concurrent);
  const [priorityLevel, setPriorityLevel] = useState<PriorityLevel>(numericToPriorityLevel(project.priority));
  const [baseBranch, setBaseBranch] = useState(project.base_branch);
  const updateMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.projects.update(project.id, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  const startMutation = useMutation({
    mutationFn: () => api.projects.start(project.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });
  const completeMutation = useMutation({
    mutationFn: () => api.projects.update(project.id, { status: "completed" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader><CardTitle className="text-sm">Lifecycle</CardTitle></CardHeader>
        <CardContent className="flex items-center gap-2">
          {project.status === "draft" && (
            <Button size="sm" onClick={() => startMutation.mutate()} disabled={startMutation.isPending}>Start project</Button>
          )}
          {project.status !== "completed" && (
            <Button size="sm" variant="outline" onClick={() => completeMutation.mutate()} disabled={completeMutation.isPending}>Mark done</Button>
          )}
          {(startMutation.isError || completeMutation.isError) && <p className="text-xs text-destructive">Action failed.</p>}
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-sm">Project configuration</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="s-goal">Goal</Label>
            <Textarea id="s-goal" value={goal} onChange={(e) => setGoal(e.target.value)} rows={2} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="s-scope">Scope</Label>
            <Textarea id="s-scope" value={scope} onChange={(e) => setScope(e.target.value)} rows={2} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="s-criteria">Completion criteria</Label>
            <Textarea id="s-criteria" value={completionCriteria} onChange={(e) => setCompletionCriteria(e.target.value)} rows={2} />
          </div>
          <div className="space-y-2">
            <Label>Execution mode</Label>
            <RadioGroup value={executionMode} onValueChange={(v) => setExecutionMode(v as "sequential" | "parallel" | "dependency_graph")} className="flex gap-4">
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="sequential" id="s-seq" /><Label htmlFor="s-seq" className="font-normal">Sequential</Label>
              </div>
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="parallel" id="s-par" /><Label htmlFor="s-par" className="font-normal">Parallel</Label>
              </div>
            </RadioGroup>
          </div>
          {executionMode === "parallel" && (
            <div className="space-y-2">
              <Label htmlFor="s-max">Max concurrent</Label>
              <Input id="s-max" type="number" min={1} max={10} value={maxConcurrent} onChange={(e) => setMaxConcurrent(Number(e.target.value))} />
            </div>
          )}
          <div className="space-y-2">
            <Label>Priority</Label>
            <Select value={priorityLevel} onValueChange={(v) => setPriorityLevel(v as PriorityLevel)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PRIORITY_OPTIONS.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>Base branch</Label>
            <BranchPicker
              repositoryId={project.repository_id}
              value={baseBranch}
              defaultBranch={project.base_branch}
              onValueChange={setBaseBranch}
              label="Base branch"
              buttonClassName="w-full justify-between"
              contentClassName="w-[var(--radix-popover-trigger-width)]"
            />
          </div>
          <div className="flex items-center justify-end gap-3 pt-2">
            {updateMutation.isError && <p className="text-xs text-destructive">Failed to save.</p>}
            <Button size="sm" onClick={() => updateMutation.mutate({
              goal: goal.trim(), scope: scope.trim() || null, completion_criteria: completionCriteria.trim() || null,
              execution_mode: executionMode, max_concurrent: maxConcurrent, priority: priorityLevelToNumeric(priorityLevel), base_branch: baseBranch.trim(),
            })} disabled={updateMutation.isPending}>
              {updateMutation.isPending ? "Saving..." : "Save changes"}
            </Button>
          </div>
        </CardContent>
      </Card>

    </div>
  );
}

// ─── Main Component ──────────────────────────────────────────────────────────

export function ProjectDetailContent({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["project", id],
    queryFn: () => api.projects.get(id),
    refetchInterval: (query) => {
      const detail = query.state.data?.data;
      if (detail && detail.project.status === "active") return 5000;
      return false;
    },
  });

  const detail = data?.data;
  usePageTitle(detail?.project.title, "Project");

  if (isLoading) {
    return (
      <div className="p-6">
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">Loading project...</CardContent>
        </Card>
      </div>
    );
  }

  if (error || !detail) {
    return (
      <div className="p-6">
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">Failed to load project details.</CardContent>
        </Card>
      </div>
    );
  }

  const { project, tasks, recent_cycles, attachments, specs } = detail;
  const status = projectStatusConfig[project.status] || projectStatusConfig.draft;
  const isActive = project.status === "active";

  const runningCount = tasks.filter((t) => t.status === "running").length;
  const prCount = tasks.filter((t) => t.pr_url).length;
  const blockedTasks = tasks.filter((t) => t.status === "failed" || t.status === "blocked");
  const lastActivity = project.updated_at || project.created_at;

  return (
    <div className="p-6 space-y-6">
      <div>
        <div className="flex items-center gap-3">
          <MobileBackButton to="/projects" label="Back to projects" />
          <h1 className="text-lg font-semibold text-foreground">{project.title}</h1>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${status.color}`}>
            {isActive && (
              <span className="relative mr-1.5 flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
            )}
            {status.label}
          </span>
        </div>

        <div className="mt-2">
          <ProgressBar completed={project.completed_tasks} total={project.total_tasks} />
        </div>

        <div className="mt-2 flex items-center gap-3 text-xs text-muted-foreground flex-wrap">
          <Badge variant="outline" className="text-xs px-1.5 py-0">{project.execution_mode}</Badge>
          {runningCount > 0 && <span className="text-blue-600 dark:text-blue-400">{runningCount} running</span>}
          {prCount > 0 && <span className="text-emerald-600 dark:text-emerald-400">{prCount} PRs</span>}
          {specs.length > 0 && <span>{specs.length} specs</span>}
          {attachments.length > 0 && <span>{attachments.length} designs</span>}
          {project.current_phase && <span>Phase: {project.current_phase}</span>}
        </div>
        <div className="mt-1.5">
          <AuditLogTrigger
            filters={{ project_id: project.id }}
            title="Project activity"
          />
        </div>
      </div>

      {/* At a glance summary */}
      <Card>
        <CardContent className="py-4 space-y-3">
          {project.goal && (
            <div className="flex gap-2">
              <Target className="h-4 w-4 mt-0.5 flex-shrink-0 text-muted-foreground" />
              <p className="text-sm text-foreground line-clamp-2">{project.goal}</p>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted-foreground">
            <span>
              {project.completed_tasks} of {project.total_tasks} tasks complete
              {project.failed_tasks > 0 && (
                <span className="text-red-600 dark:text-red-400"> &middot; {project.failed_tasks} failed</span>
              )}
            </span>

            {blockedTasks.length > 0 && (
              <span className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
                <AlertTriangle className="h-3 w-3" />
                {blockedTasks.length} {blockedTasks.length === 1 ? "task needs" : "tasks need"} attention
              </span>
            )}

            {lastActivity && (
              <span className="inline-flex items-center gap-1">
                <Clock className="h-3 w-3" />
                Last activity {new Date(lastActivity).toLocaleDateString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}
              </span>
            )}
          </div>
        </CardContent>
      </Card>

      <Tabs defaultValue="work">
        <TabsList>
          <TabsTrigger value="plan" className="gap-1.5">
            <FileText className="h-3 w-3" />
            Plan
          </TabsTrigger>
          <TabsTrigger value="work" className="gap-1.5">
            <GitPullRequest className="h-3 w-3" />
            Work
          </TabsTrigger>
          <TabsTrigger value="settings" className="gap-1.5">
            <Settings className="h-3 w-3" />
            Settings
          </TabsTrigger>
        </TabsList>

        <TabsContent value="plan">
          <PlanTab project={project} specs={specs} attachments={attachments} />
        </TabsContent>

        <TabsContent value="work">
          <WorkTab project={project} tasks={tasks} cycles={recent_cycles} />
        </TabsContent>

        <TabsContent value="settings">
          <SettingsTab project={project} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
