"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Plus, RotateCcw, ExternalLink } from "lucide-react";
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
import type { Project, ProjectTask, ProjectCycle } from "@/lib/types";

const taskStatusConfig: Record<string, { color: string; label: string }> = {
  pending: { color: "bg-gray-100 text-gray-800", label: "Pending" },
  blocked: { color: "bg-yellow-100 text-yellow-800", label: "Blocked" },
  delegated: { color: "bg-indigo-100 text-indigo-800", label: "Delegated" },
  running: { color: "bg-blue-100 text-blue-800", label: "Running" },
  completed: { color: "bg-green-100 text-green-800", label: "Completed" },
  failed: { color: "bg-red-100 text-red-800", label: "Failed" },
  skipped: { color: "bg-gray-100 text-gray-700", label: "Skipped" },
  cancelled: { color: "bg-gray-100 text-gray-700", label: "Cancelled" },
};

function formatTimestamp(dateStr?: string): string {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
}

function ProgressBar({ completed, total }: { completed: number; total: number }) {
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;
  return (
    <div className="flex items-center gap-3">
      <div className="h-2 flex-1 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full rounded-full bg-primary transition-all"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-sm text-muted-foreground whitespace-nowrap">
        {completed}/{total} ({pct}%)
      </span>
    </div>
  );
}

