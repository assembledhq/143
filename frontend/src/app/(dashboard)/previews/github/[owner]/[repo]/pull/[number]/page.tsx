"use client";

import { use, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { useSearchParams } from "next/navigation";

import { OpenPreviewButton, usePreviewLauncher } from "@/components/preview/open-preview-button";
import { PreviewDetailSurface, type PreviewCommandOverride } from "@/components/preview/preview-detail-surface";
import { useAuth } from "@/hooks/use-auth";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
import { safeExternalUrl } from "@/lib/utils";
import { useLiveHealth } from "@/components/live-event-provider";
import { useDocumentVisible } from "@/hooks/use-document-visible";
import { useLiveQueryRegistration } from "@/hooks/use-live-query-registration";
import { liveRefreshInterval } from "@/lib/live-refresh-policy";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const RESTART_LATEST_LABEL = "Start latest preview";

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
  const liveHealth = useLiveHealth();
  const documentVisible = useDocumentVisible();
  const searchParams = useSearchParams();
  const { user } = useAuth();
  const canMutate = user ? user.role !== "viewer" : false;
  const launchRequested = searchParams.get("launch") === "1";
  const intent = launchRequested && canMutate ? "open" : "status";
  const queryKey = useMemo(() => ["branch-preview-pr", owner, repo, number, intent], [intent, number, owner, repo]);
  const [launchSessionActive, setLaunchSessionActive] = useState(false);
  const autoLaunchKeyRef = useRef<string | null>(null);
  const autoActionKeyRef = useRef<string | null>(null);
  const { launchPreview, isOpening, error: launchError, bootstrapFrame } = usePreviewLauncher();
  useLiveQueryRegistration({ queryKey, families: ["preview.detail"], priority: "critical", visible: documentVisible });
  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey,
    queryFn: ({ signal }) => api.previews.getPullRequest(owner, repo, number, { intent }, { signal }),
    refetchInterval: (query) => {
      const preview = query.state.data?.data;
      const status = preview?.status;
      const action = preview?.launch?.action;
      return status === "starting" || action === "wait" ? liveRefreshInterval(queryKey, "converging", liveHealth, documentVisible) : false;
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
  const targetId = previewTargetId(preview?.target_id);
  const launch = preview?.launch;
  const launchState = launchStateCopy(preview);
  const launchSessionShouldOpen = canMutate && (launchRequested || launchSessionActive);
  const retryAllowed =
    Boolean(preview?.preview_id) &&
    launch?.action !== "closed" &&
    (launch?.action !== "blocked" || preview?.status === "failed");
  const showStartAction =
    launch?.action === "start" ||
    (Boolean(targetId) && (launch?.action === "start_latest" || launch?.action === "resume"));
  const safePreviewURL = safeExternalUrl(preview?.preview_url);
  const topContent = preview?.new_commits_available ? (
    <div className="flex items-start gap-3 rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0">
        <p className="font-medium">New commits available</p>
        <p className="break-all text-warning/80">
          {launch?.message ?? `Latest head: ${preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}`}
        </p>
      </div>
    </div>
  ) : null;
  const stalePreviewAction =
    canMutate &&
    preview?.new_commits_available &&
    safeExternalUrl(launch?.stale_preview_url ?? preview.preview_url) &&
    preview.preview_id ? (
      <OpenPreviewButton
        previewId={preview.preview_id}
        previewUrl={launch?.stale_preview_url ?? preview.preview_url}
        label={launch?.secondary_label ?? "Open stale preview"}
        variant="outline"
        className="w-full sm:w-auto"
      />
    ) : null;

  useEffect(() => {
    if (!canMutate || !preview || !launchSessionShouldOpen || !launch?.auto_open) return;
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
    canMutate,
    preview,
    restart,
    startLatest,
    startPullRequest,
  ]);

  useEffect(() => {
    if (!canMutate || !preview || !launchSessionShouldOpen || !launch?.auto_open || launch.action !== "open") return;
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
    canMutate,
    preview,
    safePreviewURL,
  ]);

  const handleStartLatest = () => {
    if (!canMutate) return;
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
    if (!canMutate || !preview?.preview_id) return;
    setLaunchSessionActive(true);
    restart.mutate(preview.preview_id);
  };

  return (
    <PreviewDetailSurface
      preview={preview}
      isLoading={previewQuery.isLoading}
      error={previewQuery.isError ? previewQuery.error : undefined}
      title={title}
      description="Pull request preview"
      launchMode={launchSessionShouldOpen}
      launchError={launchError?.message ?? null}
      startPending={startLatest.isPending || startPullRequest.isPending || restart.isPending}
      canMutate={canMutate}
      hidePrimaryStart={Boolean(
        (launch?.action === "blocked" && preview?.status !== "failed") ||
          launch?.action === "closed" ||
          (!showStartAction && !retryAllowed),
      )}
      primaryStartLabel={retryAllowed && !showStartAction ? "Retry preview" : launchStartLabel(launch?.action, launch?.primary_label)}
      commandOverride={launchState}
      topContent={topContent}
      primaryExtraAction={stalePreviewAction}
      footerContent={bootstrapFrame}
      onStartLatest={retryAllowed && !showStartAction ? handleRetry : handleStartLatest}
      onRestart={preview?.preview_id ? handleRetry : undefined}
      onRefresh={() => previewQuery.refetch()}
      canRestart={Boolean(preview?.preview_id) && launch?.action !== "closed"}
    />
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
  tone?: PreviewCommandOverride["tone"];
  loading?: boolean;
} | null {
  const launch = preview?.launch;
  if (!launch) return null;
  if (preview?.status === "failed") {
    return {
      title: "Preview failed",
      description: preview.error || launch.message || "Retry the preview to rebuild this pull request.",
      tone: "destructive",
    };
  }
  switch (launch.action) {
    case "wait":
      return {
        title: "Starting preview",
        description: launch.message ?? "The preview is starting and will be ready shortly.",
        loading: true,
      };
    case "resume":
      return {
        title: "Preview ready to resume",
        description: launch.message ?? "Resume this preview to open the running app.",
      };
    case "start":
      return {
        title: preview?.status === "expired" ? "Preview expired" : "Preview not started",
        description: launch.message ?? (preview?.status === "expired"
          ? "Start a fresh preview for this pull request."
          : "Start a preview for the latest pull request head."),
      };
    case "retry":
      return {
        title: "Preview failed",
        description: launch.message ?? preview?.error ?? "Retry the preview to rebuild this pull request.",
        tone: "destructive",
      };
    case "blocked":
      return {
        title: "Preview blocked",
        description: launch.message ?? blockedLaunchMessage(launch.reason),
        tone: "destructive",
      };
    case "closed":
      return {
        title: "Pull request closed",
        description: launch.message ?? "This pull request is closed, so 143 will not start a new preview by default.",
        tone: "muted",
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
