"use client";

import { use, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, GitBranch, GitPullRequest, Loader2, RotateCw } from "lucide-react";
import { useSearchParams } from "next/navigation";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { OpenPreviewButton, usePreviewLauncher } from "@/components/preview/open-preview-button";
import { PreviewStatusBadge } from "@/components/preview/preview-status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { formatPreviewStatus } from "@/lib/preview-types";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
import { safeExternalUrl } from "@/lib/utils";
import { pollMs } from "@/lib/poll-intervals";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const RESTART_LATEST_LABEL = "Restart";
const RESTART_LATEST_TOOLTIP = "Restart preview from the latest source state";

export default function PullRequestPreviewPage({
  params,
}: {
  params: Promise<{ owner: string; repo: string; number: string }>;
}) {
  const { owner, repo, number } = use(params);
  return <PullRequestPreviewContent owner={owner} repo={repo} number={number} />;
}

export function PullRequestPreviewContent({
  owner,
  repo,
  number,
}: {
  owner: string;
  repo: string;
  number: string;
}) {
  const queryClient = useQueryClient();
  const searchParams = useSearchParams();
  const launchRequested = searchParams.get("launch") === "1";
  const intent = launchRequested ? "open" : "status";
  const queryKey = useMemo(() => ["branch-preview-pr", owner, repo, number, intent], [intent, number, owner, repo]);
  const [launchSessionActive, setLaunchSessionActive] = useState(false);
  const autoLaunchKeyRef = useRef<string | null>(null);
  const autoActionKeyRef = useRef<string | null>(null);
  const { launchPreview, isOpening, error: launchError, bootstrapFrame } = usePreviewLauncher();
  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey,
    queryFn: () => api.previews.getPullRequest(owner, repo, number, { intent }),
    refetchInterval: (query) => {
      const preview = query.state.data?.data;
      const status = preview?.status;
      const action = preview?.launch?.action;
      return status === "starting" || action === "wait" ? pollMs(3000) : false;
    },
  });
  const startLatest = useMutation({
    mutationFn: (id: string) => api.previews.startLatest(id),
    onSuccess: (response) => {
      queryClient.setQueryData(queryKey, response);
    },
  });
  const startPullRequest = useMutation({
    mutationFn: () => api.previews.getPullRequest(owner, repo, number, { intent: "open" }),
    onSuccess: (response) => {
      queryClient.setQueryData(queryKey, response);
    },
  });
  const restart = useMutation({
    mutationFn: (id: string) => api.previews.restart(id),
    onSuccess: (response) => {
      queryClient.setQueryData(queryKey, response);
    },
  });

  const preview = previewQuery.data?.data;
  const title = `${owner}/${repo}#${number}`;
  const status = preview?.status ? formatPreviewStatus(preview.status) : "Loading";
  const targetId = previewTargetId(preview?.target_id);
  const isExpired = preview?.status === "expired";
  const launch = preview?.launch;
  const launchState = launchStateCopy(preview);
  const launchSessionShouldOpen = launchRequested || launchSessionActive;
  const retryAllowed =
    Boolean(preview?.preview_id) &&
    launch?.action !== "closed" &&
    (launch?.action !== "blocked" || preview?.status === "failed");
  const showStartAction =
    launch?.action === "start" ||
    (Boolean(targetId) && (launch?.action === "start_latest" || launch?.action === "resume"));
  const safePreviewURL = safeExternalUrl(preview?.preview_url);

  useEffect(() => {
    if (!preview || !launchSessionShouldOpen || !launch?.auto_open) return;
    if (startLatest.isPending || startPullRequest.isPending || restart.isPending) return;

    if (launch.action === "start" || launch.action === "start_latest" || launch.action === "resume") {
      const startTargetId = previewTargetId(preview.target_id);
      const key = `${launch.action}:${startTargetId ?? "pull-request"}:${preview.latest_commit_sha ?? preview.commit_sha ?? ""}`;
      if (autoActionKeyRef.current === key) return;
      autoActionKeyRef.current = key;
      if (startTargetId) {
        startLatest.mutate(startTargetId);
      } else if (launch.action === "start") {
        startPullRequest.mutate();
      }
      return;
    }

    if ((launch.action === "retry" || launch.action === "restart") && preview.preview_id) {
      const key = `${launch.action}:${preview.preview_id}:${preview.latest_commit_sha ?? preview.commit_sha ?? ""}`;
      if (autoActionKeyRef.current === key) return;
      autoActionKeyRef.current = key;
      restart.mutate(preview.preview_id);
    }
  }, [
    launch?.action,
    launch?.auto_open,
    launchSessionShouldOpen,
    preview,
    restart,
    startLatest,
    startPullRequest,
  ]);

  useEffect(() => {
    if (!preview || !launchSessionShouldOpen || !launch?.auto_open || launch.action !== "open") return;
    if (!preview.preview_id || !safePreviewURL || isOpening || launch.represents_latest === false) return;

    const key = `${preview.preview_id}:${safePreviewURL}`;
    if (autoLaunchKeyRef.current === key) return;
    autoLaunchKeyRef.current = key;
    void launchPreview({
      previewId: preview.preview_id,
      previewUrl: safePreviewURL,
      target: "current_tab",
    });
  }, [
    isOpening,
    launch?.action,
    launch?.auto_open,
    launch?.represents_latest,
    launchPreview,
    launchSessionShouldOpen,
    preview,
    safePreviewURL,
  ]);

  const handleStartLatest = () => {
    setLaunchSessionActive(true);
    const startTargetId = previewTargetId(preview?.target_id);
    if (startTargetId) {
      startLatest.mutate(startTargetId);
      return;
    }
    if (launch?.action === "start") {
      startPullRequest.mutate();
    }
  };

  const handleRetry = () => {
    if (!preview?.preview_id) return;
    setLaunchSessionActive(true);
    restart.mutate(preview.preview_id);
  };

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title={title}
          description="Pull request preview"
          action={
            <PreviewStatusBadge
              status={preview?.status ?? "loading"}
              label={status}
              variant={preview?.status === "ready" ? "default" : "secondary"}
            />
          }
        />

        <Card>
          <CardContent className="space-y-5 pt-6">
            {previewQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">Loading PR preview...</p>
            ) : previewQuery.isError ? (
              <ErrorText className="text-sm">
                {previewQuery.error instanceof Error ? previewQuery.error.message : "Preview could not be loaded."}
              </ErrorText>
            ) : preview ? (
              <>
                {preview.new_commits_available ? (
                  <div className="flex items-start gap-3 rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                    <div className="min-w-0">
                      <p className="font-medium">New commits available</p>
                      <p className="break-all text-warning/80">
                        {launch?.message ?? `Latest head: ${preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}`}
                      </p>
                    </div>
                  </div>
                ) : null}

                {launchState ? (
                  <div className={`flex items-start gap-3 rounded-md border p-3 text-sm ${launchState.className}`}>
                    {launchState.loading ? (
                      <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin" />
                    ) : (
                      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                    )}
                    <div className="min-w-0">
                      <p className="font-medium">{launchState.title}</p>
                      <p>{launchState.description}</p>
                    </div>
                  </div>
                ) : null}

                {isExpired && !launchState ? (
                  <div className="flex items-start gap-3 rounded-md border border-border bg-muted/40 p-3 text-sm">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                    <div>
                      <p className="font-medium text-foreground">Preview expired</p>
                      <p className="text-muted-foreground">Restart to launch a fresh runtime for this pull request.</p>
                    </div>
                  </div>
                ) : null}

                <div className="grid gap-3 text-sm md:grid-cols-2">
                  <div>
                    <p className="text-muted-foreground">Repository</p>
                    <p className="font-medium text-foreground">{preview.repository_full_name ?? `${owner}/${repo}`}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Branch</p>
                    <p className="break-all font-medium text-foreground">{preview.branch ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Commit</p>
                    <p className="break-all font-medium text-foreground">{preview.commit_sha?.slice(0, 12) ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Phase</p>
                    <p className="font-medium text-foreground">{preview.current_phase ? formatPreviewStatus(preview.current_phase) : status}</p>
                  </div>
                </div>

                {preview.error ? (
                  <ErrorText className="rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm">
                    {preview.error}
                  </ErrorText>
                ) : null}
                {launchError ? (
                  <ErrorText className="rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm">
                    {launchError.message}
                  </ErrorText>
                ) : null}

                {preview.phase_steps?.length ? (
                  <div className="space-y-2">
                    <p className="text-sm font-medium text-foreground">Startup progress</p>
                    <div className="grid gap-2 md:grid-cols-4">
                      {preview.phase_steps.map((step) => (
                        <div key={step.name} className="rounded-md border border-border px-3 py-2">
                          <p className="text-sm font-medium capitalize text-foreground">{step.name.replaceAll("_", " ")}</p>
                          <p className="text-xs capitalize text-muted-foreground">{step.status}</p>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                {(preview.services?.length || preview.infrastructure?.length) ? (
                  <div className="grid gap-3 md:grid-cols-2">
                    <div className="space-y-2">
                      <p className="text-sm font-medium text-foreground">Services</p>
                      {(preview.services ?? []).map((service) => (
                        <div key={service.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                          <span className="truncate">{service.service_name}</span>
                          <PreviewStatusBadge status={service.status} variant={service.status === "ready" ? "default" : "secondary"} />
                        </div>
                      ))}
                    </div>
                    <div className="space-y-2">
                      <p className="text-sm font-medium text-foreground">Infrastructure</p>
                      {(preview.infrastructure ?? []).map((infra) => (
                        <div key={infra.id} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                          <span className="truncate">{infra.infra_name}</span>
                          <PreviewStatusBadge status={infra.status} variant={infra.status === "healthy" ? "default" : "secondary"} />
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                <div className="flex flex-col gap-2 sm:flex-row">
                  {safePreviewURL && preview.preview_id && launch?.action !== "blocked" && launch?.action !== "closed" && launch?.action !== "wait" && !preview.new_commits_available ? (
                    <OpenPreviewButton previewId={preview.preview_id} previewUrl={safePreviewURL} label={launch?.primary_label ?? "Open preview"} />
                  ) : null}
                  {preview.new_commits_available && safeExternalUrl(launch?.stale_preview_url ?? preview.preview_url) && preview.preview_id ? (
                    <OpenPreviewButton
                      previewId={preview.preview_id}
                      previewUrl={launch?.stale_preview_url ?? preview.preview_url}
                      label={launch?.secondary_label ?? "Open stale preview"}
                      variant="outline"
                    />
                  ) : null}
                  {safeExternalUrl(preview.pull_request_url) ? (
                    <Button asChild variant="outline">
                      <a href={safeExternalUrl(preview.pull_request_url)} target="_blank" rel="noopener noreferrer">
                        <GitPullRequest className="h-4 w-4" />
                        Open PR
                      </a>
                    </Button>
                  ) : null}
                  {safeExternalUrl(preview.github_branch_url) ? (
                    <Button asChild variant="outline">
                      <a href={safeExternalUrl(preview.github_branch_url)} target="_blank" rel="noopener noreferrer">
                        <GitBranch className="h-4 w-4" />
                        Branch
                      </a>
                    </Button>
                  ) : null}
                  {showStartAction ? (
                    <TooltipProvider delayDuration={150}>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            type="button"
                            variant={launch?.action === "start_latest" || launch?.action === "start" || launch?.action === "resume" ? "default" : "outline"}
                            aria-label={launch?.action === "start_latest" ? RESTART_LATEST_TOOLTIP : undefined}
                            onClick={handleStartLatest}
                            disabled={startLatest.isPending || startPullRequest.isPending}
                          >
                            <GitBranch className="h-4 w-4" />
                            {startLatest.isPending || startPullRequest.isPending ? "Starting..." : launchStartLabel(launch?.action, launch?.primary_label)}
                          </Button>
                        </TooltipTrigger>
                        {launch?.action === "start_latest" ? <TooltipContent>{RESTART_LATEST_TOOLTIP}</TooltipContent> : null}
                      </Tooltip>
                    </TooltipProvider>
                  ) : null}
                  {retryAllowed ? (
                    <Button
                      type="button"
                      variant={launch?.action === "retry" ? "default" : "outline"}
                      onClick={handleRetry}
                      disabled={restart.isPending}
                    >
                      <RotateCw className="h-4 w-4" />
                      {restart.isPending ? "Retrying..." : launch?.action === "retry" ? (launch.primary_label ?? "Retry preview") : "Retry"}
                    </Button>
                  ) : null}
                </div>
                {bootstrapFrame}
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}

function previewTargetId(id: string | undefined): string | null {
  if (!id || id === ZERO_UUID) return null;
  return id;
}

function launchStartLabel(action: NonNullable<BranchPreviewResponse["launch"]>["action"] | undefined, label?: string): string {
  if (label) return label;
  switch (action) {
    case "resume":
      return "Resume preview";
    case "start":
      return "Start preview";
    case "start_latest":
      return RESTART_LATEST_LABEL;
    default:
      return RESTART_LATEST_LABEL;
  }
}

function launchStateCopy(preview: BranchPreviewResponse | undefined): {
  title: string;
  description: string;
  className: string;
  loading?: boolean;
} | null {
  const launch = preview?.launch;
  if (!launch) return null;
  if (preview?.status === "failed") {
    return {
      title: "Preview failed",
      description: preview.error || launch.message || "Retry the preview to rebuild this pull request.",
      className: "border-destructive/20 bg-destructive/5 text-destructive",
    };
  }
  switch (launch.action) {
    case "wait":
      return {
        title: "Starting preview",
        description: launch.message ?? "The preview is starting and will be ready shortly.",
        className: "border-border bg-muted/40 text-foreground",
        loading: true,
      };
    case "resume":
      return {
        title: "Preview ready to resume",
        description: launch.message ?? "Resume this preview to open the running app.",
        className: "border-border bg-muted/40 text-foreground",
      };
    case "start":
      return {
        title: preview?.status === "expired" ? "Preview expired" : "Preview not started",
        description: launch.message ?? (preview?.status === "expired"
          ? "Restart to launch a fresh runtime for this pull request."
          : "Start a preview for the latest pull request head."),
        className: "border-border bg-muted/40 text-foreground",
      };
    case "retry":
      return {
        title: "Preview failed",
        description: launch.message ?? preview?.error ?? "Retry the preview to rebuild this pull request.",
        className: "border-destructive/20 bg-destructive/5 text-destructive",
      };
    case "blocked":
      return {
        title: "Preview blocked",
        description: launch.message ?? blockedLaunchMessage(launch.reason),
        className: "border-destructive/20 bg-destructive/5 text-destructive",
      };
    case "closed":
      return {
        title: "Pull request closed",
        description: launch.message ?? "This pull request is closed, so 143 will not start a new preview by default.",
        className: "border-border bg-muted/40 text-foreground",
      };
    default:
      return null;
  }
}

function blockedLaunchMessage(reason: NonNullable<BranchPreviewResponse["launch"]>["reason"]): string {
  switch (reason) {
    case "capacity":
      return "Preview capacity is currently full. Stop another preview or try again later.";
    case "role_forbidden":
      return "You can open existing previews, but you do not have permission to start a new preview for this pull request.";
    case "token_forbidden":
      return "This token is not scoped to start or read this preview.";
    case "config_required":
      return "This repository has multiple preview configs. Choose one before starting the preview.";
    case "config_invalid":
      return "The committed preview config is invalid.";
    default:
      return "This preview cannot be opened right now.";
  }
}
