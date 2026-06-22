"use client";

import { use, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  Circle,
  Clock3,
  ExternalLink,
  GitBranch,
  GitPullRequest,
  Loader2,
  MoreHorizontal,
  RotateCw,
  Square,
  XCircle,
} from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { OpenPreviewButton } from "@/components/preview/open-preview-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ErrorNotice } from "@/components/ui/error-notice";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
import { ACTIVE_PREVIEW_STATUSES, CONTROLLABLE_PREVIEW_STATUSES, formatPreviewStatus, type PreviewStatus } from "@/lib/preview-types";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
  PREVIEW_BOOTSTRAP_TIMEOUT_ERROR,
  PREVIEW_BOOTSTRAP_TIMEOUT_MS,
  PREVIEW_LAUNCH_COMPLETE_EVENT,
  previewBootstrapTimeoutDetails,
} from "@/lib/preview-bootstrap";
import { cn, safeExternalUrl } from "@/lib/utils";
import { pollMs } from "@/lib/poll-intervals";

type PreviewStepTone = "complete" | "active" | "failed" | "pending";

function previewUnavailableRecoveryCopy(unavailableReason?: string) {
  if (unavailableReason === "endpoint_unreachable") {
    return {
      title: "Preview connection lost",
      description:
        "The worker that was serving this preview stopped responding. Start the preview again to create a fresh runtime.",
    };
  }

  return null;
}

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
  const popupMode = launchMode && searchParams.get("popup") === "1";
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const bootstrappedPreviewIdRef = useRef<string | null>(null);
  const launchStartAttemptedRef = useRef<string | null>(null);
  const [bootstrapError, setBootstrapError] = useState<string | null>(null);
  const [detailsOpen, setDetailsOpen] = useState(false);

  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey: ["branch-preview", id],
    queryFn: () => api.previews.get(id),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return status === "starting" ? pollMs(3000) : false;
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
  const isExpired = preview?.status === "expired";
  const isFailed = preview?.status === "failed";
  const isStarting = preview?.status === "starting" || restartPreview.isPending;
  const isActive = Boolean(previewStatus && ACTIVE_PREVIEW_STATUSES.includes(previewStatus));
  const isReady = Boolean(previewStatus && CONTROLLABLE_PREVIEW_STATUSES.includes(previewStatus));
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
    ? preview.repository_full_name
    : preview
      ? `Preview ${(preview.target_id ?? preview.preview_id ?? "").slice(0, 8)}`
      : "Preview";
  const subtitleParts = [
    preview?.branch,
    preview?.commit_sha ? preview.commit_sha.slice(0, 12) : null,
    preview?.preview_config_name ? `Config ${preview.preview_config_name}` : null,
  ].filter(Boolean);
  const status = preview?.status ? formatPreviewStatus(preview.status) : "Loading";
  const unavailableRecovery = previewUnavailableRecoveryCopy(preview?.unavailable_reason);
  const stoppedAtText = preview?.stopped_at ? formatDateTime(preview.stopped_at) : null;
  const launchTargetId = preview?.preview_id ?? preview?.target_id;
  const launchError =
    bootstrapError ??
    (preview?.status === "failed" ? preview.error || "Preview failed to start." : null);
  const commandIsFailed = isFailed || Boolean(bootstrapError);

  const shouldStartForLaunch =
    launchMode &&
    preview &&
    launchTargetId &&
    !isActive &&
    !restartPreview.isPending &&
    !previewQuery.isFetching;

  useEffect(() => {
    if (!shouldStartForLaunch || !launchTargetId) return;
    // Dedupe on the stable route id, not launchTargetId: start-latest mints a
    // fresh preview_id, so keying on launchTargetId lets a failed preview slip
    // past the guard and auto-restart forever, hiding the error behind
    // "Opening when ready". One auto-start per page load; failures stick.
    if (launchStartAttemptedRef.current === id) return;
    launchStartAttemptedRef.current = id;
    restartPreview.mutate({ previewId: launchTargetId, latest: true });
  }, [id, launchTargetId, restartPreview, shouldStartForLaunch]);

  useEffect(() => {
    if (!launchMode || !previewOrigin || !previewUrl || !preview?.preview_id || !isReady) return;
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
  }, [bootstrapPreview, isReady, launchMode, popupMode, preview?.preview_id, previewOrigin, previewUrl]);

  const startLatest = () => {
    if (!launchTargetId) return;
    // Manual retry starts the preview directly, so mark auto-start as done for
    // this page rather than re-arming it — otherwise a second failure would
    // trip the auto-restart loop again.
    launchStartAttemptedRef.current = id;
    restartPreview.mutate({ previewId: launchTargetId, latest: true });
  };

  return (
    <PageContainer size="default">
      <div className="space-y-4">
        <Button asChild variant="ghost" size="sm" className="w-fit">
          <Link href="/previews">
            <ArrowLeft className="h-4 w-4" />
            Previews
          </Link>
        </Button>

        <PageHeader
          title={title}
          description={subtitleParts.length ? subtitleParts.join(" · ") : "Branch preview"}
          action={
            <div className="flex items-center gap-2">
              <span className="hidden text-xs text-muted-foreground sm:inline">{status}</span>
              {preview ? (
                <PreviewActions
                  preview={preview}
                  isActive={isActive}
                  isStarting={isStarting}
                  canRestart={Boolean(preview.preview_id)}
                  stopPending={stopPreview.isPending}
                  restartPending={restartPreview.isPending}
                  onStop={() => preview.preview_id && stopPreview.mutate(preview.preview_id)}
                  onRestart={() => preview.preview_id && restartPreview.mutate({ previewId: preview.preview_id, latest: false })}
                  onStartLatest={startLatest}
                  onRefresh={() => previewQuery.refetch()}
                />
              ) : null}
            </div>
          }
        />

        <Card className="shadow-sm">
          <CardContent className="space-y-5 p-5">
            {previewQuery.isLoading ? (
              <div className="flex items-center gap-3 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading preview...
              </div>
            ) : previewQuery.isError ? (
              <PreviewError
                title="Preview could not be loaded"
                message={previewQuery.error instanceof Error ? previewQuery.error.message : "Try refreshing the page."}
              />
            ) : preview ? (
              <>
                <PreviewCommandState
                  preview={preview}
                  launchMode={launchMode}
                  isReady={isReady}
                  isStarting={isStarting}
                  isFailed={commandIsFailed}
                  isExpired={isExpired}
                  launchError={launchError}
                  stoppedAtText={stoppedAtText}
                  startLatest={startLatest}
                />

                <PreviewMetadata preview={preview} stoppedAtText={stoppedAtText} />

                {preview.new_commits_available ? (
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

                <PreviewProgress preview={preview} prominent={isStarting || commandIsFailed} />


                {unavailableRecovery ? (
                  <div className="flex items-start gap-3 rounded-md border border-border bg-muted/40 p-3 text-sm">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                    <div>
                      <p className="font-medium text-foreground">{unavailableRecovery.title}</p>
                      <p className="text-muted-foreground">{unavailableRecovery.description}</p>
                    </div>
                  </div>
                ) : null}
                {stopPreview.isError ? (
                  <PreviewError
                    title="Preview could not be stopped"
                    message={stopPreview.error instanceof Error ? stopPreview.error.message : "Try again."}
                  />
                ) : null}

                {restartPreview.isError ? (
                  <PreviewError
                    title="Preview could not be restarted"
                    message={restartPreview.error instanceof Error ? restartPreview.error.message : "Try again."}
                  />
                ) : null}

                <Collapsible open={detailsOpen} onOpenChange={setDetailsOpen}>
                  <div className="flex items-center justify-between border-t border-border pt-4">
                    <div>
                      <p className="text-sm font-medium text-foreground">Details</p>
                      <p className="text-xs text-muted-foreground">Services, infrastructure, and stable links.</p>
                    </div>
                    <CollapsibleTrigger asChild>
                      <Button type="button" variant="outline" size="sm">
                        {detailsOpen ? "Hide details" : "Show details"}
                      </Button>
                    </CollapsibleTrigger>
                  </div>
                  <CollapsibleContent className="mt-4 space-y-4">
                    <PreviewAdvancedDetails preview={preview} />
                  </CollapsibleContent>
                </Collapsible>

                {isReady && launchMode && previewUrl ? (
                  <iframe
                    ref={iframeRef}
                    src={`${previewUrl.replace(/\/$/, "")}/bootstrap`}
                    title="Preview bootstrap"
                    className="hidden"
                  />
                ) : null}
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}

function PreviewCommandState({
  preview,
  launchMode,
  isReady,
  isStarting,
  isFailed,
  isExpired,
  launchError,
  stoppedAtText,
  startLatest,
}: {
  preview: BranchPreviewResponse;
  launchMode: boolean;
  isReady: boolean;
  isStarting: boolean;
  isFailed: boolean;
  isExpired: boolean;
  launchError: string | null;
  stoppedAtText: string | null;
  startLatest: () => void;
}) {
  const previewUrl = safeExternalUrl(preview.preview_url);
  const title = getCommandTitle({ launchMode, isReady, isStarting, isFailed, isExpired });
  const description = getCommandDescription({ launchMode, isReady, isStarting, isFailed, isExpired, stoppedAtText });

  return (
    <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-start">
      <div className="min-w-0 space-y-3">
        <div className="flex items-start gap-3">
          <StatusIcon status={preview.status} launchMode={launchMode} />
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-lg font-semibold tracking-tight text-foreground">{title}</h2>
              <Badge variant={isReady ? "default" : "secondary"} className="h-5 rounded-full px-2 text-xs">
                {formatPreviewStatus(preview.status)}
              </Badge>
            </div>
            <p className="max-w-2xl text-sm text-muted-foreground">{description}</p>
          </div>
        </div>

        {launchError ? (
          <PreviewError title={launchMode ? "Preview could not open" : "Preview could not start"} message={launchError} />
        ) : null}

        {preview.error && !launchError ? (
          <PreviewError title="Preview failed" message={preview.error} />
        ) : null}

        {previewUrl ? (
          <p className="truncate rounded-md border border-border bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground">
            {previewUrl}
          </p>
        ) : preview.stable_url ? (
          <p className="break-all rounded-md border border-border bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground">
            {preview.stable_url}
          </p>
        ) : null}
      </div>

      <div className="flex gap-2 md:justify-end">
        {isReady && previewUrl && !isFailed ? (
          launchMode ? (
            <Button type="button" disabled className="w-full sm:w-auto">
              <Loader2 className="h-4 w-4 animate-spin" />
              Connecting
            </Button>
          ) : (
            <OpenPreviewButton previewId={preview.preview_id} previewUrl={preview.preview_url} className="w-full sm:w-auto" />
          )
        ) : (
          <Button type="button" onClick={startLatest} disabled={isStarting} className="w-full sm:w-auto">
            {isStarting ? <Loader2 className="h-4 w-4 animate-spin" /> : <RotateCw className="h-4 w-4" />}
            {isStarting ? (launchMode ? "Waiting" : "Starting") : isFailed ? "Retry preview" : "Start preview"}
          </Button>
        )}
      </div>
    </div>
  );
}

function PreviewMetadata({ preview, stoppedAtText }: { preview: BranchPreviewResponse; stoppedAtText: string | null }) {
  const items = [
    { label: "Repository", value: preview.repository_full_name ?? preview.repository_id ?? "Unknown" },
    { label: "Branch", value: preview.branch ?? "Unknown" },
    { label: "Commit", value: preview.commit_sha ? preview.commit_sha.slice(0, 12) : "Unknown" },
    { label: "Source", value: preview.source_type ? formatPreviewStatus(preview.source_type) : "Manual" },
    { label: "Expires", value: preview.expires_at ? formatDateTime(preview.expires_at) : "No runtime" },
    { label: "Stopped", value: stoppedAtText ?? "Not stopped" },
  ];

  return (
    <div className="grid gap-3 rounded-lg border border-border/70 bg-muted/20 p-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
      {items.map((item) => (
        <div key={item.label} className="min-w-0">
          <p className="text-xs text-muted-foreground">{item.label}</p>
          <p className="break-words font-medium text-foreground">{item.value}</p>
        </div>
      ))}
    </div>
  );
}

function PreviewProgress({ preview, prominent }: { preview: BranchPreviewResponse; prominent: boolean }) {
  if (!preview.phase_steps?.length && !prominent) {
    return null;
  }

  const steps = preview.phase_steps?.length
    ? preview.phase_steps
    : [
        { name: "checkout", status: preview.status === "failed" ? "failed" : "pending" },
        { name: "install_build", status: "pending" },
        { name: "start_services", status: "pending" },
        { name: "readiness", status: "pending" },
      ];

  return (
    <div className="space-y-3">
      <div>
        <p className="text-sm font-medium text-foreground">Startup progress</p>
        <p className="text-xs text-muted-foreground">The preview opens after code, services, and readiness checks complete.</p>
      </div>
      <div className="grid gap-2 sm:grid-cols-4">
        {steps.map((step) => (
          <PreviewStep key={step.name} name={step.name} status={step.status} />
        ))}
      </div>
    </div>
  );
}

function PreviewStep({ name, status }: { name: string; status: string }) {
  const tone = getStepTone(status);
  const Icon = tone === "complete" ? CheckCircle2 : tone === "active" ? Loader2 : tone === "failed" ? XCircle : Circle;

  return (
    <div className="flex min-h-16 items-start gap-2 rounded-md border border-border px-3 py-2">
      <Icon
        className={cn(
          "mt-0.5 h-4 w-4 shrink-0",
          tone === "complete" && "text-primary",
          tone === "active" && "animate-spin text-muted-foreground",
          tone === "failed" && "text-destructive",
          tone === "pending" && "text-muted-foreground/60",
        )}
      />
      <div className="min-w-0">
        <p className="text-sm font-medium text-foreground">{formatStepName(name)}</p>
        <p className="text-xs capitalize text-muted-foreground">{status.replaceAll("_", " ")}</p>
      </div>
    </div>
  );
}

function PreviewActions({
  preview,
  isActive,
  isStarting,
  canRestart,
  stopPending,
  restartPending,
  onStop,
  onRestart,
  onStartLatest,
  onRefresh,
}: {
  preview: BranchPreviewResponse;
  isActive: boolean;
  isStarting: boolean;
  canRestart: boolean;
  stopPending: boolean;
  restartPending: boolean;
  onStop: () => void;
  onRestart: () => void;
  onStartLatest: () => void;
  onRefresh: () => void;
}) {
  const branchUrl = safeExternalUrl(preview.github_branch_url);
  const pullRequestUrl = safeExternalUrl(preview.pull_request_url);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" size="sm">
          <MoreHorizontal className="h-4 w-4" />
          Preview actions
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel>Preview actions</DropdownMenuLabel>
        <DropdownMenuItem onSelect={onRefresh}>
          <RotateCw className="h-4 w-4" />
          Refresh status
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={onStartLatest} disabled={restartPending || isStarting}>
          <GitBranch className="h-4 w-4" />
          Restart
        </DropdownMenuItem>
        {canRestart ? (
          <DropdownMenuItem onSelect={onRestart} disabled={restartPending}>
            <RotateCw className="h-4 w-4" />
            Restart runtime
          </DropdownMenuItem>
        ) : null}
        {isActive ? (
          <DropdownMenuItem variant="destructive" onSelect={onStop} disabled={stopPending}>
            <Square className="h-4 w-4" />
            Stop runtime
          </DropdownMenuItem>
        ) : null}
        {branchUrl || pullRequestUrl ? <DropdownMenuSeparator /> : null}
        {pullRequestUrl ? (
          <DropdownMenuItem asChild>
            <a href={pullRequestUrl} target="_blank" rel="noopener noreferrer">
              <GitPullRequest className="h-4 w-4" />
              Pull request
            </a>
          </DropdownMenuItem>
        ) : null}
        {branchUrl ? (
          <DropdownMenuItem asChild>
            <a href={branchUrl} target="_blank" rel="noopener noreferrer">
              <ExternalLink className="h-4 w-4" />
              GitHub branch
            </a>
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function PreviewAdvancedDetails({ preview }: { preview: BranchPreviewResponse }) {
  const hasServices = Boolean(preview.services?.length);
  const hasInfrastructure = Boolean(preview.infrastructure?.length);

  return (
    <div className="space-y-4">
      {(hasServices || hasInfrastructure) ? (
        <div className="grid gap-4 md:grid-cols-2">
          {hasServices ? (
            <div className="space-y-2">
              <p className="text-sm font-medium text-foreground">Services</p>
              {preview.services?.map((service) => (
                <div key={service.id} className="flex min-h-10 items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm">
                  <span className="truncate text-foreground">{service.service_name}</span>
                  <Badge variant={service.status === "ready" ? "default" : "secondary"}>{formatPreviewStatus(service.status)}</Badge>
                </div>
              ))}
            </div>
          ) : null}
          {hasInfrastructure ? (
            <div className="space-y-2">
              <p className="text-sm font-medium text-foreground">Infrastructure</p>
              {preview.infrastructure?.map((infra) => (
                <div key={infra.id} className="flex min-h-10 items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm">
                  <span className="truncate text-foreground">{infra.infra_name}</span>
                  <Badge variant={infra.status === "healthy" ? "default" : "secondary"}>{formatPreviewStatus(infra.status)}</Badge>
                </div>
              ))}
            </div>
          ) : null}
        </div>
      ) : (
        <p className="rounded-md border border-border bg-muted/20 px-3 py-2 text-sm text-muted-foreground">
          No service details are available for this preview yet.
        </p>
      )}

      <Separator />

      <div className="grid gap-3 text-xs sm:grid-cols-2">
        <DetailValue label="Stable link" value={preview.stable_url} />
        <DetailValue label="Created" value={preview.created_at ? formatDateTime(preview.created_at) : "Unknown"} />
        {preview.source_url ? <DetailValue label="Source link" value={preview.source_url} /> : null}
        {preview.request_id ? <DetailValue label="Request" value={preview.request_id} /> : null}
      </div>
    </div>
  );
}

function DetailValue({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <p className="text-muted-foreground">{label}</p>
      <p className="break-all font-medium text-foreground">{value}</p>
    </div>
  );
}

function PreviewError({ title, message }: { title: string; message: string }) {
  return <ErrorNotice title={title} description={message} />;
}

function StatusIcon({ status, launchMode }: { status: string; launchMode: boolean }) {
  if (status === "ready" || status === "partially_ready" || status === "unhealthy") {
    return (
      <div className="rounded-full border border-primary/20 bg-primary/10 p-2 text-primary">
        {launchMode ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}
      </div>
    );
  }
  if (status === "starting") {
    return (
      <div className="rounded-full border border-border bg-muted/50 p-2 text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
      </div>
    );
  }
  if (status === "failed") {
    return (
      <div className="rounded-full border border-destructive/20 bg-destructive/5 p-2 text-destructive">
        <XCircle className="h-4 w-4" />
      </div>
    );
  }
  return (
    <div className="rounded-full border border-border bg-muted/40 p-2 text-muted-foreground">
      <Clock3 className="h-4 w-4" />
    </div>
  );
}

function getCommandTitle({
  launchMode,
  isReady,
  isStarting,
  isFailed,
  isExpired,
}: {
  launchMode: boolean;
  isReady: boolean;
  isStarting: boolean;
  isFailed: boolean;
  isExpired: boolean;
}) {
  if (launchMode && isFailed) return "Preview could not open";
  if (launchMode && isReady) return "Opening preview";
  if (launchMode && isStarting) return "Opening when ready";
  if (isReady) return "Preview is ready";
  if (isStarting) return "Starting preview";
  if (isFailed) return "Preview failed";
  if (isExpired) return "Preview expired";
  return "Preview is stopped";
}

function getCommandDescription({
  launchMode,
  isReady,
  isStarting,
  isFailed,
  isExpired,
  stoppedAtText,
}: {
  launchMode: boolean;
  isReady: boolean;
  isStarting: boolean;
  isFailed: boolean;
  isExpired: boolean;
  stoppedAtText: string | null;
}) {
  if (launchMode && isFailed) return "This preview failed to start. Retry to try opening it again.";
  if (launchMode && isReady) return "Connecting this browser to the running preview.";
  if (launchMode && isStarting) return "This preview will open automatically when it is ready.";
  if (isReady) return "Open the running branch preview, or use preview actions for lifecycle controls.";
  if (isStarting) return "Preparing the branch runtime. The preview will be available after readiness checks pass.";
  if (isFailed) return "Retry the preview from this page, then use details only if the failure needs investigation.";
  if (isExpired) return stoppedAtText ? `Last stopped at ${stoppedAtText}. Start it again when you need it.` : "Start a fresh runtime for this branch.";
  return stoppedAtText ? `Last stopped at ${stoppedAtText}.` : "Start this preview when you need to see the branch running.";
}

function getStepTone(status: string): PreviewStepTone {
  const normalized = status.toLowerCase();
  if (["complete", "completed", "ready", "healthy", "success"].includes(normalized)) return "complete";
  if (["active", "running", "starting", "provisioning"].includes(normalized)) return "active";
  if (["failed", "unhealthy", "error"].includes(normalized)) return "failed";
  return "pending";
}

function formatStepName(name: string) {
  const normalized = name.replaceAll("_", " ").toLowerCase();
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

function formatDateTime(value: string) {
  return new Date(value).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}
