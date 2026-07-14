"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Plus,
  ExternalLink,
  Image,
  FileText,
  Sparkles,
  Trash2,
  Pencil,
  Save,
  X,
  Loader2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  Project,
  ProjectAttachment,
  ProjectSpec,
  AISuggestion,
} from "@/lib/types";
import { formatTimeAgo } from "@/lib/utils";
import {
  CollapsibleSection,
  specTypeConfig,
  attachmentCategoryConfig,
} from "./shared";

export function SpecsSection({
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
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);

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
      setPendingDeleteId(null);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  return (
    <CollapsibleSection
      title="Specs & requirements"
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
                <Select
                  value={newType}
                  onValueChange={setNewType}
                >
                  <SelectTrigger aria-label="Spec type" className="text-sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="prd">PRD</SelectItem>
                    <SelectItem value="technical">Technical Spec</SelectItem>
                    <SelectItem value="design">Design Spec</SelectItem>
                    <SelectItem value="user_story">User Stories</SelectItem>
                  </SelectContent>
                </Select>
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
                {createMutation.isPending ? "Creating..." : "Create spec"}
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
                    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${typeCfg.color}`}>
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
                    <span className="text-xs text-muted-foreground mr-2">v{spec.version}</span>
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
                        {pendingDeleteId === spec.id ? (
                          <span className="flex items-center gap-1">
                            <Button size="sm" variant="destructive" className="h-6 text-xs px-2" onClick={() => deleteMutation.mutate(spec.id)}>
                              Confirm
                            </Button>
                            <Button size="sm" variant="ghost" className="h-6 text-xs px-2" onClick={() => setPendingDeleteId(null)}>
                              Cancel
                            </Button>
                          </span>
                        ) : (
                          <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive" onClick={() => setPendingDeleteId(spec.id)}>
                            <Trash2 className="h-3 w-3" />
                          </Button>
                        )}
                      </>
                    )}
                  </div>
                </div>
              </CardHeader>
              <CardContent>
                {isEditing ? (
                  <Textarea value={editContent} onChange={(e) => setEditContent(e.target.value)} rows={16} className="font-mono text-xs" />
                ) : (
                  <pre className="whitespace-pre-wrap break-words text-xs bg-muted/30 rounded-md p-4 font-mono max-w-full">
                    {spec.content || "(empty)"}
                  </pre>
                )}
                <p className="text-xs text-muted-foreground mt-2">Updated {formatTimeAgo(spec.updated_at)}</p>
              </CardContent>
            </Card>
          );
        })}
      </div>
    </CollapsibleSection>
  );
}

export function DesignsSection({
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
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);

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
    onSuccess: () => {
      setPendingDeleteId(null);
      queryClient.invalidateQueries({ queryKey: ["project", project.id] });
    },
  });

  return (
    <CollapsibleSection
      title="Designs & screenshots"
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
                <Label className="text-xs">File name</Label>
                <Input value={fileName} onChange={(e) => setFileName(e.target.value)} placeholder="homepage-mockup.png" />
              </div>
              <div className="space-y-1">
                <Label className="text-xs">Category</Label>
                <Select value={category} onValueChange={setCategory}>
                  <SelectTrigger aria-label="Attachment category" className="text-sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="screenshot">Screenshot</SelectItem>
                    <SelectItem value="mockup">Mockup</SelectItem>
                    <SelectItem value="wireframe">Wireframe</SelectItem>
                    <SelectItem value="reference">Reference</SelectItem>
                  </SelectContent>
                </Select>
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
                  <a href={attachment.file_url} target="_blank" rel="noopener noreferrer" className="rounded-full bg-black/65 p-2 text-white backdrop-blur-sm transition-colors hover:bg-black/80">
                    <ExternalLink className="h-4 w-4" />
                  </a>
                </div>
              </div>
              <CardContent className="p-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className={`inline-flex items-center rounded-full px-1.5 py-0 text-xs font-medium ${catCfg.color}`}>{catCfg.label}</span>
                    <span className="text-xs font-medium truncate">{attachment.file_name}</span>
                  </div>
                  {pendingDeleteId === attachment.id ? (
                    <span className="flex items-center gap-1">
                      <Button size="sm" variant="destructive" className="h-6 text-xs px-2" onClick={() => deleteMutation.mutate(attachment.id)}>
                        Confirm
                      </Button>
                      <Button size="sm" variant="ghost" className="h-6 text-xs px-2" onClick={() => setPendingDeleteId(null)}>
                        Cancel
                      </Button>
                    </span>
                  ) : (
                    <Button size="sm" variant="ghost" className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive" onClick={() => setPendingDeleteId(attachment.id)}>
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  )}
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

export function AnalysisSection({ project }: { project: Project }) {
  const [target, setTarget] = useState("spec");
  const [prompt, setPrompt] = useState("");
  const [suggestions, setSuggestions] = useState<AISuggestion[]>([]);
  const [summary, setSummary] = useState("");

  const analyzeMutation = useMutation({
    mutationFn: () => api.projects.aiImprove(project.id, { target, prompt: prompt.trim() || undefined }),
    onSuccess: (data) => { setSuggestions(data.data.suggestions); setSummary(data.data.summary); },
  });

  const priorityColors: Record<string, string> = {
    high: "bg-red-500/10 text-red-700 dark:text-red-400",
    medium: "bg-yellow-500/10 text-yellow-700 dark:text-yellow-400",
    low: "bg-muted text-muted-foreground",
  };

  const typeIcons: Record<string, string> = { addition: "+", revision: "~", question: "?", task: "#" };

  return (
    <CollapsibleSection title="Project analysis" icon={Sparkles} defaultOpen={false}>
      <div className="space-y-3">
        <p className="text-xs text-muted-foreground">
          Analyze your specs, designs, and tasks for gaps and improvements.
        </p>
        <div className="flex items-center gap-3">
          <Select value={target} onValueChange={setTarget}>
            <SelectTrigger aria-label="Analysis target" className="h-8 w-32 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="spec">Specs</SelectItem>
              <SelectItem value="design">Designs</SelectItem>
              <SelectItem value="tasks">Tasks</SelectItem>
              <SelectItem value="all">Everything</SelectItem>
            </SelectContent>
          </Select>
          <Input value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="Focus on..." className="h-8 text-xs flex-1" />
          <Button size="sm" className="h-8" onClick={() => analyzeMutation.mutate()} disabled={analyzeMutation.isPending}>
            {analyzeMutation.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Sparkles className="h-3 w-3" />}
            <span className="ml-1">Analyze</span>
          </Button>
        </div>

        {summary && (
          <div className="text-xs text-muted-foreground bg-muted/30 rounded-md px-3 py-2">{summary}</div>
        )}

        {suggestions.map((s, i) => (
          <div key={i} className="flex items-start gap-2 text-xs border-l-2 border-muted pl-3 py-1">
            <span className="flex-shrink-0 w-5 h-5 rounded-full bg-muted flex items-center justify-center font-mono font-semibold text-xs">
              {typeIcons[s.type] || "?"}
            </span>
            <div>
              <div className="flex items-center gap-1.5">
                <span className="font-medium">{s.title}</span>
                <span className={`inline-flex items-center rounded-full px-1 py-0 text-xs font-medium ${priorityColors[s.priority] || priorityColors.medium}`}>{s.priority}</span>
              </div>
              <p className="text-muted-foreground mt-0.5">{s.description}</p>
            </div>
          </div>
        ))}

        {analyzeMutation.isError && (
          <p className="text-xs text-destructive">Failed to get suggestions.</p>
        )}
      </div>
    </CollapsibleSection>
  );
}

export function PlanTab({
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
      <AnalysisSection project={project} />
    </div>
  );
}
