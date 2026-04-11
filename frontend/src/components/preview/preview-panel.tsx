"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Play,
  Square,
  RotateCw,
  ExternalLink,
  Smartphone,
  Tablet,
  Monitor,
  Maximize2,
  Loader2,
  AlertTriangle,
  CheckCircle2,
  Circle,
  Palette,
  RefreshCw,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type {
  PreviewStatus,
  PreviewPhase,
} from "@/lib/preview-types";
import { ScreenshotTimeline } from "./screenshot-timeline";
import { ConsoleBadge } from "./console-badge";
import { DesignModeOverlay } from "./design-mode-overlay";
import { TTLWarning } from "./ttl-warning";

export const PREVIEW_BOOTSTRAP_READY_EVENT = "preview_bootstrap_ready";
export const PREVIEW_BOOTSTRAP_TOKEN_EVENT = "preview_bootstrap_token";

export function buildPreviewIframeSrc(previewOrigin: string): string {
  return `${previewOrigin}/bootstrap`;
}

export interface PreviewPanelProps {
  sessionId: string;
  orgId: string;
  previewOriginTemplate: string; // e.g. "http://{id}.preview.localhost:9090"
}

const WIDTH_PRESETS = [
  { name: "Mobile", width: 375, icon: Smartphone },
  { name: "Tablet", width: 768, icon: Tablet },
  { name: "Desktop", width: 1280, icon: Monitor },
  { name: "Full", width: 0, icon: Maximize2 },
] as const;

const PHASE_LABELS: Record<PreviewPhase, string> = {
  pending: "Pending",
  building: "Building",
  initializing: "Initializing",
  starting: "Starting",
  ready: "Ready",
  partially_ready: "Partially Ready",
  stopping: "Stopping",
  stopped: "Stopped",
  failed: "Failed",
};

const PHASE_ORDER: PreviewPhase[] = [
  "building",
  "initializing",
  "starting",
  "ready",
];

function phaseProgress(phase: PreviewPhase): number {
  const idx = PHASE_ORDER.indexOf(phase);
  if (idx === -1) return 0;
  return ((idx + 1) / PHASE_ORDER.length) * 100;
}

function phaseColor(phase: PreviewPhase): string {
  switch (phase) {
    case "ready":
      return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20";
    case "partially_ready":
      return "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20";
    case "failed":
      return "bg-destructive/15 text-destructive border-destructive/20";
    case "stopped":
      return "bg-muted text-muted-foreground border-border";
    default:
      return "bg-primary/15 text-primary border-primary/20";
  }
}

function serviceStatusIcon(status: string) {
  switch (status) {
    case "ready":
      return <CheckCircle2 className="size-3 text-emerald-500" />;
    case "failed":
      return <AlertTriangle className="size-3 text-destructive" />;
    case "starting":
    case "pending":
      return <Loader2 className="size-3 animate-spin text-muted-foreground" />;
    default:
      return <Circle className="size-3 text-muted-foreground" />;
  }
}

const KNOWN_FAILURE_PATTERNS: Record<string, string> = {
  port_conflict: "Port is already in use. Try restarting the preview.",
  build_failed: "Build failed. Check the build logs for errors.",
  dependency_install: "Failed to install dependencies. Check package.json.",
  timeout: "Preview timed out during startup. Try increasing the timeout.",
  oom: "Out of memory. The preview container ran out of memory.",
  missing_env: "Missing environment variables required for startup.",
};

