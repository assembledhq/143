"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  ChevronDown,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import {
  PREVIEW_ERROR_CODES,
  type PreviewStatus,
  type PreviewInfrastructure,
  type PreviewService,
} from "@/lib/preview-types";
import { ConsoleBadge } from "./console-badge";
import { DesignModeOverlay } from "./design-mode-overlay";
import { ErrorBoundary } from "@/components/error-boundary";
import { TTLWarning } from "./ttl-warning";

export const PREVIEW_BOOTSTRAP_READY_EVENT = "preview_bootstrap_ready";
export const PREVIEW_BOOTSTRAP_TOKEN_EVENT = "preview_bootstrap_token";

export function buildPreviewIframeSrc(previewOrigin: string): string {
  return `${previewOrigin}/bootstrap`;
}

export interface PreviewPanelProps {
  sessionId: string;
  previewOriginTemplate: string; // e.g. "http://{id}.preview.localhost:9090"
}

const WIDTH_PRESETS = [
  { name: "Mobile", width: 375, icon: Smartphone },
  { name: "Tablet", width: 768, icon: Tablet },
  { name: "Desktop", width: 1280, icon: Monitor },
  { name: "Full", width: 0, icon: Maximize2 },
] as const;

const STATUS_LABELS: Record<PreviewStatus, string> = {
  starting: "Starting",
  ready: "Ready",
  partially_ready: "Partially Ready",
  unhealthy: "Unhealthy",
  stopped: "Stopped",
  failed: "Failed",
  expired: "Expired",
};

const STARTUP_PHASES = ["Provisioning", "Starting", "Opening"] as const;
const STARTUP_PHASE_RAIL_STACK_WIDTH = 300;
const STARTUP_PHASE_RAIL_COMPACT_WIDTH = 420;

type StartupPhaseRailLayout = "default" | "compact" | "stacked";

function getStartupPhaseRailLayout(
  width: number,
  phaseCount: number,
): StartupPhaseRailLayout {
  if (phaseCount <= 1) {
    return "default";
  }
  if (width < STARTUP_PHASE_RAIL_STACK_WIDTH) {
    return "stacked";
  }
  if (phaseCount >= 3 && width < STARTUP_PHASE_RAIL_COMPACT_WIDTH) {
    return "compact";
  }
  return "default";
}

