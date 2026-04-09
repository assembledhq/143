import { useState } from "react";
import { ChevronDown, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import type { FileLine } from "@/lib/types";

interface ContextExpanderProps {
  /** Number of hidden lines between hunks */
  hiddenLineCount: number;
  /** Session ID for fetching context */
  sessionId?: string;
  /** File path for fetching context */
  filePath?: string;
  /** The line number where expansion starts (end of previous hunk) */
  startLine?: number;
  /** Callback when context lines are fetched */
  onExpand?: (lines: FileLine[]) => void;
}

/**
 * Clickable expander shown between diff hunks to indicate hidden context lines.
 * Fetches additional context from the file content API when clicked.
 */
export function ContextExpander({
  hiddenLineCount,
  sessionId,
  filePath,
  startLine,
  onExpand,
}: ContextExpanderProps) {
  const [loading, setLoading] = useState(false);
  const [expanded, setExpanded] = useState(false);

  if (hiddenLineCount <= 0 || expanded) return null;

  const canExpand = sessionId && filePath && startLine != null && onExpand;

  async function handleClick() {
    if (!canExpand) return;
    setLoading(true);
    try {
      const midLine = startLine! + Math.ceil(hiddenLineCount / 2);
      const halfRange = Math.ceil(hiddenLineCount / 2) + 1;
      const resp = await api.sessions.getFileContext(
        sessionId!,
        filePath!,
        midLine,
        halfRange,
        halfRange
      );
      if (resp?.data?.lines) {
        onExpand!(resp.data.lines);
        setExpanded(true);
      }
    } catch {
      // If context fetch fails (e.g., sandbox not running), silently ignore
    } finally {
      setLoading(false);
    }
  }

  return (
    <button
      onClick={handleClick}
      disabled={loading || !canExpand}
      className="flex items-center justify-center w-full py-1 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-muted/30 transition-colors font-mono select-none gap-1 disabled:opacity-40"
      title={canExpand ? `Show ${hiddenLineCount} hidden lines` : "Context expansion unavailable (sandbox not running)"}
    >
      {loading ? (
        <Loader2 className="h-3 w-3 animate-spin" />
      ) : (
        <ChevronDown className="h-3 w-3" />
      )}
      <span>Show {hiddenLineCount} hidden lines</span>
    </button>
  );
}
