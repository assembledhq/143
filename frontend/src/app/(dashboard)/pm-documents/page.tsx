"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
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
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { Badge } from "@/components/ui/badge";
import { Plus, Pencil, Trash2, FileText, X, Check } from "lucide-react";
import type { PMDocument, ListResponse } from "@/lib/types";

const DOC_TYPE_LABELS: Record<string, string> = {
  roadmap: "Roadmap",
  philosophy: "Product Philosophy",
  strategy: "Strategy",
  market: "Market Context",
  architecture: "Architecture",
  reference: "Reference",
};

const DOC_TYPE_COLORS: Record<string, string> = {
  roadmap: "bg-blue-100 text-blue-800",
  philosophy: "bg-purple-100 text-purple-800",
  strategy: "bg-amber-100 text-amber-800",
  market: "bg-green-100 text-green-800",
  architecture: "bg-cyan-100 text-cyan-800",
  reference: "bg-gray-100 text-gray-800",
};

export default function PMDocumentsPage() {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Form state
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [docType, setDocType] = useState("roadmap");

  // Edit state
  const [editTitle, setEditTitle] = useState("");
  const [editContent, setEditContent] = useState("");
  const [editDocType, setEditDocType] = useState("");

  const { data: docsData, isLoading } = useQuery<ListResponse<PMDocument>>({
    queryKey: ["pm", "documents"],
    queryFn: () => api.pm.listDocuments(),
  });

  const docs = docsData?.data ?? [];

  const createMutation = useMutation({
    mutationFn: (body: { title: string; content: string; doc_type: string }) =>
      api.pm.createDocument(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setShowCreate(false);
      setTitle("");
      setContent("");
      setDocType("roadmap");
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      api.pm.updateDocument(id, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
      setEditingId(null);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.pm.deleteDocument(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pm", "documents"] });
    },
  });

  function handleCreate() {
    if (!title.trim()) return;
    createMutation.mutate({ title: title.trim(), content, doc_type: docType });
  }

  function startEdit(doc: PMDocument) {
    setEditingId(doc.id);
    setEditTitle(doc.title);
    setEditContent(doc.content);
    setEditDocType(doc.doc_type);
  }

  function handleUpdate() {
    if (!editingId || !editTitle.trim()) return;
    updateMutation.mutate({
      id: editingId,
      body: { title: editTitle.trim(), content: editContent, doc_type: editDocType },
    });
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="PM Documents"
          description="Upload reference documents for the PM agent — roadmaps, product philosophy, strategy docs, and more."
          action={
            !showCreate && (
              <Button size="sm" onClick={() => setShowCreate(true)}>
                <Plus className="h-4 w-4 mr-1.5" />
                Add Document
              </Button>
            )
          }
        />

        {/* Create form */}
        {showCreate && (
          <Card>
            <CardContent>
              <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <h3 className="text-sm font-medium">New Document</h3>
                  <Button variant="ghost" size="sm" onClick={() => setShowCreate(false)}>
                    <X className="h-4 w-4" />
                  </Button>
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-2">
                    <Label htmlFor="doc-title">Title</Label>
                    <Input
                      id="doc-title"
                      placeholder="e.g. Q1 2026 Roadmap"
                      value={title}
                      onChange={(e) => setTitle(e.target.value)}
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
                          <SelectItem key={value} value={value}>
                            {label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="doc-content">Content</Label>
                  <Textarea
                    id="doc-content"
                    placeholder="Paste your document content here (Markdown supported)..."
                    rows={10}
                    value={content}
                    onChange={(e) => setContent(e.target.value)}
                  />
                </div>
                <div className="flex justify-end gap-2">
                  <Button variant="outline" size="sm" onClick={() => setShowCreate(false)}>
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    onClick={handleCreate}
                    disabled={!title.trim() || createMutation.isPending}
                  >
                    {createMutation.isPending ? "Saving..." : "Save Document"}
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        )}

        {/* Documents list */}
        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Card key={i}>
                <CardContent>
                  <div className="h-12 animate-pulse rounded bg-muted" />
                </CardContent>
              </Card>
            ))}
          </div>
        ) : docs.length === 0 ? (
          <Card>
            <CardContent>
              <div className="flex flex-col items-center justify-center py-12 text-center">
                <FileText className="h-10 w-10 text-muted-foreground/40 mb-3" />
                <p className="text-sm font-medium text-muted-foreground">
                  No documents yet
                </p>
                <p className="text-xs text-muted-foreground mt-1">
                  Add your roadmap, product philosophy, or other reference documents to give the PM agent context.
                </p>
              </div>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-3">
            {docs.map((doc) => (
              <Card key={doc.id}>
                <CardContent>
                  {editingId === doc.id ? (
                    <div className="space-y-4">
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-2">
                          <Label>Title</Label>
                          <Input
                            value={editTitle}
                            onChange={(e) => setEditTitle(e.target.value)}
                          />
                        </div>
                        <div className="space-y-2">
                          <Label>Type</Label>
                          <Select value={editDocType} onValueChange={setEditDocType}>
                            <SelectTrigger>
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {Object.entries(DOC_TYPE_LABELS).map(([value, label]) => (
                                <SelectItem key={value} value={value}>
                                  {label}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        </div>
                      </div>
                      <div className="space-y-2">
                        <Label>Content</Label>
                        <Textarea
                          rows={10}
                          value={editContent}
                          onChange={(e) => setEditContent(e.target.value)}
                        />
                      </div>
                      <div className="flex justify-end gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setEditingId(null)}
                        >
                          Cancel
                        </Button>
                        <Button
                          size="sm"
                          onClick={handleUpdate}
                          disabled={!editTitle.trim() || updateMutation.isPending}
                        >
                          <Check className="h-3.5 w-3.5 mr-1" />
                          {updateMutation.isPending ? "Saving..." : "Save"}
                        </Button>
                      </div>
                    </div>
                  ) : (
                    <div>
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-3">
                          <FileText className="h-4 w-4 text-muted-foreground" />
                          <button
                            onClick={() =>
                              setExpandedId(expandedId === doc.id ? null : doc.id)
                            }
                            className="text-sm font-medium hover:underline text-left"
                          >
                            {doc.title}
                          </button>
                          <Badge
                            variant="secondary"
                            className={DOC_TYPE_COLORS[doc.doc_type] ?? DOC_TYPE_COLORS.reference}
                          >
                            {DOC_TYPE_LABELS[doc.doc_type] ?? doc.doc_type}
                          </Badge>
                        </div>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => startEdit(doc)}
                          >
                            <Pencil className="h-3.5 w-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => {
                              if (confirm("Delete this document?")) {
                                deleteMutation.mutate(doc.id);
                              }
                            }}
                          >
                            <Trash2 className="h-3.5 w-3.5 text-destructive" />
                          </Button>
                        </div>
                      </div>
                      {expandedId === doc.id && (
                        <div className="mt-3 border-t pt-3">
                          <pre className="text-xs text-muted-foreground whitespace-pre-wrap font-mono leading-relaxed max-h-96 overflow-auto">
                            {doc.content || "(empty)"}
                          </pre>
                        </div>
                      )}
                      <p className="mt-1 text-xs text-muted-foreground ml-7">
                        Updated {new Date(doc.updated_at).toLocaleDateString()}
                        {doc.content && ` · ${doc.content.length.toLocaleString()} chars`}
                      </p>
                    </div>
                  )}
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </div>
    </PageContainer>
  );
}
