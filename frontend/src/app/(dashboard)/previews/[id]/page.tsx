"use client";

import { use } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ExternalLink, GitBranch, GitPullRequest, KeyRound, RotateCw, Square } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";

export default function PreviewLandingPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const queryClient = useQueryClient();
  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey: ["branch-preview", id],
    queryFn: () => api.previews.get(id),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return status === "starting" ? 3000 : false;
    },
  });
  const stopPreview = useMutation({
    mutationFn: (previewId: string) => api.previews.stop(previewId),
    onSuccess: (response) => {
      queryClient.setQueryData(["branch-preview", id], response);
    },
  });
  const restartPreview = useMutation({
    mutationFn: ({ previewId, latest }: { previewId: string; latest: boolean }) =>
      latest ? api.previews.startLatest(previewId) : api.previews.restart(previewId),
    onSuccess: (response) => {
      queryClient.setQueryData(["branch-preview", id], response);
    },
  });
  const bootstrapPreview = useMutation({
    mutationFn: (previewId: string) => api.previews.bootstrap(previewId),
  });

  const preview = previewQuery.data?.data;
  const title = preview?.repository_full_name
    ? `${preview.repository_full_name}${preview.branch ? ` · ${preview.branch}` : ""}`
    : preview
      ? `Preview ${preview.target_id.slice(0, 8)}`
      : "Preview";
  const status = preview?.status.replaceAll("_", " ") ?? "Loading";

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title={title}
          description="Stable branch preview control plane."
          action={<Badge variant={preview?.status === "ready" ? "default" : "secondary"}>{status}</Badge>}
        />

        <Card>
          <CardContent className="space-y-4 pt-6">
            {previewQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">Loading preview status...</p>
            ) : previewQuery.isError ? (
              <p className="text-sm text-destructive">
                {previewQuery.error instanceof Error ? previewQuery.error.message : "Preview could not be loaded."}
              </p>
            ) : preview ? (
              <>
                <div className="grid gap-3 text-sm md:grid-cols-2">
                  <div>
                    <p className="text-muted-foreground">Repository</p>
                    <p className="break-all font-medium text-foreground">{preview.repository_full_name ?? preview.repository_id ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Target</p>
                    <p className="break-all font-medium text-foreground">{preview.target_id}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Branch</p>
                    <p className="break-all font-medium text-foreground">{preview.branch ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Commit</p>
                    <p className="break-all font-medium text-foreground">{preview.commit_sha ? preview.commit_sha.slice(0, 12) : "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Phase</p>
                    <p className="font-medium capitalize text-foreground">{preview.current_phase?.replaceAll("_", " ") ?? status}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Source</p>
                    <p className="font-medium capitalize text-foreground">{preview.source_type?.replaceAll("_", " ") ?? "Manual"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Runtime</p>
                    <p className="break-all font-medium text-foreground">{preview.preview_id ?? "Not started"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Expires</p>
                    <p className="font-medium text-foreground">
                      {preview.expires_at ? new Date(preview.expires_at).toLocaleString() : "No runtime"}
                    </p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Stable URL</p>
                    <p className="break-all font-medium text-foreground">{preview.stable_url}</p>
                  </div>
                </div>

                {preview.new_commits_available ? (
                  <div className="flex items-start gap-3 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                    <div className="min-w-0">
                      <p className="font-medium">New commits available</p>
                      <p className="break-all text-amber-800 dark:text-amber-300">
                        Latest head: {preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}
                      </p>
                    </div>
                  </div>
                ) : null}

                {preview.error ? (
                  <p className="rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm text-destructive">{preview.error}</p>
                ) : null}

                {(preview.services?.length || preview.infrastructure?.length) ? (
                  <div className="grid gap-3 md:grid-cols-2">
                    <div className="space-y-2">
                      <p className="text-sm font-medium text-foreground">Services</p>
                      {(preview.services ?? []).map((service) => (
                        <div key={service.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                          <span className="truncate">{service.service_name}</span>
                          <Badge variant={service.status === "ready" ? "default" : "secondary"}>{service.status.replaceAll("_", " ")}</Badge>
                        </div>
                      ))}
                    </div>
                    <div className="space-y-2">
                      <p className="text-sm font-medium text-foreground">Infrastructure</p>
                      {(preview.infrastructure ?? []).map((infra) => (
                        <div key={infra.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                          <span className="truncate">{infra.infra_name}</span>
                          <Badge variant={infra.status === "healthy" ? "default" : "secondary"}>{infra.status.replaceAll("_", " ")}</Badge>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                {preview.logs?.length ? (
                  <div className="space-y-2">
                    <p className="text-sm font-medium text-foreground">Recent logs</p>
                    <div className="max-h-56 overflow-auto rounded-md border border-border">
                      {preview.logs.slice(-20).map((log) => (
                        <div key={log.id} className="border-b border-border px-3 py-2 text-xs last:border-b-0">
                          <span className="font-medium text-foreground">{log.step}</span>
                          <span className="ml-2 text-muted-foreground">{log.message}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                {stopPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {stopPreview.error instanceof Error ? stopPreview.error.message : "Preview could not be stopped."}
                  </p>
                ) : null}
                {bootstrapPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {bootstrapPreview.error instanceof Error ? bootstrapPreview.error.message : "Bootstrap token could not be minted."}
                  </p>
                ) : bootstrapPreview.data?.data.token ? (
                  <p className="break-all text-sm text-muted-foreground">Bootstrap token: {bootstrapPreview.data.data.token}</p>
                ) : null}
                {restartPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {restartPreview.error instanceof Error ? restartPreview.error.message : "Preview could not be restarted."}
                  </p>
                ) : null}

                <div className="flex flex-col gap-2 sm:flex-row">
                  {preview.preview_url ? (
                    <Button asChild>
                      <a href={preview.preview_url}>
                        <ExternalLink className="h-4 w-4" />
                        Open preview
                      </a>
                    </Button>
                  ) : null}
                  {preview.pull_request_url ? (
                    <Button asChild variant="outline">
                      <a href={preview.pull_request_url}>
                        <GitPullRequest className="h-4 w-4" />
                        PR
                      </a>
                    </Button>
                  ) : null}
                  {preview.github_branch_url ? (
                    <Button asChild variant="outline">
                      <a href={preview.github_branch_url}>
                        <GitBranch className="h-4 w-4" />
                        Branch
                      </a>
                    </Button>
                  ) : null}
                  {preview.preview_id && ["ready", "partially_ready", "unhealthy"].includes(preview.status) ? (
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => bootstrapPreview.mutate(preview.preview_id!)}
                      disabled={bootstrapPreview.isPending}
                    >
                      <KeyRound className="h-4 w-4" />
                      Bootstrap token
                    </Button>
                  ) : null}
                  {preview.preview_id && !["stopped", "failed", "expired"].includes(preview.status) ? (
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => stopPreview.mutate(preview.preview_id!)}
                      disabled={stopPreview.isPending}
                    >
                      <Square className="h-4 w-4" />
                      Stop
                    </Button>
                  ) : null}
                  {preview.preview_id ? (
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => restartPreview.mutate({ previewId: preview.preview_id!, latest: false })}
                      disabled={restartPreview.isPending}
                    >
                      <RotateCw className="h-4 w-4" />
                      Restart
                    </Button>
                  ) : null}
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => restartPreview.mutate({ previewId: preview.preview_id ?? preview.target_id, latest: true })}
                    disabled={restartPreview.isPending}
                  >
                    <GitBranch className="h-4 w-4" />
                    Start latest
                  </Button>
                  <Button type="button" variant="outline" onClick={() => previewQuery.refetch()}>
                    <RotateCw className="h-4 w-4" />
                    Refresh
                  </Button>
                </div>
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}
