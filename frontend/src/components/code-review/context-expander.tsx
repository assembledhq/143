import { useState } from "react";
import { ChevronDown, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import type { FileLine } from "@/lib/types";
import { Button } from "@/components/ui/button";

export interface ContextExpandResult {
  startLine: number;
  endLine: number;
  hasMoreAbove: boolean;
  hasMoreBelow: boolean;
  totalLines: number;
}

interface ContextExpanderProps {
  kind: "top" | "middle" | "bottom";
  /** Number of hidden lines between hunks */
  hiddenLineCount: number;
  /** Session ID for fetching context */
  sessionId?: string;
  /** File path for fetching context */
  filePath?: string;
  /** Inclusive hidden range bounds. */
  hiddenStart: number;
  hiddenEnd: number;
  /** Currently visible range inside the hidden range. */
  visibleStart?: number;
  visibleEnd?: number;
  /** Callback when context lines are fetched */
  onExpand?: (direction: "above" | "below" | "all", lines: FileLine[], meta: ContextExpandResult) => void;
}

/**
 * Clickable expander shown between diff hunks to indicate hidden context lines.
 * Fetches additional context from the file content API when clicked.
 */
export function ContextExpander({
  kind,
  hiddenLineCount,
  sessionId,
  filePath,
  hiddenStart,
  hiddenEnd,
  visibleStart,
  visibleEnd,
  onExpand,
}: ContextExpanderProps) {
  const [loading, setLoading] = useState(false);

  if (hiddenLineCount <= 0) return null;

  const canExpand = sessionId && filePath && onExpand;
  const canExpandAbove = canExpand && (visibleStart == null || visibleStart > hiddenStart);
  const canExpandBelow = canExpand && (visibleEnd == null || visibleEnd < hiddenEnd);
  const canExpandAll = canExpand && (visibleStart !== hiddenStart || visibleEnd !== hiddenEnd);

  async function fetchRange(direction: "above" | "below" | "all") {
    if (!canExpand) return;
    if (direction === "above" && !canExpandAbove) return;
    if (direction === "below" && !canExpandBelow) return;
    if (direction === "all" && !canExpandAll) return;
    setLoading(true);
    try {
      let line = hiddenStart;
      const above = 0;
      let below = 0;

      if (direction === "above") {
        const fetchEnd = visibleStart != null ? visibleStart - 1 : hiddenEnd;
        const fetchStart = Math.max(hiddenStart, fetchEnd - 19);
        line = fetchStart;
        below = fetchEnd - fetchStart;
      } else if (direction === "below") {
        const fetchStart = visibleEnd != null ? visibleEnd + 1 : hiddenStart;
        const fetchEnd = Math.min(hiddenEnd, fetchStart + 19);
        line = fetchStart;
        below = fetchEnd - fetchStart;
      } else {
        line = hiddenStart;
        below = hiddenEnd - hiddenStart;
      }

      const resp = await api.sessions.getFileContext(
        sessionId!,
        filePath!,
        line,
        above,
        below
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
    } catch {
      // If context fetch fails (e.g., sandbox not running), silently ignore
    } finally {
      setLoading(false);
    }
  }

  return (
    <div
      className="flex items-center justify-center gap-2 px-3 py-2 text-xs text-muted-foreground/80 border-y border-border/40 bg-muted/15"
      title={canExpand ? `Show ${hiddenLineCount} hidden lines` : "Context expansion unavailable (sandbox not running)"}
    >
      {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <ChevronDown className="h-3 w-3" />}
      <Button
        type="button"
        variant="ghost"
        size="sm"
        disabled={loading || !canExpandAbove}
        className="h-7 px-2 font-mono text-xs"
        onClick={() => fetchRange("above")}
        aria-label="Show 20 above"
      >
        Show 20 above
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        disabled={loading || !canExpandBelow}
        className="h-7 px-2 font-mono text-xs"
        onClick={() => fetchRange("below")}
        aria-label="Show 20 below"
      >
        Show 20 below
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        disabled={loading || !canExpandAll}
        className="h-7 px-2 font-mono text-xs"
        onClick={() => fetchRange("all")}
        aria-label="Show all hidden lines"
      >
        Show all
      </Button>
      <span aria-label={`Show ${hiddenLineCount} hidden lines`}>
        {kind === "top" ? "Before change" : kind === "bottom" ? "After change" : `Show ${hiddenLineCount} hidden lines`}
      </span>
    </div>
  );
}
