"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Plus,
  RotateCcw,
  ExternalLink,
  GitPullRequest,
  ArrowUpRight,
} from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Project, ProjectTask, ProjectCycle } from "@/lib/types";
import { CollapsibleSection, taskStatusConfig, formatTimestamp } from "./shared";

function BoardSection({
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
    mutationFn: ({ taskId }: { taskId: string }) => api.projects.retryTask(project.id, taskId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  const createTaskMutation = useMutation({
    mutationFn: () =>
      api.projects.createTask(project.id, {
        title: newTaskTitle.trim(),
        description: newTaskDescription.trim() || undefined,
      }),
    onSuccess: () => {
      setNewTaskTitle(""); setNewTaskDescription(""); setShowAddForm(false);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const columns: { key: string; label: string; statuses: string[]; accent: string }[] = [
    { key: "todo", label: "To do", statuses: ["pending", "blocked"], accent: "border-t-gray-400" },
    { key: "in_progress", label: "In progress", statuses: ["running", "delegated"], accent: "border-t-info" },
    { key: "done", label: "Done", statuses: ["completed"], accent: "border-t-success" },
    { key: "needs_attention", label: "Needs attention", statuses: ["failed", "skipped", "cancelled"], accent: "border-t-destructive" },
  ];

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Task board</h3>
        <Button size="sm" variant="ghost" className="h-6 text-xs" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="h-3 w-3 mr-1" /> Add task
        </Button>
      </div>

      {showAddForm && (
        <Card>
          <CardContent className="space-y-3 pt-4">
            <Input value={newTaskTitle} onChange={(e) => setNewTaskTitle(e.target.value)} placeholder="Task title" />
            <Textarea value={newTaskDescription} onChange={(e) => setNewTaskDescription(e.target.value)} placeholder="Description (optional)" rows={2} />
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => createTaskMutation.mutate()} disabled={!newTaskTitle.trim() || createTaskMutation.isPending}>
                {createTaskMutation.isPending ? "Adding..." : "Add"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setShowAddForm(false)}>Cancel</Button>
            </div>
          </CardContent>
        </Card>
      )}

      {tasks.length === 0 && !showAddForm ? (
        <p className="text-xs text-muted-foreground py-4 text-center">
          No tasks yet. Add tasks or let the PM agent plan them.
        </p>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3">
          {columns.map((col) => {
            const colTasks = tasks.filter((t) => col.statuses.includes(t.status));
            return (
              <div key={col.key} className="space-y-2">
                <div className={`border-t-2 ${col.accent} rounded-t-md bg-muted/30 px-3 py-2`}>
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-semibold">{col.label}</span>
                    <Badge variant="outline" className="text-xs px-1 py-0">{colTasks.length}</Badge>
                  </div>
                </div>
                <div className="space-y-2 min-h-[60px]">
                  {colTasks.map((task) => {
                    const cfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
                    const StatusIcon = cfg.icon;
                    return (
                      <Card key={task.id} className="shadow-sm">
                        <CardContent className="p-3">
                          <div className="flex items-start gap-2">
                            <StatusIcon className={`h-3.5 w-3.5 mt-0.5 flex-shrink-0 ${task.status === "running" ? "animate-spin text-info" : task.status === "completed" ? "text-success" : task.status === "failed" ? "text-destructive" : "text-gray-400"}`} />
                            <div className="min-w-0 flex-1">
                              <p className="text-xs font-medium truncate">{task.title}</p>
                              {task.description && (
                                <p className="text-xs text-muted-foreground mt-0.5 line-clamp-2">{task.description}</p>
                              )}
                              <div className="mt-1.5 flex items-center gap-2 flex-wrap">
                                {task.complexity && (
                                  <Badge variant="outline" className="text-xs px-1 py-0">{task.complexity}</Badge>
                                )}
                                {task.pr_url && (
                                  <a href={task.pr_url} target="_blank" rel="noopener noreferrer" className="text-xs text-primary underline inline-flex items-center gap-0.5">
                                    PR <ExternalLink className="h-2.5 w-2.5" />
                                  </a>
                                )}
                                {task.session_id && (
                                  <Link href={`/sessions/${task.session_id}`} className="text-xs text-primary underline inline-flex items-center gap-0.5">
                                    Session <ExternalLink className="h-2.5 w-2.5" />
                                  </Link>
                                )}
                              </div>
                              {task.status === "failed" && (
                                <Button size="sm" variant="outline" className="h-5 text-xs mt-1.5" onClick={() => retryMutation.mutate({ taskId: task.id })} disabled={retryMutation.isPending}>
                                  <RotateCcw className="mr-0.5 h-2.5 w-2.5" /> Retry
                                </Button>
                              )}
                            </div>
                          </div>
                        </CardContent>
                      </Card>
                    );
                  })}
                  {colTasks.length === 0 && (
                    <div className="rounded-md border border-dashed border-border p-3 text-center">
                      <p className="text-xs text-muted-foreground">No tasks</p>
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function PRsSection({ tasks }: { tasks: ProjectTask[] }) {
  const tasksWithPRs = tasks.filter((t) => t.pr_url);
  if (tasksWithPRs.length === 0) return null;

  return (
    <CollapsibleSection title="Pull requests" icon={GitPullRequest} count={tasksWithPRs.length}>
      <div className="space-y-1">
        {tasksWithPRs.map((task) => {
          const cfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
          return (
            <div key={task.id} className="flex items-center justify-between py-2 border-b border-border last:border-b-0">
              <div className="flex items-center gap-2 min-w-0 flex-1">
                <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-xs font-medium ${cfg.color}`}>{cfg.label}</span>
                <span className="text-xs font-medium truncate">{task.title}</span>
                {task.branch_name && <span className="text-xs font-mono text-muted-foreground hidden md:inline">{task.branch_name}</span>}
              </div>
              <div className="flex items-center gap-2 flex-shrink-0">
                {task.session_id && (
                  <Link href={`/sessions/${task.session_id}`} className="text-xs text-primary underline inline-flex items-center gap-0.5">
                    Run <ExternalLink className="h-2.5 w-2.5" />
                  </Link>
                )}
                <a href={task.pr_url!} target="_blank" rel="noopener noreferrer" className="text-xs text-primary underline inline-flex items-center gap-0.5">
                  PR <ExternalLink className="h-2.5 w-2.5" />
                </a>
              </div>
            </div>
          );
        })}
      </div>
    </CollapsibleSection>
  );
}

function TimelineSection({ cycles }: { cycles: ProjectCycle[] }) {
  if (cycles.length === 0) return null;

  return (
    <CollapsibleSection title="Planning cycles" icon={ArrowUpRight} count={cycles.length} defaultOpen={false}>
      <div className="space-y-3">
        {cycles.map((cycle) => (
          <div key={cycle.id} className="border-l-2 border-muted pl-3 py-1">
            <div className="flex items-center gap-2">
              <span className="text-xs font-semibold">Cycle #{cycle.cycle_number}</span>
              <span className="text-xs text-muted-foreground">{formatTimestamp(cycle.created_at)}</span>
            </div>
            <p className="text-xs mt-1">{cycle.analysis}</p>
            <div className="flex items-center gap-3 mt-1 text-xs text-muted-foreground">
              {cycle.progress_pct != null && <span>{cycle.progress_pct}% done</span>}
              <span className="text-success">{cycle.tasks_completed_this_cycle} completed</span>
              {cycle.tasks_failed_this_cycle > 0 && <span className="text-destructive">{cycle.tasks_failed_this_cycle} failed</span>}
              {cycle.tasks_created_this_cycle > 0 && <span>{cycle.tasks_created_this_cycle} created</span>}
            </div>
          </div>
        ))}
      </div>
    </CollapsibleSection>
  );
}

export function WorkTab({
  project,
  tasks,
  cycles,
}: {
  project: Project;
  tasks: ProjectTask[];
  cycles: ProjectCycle[];
}) {
  return (
    <div className="space-y-2 divide-y divide-border">
      <BoardSection project={project} tasks={tasks} />
      <PRsSection tasks={tasks} />
      <TimelineSection cycles={cycles} />
    </div>
  );
}
