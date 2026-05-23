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
  const isExpired = preview?.status === "expired";
  const isActive = preview?.status && ["ready", "partially_ready", "unhealthy", "starting"].includes(preview.status);
  const title = preview?.repository_full_name
    ? `${preview.repository_full_name}${preview.branch ? ` · ${preview.branch}` : ""}`
    : preview
      ? `Preview ${preview.target_id.slice(0, 8)}`
      : "Preview";
  const status = preview?.status.replaceAll("_", " ") ?? "Loading";

  return (
    <PageContainer size="default">
      <div className="space-y-4">
        <PageHeader
          title={title}
          description={preview?.branch ? `Branch preview for ${preview.branch}` : "Branch preview"}
          action={<Badge variant={preview?.status === "ready" ? "default" : "secondary"}>{status}</Badge>}
        />

        {/* Hero: preview link */}
        {previewQuery.isLoading ? null : preview?.preview_url ? (
          <Card>
            <CardContent className="pt-4 pb-4">
              <div className="flex items-center gap-3">
                <a
                  href={preview.preview_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex-1 truncate font-mono text-sm text-foreground underline-offset-4 hover:underline"
                >
                  {preview.preview_url}
                </a>
                <Button asChild size="sm">
                  <a href={preview.preview_url} target="_blank" rel="noopener noreferrer">
                    <ExternalLink className="h-4 w-4" />
                    Open preview
                  </a>
                </Button>
              </div>
            </CardContent>
          </Card>
        ) : preview?.stable_url ? (
          <Card>
            <CardContent className="pt-4 pb-4">
              <p className="mb-1 text-xs text-muted-foreground">Stable URL — always points to this preview</p>
              <p className="break-all font-mono text-sm text-foreground">{preview.stable_url}</p>
            </CardContent>
          </Card>
        ) : null}

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
                {/* Key metadata */}
                <div className="grid gap-3 text-sm sm:grid-cols-3">
                  <div>
                    <p className="text-muted-foreground">Repository</p>
                    <p className="break-all font-medium text-foreground">{preview.repository_full_name ?? preview.repository_id ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Branch</p>
                    <p className="break-all font-medium text-foreground">{preview.branch ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Commit</p>
                    <p className="font-medium text-foreground">{preview.commit_sha ? preview.commit_sha.slice(0, 12) : "Unknown"}</p>
                  </div>
                </div>

                {/* Alerts */}
                {preview.new_commits_available ? (
                  <div className="flex items-start gap-3 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                    <div className="min-w-0">
                      <p className="font-medium">New commits available</p>
                      <p className="break-all text-amber-800 dark:text-amber-300">
                        Latest: {preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}
                      </p>
                    </div>
                  </div>
                ) : null}

                {isExpired ? (
                  <div className="flex items-start gap-3 rounded-md border border-border bg-muted/40 p-3 text-sm">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                    <div>
                      <p className="font-medium text-foreground">Preview expired</p>
                      <p className="text-muted-foreground">Use "Start latest" to launch a fresh runtime.</p>
                    </div>
                  </div>
                ) : null}

                {preview.error ? (
                  <p className="rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm text-destructive">{preview.error}</p>
                ) : null}

                {/* Startup progress */}
                {preview.phase_steps?.length ? (
                  <div className="space-y-2">
                    <p className="text-sm font-medium text-foreground">Startup progress</p>
                    <div className="grid gap-2 sm:grid-cols-4">
                      {preview.phase_steps.map((step) => (
                        <div key={step.name} className="rounded-md border border-border px-3 py-2">
                          <p className="text-sm font-medium capitalize text-foreground">{step.name.replaceAll("_", " ")}</p>
                          <p className="text-xs capitalize text-muted-foreground">{step.status}</p>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                {/* Services / infrastructure */}
                {(preview.services?.length || preview.infrastructure?.length) ? (
                  <div className="grid gap-3 sm:grid-cols-2">
                    {preview.services?.length ? (
                      <div className="space-y-2">
                        <p className="text-sm font-medium text-foreground">Services</p>
                        {preview.services.map((service) => (
                          <div key={service.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                            <span className="truncate">{service.service_name}</span>
                            <Badge variant={service.status === "ready" ? "default" : "secondary"}>{service.status.replaceAll("_", " ")}</Badge>
                          </div>
                        ))}
                      </div>
                    ) : null}
                    {preview.infrastructure?.length ? (
                      <div className="space-y-2">
                        <p className="text-sm font-medium text-foreground">Infrastructure</p>
                        {preview.infrastructure.map((infra) => (
                          <div key={infra.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                            <span className="truncate">{infra.infra_name}</span>
                            <Badge variant={infra.status === "healthy" ? "default" : "secondary"}>{infra.status.replaceAll("_", " ")}</Badge>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                {/* Mutation errors */}
                {stopPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {stopPreview.error instanceof Error ? stopPreview.error.message : "Preview could not be stopped."}
                  </p>
                ) : null}
                {restartPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {restartPreview.error instanceof Error ? restartPreview.error.message : "Preview could not be restarted."}
                  </p>
                ) : null}
                {bootstrapPreview.isError ? (
                  <p className="text-sm text-destructive">
                    {bootstrapPreview.error instanceof Error ? bootstrapPreview.error.message : "Bootstrap token could not be minted."}
                  </p>
                ) : bootstrapPreview.data?.data.token ? (
                  <p className="break-all rounded-md border border-border bg-muted/40 p-3 font-mono text-xs text-foreground">
                    {bootstrapPreview.data.data.token}
                  </p>
                ) : null}

                {/* Actions */}
                <div className="flex flex-wrap gap-2">
                  {preview.pull_request_url ? (
                    <Button asChild variant="outline" size="sm">
                      <a href={preview.pull_request_url} target="_blank" rel="noopener noreferrer">
                        <GitPullRequest className="h-4 w-4" />
                        PR
                      </a>
                    </Button>
                  ) : null}
                  {preview.github_branch_url ? (
                    <Button asChild variant="outline" size="sm">
                      <a href={preview.github_branch_url} target="_blank" rel="noopener noreferrer">
                        <GitBranch className="h-4 w-4" />
                        Branch
                      </a>
                    </Button>
                  ) : null}
                  {preview.preview_id && isActive ? (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
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
                      size="sm"
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
                    size="sm"
                    onClick={() => restartPreview.mutate({ previewId: preview.preview_id ?? preview.target_id, latest: true })}
                    disabled={restartPreview.isPending}
                  >
                    <GitBranch className="h-4 w-4" />
                    Start latest
                  </Button>
                  <Button type="button" variant="ghost" size="sm" onClick={() => previewQuery.refetch()}>
                    <RotateCw className="h-4 w-4" />
                    Refresh
                  </Button>
                  {preview.preview_id && ["ready", "partially_ready", "unhealthy"].includes(preview.status) ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => bootstrapPreview.mutate(preview.preview_id!)}
                      disabled={bootstrapPreview.isPending}
                    >
                      <KeyRound className="h-4 w-4" />
                      Bootstrap token
                    </Button>
                  ) : null}
                </div>

                {/* Secondary metadata */}
                <div className="grid gap-2 border-t border-border pt-3 text-xs sm:grid-cols-3">
                  <div>
                    <span className="text-muted-foreground">Expires: </span>
                    <span className="text-foreground">
                      {preview.expires_at ? new Date(preview.expires_at).toLocaleString() : "No runtime"}
                    </span>
                  </div>
                  <div>
                    <span className="text-muted-foreground">Source: </span>
                    <span className="capitalize text-foreground">{preview.source_type?.replaceAll("_", " ") ?? "Manual"}</span>
                  </div>
                  {preview.stable_url && preview.preview_url ? (
                    <div className="sm:col-span-1">
                      <span className="text-muted-foreground">Stable URL: </span>
                      <span className="break-all text-foreground">{preview.stable_url}</span>
                    </div>
                  ) : null}
                </div>
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}
