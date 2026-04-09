"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { X, Plus, Pencil, Trash2, FileText, Check, ExternalLink, Link as LinkIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import type { PMDocument, ListResponse } from "@/lib/types";

/** Only allow http/https URLs to prevent javascript: XSS. */
function isSafeUrl(url: string | undefined | null): url is string {
  if (!url) return false;
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

const DOC_TYPE_LABELS: Record<string, string> = {
  roadmap: "Roadmap",
  philosophy: "Product philosophy",
  strategy: "Strategy",
  market: "Market context",
  architecture: "Architecture",
  reference: "Reference",
};

const DOC_TYPE_COLORS: Record<string, string> = {
  roadmap: "bg-blue-500/10 text-blue-700 dark:text-blue-400",
  philosophy: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
  strategy: "bg-amber-500/10 text-amber-700 dark:text-amber-400",
  market: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  architecture: "bg-cyan-500/10 text-cyan-700 dark:text-cyan-400",
  reference: "bg-muted text-muted-foreground",
};

const SOURCE_TYPE_LABELS: Record<string, string> = {
  manual: "Manual",
  url: "URL",
  notion: "Notion",
  google_docs: "Google Docs",
  confluence: "Confluence",
  file_upload: "File upload",
};

export function DocumentsManager() {
  const queryClient = useQueryClient();

  const [showDocCreate, setShowDocCreate] = useState(false);
  const [docEditingId, setDocEditingId] = useState<string | null>(null);
  const [expandedDocId, setExpandedDocId] = useState<string | null>(null);
  const [deleteConfirmId, setDeleteConfirmId] = useState<string | null>(null);
  const [docTitle, setDocTitle] = useState("");
  const [docContent, setDocContent] = useState("");
  const [docType, setDocType] = useState("roadmap");
  const [docSourceType, setDocSourceType] = useState("manual");
  const [docSourceUrl, setDocSourceUrl] = useState("");
  const [editDocTitle, setEditDocTitle] = useState("");
  const [editDocContent, setEditDocContent] = useState("");
  const [editDocType, setEditDocType] = useState("");
  const [editDocSourceUrl, setEditDocSourceUrl] = useState("");

  const { data: docsData } = useQuery<ListResponse<PMDocument>>({
    queryKey: ["pm", "documents"],
    queryFn: () => api.pm.listDocuments(),
  });
  const docs = docsData?.data ?? [];

  const createDocMutation = useMutation({
    mutationFn: (body: Parameters<typeof api.pm.createDocument>[0]) =>
      api.pm.createDocument(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setShowDocCreate(false);
      setDocTitle("");
      setDocContent("");
      setDocType("roadmap");
      setDocSourceType("manual");
      setDocSourceUrl("");
    },
  });

  const updateDocMutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      api.pm.updateDocument(id, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setDocEditingId(null);
    },
  });

  const deleteDocMutation = useMutation({
    mutationFn: (id: string) => api.pm.deleteDocument(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setDeleteConfirmId(null);
    },
  });

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-[13px] font-medium text-foreground">Reference documents</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            Roadmaps, strategy docs, and other references the PM agent reads during planning.
          </p>
        </div>
        {!showDocCreate && (
          <Button variant="outline" size="sm" onClick={() => setShowDocCreate(true)}>
            <Plus className="h-3.5 w-3.5 mr-1" />
            Add
          </Button>
        )}
      </div>

      {showDocCreate && (
        <Card>
          <CardContent>
            <div className="space-y-4">
              <div className="grid grid-cols-3 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="doc-title">Title</Label>
                  <Input
                    id="doc-title"
                    placeholder="e.g. Q1 2026 Roadmap"
                    value={docTitle}
                    onChange={(e) => setDocTitle(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="doc-type">Type</Label>
                  <Select value={docType} onValueChange={setDocType}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {Object.entries(DOC_TYPE_LABELS).map(([value, label]) => (
                        <SelectItem key={value} value={value}>{label}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="doc-source">Source</Label>
                  <Select value={docSourceType} onValueChange={setDocSourceType}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {Object.entries(SOURCE_TYPE_LABELS).map(([value, label]) => (
                        <SelectItem key={value} value={value}>{label}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              {docSourceType !== "manual" && (
                <div className="space-y-2">
                  <Label htmlFor="doc-source-url">Source URL</Label>
                  <Input
                    id="doc-source-url"
                    type="url"
                    placeholder="https://notion.so/... or link to original document"
                    value={docSourceUrl}
                    onChange={(e) => setDocSourceUrl(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    Link to the original document. Paste the content below — the PM agent uses the local copy.
                  </p>
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor="doc-content">Content</Label>
                <Textarea
                  id="doc-content"
                  placeholder="Paste your document content here (Markdown supported)..."
                  rows={8}
                  value={docContent}
                  onChange={(e) => setDocContent(e.target.value)}
                />
              </div>
              <div className="flex justify-end gap-2">
                <Button variant="outline" size="sm" onClick={() => setShowDocCreate(false)}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    if (!docTitle.trim()) return;
                    createDocMutation.mutate({
                      title: docTitle.trim(),
                      content: docContent,
                      doc_type: docType,
                      source_type: docSourceType,
                      source_url: docSourceUrl.trim() || undefined,
                    });
                  }}
                  disabled={!docTitle.trim() || createDocMutation.isPending}
                >
                  {createDocMutation.isPending ? "Saving..." : "Save document"}
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      {docs.length === 0 && !showDocCreate ? (
        <Card>
          <CardContent>
            <div className="flex flex-col items-center justify-center py-8 text-center">
              <FileText className="h-8 w-8 text-muted-foreground/40 mb-2" />
              <p className="text-xs text-muted-foreground">
                No reference documents yet. Add roadmaps, strategy docs, or product philosophy to guide the PM agent.
              </p>
            </div>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-2">
          {docs.map((doc) => (
            <Card key={doc.id}>
              <CardContent className="py-3">
                {docEditingId === doc.id ? (
                  <div className="space-y-3">
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-1.5">
                        <Label className="text-xs">Title</Label>
                        <Input value={editDocTitle} onChange={(e) => setEditDocTitle(e.target.value)} />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">Type</Label>
                        <Select value={editDocType} onValueChange={setEditDocType}>
                          <SelectTrigger><SelectValue /></SelectTrigger>
                          <SelectContent>
                            {Object.entries(DOC_TYPE_LABELS).map(([value, label]) => (
                              <SelectItem key={value} value={value}>{label}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                    </div>
                    <div className="space-y-1.5">
                      <Label className="text-xs">Source URL</Label>
                      <Input
                        type="url"
                        placeholder="https://... (optional)"
                        value={editDocSourceUrl}
                        onChange={(e) => setEditDocSourceUrl(e.target.value)}
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label className="text-xs">Content</Label>
                      <Textarea rows={8} value={editDocContent} onChange={(e) => setEditDocContent(e.target.value)} />
                    </div>
                    <div className="flex justify-end gap-2">
                      <Button variant="outline" size="sm" onClick={() => setDocEditingId(null)}>Cancel</Button>
                      <Button
                        size="sm"
                        onClick={() => {
                          if (!editDocTitle.trim()) return;
                          updateDocMutation.mutate({
                            id: doc.id,
                            body: { title: editDocTitle.trim(), content: editDocContent, doc_type: editDocType, source_url: editDocSourceUrl.trim() || null },
                          });
                        }}
                        disabled={!editDocTitle.trim() || updateDocMutation.isPending}
                      >
                        <Check className="h-3.5 w-3.5 mr-1" />
                        {updateDocMutation.isPending ? "Saving..." : "Save"}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <div>
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2 min-w-0">
                        <FileText className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                        <button
                          onClick={() => setExpandedDocId(expandedDocId === doc.id ? null : doc.id)}
                          className="text-[13px] font-medium hover:underline text-left truncate"
                        >
                          {doc.title}
                        </button>
                        <Badge variant="secondary" className={`text-xs ${DOC_TYPE_COLORS[doc.doc_type] ?? DOC_TYPE_COLORS.reference}`}>
                          {DOC_TYPE_LABELS[doc.doc_type] ?? doc.doc_type}
                        </Badge>
                        {doc.source_type !== "manual" && (
                          <Badge variant="outline" className="text-xs gap-0.5">
                            <LinkIcon className="h-2.5 w-2.5" />
                            {SOURCE_TYPE_LABELS[doc.source_type] ?? doc.source_type}
                          </Badge>
                        )}
                      </div>
                      <div className="flex items-center gap-0.5 shrink-0">
                        {isSafeUrl(doc.source_url) && (
                          <Button variant="ghost" size="sm" className="h-7 w-7 p-0" asChild>
                            <a href={doc.source_url} target="_blank" rel="noopener noreferrer">
                              <ExternalLink className="h-3 w-3" />
                            </a>
                          </Button>
                        )}
                        <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => {
                          setDocEditingId(doc.id);
                          setEditDocTitle(doc.title);
                          setEditDocContent(doc.content);
                          setEditDocType(doc.doc_type);
                          setEditDocSourceUrl(doc.source_url ?? "");
                        }}>
                          <Pencil className="h-3 w-3" />
                        </Button>
                        {deleteConfirmId === doc.id ? (
                          <span className="flex items-center gap-1 ml-1">
                            <Button
                              variant="destructive"
                              size="sm"
                              className="h-7 text-xs px-2"
                              onClick={() => deleteDocMutation.mutate(doc.id)}
                              disabled={deleteDocMutation.isPending}
                            >
                              {deleteDocMutation.isPending ? "Deleting..." : "Confirm"}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 w-7 p-0"
                              aria-label="Cancel delete"
                              onClick={() => setDeleteConfirmId(null)}
                            >
                              <X className="h-3 w-3" />
                            </Button>
                          </span>
                        ) : (
                          <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={() => setDeleteConfirmId(doc.id)}>
                            <Trash2 className="h-3 w-3 text-destructive" />
                          </Button>
                        )}
                      </div>
                    </div>
                    {expandedDocId === doc.id && (
                      <div className="mt-2 border-t pt-2">
                        {isSafeUrl(doc.source_url) && (
                          <p className="text-xs text-muted-foreground mb-1.5">
                            Source: <a href={doc.source_url} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline">{doc.source_url}</a>
                            {doc.last_synced_at && <span className="ml-1.5">(synced {new Date(doc.last_synced_at).toLocaleDateString()})</span>}
                          </p>
                        )}
                        <pre className="text-xs text-muted-foreground whitespace-pre-wrap font-mono leading-relaxed max-h-72 overflow-auto">
                          {doc.content || "(empty)"}
                        </pre>
                      </div>
                    )}
                    <p className="mt-0.5 text-xs text-muted-foreground ml-5.5">
                      Updated {new Date(doc.updated_at).toLocaleDateString()}
                      {doc.content && ` \u00b7 ${doc.content.length.toLocaleString()} chars`}
                    </p>
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </section>
  );
}
