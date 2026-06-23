import { useState } from "react";
import { ChevronDown, ChevronUp, Loader2, type LucideIcon } from "lucide-react";
import { ApiError, api } from "@/lib/api";
import type { FileLine } from "@/lib/types";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

export interface ContextExpandResult {
  startLine: number;
  endLine: number;
  hasMoreAbove: boolean;
  hasMoreBelow: boolean;
  totalLines: number;
}

interface ContextExpanderProps {
  kind: "top" | "middle" | "bottom";
  viewMode?: "unified" | "split";
  /** Number of hidden lines between hunks */
  hiddenLineCount: number;
  /** Session ID for fetching context */
  sessionId?: string;
  /** File path for fetching context */
  filePath?: string;
  /** Inclusive hidden range bounds. */
  hiddenStart: number;
  hiddenEnd?: number;
  /** Currently visible range inside the hidden range. */
  visibleStart?: number;
  visibleEnd?: number;
  /** Edge-specific visible boundaries for gaps revealed from both ends. */
  aboveVisibleStart?: number;
  belowVisibleEnd?: number;
  /** Callback when context lines are fetched */
  onExpand?: (direction: "above" | "below" | "all", lines: FileLine[], meta: ContextExpandResult) => void;
  /** When true, disables expansion controls and shows an explicit unavailable message. */
  contextUnavailable?: boolean;
  /** Called when a fetch fails because the session has no live sandbox. */
  onContextUnavailable?: () => void;
}

/**
 * Clickable expander shown between diff hunks to indicate hidden context lines.
 * Fetches additional context from the file content API when clicked.
 */
