"use client";

import { use, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";

import { PreviewDetailSurface } from "@/components/preview/preview-detail-surface";
import { ErrorNotice } from "@/components/ui/error-notice";
import { useAuth } from "@/hooks/use-auth";
import { api } from "@/lib/api";
import { pollMs } from "@/lib/poll-intervals";
import { ACTIVE_PREVIEW_STATUSES, type PreviewStatus } from "@/lib/preview-types";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
  PREVIEW_BOOTSTRAP_TIMEOUT_ERROR,
  PREVIEW_BOOTSTRAP_TIMEOUT_MS,
  PREVIEW_LAUNCH_COMPLETE_EVENT,
  previewBootstrapTimeoutDetails,
} from "@/lib/preview-bootstrap";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
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
  const { user } = useAuth();
  const launchMode = searchParams.get("launch") === "1";
  const popupMode = launchMode && searchParams.get("popup") === "1";
  const canMutate = user ? user.role !== "viewer" : false;
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const bootstrappedPreviewIdRef = useRef<string | null>(null);
  const launchStartAttemptedRef = useRef<string | null>(null);
  const [bootstrapError, setBootstrapError] = useState<string | null>(null);

  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey: ["branch-preview", id],
    queryFn: () => api.previews.get(id),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      if (status === "starting") return pollMs(3000);
      if (status === "ready" || status === "partially_ready" || status === "unhealthy") {
        return pollMs(15000);
      }
      return false;
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
  const previewStatus = preview?.status as PreviewStatus | undefined;
  const isActive = Boolean(previewStatus && ACTIVE_PREVIEW_STATUSES.includes(previewStatus));
  const isReady = previewStatus === "ready" || previewStatus === "partially_ready";
  const previewUrl = safeExternalUrl(preview?.preview_url);
  const previewOrigin = useMemo(() => {
    if (!previewUrl) return "";
    try {
      return new URL(previewUrl).origin;
    } catch {
      return "";
    }
  }, [previewUrl]);
  const launchTargetId = preview?.preview_id ?? preview?.target_id;
  const shouldStartForLaunch =
    canMutate &&
    launchMode &&
    preview &&
    launchTargetId &&
    !isActive &&
    !restartPreview.isPending &&
    !previewQuery.isFetching;
  const launchError =
    bootstrapError ??
    (preview?.status === "failed" ? preview.error || "Preview failed to start." : null) ??
    (preview?.status === "unhealthy"
      ? preview.error || "The preview stopped responding - a service crashed. Restart to try again."
      : null);

  useEffect(() => {
    if (!shouldStartForLaunch || !launchTargetId) return;
    if (launchStartAttemptedRef.current === id) return;
    launchStartAttemptedRef.current = id;
    restartPreview.mutate({ previewId: launchTargetId, latest: true });
  }, [id, launchTargetId, restartPreview, shouldStartForLaunch]);

  useEffect(() => {
    if (!canMutate || !launchMode || !previewOrigin || !previewUrl || !preview?.preview_id || !isReady) return;
    const activePreviewId = preview.preview_id;
    let completed = false;
    let timedOut = false;
    const timeout = window.setTimeout(() => {
      if (completed) return;
      timedOut = true;
      bootstrappedPreviewIdRef.current = null;
      setBootstrapError(`${PREVIEW_BOOTSTRAP_TIMEOUT_ERROR} ${previewBootstrapTimeoutDetails()}`);
    }, PREVIEW_BOOTSTRAP_TIMEOUT_MS);

    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== previewOrigin) return;
      if (event.data?.type === PREVIEW_BOOTSTRAP_COMPLETE_EVENT) {
        if (!timedOut && bootstrappedPreviewIdRef.current === activePreviewId) {
          completed = true;
          window.clearTimeout(timeout);
          if (popupMode && window.opener) {
            (window.opener as Window).postMessage(
              { type: PREVIEW_LAUNCH_COMPLETE_EVENT, url: previewUrl },
              previewOrigin,
            );
            window.close();
            return;
          }
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
          if (timedOut) return;
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
    return () => {
      window.clearTimeout(timeout);
      window.removeEventListener("message", handleMessage);
    };
  }, [bootstrapPreview, canMutate, isReady, launchMode, popupMode, preview?.preview_id, previewOrigin, previewUrl]);

  const startLatest = () => {
    if (!canMutate || !launchTargetId) return;
    launchStartAttemptedRef.current = id;
    restartPreview.mutate({ previewId: launchTargetId, latest: true });
  };

  const title = preview?.repository_full_name
    ? preview.repository_full_name
    : preview
      ? `Preview ${(preview.target_id ?? preview.preview_id ?? "").slice(0, 8)}`
      : "Preview";
  const subtitleParts = [
    preview?.branch,
    preview?.commit_sha ? preview.commit_sha.slice(0, 12) : null,
    preview?.preview_config_name ? `Config ${preview.preview_config_name}` : null,
  ].filter(Boolean);
  const mutationError = stopPreview.error ?? restartPreview.error;

  return (
    <PreviewDetailSurface
      preview={preview}
      isLoading={previewQuery.isLoading}
      error={previewQuery.isError ? previewQuery.error : undefined}
      title={title}
      description={subtitleParts.length ? subtitleParts.join(" · ") : "Branch preview"}
      launchMode={launchMode}
      launchError={launchError}
      startPending={restartPreview.isPending}
      stopPending={stopPreview.isPending}
      restartPending={restartPreview.isPending}
      canMutate={canMutate}
      onStartLatest={startLatest}
      onStop={() => canMutate && preview?.preview_id && stopPreview.mutate(preview.preview_id)}
      onRestart={() =>
        canMutate && preview?.preview_id && restartPreview.mutate({ previewId: preview.preview_id, latest: false })
      }
      onRefresh={() => previewQuery.refetch()}
      topContent={
        <>
          {preview?.new_commits_available ? (
            <div className="flex items-start gap-3 rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
              <div className="min-w-0">
                <p className="font-medium">New commits available</p>
                <p className="break-all text-warning/80">
                  Latest: {preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}
                </p>
              </div>
            </div>
          ) : null}
          {mutationError ? (
            <ErrorNotice
              title="Preview action failed"
              description={mutationError instanceof Error ? mutationError.message : "Try again."}
            />
          ) : null}
        </>
      }
      footerContent={
        canMutate && isReady && launchMode && previewUrl ? (
          <iframe
            ref={iframeRef}
            src={`${previewUrl.replace(/\/$/, "")}/bootstrap`}
            title="Preview bootstrap"
            className="hidden"
          />
        ) : null
      }
    />
  );
}
