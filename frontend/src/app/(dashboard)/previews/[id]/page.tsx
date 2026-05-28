"use client";

import { use, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, GitBranch, GitPullRequest, KeyRound, Loader2, RotateCw, Square } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { OpenPreviewButton } from "@/components/preview/open-preview-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
import { ACTIVE_PREVIEW_STATUSES, CONTROLLABLE_PREVIEW_STATUSES, formatPreviewStatus, type PreviewStatus } from "@/lib/preview-types";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
} from "@/lib/preview-bootstrap";
import { safeExternalUrl } from "@/lib/utils";

export default function PreviewLandingPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <PreviewLandingContent id={id} />;
}

export function PreviewLandingContent({ id }: { id: string }) {
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const launchMode = searchParams.get("launch") === "1";
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const bootstrappedPreviewIdRef = useRef<string | null>(null);
  const launchStartAttemptedRef = useRef<string | null>(null);
  const [bootstrapError, setBootstrapError] = useState<string | null>(null);
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
  const isActive = preview?.status && ACTIVE_PREVIEW_STATUSES.includes(preview.status as PreviewStatus);
  const isReady = Boolean(preview?.status && CONTROLLABLE_PREVIEW_STATUSES.includes(preview.status as PreviewStatus));
  const previewUrl = safeExternalUrl(preview?.preview_url);
  const previewOrigin = useMemo(() => {
    if (!previewUrl) return "";
    try {
      return new URL(previewUrl).origin;
    } catch {
      return "";
    }
  }, [previewUrl]);
  const title = preview?.repository_full_name
    ? `${preview.repository_full_name}${preview.branch ? ` · ${preview.branch}` : ""}`
    : preview
      ? `Preview ${(preview.target_id ?? preview.preview_id ?? "").slice(0, 8)}`
      : "Preview";
  const status = preview?.status ? formatPreviewStatus(preview.status) : "Loading";
  const stoppedAtText = preview?.stopped_at ? new Date(preview.stopped_at).toLocaleString() : null;
  const launchTargetId = preview?.preview_id ?? preview?.target_id;
  const shouldStartForLaunch =
    launchMode &&
    preview &&
    launchTargetId &&
    !isActive &&
    !restartPreview.isPending &&
    !previewQuery.isFetching;

  useEffect(() => {
    if (!shouldStartForLaunch || !launchTargetId) return;
    if (launchStartAttemptedRef.current === launchTargetId) return;
    launchStartAttemptedRef.current = launchTargetId;
    restartPreview.mutate({ previewId: launchTargetId, latest: true });
  }, [launchTargetId, restartPreview, shouldStartForLaunch]);

  useEffect(() => {
    if (!launchMode || !previewOrigin || !previewUrl || !preview?.preview_id || !isReady) return;
    const activePreviewId = preview.preview_id;

    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== previewOrigin) return;
      if (event.data?.type === PREVIEW_BOOTSTRAP_COMPLETE_EVENT) {
        if (bootstrappedPreviewIdRef.current === activePreviewId) {
          window.location.href = previewUrl;
        }
        return;
      }
      if (event.data?.type !== PREVIEW_BOOTSTRAP_READY_EVENT) return;
      if (bootstrappedPreviewIdRef.current === activePreviewId || bootstrapPreview.isPending) return;

      bootstrappedPreviewIdRef.current = activePreviewId;
      setBootstrapError(null);
      bootstrapPreview.mutate(activePreviewId, {
        onSuccess: (data) => {
          iframeRef.current?.contentWindow?.postMessage(
            { type: PREVIEW_BOOTSTRAP_TOKEN_EVENT, token: data.data.token },
            previewOrigin,
          );
        },
        onError: (err) => {
          bootstrappedPreviewIdRef.current = null;
          setBootstrapError(err instanceof Error ? err.message : "Preview bootstrap failed.");
        },
      });
    };

    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [bootstrapPreview, isReady, launchMode, preview?.preview_id, previewOrigin, previewUrl]);

  if (launchMode) {
    const launchError =
      bootstrapError ??
      (preview?.status === "failed" ? preview.error || "Preview failed to start." : null) ??
      (restartPreview.isError
        ? restartPreview.error instanceof Error
          ? restartPreview.error.message
          : "Preview could not be started."
        : null);
    const launchTitle = isReady ? "Opening preview" : isExpired ? "Restarting preview" : "Starting preview";
    const launchDescription = isReady
      ? "Connecting this browser to the preview."
      : preview?.status === "starting"
        ? "The preview is starting. This page will open it when it is ready."
        : stoppedAtText
          ? `Last stopped at ${stoppedAtText}. Starting the latest runtime for this preview.`
          : "Starting the latest runtime for this preview.";

    return (
      <PageContainer size="narrow">
        <Card>
          <CardContent className="space-y-4 pt-6">
            <div className="flex items-start gap-3">
              <div className="rounded-full border border-border bg-surface-raised p-2">
                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              </div>
              <div className="min-w-0 space-y-1">
                <p className="text-base font-semibold text-foreground">{launchTitle}</p>
                <p className="text-sm text-muted-foreground">{launchDescription}</p>
                <p className="text-xs text-muted-foreground">Status: {status}</p>
              </div>
            </div>

            {launchError ? (
              <div className="flex items-start gap-2 rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm text-destructive">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                <span>{launchError}</span>
              </div>
            ) : null}

            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  launchStartAttemptedRef.current = null;
                  previewQuery.refetch();
                }}
              >
                <RotateCw className="h-4 w-4" />
                Refresh
              </Button>
              <Button asChild variant="ghost" size="sm">
                <a href={`/previews/${id}`}>View status and logs</a>
              </Button>
            </div>

            {isReady && previewUrl ? (
              <iframe
                ref={iframeRef}
                src={`${previewUrl.replace(/\/$/, "")}/bootstrap`}
                title="Preview bootstrap"
                className="hidden"
              />
            ) : null}
          </CardContent>
        </Card>
      </PageContainer>
    );
  }

  return (
    <PageContainer size="default">
      <div className="space-y-4">
        <PageHeader
          title={title}
          description={preview?.branch ? `Branch preview for ${preview.branch}` : "Branch preview"}
          action={<Badge variant={preview?.status === "ready" ? "default" : "secondary"}>{status}</Badge>}
        />

        {/* Hero: preview link */}
        {previewQuery.isLoading ? null : safeExternalUrl(preview?.preview_url) ? (
          <Card>
            <CardContent className="pt-4 pb-4">
              <div className="flex items-center gap-3">
                <p className="flex-1 truncate font-mono text-sm text-foreground">
                  {preview!.preview_url}
                </p>
                <OpenPreviewButton previewId={preview?.preview_id} previewUrl={preview?.preview_url} size="sm" />
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
                      <p className="text-muted-foreground">Use &quot;Start latest&quot; to launch a fresh runtime.</p>
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
                            <Badge variant={service.status === "ready" ? "default" : "secondary"}>{formatPreviewStatus(service.status)}</Badge>
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
                            <Badge variant={infra.status === "healthy" ? "default" : "secondary"}>{formatPreviewStatus(infra.status)}</Badge>
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
                  {safeExternalUrl(preview.pull_request_url) ? (
                    <Button asChild variant="outline" size="sm">
                      <a href={safeExternalUrl(preview.pull_request_url)} target="_blank" rel="noopener noreferrer">
                        <GitPullRequest className="h-4 w-4" />
                        PR
                      </a>
                    </Button>
                  ) : null}
                  {safeExternalUrl(preview.github_branch_url) ? (
                    <Button asChild variant="outline" size="sm">
                      <a href={safeExternalUrl(preview.github_branch_url)} target="_blank" rel="noopener noreferrer">
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
                  {preview.preview_id && CONTROLLABLE_PREVIEW_STATUSES.includes(preview.status as PreviewStatus) ? (
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
                    <span className="text-muted-foreground">Stopped: </span>
                    <span className="text-foreground">{stoppedAtText ?? "Not stopped"}</span>
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
