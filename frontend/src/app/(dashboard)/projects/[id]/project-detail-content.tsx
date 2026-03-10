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
  Sparkles,
  Trash2,
  Pencil,
  Save,
  X,
  ChevronDown,
  ChevronRight,
  AlertCircle,
  CheckCircle2,
  Circle,
  Loader2,
  Ban,
  Pause,
  ArrowUpRight,
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
import type {
  Project,
  ProjectTask,
  ProjectCycle,
  ProjectAttachment,
  ProjectSpec,
  AISuggestion,
} from "@/lib/types";

// ─── Shared config & helpers ─────────────────────────────────────────────────

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

function CollapsibleSection({
  title,
  icon: Icon,
  count,
  defaultOpen = true,
  children,
  actions,
}: {
  title: string;
  icon: typeof FileText;
  count?: number;
  defaultOpen?: boolean;
  children: React.ReactNode;
  actions?: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-2 group"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
        )}
        <Icon className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-sm font-semibold">{title}</span>
        {count != null && count > 0 && (
          <Badge variant="secondary" className="text-[9px] px-1.5 py-0">
            {count}
          </Badge>
        )}
        <div className="flex-1" />
        {actions && (
          <span onClick={(e) => e.stopPropagation()}>{actions}</span>
        )}
      </button>
      {open && <div className="pl-6 pb-4">{children}</div>}
    </div>
  );
}

// ─── Plan Tab: Specs + Designs + AI ──────────────────────────────────────────

function SpecsSection({
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

  return (
    <CollapsibleSection
      title="Specs & Requirements"
      icon={FileText}
      count={specs.length}
      actions={
        <Button size="sm" variant="ghost" className="h-6 text-xs" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="h-3 w-3 mr-1" /> Add
        </Button>
      }
    >
      {showAddForm && (
        <Card className="mb-3">
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
                placeholder={"# Overview\n\nDescribe the feature, user stories, acceptance criteria..."}
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
        <p className="text-xs text-muted-foreground py-2">
          No specs yet. Add product requirements, technical specs, or user stories to define what you&apos;re building.
        </p>
      )}

      <div className="space-y-3">
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
                          size="sm" variant="ghost" className="h-6 w-6 p-0"
                          onClick={() => updateMutation.mutate({ specId: spec.id, body: { title: editTitle, content: editContent } })}
                          disabled={updateMutation.isPending}
                        >
                          <Save className="h-3 w-3" />
                        </Button>
                        <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => setEditingId(null)}>
                          <X className="h-3 w-3" />
                        </Button>
                      </>
                    ) : (
                      <>
                        <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={() => { setEditingId(spec.id); setEditTitle(spec.title); setEditContent(spec.content); }}>
                          <Pencil className="h-3 w-3" />
                        </Button>
                        <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive" onClick={() => deleteMutation.mutate(spec.id)}>
                          <Trash2 className="h-3 w-3" />
                        </Button>
                      </>
                    )}
                  </div>
                </div>
              </CardHeader>
              <CardContent>
                {isEditing ? (
                  <Textarea value={editContent} onChange={(e) => setEditContent(e.target.value)} rows={16} className="font-mono text-xs" />
                ) : (
                  <pre className="whitespace-pre-wrap text-xs bg-muted/30 rounded-md p-4 font-mono">
                    {spec.content || "(empty)"}
                  </pre>
                )}
                <p className="text-[10px] text-muted-foreground mt-2">Updated {formatRelativeTime(spec.updated_at)}</p>
              </CardContent>
            </Card>
          );
        })}
      </div>
    </CollapsibleSection>
  );
}

