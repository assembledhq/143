"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Plus,
  RotateCcw,
  ExternalLink,
  Image,
  FileText,
  LayoutGrid,
  GitPullRequest,
  Sparkles,
  Trash2,
  Pencil,
  Save,
  X,
  ChevronRight,
  Clock,
  AlertCircle,
  CheckCircle2,
  Circle,
  Loader2,
  Ban,
  Pause,
  ArrowUpRight,
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
import type {
  Project,
  ProjectTask,
  ProjectCycle,
  ProjectAttachment,
  ProjectSpec,
  AISuggestion,
} from "@/lib/types";

const taskStatusConfig: Record<
  string,
  { color: string; label: string; icon: typeof Circle }
> = {
  pending: { color: "bg-gray-100 text-gray-800", label: "Pending", icon: Circle },
  blocked: { color: "bg-yellow-100 text-yellow-800", label: "Blocked", icon: Pause },
  delegated: { color: "bg-indigo-100 text-indigo-800", label: "Delegated", icon: ArrowUpRight },
  running: { color: "bg-blue-100 text-blue-800", label: "Running", icon: Loader2 },
  completed: { color: "bg-green-100 text-green-800", label: "Completed", icon: CheckCircle2 },
  failed: { color: "bg-red-100 text-red-800", label: "Failed", icon: AlertCircle },
  skipped: { color: "bg-gray-100 text-gray-700", label: "Skipped", icon: Ban },
  cancelled: { color: "bg-gray-100 text-gray-700", label: "Cancelled", icon: Ban },
};

const specTypeConfig: Record<string, { label: string; color: string }> = {
  prd: { label: "PRD", color: "bg-blue-100 text-blue-800" },
  technical: { label: "Technical", color: "bg-purple-100 text-purple-800" },
  design: { label: "Design", color: "bg-pink-100 text-pink-800" },
  user_story: { label: "User Story", color: "bg-green-100 text-green-800" },
};

const attachmentCategoryConfig: Record<string, { label: string; color: string }> = {
  screenshot: { label: "Screenshot", color: "bg-blue-100 text-blue-800" },
  mockup: { label: "Mockup", color: "bg-purple-100 text-purple-800" },
  wireframe: { label: "Wireframe", color: "bg-orange-100 text-orange-800" },
  reference: { label: "Reference", color: "bg-gray-100 text-gray-800" },
};

function formatTimestamp(dateStr?: string): string {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
}

