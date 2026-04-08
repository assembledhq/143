"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Hash,
  Mail,
  FileText,
  Globe,
  Plus,
  Trash2,
  ToggleLeft,
  ToggleRight,
} from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { OutputDestination } from "@/lib/types";

const DEST_TYPE_META: Record<
  string,
  { label: string; icon: React.ComponentType<{ className?: string }>; description: string }
> = {
  slack: { label: "Slack", icon: Hash, description: "Post results to a Slack channel" },
  email: { label: "Email", icon: Mail, description: "Send results via email" },
  notion: { label: "Notion", icon: FileText, description: "Append results to a Notion page" },
  webhook: { label: "Webhook", icon: Globe, description: "POST results to any URL" },
};

function DestinationCard({
  dest,
  projectId,
}: {
  dest: OutputDestination;
  projectId: string;
}) {
  const queryClient = useQueryClient();
  const meta = DEST_TYPE_META[dest.destination_type];
  const Icon = meta?.icon ?? Globe;

  const toggleMutation = useMutation({
    mutationFn: () =>
      api.projects.updateOutput(projectId, dest.id, {
        destination_type: dest.destination_type,
        label: dest.label,
        config: dest.config,
        enabled: !dest.enabled,
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project-outputs", projectId] }),
  });

  const deleteMutation = useMutation({
    mutationFn: () => api.projects.deleteOutput(projectId, dest.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["project-outputs", projectId] }),
  });

  const configSummary = (() => {
    const c = dest.config;
    switch (dest.destination_type) {
      case "slack":
        return (c as { channel_name?: string }).channel_name || (c as { channel_id?: string }).channel_id || "Channel";
      case "email":
        return ((c as { recipients?: string[] }).recipients ?? []).join(", ") || "No recipients";
      case "notion":
        return (c as { page_title?: string }).page_title || (c as { page_id?: string }).page_id || "Page";
      case "webhook":
        return (c as { url?: string }).url || "URL";
      default:
        return "";
    }
  })();

  return (
    <div className={`flex items-center gap-3 rounded-lg border p-3 ${dest.enabled ? "border-border" : "border-border/50 opacity-60"}`}>
      <Icon className="h-4 w-4 text-muted-foreground shrink-0" />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{dest.label || meta?.label}</span>
          <span className="text-[11px] text-muted-foreground bg-muted px-1.5 py-0.5 rounded">{meta?.label}</span>
        </div>
        <p className="text-xs text-muted-foreground truncate">{configSummary}</p>
      </div>
      <button
        type="button"
        onClick={() => toggleMutation.mutate()}
        disabled={toggleMutation.isPending}
        className="text-muted-foreground hover:text-foreground transition-colors"
        title={dest.enabled ? "Disable" : "Enable"}
      >
        {dest.enabled ? <ToggleRight className="h-5 w-5 text-primary" /> : <ToggleLeft className="h-5 w-5" />}
      </button>
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <button
            type="button"
            className="text-muted-foreground hover:text-destructive transition-colors"
            title="Remove"
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove destination</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently remove the {meta?.label} destination &quot;{dest.label || meta?.label}&quot;. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deleteMutation.mutate()}
              disabled={deleteMutation.isPending}
            >
              {deleteMutation.isPending ? "Removing..." : "Remove"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function isFormValid(
  type: string,
  fields: { channelId: string; recipients: string; pageId: string; webhookUrl: string },
): boolean {
  switch (type) {
    case "slack":
      return fields.channelId.trim().length > 0;
    case "email":
      return fields.recipients.split(",").some((s) => s.trim().length > 0);
    case "notion":
      return fields.pageId.trim().length > 0;
    case "webhook":
      return fields.webhookUrl.trim().length > 0;
    default:
      return false;
  }
}

function AddDestinationForm({ projectId }: { projectId: string }) {
  const queryClient = useQueryClient();
  const [type, setType] = useState<string>("");
  const [label, setLabel] = useState("");
  // Config fields
  const [channelId, setChannelId] = useState("");
  const [channelName, setChannelName] = useState("");
  const [recipients, setRecipients] = useState("");
  const [pageId, setPageId] = useState("");
  const [pageTitle, setPageTitle] = useState("");
  const [webhookUrl, setWebhookUrl] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");

  const resetForm = () => {
    setType("");
    setLabel("");
    setChannelId("");
    setChannelName("");
    setRecipients("");
    setPageId("");
    setPageTitle("");
    setWebhookUrl("");
    setWebhookSecret("");
  };

  const createMutation = useMutation({
    mutationFn: () => {
      let config: Record<string, unknown> = {};
      switch (type) {
        case "slack":
          config = { channel_id: channelId.trim(), channel_name: channelName.trim() };
          break;
        case "email":
          config = { recipients: recipients.split(",").map((s) => s.trim()).filter(Boolean) };
          break;
        case "notion":
          config = { page_id: pageId.trim(), page_title: pageTitle.trim() };
          break;
        case "webhook":
          config = { url: webhookUrl.trim(), secret: webhookSecret.trim() || undefined };
          break;
      }
      return api.projects.createOutput(projectId, {
        destination_type: type,
        label: label.trim() || DEST_TYPE_META[type]?.label || type,
        config,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["project-outputs", projectId] });
      resetForm();
    },
  });

  if (!type) {
    return (
      <div className="space-y-2">
        <p className="text-xs text-muted-foreground">Add a destination to receive results when this project runs.</p>
        <div className="grid grid-cols-2 gap-2">
          {Object.entries(DEST_TYPE_META).map(([key, meta]) => {
            const Icon = meta.icon;
            return (
              <button
                key={key}
                type="button"
                onClick={() => setType(key)}
                className="flex items-center gap-2 rounded-lg border border-border p-3 text-left hover:border-primary/40 hover:bg-accent transition-colors"
              >
                <Icon className="h-4 w-4 text-muted-foreground" />
                <div>
                  <div className="text-sm font-medium">{meta.label}</div>
                  <div className="text-[11px] text-muted-foreground">{meta.description}</div>
                </div>
              </button>
            );
          })}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-primary/20 bg-primary/5 p-4">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">
          New {DEST_TYPE_META[type]?.label} destination
        </span>
        <button type="button" onClick={resetForm} className="text-xs text-muted-foreground hover:text-foreground">
          Cancel
        </button>
      </div>

      <div className="space-y-2">
        <Label className="text-xs">Label (optional)</Label>
        <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={DEST_TYPE_META[type]?.label} className="h-8" />
      </div>

      {type === "slack" && (
        <div className="grid gap-2 grid-cols-2">
          <div className="space-y-1">
            <Label className="text-xs">Channel ID</Label>
            <Input value={channelId} onChange={(e) => setChannelId(e.target.value)} placeholder="C01ABC23DEF" className="h-8" />
          </div>
          <div className="space-y-1">
            <Label className="text-xs">Channel name</Label>
            <Input value={channelName} onChange={(e) => setChannelName(e.target.value)} placeholder="#engineering" className="h-8" />
          </div>
        </div>
      )}

      {type === "email" && (
        <div className="space-y-1">
          <Label className="text-xs">Recipients (comma-separated)</Label>
          <Input value={recipients} onChange={(e) => setRecipients(e.target.value)} placeholder="alice@company.com, bob@company.com" className="h-8" />
        </div>
      )}

      {type === "notion" && (
        <div className="grid gap-2 grid-cols-2">
          <div className="space-y-1">
            <Label className="text-xs">Page ID</Label>
            <Input value={pageId} onChange={(e) => setPageId(e.target.value)} placeholder="abc123..." className="h-8" />
          </div>
          <div className="space-y-1">
            <Label className="text-xs">Page title</Label>
            <Input value={pageTitle} onChange={(e) => setPageTitle(e.target.value)} placeholder="Agent Reports" className="h-8" />
          </div>
        </div>
      )}

      {type === "webhook" && (
        <div className="space-y-2">
          <div className="space-y-1">
            <Label className="text-xs">URL</Label>
            <Input value={webhookUrl} onChange={(e) => setWebhookUrl(e.target.value)} placeholder="https://hooks.example.com/143" className="h-8" />
          </div>
          <div className="space-y-1">
            <Label className="text-xs">HMAC secret (optional)</Label>
            <Input value={webhookSecret} onChange={(e) => setWebhookSecret(e.target.value)} placeholder="optional signing secret" className="h-8" type="password" />
          </div>
        </div>
      )}

      <Button
        size="sm"
        onClick={() => createMutation.mutate()}
        disabled={createMutation.isPending || !isFormValid(type, { channelId, recipients, pageId, webhookUrl })}
      >
        <Plus className="h-3 w-3 mr-1" />
        {createMutation.isPending ? "Adding..." : "Add destination"}
      </Button>
      {createMutation.isError && (
        <p className="text-xs text-destructive">
          {createMutation.error?.message || "Failed to add destination."}
        </p>
      )}
    </div>
  );
}

export function OutputsTab({ projectId }: { projectId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["project-outputs", projectId],
    queryFn: () => api.projects.listOutputs(projectId),
  });

  const destinations = data?.data ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Output destinations</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-xs text-muted-foreground">
            Configure where results from scheduled runs are delivered. Uses your
            existing integrations — no local MCP servers needed.
          </p>

          {isLoading && <p className="text-xs text-muted-foreground">Loading...</p>}

          {destinations.length > 0 && (
            <div className="space-y-2">
              {destinations.map((dest) => (
                <DestinationCard key={dest.id} dest={dest} projectId={projectId} />
              ))}
            </div>
          )}

          <AddDestinationForm projectId={projectId} />
        </CardContent>
      </Card>
    </div>
  );
}