function DesignsSection({
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
      setFileName(""); setFileUrl(""); setCaption(""); setCategory("screenshot");
      setShowAddForm(false);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.projects.deleteAttachment(project.id, id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project", project.id] }),
  });

  return (
    <CollapsibleSection
      title="Designs & Screenshots"
      icon={Image}
      count={attachments.length}
      actions={
        <Button size="sm" variant="ghost" className="h-6 text-xs" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="h-3 w-3 mr-1" /> Add
        </Button>
      }
    >
      {showAddForm && (
        <Card className="mb-3">
          <CardContent className="space-y-3 pt-4">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <Label className="text-xs">File Name</Label>
                <Input value={fileName} onChange={(e) => setFileName(e.target.value)} placeholder="homepage-mockup.png" />
              </div>
              <div className="space-y-1">
                <Label className="text-xs">Category</Label>
                <select value={category} onChange={(e) => setCategory(e.target.value)} className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
                  <option value="screenshot">Screenshot</option>
                  <option value="mockup">Mockup</option>
                  <option value="wireframe">Wireframe</option>
                  <option value="reference">Reference</option>
                </select>
              </div>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Image URL</Label>
              <Input value={fileUrl} onChange={(e) => setFileUrl(e.target.value)} placeholder="https://..." />
            </div>
            <div className="space-y-1">
              <Label className="text-xs">Caption (optional)</Label>
              <Input value={caption} onChange={(e) => setCaption(e.target.value)} placeholder="Describe what this shows..." />
            </div>
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => createMutation.mutate()} disabled={!fileName.trim() || !fileUrl.trim() || createMutation.isPending}>
                {createMutation.isPending ? "Adding..." : "Add"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setShowAddForm(false)}>Cancel</Button>
            </div>
          </CardContent>
        </Card>
      )}

      {attachments.length === 0 && !showAddForm && (
        <p className="text-xs text-muted-foreground py-2">
          No designs yet. Add screenshots, mockups, or wireframes to give AI agents visual context.
        </p>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
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
                  onError={(e) => { (e.target as HTMLImageElement).style.display = "none"; }}
                />
                <div className="absolute inset-0 bg-black/0 group-hover:bg-black/20 transition-all flex items-center justify-center opacity-0 group-hover:opacity-100">
                  <a href={attachment.file_url} target="_blank" rel="noopener noreferrer" className="p-2 rounded-full bg-white/90 text-gray-800 hover:bg-white">
                    <ExternalLink className="h-4 w-4" />
                  </a>
                </div>
              </div>
              <CardContent className="p-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${catCfg.color}`}>{catCfg.label}</span>
                    <span className="text-xs font-medium truncate">{attachment.file_name}</span>
                  </div>
                  <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive" onClick={() => deleteMutation.mutate(attachment.id)}>
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </div>
                {attachment.caption && <p className="text-xs text-muted-foreground mt-1">{attachment.caption}</p>}
              </CardContent>
            </Card>
          );
        })}
      </div>
    </CollapsibleSection>
  );
}