export function PreviewPanel({
  sessionId,
  orgId: _orgId,
  previewOriginTemplate,
}: PreviewPanelProps) {
  const queryClient = useQueryClient();
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [selectedWidth, setSelectedWidth] = useState<number>(0); // 0 = full
  const [designMode, setDesignMode] = useState(false);
  const [bootstrapComplete, setBootstrapComplete] = useState(false);
  const [_iframeLoaded, setIframeLoaded] = useState(false);
  const [mutationError, setMutationError] = useState<string | null>(null);

  // Poll preview status every 3s when active
  const {
    data: previewStatus,
    isLoading: statusLoading,
    error: statusError,
    refetch: refetchStatus,
  } = useQuery({
    queryKey: ["preview-status", sessionId],
    queryFn: () => api.sessions.preview.get(sessionId),
    refetchInterval: (query) => {
      const phase = query.state.data?.instance?.phase;
      if (!phase || phase === "stopped" || phase === "failed") return false;
      return 3000;
    },
    retry: false,
  });

  const instance = previewStatus?.instance;
  const services = previewStatus?.services ?? [];
  const phase = instance?.phase;
  const isActive =
    phase === "ready" ||
    phase === "partially_ready" ||
    phase === "pending" ||
    phase === "building" ||
    phase === "initializing" ||
    phase === "starting";
  const isReady = phase === "ready" || phase === "partially_ready";

  // Start preview
  const startMutation = useMutation({
    mutationFn: () => api.sessions.preview.start(sessionId),
    onSuccess: () => {
      setMutationError(null);
      queryClient.invalidateQueries({
        queryKey: ["preview-status", sessionId],
      });
    },
    onError: (err) => {
      setMutationError(`Failed to start preview: ${err.message}`);
    },
  });

  // Stop preview
  const stopMutation = useMutation({
    mutationFn: () => api.sessions.preview.stop(sessionId),
    onSuccess: () => {
      setMutationError(null);
      setBootstrapComplete(false);
      setIframeLoaded(false);
      queryClient.invalidateQueries({
        queryKey: ["preview-status", sessionId],
      });
    },
    onError: (err) => {
      setMutationError(`Failed to stop preview: ${err.message}`);
    },
  });

  // Restart preview
  const restartMutation = useMutation({
    mutationFn: () => api.sessions.preview.restart(sessionId),
    onSuccess: () => {
      setMutationError(null);
      setBootstrapComplete(false);
      setIframeLoaded(false);
      queryClient.invalidateQueries({
        queryKey: ["preview-status", sessionId],
      });
    },
    onError: (err) => {
      setMutationError(`Failed to restart preview: ${err.message}`);
    },
  });

  // Bootstrap token exchange
  const bootstrapMutation = useMutation({
    mutationFn: () => api.sessions.preview.bootstrap(sessionId),
    onError: (err) => {
      setMutationError(`Failed to bootstrap preview: ${err.message}`);
    },
  });

  const bootstrapMutateRef = useRef(bootstrapMutation.mutate);
  useEffect(() => {
    bootstrapMutateRef.current = bootstrapMutation.mutate;
  }, [bootstrapMutation.mutate]);

  const previewOrigin = instance
    ? previewOriginTemplate.replace("{id}", instance.id)
    : "";

  // Post bootstrap token to iframe, retrying on iframe load if needed
  const sendBootstrapToken = useCallback(
    (token: string, origin: string) => {
      const contentWindow = iframeRef.current?.contentWindow;
      if (contentWindow) {
        contentWindow.postMessage(
          { type: PREVIEW_BOOTSTRAP_TOKEN_EVENT, token },
          origin
        );
        setBootstrapComplete(true);
        return;
      }
      // Iframe not loaded yet - wait for load event and retry
      const iframe = iframeRef.current;
      if (!iframe) return;
      const onLoad = () => {
        iframe.removeEventListener("load", onLoad);
        const cw = iframe.contentWindow;
        if (cw) {
          cw.postMessage(
            { type: PREVIEW_BOOTSTRAP_TOKEN_EVENT, token },
            origin
          );
          setBootstrapComplete(true);
        }
      };
      iframe.addEventListener("load", onLoad);
    },
    []
  );

  // Handle postMessage exchange for bootstrap
  const handleMessage = useCallback(
    (event: MessageEvent) => {
      if (!previewOrigin || event.origin !== new URL(previewOrigin).origin)
        return;

      if (event.data?.type === PREVIEW_BOOTSTRAP_READY_EVENT) {
        bootstrapMutateRef.current(undefined, {
          onSuccess: (data) => {
            setMutationError(null);
            sendBootstrapToken(data.token, new URL(previewOrigin).origin);
          },
        });
      }
    },
    [previewOrigin, sendBootstrapToken]
  );

  useEffect(() => {
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [handleMessage]);

  // Set iframe src when preview is ready
  const iframeSrc =
    isReady && previewOrigin ? buildPreviewIframeSrc(previewOrigin) : undefined;

  const isMutating =
    startMutation.isPending ||
    stopMutation.isPending ||
    restartMutation.isPending;

  if (statusLoading) {
    return (
      <div className="flex flex-col gap-3">
        <div className="rounded-lg border border-dashed p-8 flex items-center justify-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Loading preview status...
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {/* Controls bar */}
      <div className="flex items-center gap-2 flex-wrap">
        {/* Start / Stop / Restart */}
        <div className="flex items-center gap-1">
          {!isActive && phase !== "stopping" ? (
            <Button
              size="sm"
              onClick={() => startMutation.mutate()}
              disabled={isMutating}
              loading={startMutation.isPending}
            >
              <Play className="size-3.5" />
              Start Preview
            </Button>
          ) : (
            <>
              <Button
                size="sm"
                variant="outline"
                onClick={() => stopMutation.mutate()}
                disabled={isMutating}
                loading={stopMutation.isPending}
              >
                <Square className="size-3.5" />
                Stop
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => restartMutation.mutate()}
                disabled={isMutating}
                loading={restartMutation.isPending}
              >
                <RotateCw className="size-3.5" />
                Restart
              </Button>
            </>
          )}
        </div>

        {/* Status badge */}
        {phase && (
          <Badge variant="secondary" className={cn(phaseColor(phase))}>
            {(phase === "building" ||
              phase === "initializing" ||
              phase === "starting") && (
              <Loader2 className="size-3 animate-spin" />
            )}
            {phase === "ready" && <CheckCircle2 className="size-3" />}
            {phase === "failed" && <AlertTriangle className="size-3" />}
            {PHASE_LABELS[phase]}
          </Badge>
        )}

        {/* Console errors badge */}
        {isReady && <ConsoleBadge sessionId={sessionId} />}

        {/* TTL Warning */}
        {instance?.expires_at && isActive && (
          <TTLWarning
            expiresAt={instance.expires_at}
            sessionId={sessionId}
          />
        )}

        <div className="flex-1" />

        {/* Width presets */}
        {isReady && (
          <TooltipProvider>
            <div className="flex items-center gap-0.5 rounded-md border bg-muted/50 p-0.5">
              {WIDTH_PRESETS.map((preset) => (
                <Tooltip key={preset.name}>
                  <TooltipTrigger asChild>
                    <button
                      onClick={() => setSelectedWidth(preset.width)}
                      className={cn(
                        "rounded p-1 transition-colors",
                        selectedWidth === preset.width
                          ? "bg-background text-foreground shadow-sm"
                          : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      <preset.icon className="size-3.5" />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent>
                    {preset.name}
                    {preset.width > 0 ? ` (${preset.width}px)` : ""}
                  </TooltipContent>
                </Tooltip>
              ))}
            </div>
          </TooltipProvider>
        )}

        {/* Design mode toggle */}
        {isReady && (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="icon-sm"
                  variant={designMode ? "default" : "outline"}
                  onClick={() => setDesignMode(!designMode)}
                >
                  <Palette className="size-3.5" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {designMode ? "Exit Design Mode" : "Design Mode"}
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}

        {/* Open in new tab */}
        {isReady && previewOrigin && (
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  asChild
                >
                  <a
                    href={previewOrigin}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <ExternalLink className="size-3.5" />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Open in new tab</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )}
      </div>

      {/* Mutation error banner */}
      {mutationError && (
        <div className="flex items-center gap-2 rounded-lg border border-destructive/20 bg-destructive/5 p-2 text-sm text-destructive">
          <AlertTriangle className="size-4 shrink-0" />
          <span className="flex-1">{mutationError}</span>
          <button
            onClick={() => setMutationError(null)}
            className="rounded p-0.5 hover:bg-destructive/10"
          >
            <X className="size-3.5" />
          </button>
        </div>
      )}

      {/* Query error state */}
      {statusError && (
        <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-3 space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium text-destructive">
            <AlertTriangle className="size-4" />
            Failed to load preview status
          </div>
          <p className="text-xs text-muted-foreground">
            {statusError.message}
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => refetchStatus()}
          >
            <RefreshCw className="size-3.5" />
            Retry
          </Button>
        </div>
      )}

      {/* Service status indicators */}
      {services.length > 1 && isActive && (
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          {services.map((svc) => (
            <div key={svc.name} className="flex items-center gap-1">
              {serviceStatusIcon(svc.status)}
              <span>{svc.name}</span>
              {svc.error && (
                <span className="text-destructive truncate max-w-[200px]">
                  ({svc.error})
                </span>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Startup progress */}
      {isActive && !isReady && (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            {PHASE_ORDER.map((p, i) => {
              const currentIdx = PHASE_ORDER.indexOf(
                phase as PreviewPhase
              );
              const isDone = i < currentIdx;
              const isCurrent = i === currentIdx;
              return (
                <div
                  key={p}
                  className={cn(
                    "flex items-center gap-1",
                    isDone && "text-emerald-600 dark:text-emerald-400",
                    isCurrent && "text-primary font-medium"
                  )}
                >
                  {isDone ? (
                    <CheckCircle2 className="size-3" />
                  ) : isCurrent ? (
                    <Loader2 className="size-3 animate-spin" />
                  ) : (
                    <Circle className="size-3" />
                  )}
                  {PHASE_LABELS[p]}
                  {i < PHASE_ORDER.length - 1 && (
                    <span className="text-muted-foreground/50 mx-1">
                      &rarr;
                    </span>
                  )}
                </div>
              );
            })}
          </div>
          <div className="h-1 rounded-full bg-muted overflow-hidden">
            <div
              className="h-full bg-primary rounded-full transition-all duration-500"
              style={{ width: `${phaseProgress(phase as PreviewPhase)}%` }}
            />
          </div>
        </div>
      )}

      {/* Failure diagnostics */}
      {phase === "failed" && instance && (
        <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-3 space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium text-destructive">
            <AlertTriangle className="size-4" />
            Preview failed to start
          </div>
          {instance.error && (
            <p className="text-xs text-muted-foreground">{instance.error}</p>
          )}
          {instance.failure_pattern &&
            KNOWN_FAILURE_PATTERNS[instance.failure_pattern] && (
              <p className="text-xs text-muted-foreground">
                <strong>Suggestion:</strong>{" "}
                {KNOWN_FAILURE_PATTERNS[instance.failure_pattern]}
              </p>
            )}
          {instance.build_log && (
            <details className="text-xs">
              <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                Build log
              </summary>
              <pre className="mt-1 max-h-40 overflow-auto rounded bg-muted p-2 text-xs leading-relaxed">
                {instance.build_log}
              </pre>
            </details>
          )}
          <Button
            size="sm"
            variant="outline"
            onClick={() => restartMutation.mutate()}
            disabled={isMutating}
          >
            <RefreshCw className="size-3.5" />
            Try Again
          </Button>
        </div>
      )}

      {/* Preview iframe */}
      {isReady && iframeSrc && (
        <div className="relative rounded-lg border bg-muted/30 overflow-hidden">
          <div
            className={cn(
              "mx-auto transition-all duration-300",
              selectedWidth > 0 ? "border-x border-dashed border-border" : ""
            )}
            style={{
              maxWidth: selectedWidth > 0 ? `${selectedWidth}px` : "100%",
              width: "100%",
            }}
          >
            <div className="relative" style={{ paddingBottom: "62.5%" }}>
              <iframe
                ref={iframeRef}
                src={iframeSrc}
                className="absolute inset-0 w-full h-full bg-white"
                sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-downloads allow-popups allow-popups-to-escape-sandbox"
                onLoad={() => setIframeLoaded(true)}
                title="Preview"
              />
              {/* Loading overlay before bootstrap */}
              {!bootstrapComplete && (
                <div className="absolute inset-0 flex items-center justify-center bg-background/80">
                  <div className="flex items-center gap-2 text-sm text-muted-foreground">
                    <Loader2 className="size-4 animate-spin" />
                    Connecting to preview...
                  </div>
                </div>
              )}
              {/* Design mode overlay */}
              {designMode && bootstrapComplete && (
                <DesignModeOverlay
                  sessionId={sessionId}
                  iframeRef={iframeRef}
                  previewOrigin={previewOrigin}
                />
              )}
            </div>
          </div>
        </div>
      )}

      {/* Idle state */}
      {(!phase || phase === "stopped") && !statusLoading && (
        <div className="rounded-lg border border-dashed p-8 flex flex-col items-center justify-center gap-3 text-center">
          <div className="rounded-full bg-muted p-3">
            <Monitor className="size-5 text-muted-foreground" />
          </div>
          <div className="space-y-1">
            <p className="text-sm font-medium">No preview running</p>
            <p className="text-xs text-muted-foreground">
              Start a preview to see live changes from the agent.
            </p>
          </div>
          <Button
            size="sm"
            onClick={() => startMutation.mutate()}
            disabled={isMutating}
            loading={startMutation.isPending}
          >
            <Play className="size-3.5" />
            Start Preview
          </Button>
        </div>
      )}

      {/* Screenshot timeline */}
      {isReady && (
        <ScreenshotTimeline
          snapshots={previewStatus?.snapshots ?? []}
        />
      )}
    </div>
  );
}
