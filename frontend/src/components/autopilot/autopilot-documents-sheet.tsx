"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { queryKeys } from "@/lib/query-keys";
import type { PMDocument } from "@/lib/types";

const DOC_TYPE_LABELS: Record<string, string> = {
  roadmap: "Roadmap",
  philosophy: "Product philosophy",
  strategy: "Strategy",
  market: "Market context",
  architecture: "Architecture",
  reference: "Reference",
};

export function AutopilotDocumentsSheet({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [editingId, setEditingId] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [docType, setDocType] = useState("roadmap");
  const [sourceUrl, setSourceUrl] = useState("");
  const [showForm, setShowForm] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: queryKeys.pm.documents,
    queryFn: () => api.pm.listDocuments(),
    enabled: open,
  });

  const createMutation = useMutation({
    mutationFn: (payload: Parameters<typeof api.pm.createDocument>[0]) => api.pm.createDocument(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pm.documents });
      resetForm();
    },
    onError: (error) => {
      console.error("failed to create document", error);
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, payload }: { id: string; payload: Record<string, unknown> }) => api.pm.updateDocument(id, payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pm.documents });
      resetForm();
    },
    onError: (error) => {
      console.error("failed to update document", error);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.pm.deleteDocument(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.pm.documents });
    },
    onError: (error) => {
      console.error("failed to delete document", error);
    },
  });

  const sortedDocuments = useMemo(() => {
    const documents = data?.data ?? [];
    return [...documents].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
  }, [data?.data]);

  function resetForm() {
    setEditingId(null);
    setTitle("");
    setContent("");
    setDocType("roadmap");
    setSourceUrl("");
    setShowForm(false);
  }

  function startEditing(document: PMDocument) {
    setEditingId(document.id);
    setTitle(document.title);
    setContent(document.content);
    setDocType(document.doc_type);
    setSourceUrl(document.source_url ?? "");
    setShowForm(true);
  }

  function handleSave() {
    if (!title.trim()) return;
    if (editingId) {
      updateMutation.mutate({
        id: editingId,
        payload: {
          title: title.trim(),
          content,
          doc_type: docType,
          source_url: sourceUrl.trim() || null,
        },
      });
      return;
    }

    createMutation.mutate({
      title: title.trim(),
      content,
      doc_type: docType,
      source_type: "manual",
      source_url: sourceUrl.trim() || undefined,
    });
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>Documents</SheetTitle>
          <SheetDescription>Manage the roadmap, philosophy, and reference docs Autopilot reads during planning.</SheetDescription>
        </SheetHeader>
        <div className="mt-6 space-y-5">
          <div className="flex justify-end">
            <Button onClick={() => setShowForm((current) => !current)}>
              {showForm ? "Close form" : "Add document"}
            </Button>
          </div>

          {showForm && (
            <div className="space-y-4 rounded-xl border p-4">
              <div className="space-y-2">
                <Label htmlFor="document-title">Title</Label>
                <Input id="document-title" value={title} onChange={(event) => setTitle(event.target.value)} />
              </div>
              <div className="space-y-2">
                <Label htmlFor="document-type">Type</Label>
                <Select value={docType} onValueChange={setDocType}>
                  <SelectTrigger id="document-type" aria-label="Type">
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
                <Label htmlFor="document-source-url">Source URL</Label>
                <Input
                  id="document-source-url"
                  value={sourceUrl}
                  onChange={(event) => setSourceUrl(event.target.value)}
                  placeholder="https://..."
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="document-content">Content</Label>
                <Textarea
                  id="document-content"
                  rows={8}
                  value={content}
                  onChange={(event) => setContent(event.target.value)}
                />
              </div>
              <div className="flex justify-end gap-2">
                <Button variant="outline" onClick={resetForm}>Cancel</Button>
                <Button
                  onClick={handleSave}
                  disabled={!title.trim() || createMutation.isPending || updateMutation.isPending}
                >
                  {editingId ? "Save document" : "Save document"}
                </Button>
              </div>
            </div>
          )}

          {isLoading ? (
            <p className="text-sm text-muted-foreground">Loading documents...</p>
          ) : sortedDocuments.length === 0 ? (
            <p className="text-sm text-muted-foreground">No documents yet.</p>
          ) : (
            <div className="space-y-3">
              {sortedDocuments.map((document) => (
                <div key={document.id} className="rounded-xl border p-4">
                  <div className="flex items-start justify-between gap-4">
                    <div className="space-y-1">
                      <p className="text-sm font-medium text-foreground">{document.title}</p>
                      <p className="text-sm text-muted-foreground">{DOC_TYPE_LABELS[document.doc_type] ?? document.doc_type}</p>
                    </div>
                    <div className="flex gap-2">
                      <Button variant="ghost" size="sm" onClick={() => startEditing(document)}>Edit</Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => deleteMutation.mutate(document.id)}
                        disabled={deleteMutation.isPending}
                      >
                        Delete
                      </Button>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