function AISection({ project }: { project: Project }) {
  const [target, setTarget] = useState("spec");
  const [prompt, setPrompt] = useState("");
  const [suggestions, setSuggestions] = useState<AISuggestion[]>([]);
  const [summary, setSummary] = useState("");

  const improveMutation = useMutation({
    mutationFn: () => api.projects.aiImprove(project.id, { target, prompt: prompt.trim() || undefined }),
    onSuccess: (data) => { setSuggestions(data.data.suggestions); setSummary(data.data.summary); },
  });

  const priorityColors: Record<string, string> = {
    high: "bg-red-100 text-red-800",
    medium: "bg-yellow-100 text-yellow-800",
    low: "bg-gray-100 text-gray-800",
  };

  const typeIcons: Record<string, string> = { addition: "+", revision: "~", question: "?", task: "#" };

  return (
    <CollapsibleSection title="AI Assistant" icon={Sparkles} defaultOpen={false}>
      <div className="space-y-3">
        <p className="text-xs text-muted-foreground">
          Analyze your specs, designs, and tasks for gaps and improvements.
        </p>
        <div className="flex items-center gap-3">
          <select value={target} onChange={(e) => setTarget(e.target.value)} className="flex h-8 rounded-md border border-input bg-transparent px-2 py-1 text-xs">
            <option value="spec">Specs</option>
            <option value="design">Designs</option>
            <option value="tasks">Tasks</option>
            <option value="all">Everything</option>
          </select>
          <Input value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="Focus on..." className="h-8 text-xs flex-1" />
          <Button size="sm" className="h-8" onClick={() => improveMutation.mutate()} disabled={improveMutation.isPending}>
            {improveMutation.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Sparkles className="h-3 w-3" />}
            <span className="ml-1">Analyze</span>
          </Button>
        </div>

        {summary && (
          <div className="text-[11px] text-muted-foreground bg-muted/30 rounded-md px-3 py-2">{summary}</div>
        )}

        {suggestions.map((s, i) => (
          <div key={i} className="flex items-start gap-2 text-xs border-l-2 border-muted pl-3 py-1">
            <span className="flex-shrink-0 w-5 h-5 rounded-full bg-muted flex items-center justify-center font-mono font-bold text-[10px]">
              {typeIcons[s.type] || "?"}
            </span>
            <div>
              <div className="flex items-center gap-1.5">
                <span className="font-medium">{s.title}</span>
                <span className={`inline-flex items-center rounded-full px-1 py-0 text-[9px] font-medium ${priorityColors[s.priority] || priorityColors.medium}`}>{s.priority}</span>
              </div>
              <p className="text-muted-foreground mt-0.5">{s.description}</p>
            </div>
          </div>
        ))}

        {improveMutation.isError && (
          <p className="text-xs text-destructive">Failed to get suggestions.</p>
        )}
      </div>
    </CollapsibleSection>
  );
}

function PlanTab({
  project,
  specs,
  attachments,
}: {
  project: Project;
  specs: ProjectSpec[];
  attachments: ProjectAttachment[];
}) {
  return (
    <div className="space-y-2 divide-y divide-border">
      <SpecsSection project={project} specs={specs} />
      <DesignsSection project={project} attachments={attachments} />
      <AISection project={project} />
    </div>
  );
}