function TasksTab({
  project,
  tasks,
}: {
  project: Project;
  tasks: ProjectTask[];
}) {
  const queryClient = useQueryClient();
  const [showAddForm, setShowAddForm] = useState(false);
  const [newTaskTitle, setNewTaskTitle] = useState("");
  const [newTaskDescription, setNewTaskDescription] = useState("");

  const retryMutation = useMutation({
    mutationFn: ({ taskId }: { taskId: string }) =>
      api.projects.retryTask(project.id, taskId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const createTaskMutation = useMutation({
    mutationFn: () =>
      api.projects.createTask(project.id, {
        title: newTaskTitle.trim(),
        description: newTaskDescription.trim() || undefined,
      }),
    onSuccess: () => {
      setNewTaskTitle("");
      setNewTaskDescription("");
      setShowAddForm(false);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const groupedTasks: Record<string, ProjectTask[]> = {};
  for (const task of tasks) {
    const s = task.status;
    if (!groupedTasks[s]) groupedTasks[s] = [];
    groupedTasks[s].push(task);
  }

  const statusOrder = ["running", "pending", "blocked", "delegated", "failed", "completed", "skipped", "cancelled"];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Tasks</h3>
        <Button size="sm" variant="outline" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="mr-1 h-3 w-3" />
          Add Task
        </Button>
      </div>

      {showAddForm && (
        <Card>
          <CardContent className="space-y-3 pt-4">
            <Input
              value={newTaskTitle}
              onChange={(e) => setNewTaskTitle(e.target.value)}
              placeholder="Task title"
            />
            <Textarea
              value={newTaskDescription}
              onChange={(e) => setNewTaskDescription(e.target.value)}
              placeholder="Description (optional)"
              rows={2}
            />
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                onClick={() => createTaskMutation.mutate()}
                disabled={newTaskTitle.trim().length === 0 || createTaskMutation.isPending}
              >
                {createTaskMutation.isPending ? "Adding..." : "Add"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setShowAddForm(false)}>
                Cancel
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {tasks.length === 0 && (
        <Card>
          <CardContent className="py-8 text-center text-sm text-muted-foreground">
            No tasks yet. Add a task or let the PM agent plan them.
          </CardContent>
        </Card>
      )}

      {statusOrder.map((status) => {
        const group = groupedTasks[status];
        if (!group || group.length === 0) return null;
        const statusCfg = taskStatusConfig[status] || taskStatusConfig.pending;
        return (
          <Card key={status}>
            <CardContent className="p-0">
              <div className="flex items-center gap-2 px-4 py-3 border-b border-border bg-muted/30">
                <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${statusCfg.color}`}>
                  {statusCfg.label}
                </span>
                <span className="text-xs text-muted-foreground">({group.length})</span>
              </div>
              {group.map((task) => (
                <div
                  key={task.id}
                  className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0"
                >
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium text-foreground truncate">
                        {task.title}
                      </span>
                      {task.complexity && (
                        <Badge variant="outline" className="text-[11px] px-1.5 py-0">{task.complexity}</Badge>
                      )}
                    </div>
                    <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
                      <span>Batch {task.batch_number}</span>
                      {task.agent_run_id && (
                        <Link href={`/runs/${task.agent_run_id}`} className="text-primary underline inline-flex items-center gap-1">
                          View run <ExternalLink className="h-3 w-3" />
                        </Link>
                      )}
                      {task.pr_url && (
                        <a href={task.pr_url} target="_blank" rel="noopener noreferrer" className="text-primary underline inline-flex items-center gap-1">
                          PR <ExternalLink className="h-3 w-3" />
                        </a>
                      )}
                    </div>
                  </div>
                  {task.status === "failed" && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => retryMutation.mutate({ taskId: task.id })}
                      disabled={retryMutation.isPending}
                    >
                      <RotateCcw className="mr-1 h-3 w-3" />
                      Retry
                    </Button>
                  )}
                </div>
              ))}
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

function TimelineTab({ cycles }: { cycles: ProjectCycle[] }) {
  if (cycles.length === 0) {
    return (
      <Card>
        <CardContent className="py-8 text-center text-sm text-muted-foreground">
          No cycles yet. The PM agent creates cycles as it works on the project.
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      {cycles.map((cycle) => (
        <Card key={cycle.id}>
          <CardHeader>
            <CardTitle className="text-sm">
              Cycle #{cycle.cycle_number}
              <span className="ml-2 text-xs font-normal text-muted-foreground">
                {formatTimestamp(cycle.created_at)}
              </span>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <p>{cycle.analysis}</p>
            <div className="flex items-center gap-4 text-xs text-muted-foreground">
              {cycle.progress_pct != null && (
                <span>Progress: {cycle.progress_pct}%</span>
              )}
              <span className="text-green-600">{cycle.tasks_completed_this_cycle} completed</span>
              {cycle.tasks_failed_this_cycle > 0 && (
                <span className="text-red-600">{cycle.tasks_failed_this_cycle} failed</span>
              )}
              {cycle.tasks_created_this_cycle > 0 && (
                <span>{cycle.tasks_created_this_cycle} created</span>
              )}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

function SettingsTab({ project }: { project: Project }) {
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  function handleSave() {
    updateMutation.mutate({
      goal: goal.trim(),
      scope: scope.trim() || null,
      completion_criteria: completionCriteria.trim() || null,
      execution_mode: executionMode,
      max_concurrent: maxConcurrent,
      priority,
      base_branch: baseBranch.trim(),
    });
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Lifecycle</CardTitle>
        </CardHeader>
        <CardContent className="flex items-center gap-2">
          {(project.status === "draft" || project.status === "planning") && (
            <Button size="sm" onClick={() => lifecycleMutation.mutate("start")} disabled={lifecycleMutation.isPending}>
              Start Project
            </Button>
          )}
          {project.status === "active" && (
            <Button size="sm" variant="outline" onClick={() => lifecycleMutation.mutate("pause")} disabled={lifecycleMutation.isPending}>
              Pause
            </Button>
          )}
          {project.status === "paused" && (
            <Button size="sm" onClick={() => lifecycleMutation.mutate("resume")} disabled={lifecycleMutation.isPending}>
              Resume
            </Button>
          )}
          {project.status !== "completed" && project.status !== "cancelled" && (
            <Button size="sm" variant="destructive" onClick={() => lifecycleMutation.mutate("cancel")} disabled={lifecycleMutation.isPending}>
              Cancel Project
            </Button>
          )}
          {lifecycleMutation.isError && (
            <p className="text-xs text-destructive">Action failed.</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Settings</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="settings-goal">Goal</Label>
            <Textarea id="settings-goal" value={goal} onChange={(e) => setGoal(e.target.value)} rows={2} />
          </div>

          <div className="space-y-2">
            <Label htmlFor="settings-scope">Scope</Label>
            <Textarea id="settings-scope" value={scope} onChange={(e) => setScope(e.target.value)} rows={2} />
          </div>

          <div className="space-y-2">
            <Label htmlFor="settings-criteria">Completion Criteria</Label>
            <Textarea id="settings-criteria" value={completionCriteria} onChange={(e) => setCompletionCriteria(e.target.value)} rows={2} />
          </div>

          <div className="space-y-2">
            <Label>Execution Mode</Label>
            <RadioGroup value={executionMode} onValueChange={(v) => setExecutionMode(v as "sequential" | "parallel" | "dependency_graph")} className="flex gap-4">
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="sequential" id="settings-exec-sequential" />
                <Label htmlFor="settings-exec-sequential" className="font-normal">Sequential</Label>
              </div>
              <div className="flex items-center space-x-2">
                <RadioGroupItem value="parallel" id="settings-exec-parallel" />
                <Label htmlFor="settings-exec-parallel" className="font-normal">Parallel</Label>
              </div>
            </RadioGroup>
          </div>

          {executionMode === "parallel" && (
            <div className="space-y-2">
              <Label htmlFor="settings-max-concurrent">Max Concurrent</Label>
              <Input
                id="settings-max-concurrent"
                type="number"
                min={1}
                max={10}
                value={maxConcurrent}
                onChange={(e) => setMaxConcurrent(Number(e.target.value))}
              />
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="settings-priority">Priority (0-100)</Label>
            <Input
              id="settings-priority"
              type="number"
              min={0}
              max={100}
              value={priority}
              onChange={(e) => setPriority(Number(e.target.value))}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="settings-base-branch">Base Branch</Label>
            <Input
              id="settings-base-branch"
              value={baseBranch}
              onChange={(e) => setBaseBranch(e.target.value)}
            />
          </div>

          <div className="flex items-center gap-3 pt-2">
            <Button size="sm" onClick={handleSave} disabled={updateMutation.isPending}>
              {updateMutation.isPending ? "Saving..." : "Save Changes"}
            </Button>
            {updateMutation.isError && (
              <p className="text-xs text-destructive">Failed to save.</p>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

export function ProjectDetailContent({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["project", id],
    queryFn: () => api.projects.get(id),
    refetchInterval: (query) => {
      const detail = query.state.data?.data;
      if (detail && detail.project.status === "active") {
        return 5000;
      }
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
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading project...
          </CardContent>
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
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load project details.
          </CardContent>
        </Card>
      </div>
    );
  }

  const { project, tasks, recent_cycles } = detail;
  const status = projectStatusConfig[project.status] || projectStatusConfig.draft;
  const isActive = project.status === "active";

  return (
    <div className="space-y-6">
      <Link href="/projects" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Back to projects
      </Link>

      <div>
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold text-foreground">{project.title}</h1>
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
        {project.current_phase && (
          <p className="mt-1 text-xs text-muted-foreground">Phase: {project.current_phase}</p>
        )}
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {project.execution_mode}
          </Badge>
          <span>Priority: {project.priority}</span>
          <span>Created: {formatTimestamp(project.created_at)}</span>
        </div>
      </div>

      <Tabs defaultValue="tasks">
        <TabsList>
          <TabsTrigger value="tasks">Tasks</TabsTrigger>
          <TabsTrigger value="timeline">Timeline</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="tasks">
          <TasksTab project={project} tasks={tasks} />
        </TabsContent>

        <TabsContent value="timeline">
          <TimelineTab cycles={recent_cycles} />
        </TabsContent>

        <TabsContent value="settings">
          <SettingsTab project={project} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
