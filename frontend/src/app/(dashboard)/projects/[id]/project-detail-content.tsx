"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  FileText,
  GitPullRequest,
  Settings,
} from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { projectStatusConfig } from "@/lib/types";
import { ProgressBar } from "./shared";
import { PlanTab } from "./plan-tab";
import { WorkTab } from "./work-tab";

// ─── Settings Tab ────────────────────────────────────────────────────────────

function SettingsTab({ project }: { project: import("@/lib/types").Project }) {
  const queryClient = useQueryClient();
  const [goal, setGoal] = useState(project.goal);
  const [scope, setScope] = useState(project.scope ?? "");
  const [completionCriteria, setCompletionCriteria] = useState(project.completion_criteria ?? "");
  const [executionMode, setExecutionMode] = useState(project.execution_mode);
  const [maxConcurrent, setMaxConcurrent] = useState(project.max_concurrent);
  const [priority, setPriority] = useState(project.priority);
  const [baseBranch, setBaseBranch] = useState(project.base_branch);

  const updateMutation = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.projects.update(project.id, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  const lifecycleMutation = useMutation({
    mutationFn: (action: string) => {
      switch (action) {
        case "start": return api.projects.start(project.id);
        case "pause": return api.projects.pause(project.id);
        case "resume": return api.projects.resume(project.id);
        case "cancel": return api.projects.update(project.id, { status: "cancelled" });
        default: return Promise.resolve();
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader><CardTitle className="text-sm">Lifecycle</CardTitle></CardHeader>
        <CardContent className="flex items-center gap-2">
          {(project.status === "draft" || project.status === "planning") && (
            <Button size="sm" onClick={() => lifecycleMutation.mutate("start")} disabled={lifecycleMutation.isPending}>Start Project</Button>
          )}
          {project.status === "active" && (
            <Button size="sm" variant="outline" onClick={() => lifecycleMutation.mutate("pause")} disabled={lifecycleMutation.isPending}>Pause</Button>
          )}
          {project.status === "paused" && (
            <Button size="sm" onClick={() => lifecycleMutation.mutate("resume")} disabled={lifecycleMutation.isPending}>Resume</Button>
          )}
          {project.status !== "completed" && project.status !== "cancelled" && (
            <Button size="sm" variant="destructive" onClick={() => lifecycleMutation.mutate("cancel")} disabled={lifecycleMutation.isPending}>Cancel Project</Button>
          )}
          {lifecycleMutation.isError && <p className="text-xs text-destructive">Action failed.</p>}
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-sm">Project Configuration</CardTitle></CardHeader>
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
            <Label htmlFor="s-criteria">Completion Criteria</Label>
            <Textarea id="s-criteria" value={completionCriteria} onChange={(e) => setCompletionCriteria(e.target.value)} rows={2} />
          </div>
          <div className="space-y-2">
            <Label>Execution Mode</Label>
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
              <Label htmlFor="s-max">Max Concurrent</Label>
              <Input id="s-max" type="number" min={1} max={10} value={maxConcurrent} onChange={(e) => setMaxConcurrent(Number(e.target.value))} />
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor="s-priority">Priority (0-100)</Label>
            <Input id="s-priority" type="number" min={0} max={100} value={priority} onChange={(e) => setPriority(Number(e.target.value))} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="s-branch">Base Branch</Label>
            <Input id="s-branch" value={baseBranch} onChange={(e) => setBaseBranch(e.target.value)} />
          </div>
          <div className="flex items-center gap-3 pt-2">
            <Button size="sm" onClick={() => updateMutation.mutate({
              goal: goal.trim(), scope: scope.trim() || null, completion_criteria: completionCriteria.trim() || null,
              execution_mode: executionMode, max_concurrent: maxConcurrent, priority, base_branch: baseBranch.trim(),
            })} disabled={updateMutation.isPending}>
              {updateMutation.isPending ? "Saving..." : "Save Changes"}
            </Button>
            {updateMutation.isError && <p className="text-xs text-destructive">Failed to save.</p>}
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

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Link href="/projects" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to projects
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">Loading project...</CardContent>
        </Card>
      </div>
    );
  }

  if (error || !detail) {
    return (
      <div className="space-y-6">
        <Link href="/projects" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to projects
        </Link>
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

  return (
    <div className="space-y-6">
      <Link href="/projects" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Back to projects
      </Link>

      <div>
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-semibold text-foreground">{project.title}</h1>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
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
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">{project.execution_mode}</Badge>
          {runningCount > 0 && <span className="text-blue-600">{runningCount} running</span>}
          {prCount > 0 && <span className="text-green-600">{prCount} PRs</span>}
          {specs.length > 0 && <span>{specs.length} specs</span>}
          {attachments.length > 0 && <span>{attachments.length} designs</span>}
          {project.current_phase && <span>Phase: {project.current_phase}</span>}
        </div>
      </div>

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