// ─── Work Tab: Board + PRs + Timeline ────────────────────────────────────────

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
    { key: "todo", label: "To Do", statuses: ["pending", "blocked"], accent: "border-t-gray-400" },
    { key: "in_progress", label: "In Progress", statuses: ["running", "delegated"], accent: "border-t-blue-500" },
    { key: "done", label: "Done", statuses: ["completed"], accent: "border-t-green-500" },
    { key: "needs_attention", label: "Needs Attention", statuses: ["failed", "skipped", "cancelled"], accent: "border-t-red-500" },
  ];

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Task Board</h3>
        <Button size="sm" variant="ghost" className="h-6 text-xs" onClick={() => setShowAddForm(!showAddForm)}>
          <Plus className="h-3 w-3 mr-1" /> Add Task
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
                    <Badge variant="outline" className="text-[10px] px-1 py-0">{colTasks.length}</Badge>
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
                            <StatusIcon className={`h-3.5 w-3.5 mt-0.5 flex-shrink-0 ${task.status === "running" ? "animate-spin text-blue-500" : task.status === "completed" ? "text-green-500" : task.status === "failed" ? "text-red-500" : "text-gray-400"}`} />
                            <div className="min-w-0 flex-1">
                              <p className="text-xs font-medium truncate">{task.title}</p>
                              {task.description && (
                                <p className="text-[10px] text-muted-foreground mt-0.5 line-clamp-2">{task.description}</p>
                              )}
                              <div className="mt-1.5 flex items-center gap-2 flex-wrap">
                                {task.complexity && (
                                  <Badge variant="outline" className="text-[9px] px-1 py-0">{task.complexity}</Badge>
                                )}
                                {task.pr_url && (
                                  <a href={task.pr_url} target="_blank" rel="noopener noreferrer" className="text-[10px] text-primary underline inline-flex items-center gap-0.5">
                                    PR <ExternalLink className="h-2.5 w-2.5" />
                                  </a>
                                )}
                                {task.agent_run_id && (
                                  <Link href={`/runs/${task.agent_run_id}`} className="text-[10px] text-primary underline inline-flex items-center gap-0.5">
                                    Run <ExternalLink className="h-2.5 w-2.5" />
                                  </Link>
                                )}
                              </div>
                              {task.status === "failed" && (
                                <Button size="sm" variant="outline" className="h-5 text-[10px] mt-1.5" onClick={() => retryMutation.mutate({ taskId: task.id })} disabled={retryMutation.isPending}>
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

function PRsSection({ tasks }: { tasks: ProjectTask[] }) {
  const tasksWithPRs = tasks.filter((t) => t.pr_url);
  if (tasksWithPRs.length === 0) return null;

  return (
    <CollapsibleSection title="Pull Requests" icon={GitPullRequest} count={tasksWithPRs.length}>
      <div className="space-y-1">
        {tasksWithPRs.map((task) => {
          const cfg = taskStatusConfig[task.status] || taskStatusConfig.pending;
          return (
            <div key={task.id} className="flex items-center justify-between py-2 border-b border-border last:border-b-0">
              <div className="flex items-center gap-2 min-w-0 flex-1">
                <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-[10px] font-medium ${cfg.color}`}>{cfg.label}</span>
                <span className="text-xs font-medium truncate">{task.title}</span>
                {task.branch_name && <span className="text-[10px] font-mono text-muted-foreground hidden md:inline">{task.branch_name}</span>}
              </div>
              <div className="flex items-center gap-2 flex-shrink-0">
                {task.agent_run_id && (
                  <Link href={`/runs/${task.agent_run_id}`} className="text-[10px] text-primary underline inline-flex items-center gap-0.5">
                    Run <ExternalLink className="h-2.5 w-2.5" />
                  </Link>
                )}
                <a href={task.pr_url!} target="_blank" rel="noopener noreferrer" className="text-[10px] text-primary underline inline-flex items-center gap-0.5">
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
    <CollapsibleSection title="Planning Cycles" icon={ArrowUpRight} count={cycles.length} defaultOpen={false}>
      <div className="space-y-3">
        {cycles.map((cycle) => (
          <div key={cycle.id} className="border-l-2 border-muted pl-3 py-1">
            <div className="flex items-center gap-2">
              <span className="text-xs font-semibold">Cycle #{cycle.cycle_number}</span>
              <span className="text-[10px] text-muted-foreground">{formatTimestamp(cycle.created_at)}</span>
            </div>
            <p className="text-xs mt-1">{cycle.analysis}</p>
            <div className="flex items-center gap-3 mt-1 text-[10px] text-muted-foreground">
              {cycle.progress_pct != null && <span>{cycle.progress_pct}% done</span>}
              <span className="text-green-600">{cycle.tasks_completed_this_cycle} completed</span>
              {cycle.tasks_failed_this_cycle > 0 && <span className="text-red-600">{cycle.tasks_failed_this_cycle} failed</span>}
              {cycle.tasks_created_this_cycle > 0 && <span>{cycle.tasks_created_this_cycle} created</span>}
            </div>
          </div>
        ))}
      </div>
    </CollapsibleSection>
  );
}

function WorkTab({
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

      {/* ── Sticky header ── */}
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

        {/* Summary chips */}
        <div className="mt-2 flex items-center gap-3 text-xs text-muted-foreground flex-wrap">
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">{project.execution_mode}</Badge>
          {runningCount > 0 && <span className="text-blue-600">{runningCount} running</span>}
          {prCount > 0 && <span className="text-green-600">{prCount} PRs</span>}
          {specs.length > 0 && <span>{specs.length} specs</span>}
          {attachments.length > 0 && <span>{attachments.length} designs</span>}
          {project.current_phase && <span>Phase: {project.current_phase}</span>}
        </div>
      </div>

      {/* ── Three tabs ── */}
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