function formatRelativeTime(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
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

// ─── Overview Tab ────────────────────────────────────────────────────────────

function OverviewTab({
  project,
  tasks,
  cycles,
  attachments,
  specs,
}: {
  project: Project;
  tasks: ProjectTask[];
  cycles: ProjectCycle[];
  attachments: ProjectAttachment[];
  specs: ProjectSpec[];
}) {
  const runningTasks = tasks.filter((t) => t.status === "running").length;
  const pendingTasks = tasks.filter((t) => t.status === "pending").length;
  const failedTasks = tasks.filter((t) => t.status === "failed").length;
  const tasksWithPRs = tasks.filter((t) => t.pr_url).length;

  return (
    <div className="space-y-6">
      {/* Stats row */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <Card>
          <CardContent className="pt-4 pb-4">
            <div className="text-2xl font-bold">{tasks.length}</div>
            <div className="text-xs text-muted-foreground">Total Tasks</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4 pb-4">
            <div className="text-2xl font-bold text-blue-600">{runningTasks}</div>
            <div className="text-xs text-muted-foreground">Running</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4 pb-4">
            <div className="text-2xl font-bold text-green-600">{tasksWithPRs}</div>
            <div className="text-xs text-muted-foreground">PRs Created</div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4 pb-4">
            <div className="text-2xl font-bold text-red-600">{failedTasks}</div>
            <div className="text-xs text-muted-foreground">Failed</div>
          </CardContent>
        </Card>
      </div>

      {/* Goal and scope */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Project Goal</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          <p>{project.goal}</p>
          {project.scope && (
            <div>
              <span className="text-xs font-medium text-muted-foreground">Scope:</span>
              <p className="mt-1">{project.scope}</p>
            </div>
          )}
          {project.completion_criteria && (
            <div>
              <span className="text-xs font-medium text-muted-foreground">Completion Criteria:</span>
              <p className="mt-1">{project.completion_criteria}</p>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Quick status cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-1.5">
              <FileText className="h-3.5 w-3.5" />
              Specs ({specs.length})
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm">
            {specs.length === 0 ? (
              <p className="text-muted-foreground text-xs">No specs yet</p>
            ) : (
              <div className="space-y-1">
                {specs.slice(0, 3).map((s) => (
                  <div key={s.id} className="flex items-center gap-2">
                    <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${specTypeConfig[s.spec_type]?.color || "bg-gray-100 text-gray-800"}`}>
                      {specTypeConfig[s.spec_type]?.label || s.spec_type}
                    </span>
                    <span className="text-xs truncate">{s.title}</span>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-1.5">
              <Image className="h-3.5 w-3.5" />
              Designs ({attachments.length})
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm">
            {attachments.length === 0 ? (
              <p className="text-muted-foreground text-xs">No designs yet</p>
            ) : (
              <div className="space-y-1">
                {attachments.slice(0, 3).map((a) => (
                  <div key={a.id} className="flex items-center gap-2">
                    <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${attachmentCategoryConfig[a.category]?.color || "bg-gray-100 text-gray-800"}`}>
                      {attachmentCategoryConfig[a.category]?.label || a.category}
                    </span>
                    <span className="text-xs truncate">{a.file_name}</span>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-1.5">
              <Clock className="h-3.5 w-3.5" />
              Recent Activity
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm">
            {cycles.length === 0 ? (
              <p className="text-muted-foreground text-xs">No activity yet</p>
            ) : (
              <div className="space-y-1">
                {cycles.slice(0, 3).map((c) => (
                  <div key={c.id} className="text-xs text-muted-foreground">
                    Cycle #{c.cycle_number}: {c.tasks_completed_this_cycle} done, {c.tasks_created_this_cycle} created
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Lessons learned */}
      {project.lessons_learned && project.lessons_learned.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Lessons Learned</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-sm">
              {project.lessons_learned.map((lesson, i) => (
                <li key={i} className="flex items-start gap-2">
                  <ChevronRight className="h-3.5 w-3.5 mt-0.5 text-muted-foreground flex-shrink-0" />
                  <span>{lesson}</span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

// ─── Designs Tab ─────────────────────────────────────────────────────────────

function DesignsTab({
  project,
  attachments,
}: {
  project: Project;
  attachments: ProjectAttachment[];
}) {
  const queryClient = useQueryClient();
  const [showAddForm, setShowAddForm] = useState(false);
  const [fileName, setFileName] = useState("");
  const [fileUrl, setFileUrl] = useState("");
  const [caption, setCaption] = useState("");
  const [category, setCategory] = useState("screenshot");

  const createMutation = useMutation({
    mutationFn: () =>
      api.projects.createAttachment(project.id, {
        file_name: fileName.trim(),
        file_url: fileUrl.trim(),
        category,
        caption: caption.trim() || undefined,
      }),
    onSuccess: () => {
      setFileName("");
      setFileUrl("");
      setCaption("");
      setCategory("screenshot");
      setShowAddForm(false);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (attachmentId: string) =>
      api.projects.deleteAttachment(project.id, attachmentId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Screenshots & Designs</h3>
        <Button size="sm" variant="outline" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="mr-1 h-3 w-3" />
          Add Design
        </Button>
      </div>

      {showAddForm && (
        <Card>
          <CardContent className="space-y-3 pt-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <Label className="text-xs">File Name</Label>
                <Input
                  value={fileName}
                  onChange={(e) => setFileName(e.target.value)}
                  placeholder="homepage-mockup.png"
                />
              </div>
              <div className="space-y-1">
                <Label className="text-xs">Category</Label>
                <select
                  value={category}
                  onChange={(e) => setCategory(e.target.value)}
                  className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
                >
                  <option value="screenshot">Screenshot</option>
                  <option value="mockup">Mockup</option>
                  <option value="wireframe">Wireframe</option>
                  <option value="reference">Reference</option>
                </select>
              </div>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Image URL</Label>
              <Input
                value={fileUrl}
                onChange={(e) => setFileUrl(e.target.value)}
                placeholder="https://..."
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Caption (optional)</Label>
              <Input
                value={caption}
                onChange={(e) => setCaption(e.target.value)}
                placeholder="Describe what this shows..."
              />
            </div>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                onClick={() => createMutation.mutate()}
                disabled={!fileName.trim() || !fileUrl.trim() || createMutation.isPending}
              >
                {createMutation.isPending ? "Adding..." : "Add"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setShowAddForm(false)}>
                Cancel
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {attachments.length === 0 && (
        <Card>
          <CardContent className="py-12 text-center">
            <Image className="h-10 w-10 mx-auto text-muted-foreground/50 mb-3" />
            <p className="text-sm text-muted-foreground">
              No designs yet. Add screenshots, mockups, or wireframes to give AI agents visual context.
            </p>
          </CardContent>
        </Card>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {attachments.map((attachment) => {
          const catCfg = attachmentCategoryConfig[attachment.category] || attachmentCategoryConfig.reference;
          return (
            <Card key={attachment.id} className="overflow-hidden">
              <div className="aspect-video bg-muted relative group">
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img
                  src={attachment.file_url}
                  alt={attachment.file_name}
                  className="w-full h-full object-cover"
                  onError={(e) => {
                    (e.target as HTMLImageElement).style.display = "none";
                  }}
                />
                <div className="absolute inset-0 bg-black/0 group-hover:bg-black/20 transition-all flex items-center justify-center opacity-0 group-hover:opacity-100">
                  <a
                    href={attachment.file_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="p-2 rounded-full bg-white/90 text-gray-800 hover:bg-white"
                  >
                    <ExternalLink className="h-4 w-4" />
                  </a>
                </div>
              </div>
              <CardContent className="p-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${catCfg.color}`}>
                      {catCfg.label}
                    </span>
                    <span className="text-xs font-medium truncate">{attachment.file_name}</span>
                  </div>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                    onClick={() => deleteMutation.mutate(attachment.id)}
                  >
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
                {attachment.caption && (
                  <p className="text-xs text-muted-foreground mt-1">{attachment.caption}</p>
                )}
                <p className="text-[10px] text-muted-foreground mt-1">
                  {formatRelativeTime(attachment.created_at)}
                </p>
              </CardContent>
            </Card>
          );
        })}
      </div>
    </div>
  );
}

// ─── Specs Tab ───────────────────────────────────────────────────────────────

function SpecsTab({
  project,
  specs,
}: {
  project: Project;
  specs: ProjectSpec[];
}) {
  const queryClient = useQueryClient();
  const [showAddForm, setShowAddForm] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [newContent, setNewContent] = useState("");
  const [newType, setNewType] = useState("prd");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editContent, setEditContent] = useState("");
  const [editTitle, setEditTitle] = useState("");

  const createMutation = useMutation({
    mutationFn: () =>
      api.projects.createSpec(project.id, {
        title: newTitle.trim(),
        content: newContent.trim(),
        spec_type: newType,
      }),
    onSuccess: () => {
      setNewTitle("");
      setNewContent("");
      setNewType("prd");
      setShowAddForm(false);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ specId, body }: { specId: string; body: Record<string, unknown> }) =>
      api.projects.updateSpec(project.id, specId, body),
    onSuccess: () => {
      setEditingId(null);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (specId: string) => api.projects.deleteSpec(project.id, specId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  function startEditing(spec: ProjectSpec) {
    setEditingId(spec.id);
    setEditTitle(spec.title);
    setEditContent(spec.content);
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Product Specs & Requirements</h3>
        <Button size="sm" variant="outline" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="mr-1 h-3 w-3" />
          Add Spec
        </Button>
      </div>

      {showAddForm && (
        <Card>
          <CardContent className="space-y-3 pt-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <Label className="text-xs">Title</Label>
                <Input
                  value={newTitle}
                  onChange={(e) => setNewTitle(e.target.value)}
                  placeholder="Product Requirements Document"
                />
              </div>
              <div className="space-y-1">
                <Label className="text-xs">Type</Label>
                <select
                  value={newType}
                  onChange={(e) => setNewType(e.target.value)}
                  className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
                >
                  <option value="prd">PRD</option>
                  <option value="technical">Technical Spec</option>
                  <option value="design">Design Spec</option>
                  <option value="user_story">User Stories</option>
                </select>
              </div>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Content (Markdown)</Label>
              <Textarea
                value={newContent}
                onChange={(e) => setNewContent(e.target.value)}
                placeholder="# Overview&#10;&#10;Describe the feature, user stories, acceptance criteria..."
                rows={12}
                className="font-mono text-xs"
              />
            </div>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                onClick={() => createMutation.mutate()}
                disabled={!newTitle.trim() || createMutation.isPending}
              >
                {createMutation.isPending ? "Creating..." : "Create Spec"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setShowAddForm(false)}>
                Cancel
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {specs.length === 0 && !showAddForm && (
        <Card>
          <CardContent className="py-12 text-center">
            <FileText className="h-10 w-10 mx-auto text-muted-foreground/50 mb-3" />
            <p className="text-sm text-muted-foreground">
              No specs yet. Add product requirements, technical specs, or user stories.
            </p>
          </CardContent>
        </Card>
      )}

      {specs.map((spec) => {
        const typeCfg = specTypeConfig[spec.spec_type] || specTypeConfig.prd;
        const isEditing = editingId === spec.id;

        return (
          <Card key={spec.id}>
            <CardHeader className="pb-2">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${typeCfg.color}`}>
                    {typeCfg.label}
                  </span>
                  {isEditing ? (
                    <Input
                      value={editTitle}
                      onChange={(e) => setEditTitle(e.target.value)}
                      className="h-7 text-sm font-semibold"
                    />
                  ) : (
                    <CardTitle className="text-sm">{spec.title}</CardTitle>
                  )}
                </div>
                <div className="flex items-center gap-1">
                  <span className="text-[10px] text-muted-foreground mr-2">v{spec.version}</span>
                  {isEditing ? (
                    <>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 w-6 p-0"
                        onClick={() => updateMutation.mutate({
                          specId: spec.id,
                          body: { title: editTitle, content: editContent },
                        })}
                        disabled={updateMutation.isPending}
                      >
                        <Save className="h-3 w-3" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 w-6 p-0"
                        onClick={() => setEditingId(null)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </>
                  ) : (
                    <>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 w-6 p-0"
                        onClick={() => startEditing(spec)}
                      >
                        <Pencil className="h-3 w-3" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                        onClick={() => deleteMutation.mutate(spec.id)}
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </>
                  )}
                </div>
              </div>
            </CardHeader>
            <CardContent>
              {isEditing ? (
                <Textarea
                  value={editContent}
                  onChange={(e) => setEditContent(e.target.value)}
                  rows={16}
                  className="font-mono text-xs"
                />
              ) : (
                <div className="prose prose-sm max-w-none">
                  <pre className="whitespace-pre-wrap text-xs bg-muted/30 rounded-md p-4 font-mono">
                    {spec.content || "(empty)"}
                  </pre>
                </div>
              )}
              <p className="text-[10px] text-muted-foreground mt-2">
                Updated {formatRelativeTime(spec.updated_at)}
              </p>
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

// ─── Board Tab (Kanban) ──────────────────────────────────────────────────────

function BoardTab({
  project,
  tasks,
}: {
  project: Project;
  tasks: ProjectTask[];
}) {
  const queryClient = useQueryClient();

  const retryMutation = useMutation({
    mutationFn: ({ taskId }: { taskId: string }) =>
      api.projects.retryTask(project.id, taskId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const columns: { key: string; label: string; statuses: string[]; color: string }[] = [
    { key: "todo", label: "To Do", statuses: ["pending", "blocked"], color: "border-t-gray-400" },
    { key: "in_progress", label: "In Progress", statuses: ["running", "delegated"], color: "border-t-blue-500" },
    { key: "done", label: "Done", statuses: ["completed"], color: "border-t-green-500" },
    { key: "failed", label: "Needs Attention", statuses: ["failed", "skipped", "cancelled"], color: "border-t-red-500" },
  ];

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold">Task Board</h3>

      {tasks.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <LayoutGrid className="h-10 w-10 mx-auto text-muted-foreground/50 mb-3" />
            <p className="text-sm text-muted-foreground">
              No tasks yet. Add tasks or let the PM agent plan them.
            </p>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
          {columns.map((col) => {
            const colTasks = tasks.filter((t) => col.statuses.includes(t.status));
            return (
              <div key={col.key} className="space-y-2">
                <div className={`border-t-2 ${col.color} rounded-t-md bg-muted/30 px-3 py-2`}>
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-semibold">{col.label}</span>
                    <Badge variant="outline" className="text-[10px] px-1 py-0">
                      {colTasks.length}
                    </Badge>
                  </div>
                </div>
                <div className="space-y-2 min-h-[100px]">
                  {colTasks.map((task) => {
                    const statusCfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
                    const StatusIcon = statusCfg.icon;
                    return (
                      <Card key={task.id} className="shadow-sm">
                        <CardContent className="p-3">
                          <div className="flex items-start gap-2">
                            <StatusIcon className={`h-3.5 w-3.5 mt-0.5 flex-shrink-0 ${task.status === "running" ? "animate-spin text-blue-500" : task.status === "completed" ? "text-green-500" : task.status === "failed" ? "text-red-500" : "text-gray-400"}`} />
                            <div className="min-w-0 flex-1">
                              <p className="text-xs font-medium truncate">{task.title}</p>
                              {task.description && (
                                <p className="text-[10px] text-muted-foreground mt-0.5 line-clamp-2">
                                  {task.description}
                                </p>
                              )}
                              <div className="mt-1.5 flex items-center gap-2 flex-wrap">
                                {task.complexity && (
                                  <Badge variant="outline" className="text-[9px] px-1 py-0">{task.complexity}</Badge>
                                )}
                                {task.pr_url && (
                                  <a
                                    href={task.pr_url}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    className="text-[10px] text-primary underline inline-flex items-center gap-0.5"
                                  >
                                    PR <ExternalLink className="h-2.5 w-2.5" />
                                  </a>
                                )}
                                {task.agent_run_id && (
                                  <Link
                                    href={`/runs/${task.agent_run_id}`}
                                    className="text-[10px] text-primary underline inline-flex items-center gap-0.5"
                                  >
                                    Run <ExternalLink className="h-2.5 w-2.5" />
                                  </Link>
                                )}
                              </div>
                              {task.status === "failed" && (
                                <Button
                                  size="sm"
                                  variant="outline"
                                  className="h-5 text-[10px] mt-1.5"
                                  onClick={() => retryMutation.mutate({ taskId: task.id })}
                                  disabled={retryMutation.isPending}
                                >
                                  <RotateCcw className="mr-0.5 h-2.5 w-2.5" />
                                  Retry
                                </Button>
                              )}
                            </div>
                          </div>
                        </CardContent>
                      </Card>
                    );
                  })}
                  {colTasks.length === 0 && (
                    <div className="rounded-md border border-dashed border-border p-4 text-center">
                      <p className="text-[10px] text-muted-foreground">No tasks</p>
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

// ─── PRs Tab ─────────────────────────────────────────────────────────────────

function PRsTab({ tasks }: { tasks: ProjectTask[] }) {
  const tasksWithPRs = tasks.filter((t) => t.pr_url);
  const tasksWithRuns = tasks.filter((t) => t.agent_run_id);

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold">Pull Requests & Agent Runs</h3>

      {tasksWithPRs.length === 0 && tasksWithRuns.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <GitPullRequest className="h-10 w-10 mx-auto text-muted-foreground/50 mb-3" />
            <p className="text-sm text-muted-foreground">
              No PRs yet. PRs will appear here as the AI agent completes tasks.
            </p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {/* PRs section */}
          {tasksWithPRs.length > 0 && (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs flex items-center gap-1.5">
                  <GitPullRequest className="h-3.5 w-3.5" />
                  Pull Requests ({tasksWithPRs.length})
                </CardTitle>
              </CardHeader>
              <CardContent className="p-0">
                {tasksWithPRs.map((task) => {
                  const statusCfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
                  return (
                    <div
                      key={task.id}
                      className="flex items-center justify-between px-4 py-3 border-b border-border last:border-b-0"
                    >
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${statusCfg.color}`}>
                            {statusCfg.label}
                          </span>
                          <span className="text-sm font-medium truncate">{task.title}</span>
                        </div>
                        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
                          {task.branch_name && <span className="font-mono">{task.branch_name}</span>}
                          {task.outcome_notes && <span>{task.outcome_notes}</span>}
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        {task.agent_run_id && (
                          <Link
                            href={`/runs/${task.agent_run_id}`}
                            className="text-xs text-primary underline inline-flex items-center gap-1"
                          >
                            View Run <ExternalLink className="h-3 w-3" />
                          </Link>
                        )}
                        {task.pr_url && (
                          <a
                            href={task.pr_url}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="text-xs text-primary underline inline-flex items-center gap-1"
                          >
                            View PR <ExternalLink className="h-3 w-3" />
                          </a>
                        )}
                      </div>
                    </div>
                  );
                })}
              </CardContent>
            </Card>
          )}

          {/* Active runs (no PR yet) */}
          {tasksWithRuns.filter((t) => !t.pr_url).length > 0 && (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="text-xs flex items-center gap-1.5">
                  <Loader2 className="h-3.5 w-3.5" />
                  Active Runs (Awaiting PR)
                </CardTitle>
              </CardHeader>
              <CardContent className="p-0">
                {tasksWithRuns
                  .filter((t) => !t.pr_url)
                  .map((task) => {
                    const statusCfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
                    return (
                      <div
                        key={task.id}
                        className="flex items-center justify-between px-4 py-3 border-b border-border last:border-b-0"
                      >
                        <div className="flex items-center gap-2">
                          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${statusCfg.color}`}>
                            {statusCfg.label}
                          </span>
                          <span className="text-sm truncate">{task.title}</span>
                        </div>
                        <Link
                          href={`/runs/${task.agent_run_id}`}
                          className="text-xs text-primary underline inline-flex items-center gap-1"
                        >
                          View Run <ExternalLink className="h-3 w-3" />
                        </Link>
                      </div>
                    );
                  })}
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}

// ─── AI Tab ──────────────────────────────────────────────────────────────────

function AITab({ project }: { project: Project }) {
  const [target, setTarget] = useState("spec");
  const [prompt, setPrompt] = useState("");
  const [suggestions, setSuggestions] = useState<AISuggestion[]>([]);
  const [summary, setSummary] = useState("");

  const improveMutation = useMutation({
    mutationFn: () =>
      api.projects.aiImprove(project.id, {
        target,
        prompt: prompt.trim() || undefined,
      }),
    onSuccess: (data) => {
      setSuggestions(data.data.suggestions);
      setSummary(data.data.summary);
    },
  });

  const priorityColors: Record<string, string> = {
    high: "bg-red-100 text-red-800",
    medium: "bg-yellow-100 text-yellow-800",
    low: "bg-gray-100 text-gray-800",
  };

  const typeIcons: Record<string, string> = {
    addition: "+",
    revision: "~",
    question: "?",
    task: "#",
  };

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold">AI Assistant</h3>
      <p className="text-xs text-muted-foreground">
        Get AI-powered suggestions to improve your project specs, designs, or task breakdown.
      </p>

      <Card>
        <CardContent className="space-y-4 pt-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <Label className="text-xs">What to improve</Label>
              <select
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
              >
                <option value="spec">Specs & Requirements</option>
                <option value="design">Designs & Screenshots</option>
                <option value="tasks">Task Breakdown</option>
                <option value="all">Everything</option>
              </select>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Additional context (optional)</Label>
              <Input
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder="Focus on mobile UX..."
              />
            </div>
          </div>
          <Button
            size="sm"
            onClick={() => improveMutation.mutate()}
            disabled={improveMutation.isPending}
          >
            {improveMutation.isPending ? (
              <>
                <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                Analyzing...
              </>
            ) : (
              <>
                <Sparkles className="mr-1 h-3 w-3" />
                Get Suggestions
              </>
            )}
          </Button>
        </CardContent>
      </Card>

      {summary && (
        <div className="text-xs text-muted-foreground bg-muted/30 rounded-md px-3 py-2">
          {summary}
        </div>
      )}

      {suggestions.length > 0 && (
        <div className="space-y-3">
          {suggestions.map((suggestion, i) => (
            <Card key={i}>
              <CardContent className="p-4">
                <div className="flex items-start gap-3">
                  <span className="flex-shrink-0 w-6 h-6 rounded-full bg-muted flex items-center justify-center text-xs font-mono font-bold">
                    {typeIcons[suggestion.type] || "?"}
                  </span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{suggestion.title}</span>
                      <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${priorityColors[suggestion.priority] || priorityColors.medium}`}>
                        {suggestion.priority}
                      </span>
                    </div>
                    <p className="text-xs text-muted-foreground mt-1">{suggestion.description}</p>
                  </div>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {improveMutation.isError && (
        <Card>
          <CardContent className="py-4 text-center text-sm text-destructive">
            Failed to get suggestions. Please try again.
          </CardContent>
        </Card>
      )}
    </div>
  );
}

// ─── Timeline Tab ────────────────────────────────────────────────────────────

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

// ─── Settings Tab ────────────────────────────────────────────────────────────

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

// ─── Main Component ──────────────────────────────────────────────────────────

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

  const { project, tasks, recent_cycles, attachments, specs } = detail;
  const status = projectStatusConfig[project.status] || projectStatusConfig.draft;
  const isActive = project.status === "active";

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

      <Tabs defaultValue="overview">
        <TabsList className="flex-wrap">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="designs" className="gap-1">
            <Image className="h-3 w-3" />
            Designs
            {attachments.length > 0 && (
              <Badge variant="secondary" className="text-[9px] px-1 py-0 ml-0.5">{attachments.length}</Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="specs" className="gap-1">
            <FileText className="h-3 w-3" />
            Specs
            {specs.length > 0 && (
              <Badge variant="secondary" className="text-[9px] px-1 py-0 ml-0.5">{specs.length}</Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="board" className="gap-1">
            <LayoutGrid className="h-3 w-3" />
            Board
          </TabsTrigger>
          <TabsTrigger value="prs" className="gap-1">
            <GitPullRequest className="h-3 w-3" />
            PRs
            {tasks.filter((t) => t.pr_url).length > 0 && (
              <Badge variant="secondary" className="text-[9px] px-1 py-0 ml-0.5">{tasks.filter((t) => t.pr_url).length}</Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="ai" className="gap-1">
            <Sparkles className="h-3 w-3" />
            AI
          </TabsTrigger>
          <TabsTrigger value="timeline">Timeline</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <OverviewTab project={project} tasks={tasks} cycles={recent_cycles} attachments={attachments} specs={specs} />
        </TabsContent>

        <TabsContent value="designs">
          <DesignsTab project={project} attachments={attachments} />
        </TabsContent>

        <TabsContent value="specs">
          <SpecsTab project={project} specs={specs} />
        </TabsContent>

        <TabsContent value="board">
          <BoardTab project={project} tasks={tasks} />
        </TabsContent>

        <TabsContent value="prs">
          <PRsTab tasks={tasks} />
        </TabsContent>

        <TabsContent value="ai">
          <AITab project={project} />
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