function statusColor(status: PreviewStatus): string {
  switch (status) {
    case "ready":
      return "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20";
    case "partially_ready":
      return "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20";
    case "failed":
    case "unhealthy":
      return "bg-destructive/15 text-destructive border-destructive/20";
    case "stopped":
    case "expired":
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

type StartupChecklistStepState = "complete" | "active" | "pending" | "failed";

interface StartupChecklistStep {
  title: string;
  state: StartupChecklistStepState;
  detail: string;
}

function buildStartupChecklist(
  status: PreviewStatus | undefined,
  services: PreviewService[],
  infrastructure: PreviewInfrastructure[],
): StartupChecklistStep[] {
  // When the parent preview reaches a terminal state, force any child rows
  // that were still pending to render as terminal too. The backend cascades
  // these on terminal transitions; this defensive layer also covers
  // historical rows from before the cascade landed.
  const parentTerminal =
    status === "failed" || status === "stopped" || status === "expired";
  const parentFailed = status === "failed";

  const infraFailed = infrastructure.find(
    (item) => item.status === "failed" || item.status === "unhealthy",
  );
  const infraProvisioning = infrastructure.find(
    (item) => item.status === "provisioning",
  );
  const allInfraHealthy =
    infrastructure.length > 0 &&
    infrastructure.every((item) => item.status === "healthy");

  const serviceFailed = services.find((service) => service.status === "failed");
  const serviceStarting = services.find((service) => service.status === "starting");
  const allServicesReady =
    services.length > 0 && services.every((service) => service.status === "ready");

  const openPreviewState: StartupChecklistStepState =
    status === "failed"
      ? "failed"
      : status === "ready" || status === "partially_ready"
        ? "complete"
        : status === "starting"
          ? "active"
          : "pending";

  const openPreviewDetail =
    status === "ready"
      ? "Preview is ready to open."
      : status === "partially_ready"
        ? "Primary service is ready while background services finish."
        : status === "failed"
          ? "Preview startup failed before the app became reachable."
          : "Waiting for the preview URL to become reachable.";

  const steps: StartupChecklistStep[] = [];

  if (infrastructure.length > 0) {
    let infraState: StartupChecklistStepState;
    let infraDetail: string;
    if (infraFailed) {
      infraState = "failed";
      infraDetail = `${infraFailed.infra_name} failed to become healthy`;
    } else if (allInfraHealthy) {
      infraState = "complete";
      infraDetail = `${
        infrastructure.length === 1
          ? infrastructure[0].infra_name
          : `${infrastructure.length} services`
      } ready`;
    } else if (parentTerminal) {
      infraState = parentFailed ? "failed" : "pending";
      infraDetail = parentFailed
        ? `${infraProvisioning?.infra_name ?? "Infrastructure"} did not finish provisioning`
        : "Infrastructure was stopped before reaching ready.";
    } else if (infraProvisioning) {
      infraState = "active";
      infraDetail = `${infraProvisioning.infra_name} is provisioning`;
    } else {
      infraState = "pending";
      infraDetail = "Waiting to start preview infrastructure.";
    }
    steps.push({ title: "Spin up infrastructure", state: infraState, detail: infraDetail });
  }

  let serviceState: StartupChecklistStepState;
  let serviceDetail: string;
  if (serviceFailed) {
    serviceState = "failed";
    serviceDetail = `${serviceFailed.service_name} failed to start`;
  } else if (allServicesReady) {
    serviceState = "complete";
    serviceDetail = `${
      services.length === 1
        ? services[0]?.service_name ?? "App"
        : `${services.length} services`
    } ready`;
  } else if (parentTerminal) {
    serviceState = parentFailed ? "failed" : "pending";
    serviceDetail = parentFailed
      ? `${serviceStarting?.service_name ?? "Service"} did not finish starting`
      : "Services were stopped before reaching ready.";
  } else if (serviceStarting) {
    serviceState = "active";
    serviceDetail = `${serviceStarting.service_name} is starting`;
  } else {
    serviceState = "pending";
    serviceDetail = "Waiting for services to boot.";
  }
  steps.push({ title: "Start services", state: serviceState, detail: serviceDetail });

  steps.push({
    title: "Open the preview",
    state: openPreviewState,
    detail: openPreviewDetail,
  });

  return steps;
}

function startupStepIcon(state: StartupChecklistStepState) {
  switch (state) {
    case "complete":
      return <CheckCircle2 className="size-3.5 text-emerald-500" />;
    case "active":
      return <Loader2 className="size-3.5 animate-spin text-primary" />;
    case "failed":
      return <AlertTriangle className="size-3.5 text-destructive" />;
    default:
      return <Circle className="size-3.5 text-muted-foreground" />;
  }
}

function getStartupSubtitle(
  status: PreviewStatus | undefined,
  services: PreviewService[],
  infrastructure: PreviewInfrastructure[],
): string {
  const provisioning = infrastructure.find(
    (item) => item.status === "provisioning",
  );
  if (provisioning) return `Provisioning ${provisioning.infra_name}`;

  const starting = services.find((service) => service.status === "starting");
  if (starting) return `Starting ${starting.service_name}`;

  if (status === "partially_ready") {
    return "Opening preview";
  }

  return "Starting services";
}

function startupPhaseState(
  phase: (typeof STARTUP_PHASES)[number],
  services: PreviewService[],
  infrastructure: PreviewInfrastructure[],
): StartupChecklistStepState {
  const hasInfrastructure = infrastructure.length > 0;
  const allInfrastructureHealthy =
    hasInfrastructure && infrastructure.every((item) => item.status === "healthy");
  const provisioning = infrastructure.some((item) => item.status === "provisioning");
  const anyServiceReady = services.some((service) => service.status === "ready");
  const anyServiceStarting = services.some((service) => service.status === "starting");
  const allServicesReady =
    services.length > 0 && services.every((service) => service.status === "ready");

  if (phase === "Provisioning") {
    if (!hasInfrastructure || allInfrastructureHealthy) return "complete";
    if (provisioning) return "active";
    return "pending";
  }

  if (phase === "Starting") {
    if (allServicesReady) return "complete";
    if (!hasInfrastructure || allInfrastructureHealthy || anyServiceStarting || anyServiceReady) {
      return "active";
    }
    return "pending";
  }

  // "Opening" has no `complete` state inside this canvas — completion is
  // signalled by the canvas unmounting once the parent transitions to
  // partially_ready or ready.
  if (allServicesReady) return "active";
  return "pending";
}

export function PreviewPanel({
  sessionId,
  previewOriginTemplate,
}: PreviewPanelProps) {
  const queryClient = useQueryClient();
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const startupPhaseRailRef = useRef<HTMLDivElement | null>(null);
  const [selectedWidth, setSelectedWidth] = useState<number>(0); // 0 = full
  const [designMode, setDesignMode] = useState(false);
  const [bootstrapComplete, setBootstrapComplete] = useState(false);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const [startupPhaseRailLayout, setStartupPhaseRailLayout] = useState<StartupPhaseRailLayout>("default");

  // Poll preview status every 3s when active
  const {
    data: previewStatus,
    isLoading: statusLoading,
    error: statusError,
    refetch: refetchStatus,
  } = useQuery({
    queryKey: ["preview-status", sessionId],
    queryFn: () =>
      api.sessions.preview.get(sessionId).catch((err) => {
        // Treat NO_ACTIVE_PREVIEW as a clean "no preview" state, not an error.
        if (err?.code === "NO_ACTIVE_PREVIEW") return null;
        throw err;
      }),
    refetchInterval: (query) => {
      const st = query.state.data?.instance?.status;
      if (!st || st === "stopped" || st === "failed" || st === "expired") return false;
      return 3000;
    },
    retry: (failureCount, error) => {
      // Don't retry NO_ACTIVE_PREVIEW — it's a normal state, not a transient failure.
      if ((error as { code?: string })?.code === "NO_ACTIVE_PREVIEW") return false;
      return failureCount < 2;
    },
  });

  const instance = previewStatus?.instance;
  const rawServices = previewStatus?.services;
  const rawInfrastructure = previewStatus?.infrastructure;
  const runtimePreviewOrigin = previewStatus?.preview_origin;
  const services = useMemo(
    () => (Array.isArray(rawServices) ? rawServices : []),
    [rawServices],
  );
  const infrastructure = useMemo(
    () => (Array.isArray(rawInfrastructure) ? rawInfrastructure : []),
    [rawInfrastructure],
  );
  const status = instance?.status;
  const isActive =
    status === "ready" ||
    status === "partially_ready" ||
    status === "starting";
  const isReady = status === "ready" || status === "partially_ready";
  const hasStartupRows = services.length > 0 || infrastructure.length > 0;
  const showStartupProgress =
    (isActive && !isReady) || (status === "failed" && hasStartupRows);

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
      // Hydrate flow yields a few specific error codes. The server message
      // is already user-facing for these — pass it through verbatim rather
      // than wrapping it in a generic "Failed to start preview" prefix that
      // buries the real issue (capacity, expired snapshot, etc.).
      const code = (err as { code?: string })?.code;
      if (code === PREVIEW_ERROR_CODES.CAPACITY_REACHED) {
        setMutationError(err.message);
        return;
      }
      if (code === PREVIEW_ERROR_CODES.SNAPSHOT_EXPIRED) {
        setMutationError(
          "This session's sandbox snapshot has expired. Send a new message to the agent to rebuild it, then try Start Preview again."
        );
        return;
      }
      if (code === PREVIEW_ERROR_CODES.SNAPSHOT_UNAVAILABLE) {
        setMutationError(
          "This session's last sandbox snapshot is unavailable. Send a new message to rebuild the sandbox, then try Start Preview again."
        );
        return;
      }
      if (code === PREVIEW_ERROR_CODES.NO_SANDBOX) {
        setMutationError(
          "Preview is unavailable on this server (Docker not configured). Contact an admin."
        );
        return;
      }
      if (code === PREVIEW_ERROR_CODES.SANDBOX_BUSY) {
        // The agent is using the sandbox right now (running a turn). The
        // backend already destroyed our half-built container; the user just
        // needs to wait a beat and click again.
        setMutationError(
          "The agent is currently using this session's sandbox. Wait for the current turn to finish, then try Start Preview again."
        );
        return;
      }
      if (code === PREVIEW_ERROR_CODES.WORKER_REQUEST_FAILED) {
        // Connection to the preview worker dropped mid-request — typically
        // a timeout or a worker restart. No response body means no real
        // error code; suggest retry rather than burying the cause under the
        // generic "Failed to start preview:" prefix.
        setMutationError(
          "Could not reach the preview worker (connection dropped). Try Start Preview again — if this keeps happening, the worker may be unhealthy."
        );
        return;
      }
      if (code === PREVIEW_ERROR_CODES.NO_CONFIG) {
        // Backend message already names the file the user needs to add and
        // points to the docs — pass it through verbatim.
        setMutationError(err.message);
        return;
      }
      // Provider-side launch failures (image pull, infra health, init
      // script, readiness probe). The backend builds a message that
      // names the failing image / service and includes the underlying
      // cause, so passing it through verbatim is more useful than
      // re-wrapping with a generic prefix.
      if (
        code === PREVIEW_ERROR_CODES.INFRA_IMAGE_UNAVAILABLE ||
        code === PREVIEW_ERROR_CODES.INFRA_START_FAILED ||
        code === PREVIEW_ERROR_CODES.INFRA_UNHEALTHY ||
        code === PREVIEW_ERROR_CODES.INIT_SCRIPT_FAILED ||
        code === PREVIEW_ERROR_CODES.SERVICE_NOT_READY
      ) {
        setMutationError(err.message);
        return;
      }
      setMutationError(`Failed to start preview: ${err.message}`);
    },
  });

  const resetPreviewState = useCallback(() => {
    setMutationError(null);
    setBootstrapComplete(false);
    queryClient.invalidateQueries({
      queryKey: ["preview-status", sessionId],
    });
  }, [queryClient, sessionId]);

  // Stop preview
  const stopMutation = useMutation({
    mutationFn: () => api.sessions.preview.stop(sessionId),
    onSuccess: resetPreviewState,
    onError: (err) => {
      setMutationError(`Failed to stop preview: ${err.message}`);
    },
  });

  // Restart preview
  const restartMutation = useMutation({
    mutationFn: () => api.sessions.preview.restart(sessionId),
    onSuccess: resetPreviewState,
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

  const previewOrigin = runtimePreviewOrigin || (instance
    ? previewOriginTemplate.replace("{id}", instance.id)
    : "");

  // Cache the parsed origin to avoid re-parsing on every postMessage event
  const parsedOrigin = useMemo(() => {
    if (!previewOrigin) return "";
    try {
      return new URL(previewOrigin).origin;
    } catch {
      return "";
    }
  }, [previewOrigin]);

  // Warn if the preview origin matches the app origin — this would break the
  // cross-origin isolation that the iframe sandbox relies on for security.
  useEffect(() => {
    if (parsedOrigin && parsedOrigin === window.location.origin) {
      console.warn(
        "[143 Preview] Preview origin matches app origin (%s). " +
          "This breaks iframe sandbox isolation. " +
          "Ensure PREVIEW_ORIGIN_TEMPLATE uses a different domain/port.",
        parsedOrigin
      );
    }
  }, [parsedOrigin]);

  // Reset bootstrapComplete when preview transitions away from ready
  // (e.g., backend restart) so the loading overlay shows for the new iframe.
  // Uses the React "store previous value in state" pattern to avoid both
  // setState-in-effect and ref-access-during-render lint errors.
  const [prevIsReady, setPrevIsReady] = useState(isReady);
  if (prevIsReady !== isReady) {
    setPrevIsReady(isReady);
    if (prevIsReady && !isReady) {
      setBootstrapComplete(false);
    }
  }

  // Track pending load listener for cleanup
  const pendingLoadCleanupRef = useRef<(() => void) | null>(null);

  // Post bootstrap token to iframe, retrying on iframe load if needed
  const sendBootstrapToken = useCallback(
    (token: string, origin: string) => {
      // Clean up any previous pending listener
      pendingLoadCleanupRef.current?.();
      pendingLoadCleanupRef.current = null;

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
        pendingLoadCleanupRef.current = null;
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
      pendingLoadCleanupRef.current = () => iframe.removeEventListener("load", onLoad);
    },
    []
  );

  // Clean up pending load listener on unmount
  useEffect(() => {
    return () => {
      pendingLoadCleanupRef.current?.();
    };
  }, []);

  // Handle postMessage exchange for bootstrap
  const handleMessage = useCallback(
    (event: MessageEvent) => {
      if (!parsedOrigin || event.origin !== parsedOrigin) return;

      if (event.data?.type === PREVIEW_BOOTSTRAP_READY_EVENT) {
        bootstrapMutateRef.current(undefined, {
          onSuccess: (data) => {
            setMutationError(null);
            sendBootstrapToken(data.token, parsedOrigin);
          },
        });
      }
    },
    [parsedOrigin, sendBootstrapToken]
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
  const showStartupCanvas = isActive && !isReady;
  const startupChecklist = useMemo(
    () =>
      showStartupProgress
        ? buildStartupChecklist(status, services, infrastructure)
        : [],
    [showStartupProgress, status, services, infrastructure],
  );
  const startupSubtitle = getStartupSubtitle(status, services, infrastructure);
  const showTopControls = status !== "starting";
  const visibleStartupPhases = STARTUP_PHASES.filter(
    (phase) => phase !== "Provisioning" || infrastructure.length > 0,
  );

  useEffect(() => {
    const rail = startupPhaseRailRef.current;
    if (!rail) {
      return;
    }

    const updateLayout = (width: number) => {
      setStartupPhaseRailLayout(
        getStartupPhaseRailLayout(width, visibleStartupPhases.length),
      );
    };

    updateLayout(rail.getBoundingClientRect().width);

    if (typeof ResizeObserver === "undefined") {
      return;
    }

    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) {
        return;
      }
      updateLayout(entry.contentRect.width);
    });
    observer.observe(rail);
    return () => observer.disconnect();
  }, [visibleStartupPhases.length]);

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
      {showTopControls && (
      <div className="flex items-center gap-2 flex-wrap">
        {/* Start / Stop / Restart */}
        <div className="flex items-center gap-1">
          {isActive ? (
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
          ) : null}
        </div>

        {/* Status badge */}
        {status && (
          <Badge variant="secondary" className={cn(statusColor(status))}>
            {status === "ready" && <CheckCircle2 className="size-3" />}
            {(status === "failed" || status === "unhealthy") && <AlertTriangle className="size-3" />}
            {STATUS_LABELS[status]}
          </Badge>
        )}

        {/* Console errors badge */}
        {isReady && (
          <ErrorBoundary fallback={null}>
            <ConsoleBadge sessionId={sessionId} />
          </ErrorBoundary>
        )}

        {/* TTL Warning */}
        {instance?.expires_at && isReady && (
          <TTLWarning
            expiresAt={instance.expires_at}
            sessionId={sessionId}
            recycleScheduledAt={instance.recycle_scheduled_at}
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
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      onClick={() => setSelectedWidth(preset.width)}
                      className={cn(
                        "rounded p-1 transition-colors",
                        selectedWidth === preset.width
                          ? "bg-background text-foreground shadow-sm"
                          : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      <preset.icon className="size-3.5" />
                    </Button>
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
      )}

      {/* Mutation error banner */}
      {mutationError && (
        <div className="flex items-center gap-2 rounded-lg border border-destructive/20 bg-destructive/5 p-2 text-sm text-destructive">
          <AlertTriangle className="size-4 shrink-0" />
          <span className="flex-1">{mutationError}</span>
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setMutationError(null)}
            className="rounded p-0.5 hover:bg-destructive/10"
          >
            <X className="size-3.5" />
          </Button>
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
      {services.length > 1 && isReady && (
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          {services.map((svc) => (
            <div key={svc.service_name} className="flex items-center gap-1">
              {serviceStatusIcon(svc.status)}
              <span>{svc.service_name}</span>
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
      {showStartupCanvas && (
        <div className="rounded-lg border bg-muted/20 overflow-hidden">
          <div className="relative bg-background">
            <div className="absolute right-3 top-3 z-10 flex items-center gap-1">
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label="Stop preview"
                      onClick={() => stopMutation.mutate()}
                      disabled={isMutating}
                      loading={stopMutation.isPending}
                    >
                      {!stopMutation.isPending && <Square className="size-3.5" />}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Stop preview</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label="Restart preview"
                      onClick={() => restartMutation.mutate()}
                      disabled={isMutating}
                      loading={restartMutation.isPending}
                    >
                      {!restartMutation.isPending && <RotateCw className="size-3.5" />}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Restart preview</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
            <div className="flex min-h-[360px] flex-col items-center justify-center px-6 py-14 text-center">
              <div className="mb-4 rounded-full border bg-card p-3 shadow-sm">
                <Loader2 className="size-5 animate-spin text-primary" />
              </div>
              <div className="space-y-1">
                <p className="text-lg font-semibold text-foreground">Preparing preview</p>
                <p className="text-sm text-muted-foreground">{startupSubtitle}</p>
              </div>
              <div
                ref={startupPhaseRailRef}
                data-testid="preview-startup-phase-rail"
                data-layout={startupPhaseRailLayout}
                className={cn(
                  "mt-8 grid w-full max-w-md gap-3",
                  startupPhaseRailLayout === "stacked"
                    ? "grid-cols-1"
                    : startupPhaseRailLayout === "compact"
                      ? "grid-cols-2"
                      : visibleStartupPhases.length === 3
                        ? "grid-cols-3"
                        : "grid-cols-2",
                )}
              >
                {visibleStartupPhases.map((phase) => {
                  const phaseState = startupPhaseState(phase, services, infrastructure);
                  return (
                    <div
                      key={phase}
                      className={cn(
                        "flex flex-col items-center gap-2 rounded-lg border bg-card/70 px-3.5 py-3 text-center text-xs leading-tight text-muted-foreground",
                        phaseState === "active" && "border-primary/30 text-foreground shadow-sm",
                        phaseState === "complete" && "text-emerald-600 dark:text-emerald-400",
                      )}
                    >
                      {phaseState === "complete" ? (
                        <CheckCircle2 className="size-3.5" />
                      ) : phaseState === "active" ? (
                        <Loader2 className="size-3.5 animate-spin text-primary" />
                      ) : (
                        <Circle className="size-3.5" />
                      )}
                      <span className={cn("text-balance", phaseState === "active" ? "font-medium" : undefined)}>
                        {phase}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
          <Collapsible>
            <div className="border-t bg-card/60 px-3 py-2">
              <CollapsibleTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="group h-7 px-2 text-xs text-muted-foreground"
                >
                  Details
                  <ChevronDown className="size-3.5 transition-transform duration-200 group-data-[state=open]:rotate-180" />
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-2 pb-1">
                <div className="space-y-1.5">
                  {startupChecklist.map((step) => (
                    <div
                      key={step.title}
                      className="flex items-start gap-2 rounded-md px-2 py-1.5 text-sm"
                    >
                      <div className="mt-0.5">{startupStepIcon(step.state)}</div>
                      <div className="min-w-0">
                        <div className="font-medium text-foreground">{step.title}</div>
                        <div className="text-xs text-muted-foreground">{step.detail}</div>
                      </div>
                    </div>
                  ))}
                </div>
              </CollapsibleContent>
            </div>
          </Collapsible>
        </div>
      )}

      {/* Failure diagnostics */}
      {status === "failed" && instance && (
        <div className="rounded-lg border border-destructive/20 bg-destructive/5 p-3 space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium text-destructive">
            <AlertTriangle className="size-4" />
            Preview failed to start
          </div>
          {instance.error && (
            <p className="text-xs text-muted-foreground">{instance.error}</p>
          )}
          {/* failure_pattern and build_log will be surfaced when backend support is added */}
          <Button
            size="sm"
            variant="outline"
            onClick={() => restartMutation.mutate()}
            disabled={isMutating}
          >
            <RefreshCw className="size-3.5" />
            Try Again
          </Button>
          {hasStartupRows && (
            <Collapsible>
              <CollapsibleTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="group h-7 px-2 text-xs text-muted-foreground"
                >
                  Details
                  <ChevronDown className="size-3.5 transition-transform duration-200 group-data-[state=open]:rotate-180" />
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-2 pb-1">
                <div className="space-y-1.5">
                  {startupChecklist.map((step) => (
                    <div
                      key={step.title}
                      className="flex items-start gap-2 rounded-md px-2 py-1.5 text-sm"
                    >
                      <div className="mt-0.5">{startupStepIcon(step.state)}</div>
                      <div className="min-w-0">
                        <div className="font-medium text-foreground">{step.title}</div>
                        <div className="text-xs text-muted-foreground">{step.detail}</div>
                      </div>
                    </div>
                  ))}
                </div>
              </CollapsibleContent>
            </Collapsible>
          )}
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
              {/* Sandbox threat model: allow-same-origin is required so the
                  iframe can set cookies and use localStorage on its own
                  subdomain ({id}.preview.*). The parent app is on a different
                  origin (app.*), so the cross-origin boundary prevents the
                  framed content from accessing the parent's DOM or storage.
                  The CSP frame-ancestors header restricts which origins can
                  embed the preview, and the bootstrap token exchange ensures
                  only authenticated users can access preview content. */}
              <iframe
                ref={iframeRef}
                src={iframeSrc}
                className="absolute inset-0 w-full h-full bg-white"
                sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-downloads allow-popups"
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
                <ErrorBoundary fallback={null}>
                  <DesignModeOverlay
                    sessionId={sessionId}
                  />
                </ErrorBoundary>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Idle state */}
      {(!status || status === "stopped" || status === "expired") && !statusLoading && (
        <div className="rounded-lg border border-dashed p-8 flex flex-col items-center justify-center gap-3 text-center">
          <div className="rounded-full bg-muted p-3">
            <Monitor className="size-5 text-muted-foreground" />
          </div>
          <div className="space-y-1">
            <p className="text-sm font-medium">No preview running</p>
            <p className="text-xs text-muted-foreground">
              Start a preview to see live changes from the agent. Note that it can take a few minutes for the environment to finish booting.
            </p>
          </div>
          <Button
            size="sm"
            onClick={() => startMutation.mutate()}
            disabled={isMutating}
            loading={startMutation.isPending}
          >
            {!startMutation.isPending && <Play className="size-3.5" />}
            Start Preview
          </Button>
          <p className="text-xs text-muted-foreground">
          </p>
        </div>
      )}

    </div>
  );
}
