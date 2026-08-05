"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import type { InferredHistoricalActivity, TimelineActivityPhase } from "@/lib/activity-timeline";

type CapsuleActivity = TimelineActivityPhase | InferredHistoricalActivity;

function formatElapsed(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

function useElapsed(activity: CapsuleActivity): string | undefined {
  const authoritative = !activity.inferredHistorical;
  const running = authoritative && activity.status === "running";
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [running]);
  if (!authoritative) return undefined;
  const started = Date.parse(activity.started_at);
  const completed = activity.completed_at ? Date.parse(activity.completed_at) : now;
  if (!Number.isFinite(started) || !Number.isFinite(completed) || completed < started) return undefined;
  return formatElapsed(completed - started);
}

function capsuleStateLabel(activity: CapsuleActivity): string {
  if (activity.inferredHistorical) return "Activity";
  switch (activity.status) {
    case "running": return "Working for";
    case "failed": return "Failed after";
    case "cancelled": return activity.boundary_reason === "stopped" ? "Stopped after" : "Cancelled after";
    case "interrupted": return "Interrupted after";
    default: return "Worked for";
  }
}

export function ActivityCapsule({ activity, expanded, onExpandedChange, onInspect, children }: {
  activity: CapsuleActivity;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  onInspect?: () => void;
  children: ReactNode;
}) {
  const elapsed = useElapsed(activity);
  const toolCount = activity.inferredHistorical
    ? activity.toolCallCount
    : activity.status === "running"
      ? Math.max(activity.tool_call_count, activity.provisionalToolCallCount ?? 0)
      : activity.tool_call_count;
  const summary = useMemo(() => {
    const parts = [capsuleStateLabel(activity)];
    if (elapsed) parts[0] += ` ${elapsed}`;
    if (toolCount > 0) parts.push(`${toolCount} tool ${toolCount === 1 ? "call" : "calls"}`);
    if (!activity.inferredHistorical && activity.status === "running" && activity.latestActivityLabel) parts.push(activity.latestActivityLabel);
    return parts.join(" · ");
  }, [activity, elapsed, toolCount]);
  const detectSelection = (container: HTMLElement) => {
    if (!expanded) return;
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed || !selection.toString().trim()) return;
    if ((selection.anchorNode && container.contains(selection.anchorNode)) || (selection.focusNode && container.contains(selection.focusNode))) {
      onInspect?.();
    }
  };

  return (
    <Collapsible
      open={expanded}
      onOpenChange={onExpandedChange}
      onMouseUp={(event) => detectSelection(event.currentTarget)}
      onKeyUp={(event) => detectSelection(event.currentTarget)}
      data-activity-phase-id={activity.id}
      data-activity-capsule="true"
      data-session-entry-id={activity.inferredHistorical ? undefined : activity.anchor_id}
      className="rounded-lg border border-border bg-card"
    >
      <CollapsibleTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          className="h-auto w-full justify-start gap-2 rounded-lg px-3 py-2 text-left text-sm font-medium"
          aria-label={summary}
        >
          <ChevronRight className={`h-4 w-4 shrink-0 transition-transform motion-reduce:transition-none ${expanded ? "rotate-90" : ""}`} />
          <span className="min-w-0 flex-1 whitespace-normal tabular-nums">{summary}</span>
        </Button>
      </CollapsibleTrigger>
      {expanded ? (
        <CollapsibleContent
          data-activity-phase-body="true"
          data-activity-phase-id={activity.id}
          className="space-y-2 border-t border-border px-2 py-2"
        >
          {children}
        </CollapsibleContent>
      ) : null}
    </Collapsible>
  );
}