export function ContextExpander({
  kind,
  viewMode = "unified",
  hiddenLineCount,
  sessionId,
  filePath,
  hiddenStart,
  hiddenEnd,
  visibleStart,
  visibleEnd,
  aboveVisibleStart,
  belowVisibleEnd,
  onExpand,
  contextUnavailable = false,
  onContextUnavailable,
}: ContextExpanderProps) {
  const [loadingDirection, setLoadingDirection] = useState<"above" | "below" | null>(null);

  if (hiddenLineCount <= 0) return null;

  const loading = loadingDirection !== null;
  const canExpand = !contextUnavailable && sessionId && filePath && onExpand;
  const hasKnownHiddenEnd = hiddenEnd != null;
  const aboveBoundaryStart = aboveVisibleStart ?? visibleStart;
  const belowBoundaryEnd = belowVisibleEnd ?? visibleEnd;
  const middleRemainingStart = belowBoundaryEnd != null ? belowBoundaryEnd + 1 : hiddenStart;
  const middleRemainingEnd = aboveBoundaryStart != null ? aboveBoundaryStart - 1 : hiddenEnd;
  const hasMiddleRemaining =
    hasKnownHiddenEnd &&
    middleRemainingEnd != null &&
    middleRemainingStart <= middleRemainingEnd;
  const canExpandAbove = kind === "middle"
    ? canExpand && hasMiddleRemaining
    : canExpand && hasKnownHiddenEnd && (aboveBoundaryStart == null || aboveBoundaryStart > hiddenStart);
  const canExpandBelow = kind === "middle"
    ? canExpand && hasMiddleRemaining
    : canExpand && (!hasKnownHiddenEnd || belowBoundaryEnd == null || belowBoundaryEnd < hiddenEnd);
  const controlDirections: Array<"above" | "below"> =
    kind === "top" ? ["above"] : kind === "bottom" ? ["below"] : ["above", "below"];

  function getFetchWindow(direction: "above" | "below"): { line: number; below: number } | null {
    if (direction === "above") {
      if (hiddenEnd == null) return null;

      if (kind === "middle") {
        const fetchStart = middleRemainingStart;
        const fetchEnd = Math.min(middleRemainingEnd ?? hiddenEnd, fetchStart + 19);
        return { line: fetchStart, below: fetchEnd - fetchStart };
      }

      const fetchEnd = aboveBoundaryStart != null ? aboveBoundaryStart - 1 : hiddenEnd;
      const fetchStart = Math.max(hiddenStart, fetchEnd - 19);
      return { line: fetchStart, below: fetchEnd - fetchStart };
    }

    if (kind === "middle") {
      if (middleRemainingEnd == null) return null;
      const fetchEnd = middleRemainingEnd;
      const fetchStart = Math.max(middleRemainingStart, fetchEnd - 19);
      return { line: fetchStart, below: fetchEnd - fetchStart };
    }

    const fetchStart = belowBoundaryEnd != null ? belowBoundaryEnd + 1 : hiddenStart;
    const fetchEnd = hiddenEnd == null
      ? fetchStart + 19
      : Math.min(hiddenEnd, fetchStart + 19);
    return { line: fetchStart, below: fetchEnd - fetchStart };
  }

  async function fetchRange(direction: "above" | "below") {
    if (!canExpand) return;
    if (direction === "above" && !canExpandAbove) return;
    if (direction === "below" && !canExpandBelow) return;
    setLoadingDirection(direction);
    try {
      const above = 0;
      const fetchWindow = getFetchWindow(direction);
      if (!fetchWindow) return;

      const resp = await api.sessions.getFileContext(
        sessionId!,
        filePath!,
        fetchWindow.line,
        above,
        fetchWindow.below
      );
      if (resp?.data?.lines) {
        onExpand!(direction, resp.data.lines, {
          startLine: resp.data.start_line,
          endLine: resp.data.end_line,
          hasMoreAbove: resp.data.has_more_above,
          hasMoreBelow: resp.data.has_more_below,
          totalLines: resp.data.total_lines,
        });
      }
    } catch (err) {
      // The session container is gone (completed sessions tear down their
      // sandbox). Lift this signal so all expanders flip to the disabled
      // state instead of silently swallowing repeated clicks.
      if (err instanceof ApiError && err.code === "NO_SANDBOX") {
        onContextUnavailable?.();
      }
    } finally {
      setLoadingDirection(null);
    }
  }

  const trailingText = contextUnavailable
    ? "Additional file context unavailable for this session"
    : kind === "top"
    ? ""
    : kind === "bottom"
    ? ""
    : `${hiddenLineCount} hidden lines`;

  const titleText = contextUnavailable
    ? "Additional file context unavailable for this session"
    : canExpand
    ? hasKnownHiddenEnd
      ? `Reveal ${hiddenLineCount} hidden context lines`
      : "Reveal context below"
    : "Context expansion unavailable (sandbox not running)";
  const gutterWidthClass = viewMode === "split" ? "w-[58px]" : "w-[84px]";
  const prefixSpacerClass = viewMode === "split" ? "w-0" : "w-[16px]";

  function renderControl({
    direction,
    label,
    tooltip,
    disabled,
    Icon,
  }: {
    direction: "above" | "below";
    label: string;
    tooltip: string;
    disabled: boolean;
    Icon: LucideIcon;
  }) {
    const isLoading = loadingDirection === direction;

    return (
      <Tooltip key={direction}>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            disabled={loading || disabled}
            className="h-4 w-8 shrink-0 rounded-sm p-0 text-primary hover:bg-primary/10 disabled:text-muted-foreground"
            onClick={() => fetchRange(direction)}
            aria-label={label}
            title={tooltip}
          >
            {isLoading ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <Icon className="h-3.5 w-3.5" aria-hidden="true" />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top">{tooltip}</TooltipContent>
      </Tooltip>
    );
  }

  function getControlIcon(direction: "above" | "below"): LucideIcon {
    if (kind === "middle") {
      return direction === "above" ? ChevronDown : ChevronUp;
    }
    return direction === "above" ? ChevronUp : ChevronDown;
  }

  return (
    <div
      className="flex min-w-fit items-stretch border-y border-sky-200/70 bg-sky-50/80 text-xs text-muted-foreground/80 dark:border-sky-900/50 dark:bg-sky-950/20"
      title={titleText}
    >
      <TooltipProvider>
        <div
          data-testid="context-expander-gutter-controls"
          className={`${gutterWidthClass} flex shrink-0 items-center justify-center bg-sky-100/80 dark:bg-sky-950/40`}
        >
          <div className="flex flex-col items-center justify-center gap-0.5">
            {controlDirections.map((controlDirection) =>
              renderControl({
                direction: controlDirection,
                label:
                  controlDirection === "above"
                    ? "Reveal context above"
                    : "Reveal context below",
                tooltip:
                  controlDirection === "above"
                    ? "Reveal context above"
                    : "Reveal context below",
                disabled:
                  controlDirection === "above"
                    ? !canExpandAbove
                    : !canExpandBelow,
                Icon: getControlIcon(controlDirection),
              })
            )}
          </div>
        </div>
      </TooltipProvider>
      <div
        data-testid="context-expander-prefix-spacer"
        className={`${prefixSpacerClass} shrink-0 bg-sky-50/80 dark:bg-sky-950/20`}
      />
      <div className="flex min-h-7 flex-1 items-center px-2">
        <span
          data-testid="context-expander-label"
          aria-label={trailingText || undefined}
          className="font-mono"
        >
          {trailingText}
        </span>
      </div>
    </div>
  );
}
