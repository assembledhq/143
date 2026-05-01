"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  ArrowUpRight,
  CheckCircle2,
  ChevronRight,
  Loader2,
  Minus,
  MessageCircleWarning,
  RefreshCw,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DiffStatsBadge } from "@/components/code-review/diff-stats-badge";
import { cn, formatTimeAgo } from "@/lib/utils";
import type { AutomationRun, AutomationRunStatus, PRCreationState } from "@/lib/types";

// "Quiet" runs — no work was needed (or the schedule fired while paused).
// Treated as low-signal: rendered as thin dim rows individually and
// collapsed into a group when there are two or more in a row.
export const QUIET_RUN_STATUSES: ReadonlyArray<AutomationRunStatus> = [
  "completed_noop",
  "skipped",
];

export function isQuietRun(run: AutomationRun): boolean {
  return QUIET_RUN_STATUSES.includes(run.status);
}

// FullCardKind enumerates the variants that render as the rich row card
// (header + body + meta + action). The dispatcher in RunCard handles the
// other two row shapes — quiet and pending — separately, so the helpers
// below never need to consider them.
//
// "needs_input" is a pseudo-status the dispatcher derives when the run
// itself reports failed/completed but the linked session is actually
// waiting on the user. The hook in internal/services/automations/hooks.go
// maps needs_human_guidance → run failed, which is correct for accounting
// but wrong for UX — those runs need an answer, not a retry.
type FullCardKind =
  | "running"
  | "needs_input"
  | "completed_with_pr"
  | "completed_no_pr"
  | "failed";

type RowKind = FullCardKind | "pending" | "quiet";

function classifyRun(run: AutomationRun): RowKind {
  if (run.status === "running") return "running";
  if (run.status === "pending") return "pending";
  if (run.status === "failed") {
    if (run.session?.status === "needs_human_guidance") return "needs_input";
    return "failed";
  }
  if (run.status === "completed") {
    if (run.session?.status === "needs_human_guidance") return "needs_input";
    if (run.session?.pr) return "completed_with_pr";
    return "completed_no_pr";
  }
  return "quiet";
}

interface RunCardProps {
  run: AutomationRun;
}

export function RunCard({ run }: RunCardProps) {
  // useRouter is hoisted here once and threaded down so the three nested
  // components don't each re-subscribe to the router context.
  const router = useRouter();
  const navigateTo = (path: string) => router.push(path);

  const kind = classifyRun(run);
  if (kind === "quiet") return <QuietRunRow run={run} navigateTo={navigateTo} />;
  if (kind === "pending") return <PendingRow run={run} />;
  return <FullCard run={run} kind={kind} navigateTo={navigateTo} />;
}

type Navigator = (path: string) => void;

// ---------------------------------------------------------------------------
// Full-card variants (running / completed / failed / needs_input)
// ---------------------------------------------------------------------------

interface FullCardProps {
  run: AutomationRun;
  kind: FullCardKind;
  navigateTo: Navigator;
}

function FullCard({ run, kind, navigateTo }: FullCardProps) {
  const sessionId = run.session?.id;
  const navigate = sessionId ? () => navigateTo(`/sessions/${sessionId}`) : undefined;

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (!navigate) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      navigate();
    }
  };

  // Use the user-facing label (e.g. "Completed", "Failed", "Needs your
  // input") rather than the internal kind string — screen readers
  // shouldn't be reading "completed with pr" out loud.
  const ariaLabel = navigate
    ? `Open session for ${headlineFor(kind).label.toLowerCase()} run from ${formatTimeAgo(run.triggered_at)}`
    : undefined;

  return (
    <div
      role={navigate ? "button" : undefined}
      tabIndex={navigate ? 0 : undefined}
      aria-label={ariaLabel}
      onClick={navigate}
      onKeyDown={handleKeyDown}
      className={cn(
        "group relative rounded-lg border p-4 transition-colors",
        navigate && "cursor-pointer hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        cardSurfaceClass(kind),
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          <Header run={run} kind={kind} />
          <Body run={run} kind={kind} />
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <RowMeta run={run} kind={kind} />
          <PrimaryAction run={run} kind={kind} navigateTo={navigateTo} />
          {navigate && (
            <ChevronRight
              aria-hidden
              className="hidden h-4 w-4 text-muted-foreground/60 transition-opacity opacity-0 group-hover:opacity-100 sm:block"
            />
          )}
        </div>
      </div>
    </div>
  );
}

function cardSurfaceClass(kind: FullCardKind): string {
  switch (kind) {
    case "failed":
      return "border-red-200 bg-red-50/40 dark:border-red-900/30 dark:bg-red-950/20";
    case "needs_input":
      return "border-amber-300/70 bg-amber-50/50 dark:border-amber-900/40 dark:bg-amber-950/20";
    case "running":
      return "border-blue-200/70 bg-blue-50/30 dark:border-blue-900/30 dark:bg-blue-950/20";
    default:
      return "border-border bg-background";
  }
}

// ---------------------------------------------------------------------------
// Header (icon + label + timestamp + optional category badge)
// ---------------------------------------------------------------------------

function Header({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  const headline = headlineFor(kind);
  const Icon = headline.icon;
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <Icon
        aria-hidden
        className={cn("h-4 w-4 shrink-0", headline.iconClass, kind === "running" && "animate-spin")}
      />
      <span className={cn("text-sm font-medium", headline.labelClass)}>{headline.label}</span>
      <span
        className="text-xs text-muted-foreground"
        title={new Date(run.triggered_at).toLocaleString()}
      >
        {formatTimeAgo(run.triggered_at)}
      </span>
      {kind === "running" && <LiveDuration startedAt={run.triggered_at} />}
      {kind !== "running" && run.completed_at && (
        <span className="text-xs text-muted-foreground">
          · {formatDuration(run.triggered_at, run.completed_at)}
        </span>
      )}
      {run.session?.failure_category && (kind === "failed" || kind === "needs_input") && (
        <Badge
          variant={kind === "failed" ? "destructive" : "outline"}
          className={cn(
            "text-xs uppercase tracking-wide",
            kind === "needs_input" && "border-amber-500/40 text-amber-700 dark:text-amber-400",
          )}
        >
          {run.session.failure_category.replaceAll("_", " ")}
        </Badge>
      )}
      {run.triggered_by === "manual" && (
        <Badge variant="outline" className="text-xs">
          Manual
        </Badge>
      )}
    </div>
  );
}

function headlineFor(kind: FullCardKind): {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  iconClass: string;
  labelClass: string;
} {
  switch (kind) {
    case "running":
      return {
        icon: RefreshCw,
        label: "Running",
        iconClass: "text-blue-500",
        labelClass: "text-blue-700 dark:text-blue-300",
      };
    case "needs_input":
      return {
        icon: MessageCircleWarning,
        label: "Needs your input",
        iconClass: "text-amber-600 dark:text-amber-500",
        labelClass: "text-amber-800 dark:text-amber-300",
      };
    case "completed_with_pr":
    case "completed_no_pr":
      return {
        icon: CheckCircle2,
        label: "Completed",
        iconClass: "text-emerald-500",
        labelClass: "text-emerald-700 dark:text-emerald-300",
      };
    case "failed":
      return {
        icon: AlertTriangle,
        label: "Failed",
        iconClass: "text-red-500",
        labelClass: "text-red-700 dark:text-red-300",
      };
  }
}

// ---------------------------------------------------------------------------
// Body (title + summary + suggested next step)
// ---------------------------------------------------------------------------

function Body({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  // The row's primary "what happened" line. Prefer session title when we
  // have it, fall back to the run's result_summary, and finally to the
  // session's failure_explanation for failed runs without a title.
  const headline = primaryLine(run, kind);
  const subline = secondaryLine(run, kind);
  if (!headline && !subline) return null;
  return (
    <div className="space-y-0.5">
      {headline && (
        <p
          className="line-clamp-2 text-sm text-foreground"
          title={headline}
        >
          {headline}
        </p>
      )}
      {subline && (
        <p
          className="line-clamp-2 text-xs text-muted-foreground"
          title={subline}
        >
          {subline}
        </p>
      )}
    </div>
  );
}

function primaryLine(run: AutomationRun, kind: FullCardKind): string | null {
  if (kind === "needs_input") {
    return run.session?.title || run.result_summary || "Agent paused, waiting for your guidance.";
  }
  if (kind === "failed") {
    return run.session?.failure_explanation || run.result_summary || run.session?.title || "Run failed.";
  }
  if (kind === "running") {
    return run.session?.title || "Working on it…";
  }
  // completed_with_pr | completed_no_pr
  return run.session?.title || run.result_summary || null;
}

function secondaryLine(run: AutomationRun, kind: FullCardKind): string | null {
  // Surface the agent's first suggested next step on both failure and
  // needs-input rows — both states are blocked on the user, and the
  // session's failure_next_steps is the same field powering the session
  // page's guidance UI, so users see a consistent hint either place.
  if (kind === "failed" || kind === "needs_input") {
    const next = run.session?.failure_next_steps?.[0];
    return next ? `Suggested next step: ${next}` : null;
  }
  if (kind === "completed_with_pr" || kind === "completed_no_pr") {
    if (run.session?.title && run.result_summary && run.session.title !== run.result_summary) {
      return run.result_summary;
    }
  }
  return null;
}

// ---------------------------------------------------------------------------
// Row meta (diff stats + PR pill) — sits next to the action button
// ---------------------------------------------------------------------------

function RowMeta({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  const diff = run.session?.diff_stats;
  const pr = run.session?.pr;
  const prState = run.session?.pr_creation_state;
  const hasDiff = diff && (diff.added > 0 || diff.removed > 0);
  const showInRow = kind === "completed_with_pr" || kind === "completed_no_pr";
  if (!showInRow) return null;

  return (
    <div className="hidden items-center gap-2 sm:flex">
      {hasDiff && <DiffStatsBadge added={diff.added} removed={diff.removed} />}
      {pr ? (
        <Badge
          variant="outline"
          className={cn(
            "text-xs",
            pr.status === "merged" && "border-purple-500/40 text-purple-700 dark:text-purple-300",
            pr.status === "open" && "border-emerald-500/40 text-emerald-700 dark:text-emerald-300",
          )}
        >
          PR #{pr.number} · {pr.status}
        </Badge>
      ) : (
        // No PR row joined — distinguish "in flight" (the worker is
        // pushing a branch / opening the PR) from "PR creation failed"
        // from the everyday "session done, user hasn't asked for a PR
        // yet" case. All three conditions read off pr_creation_state,
        // which is now a typed PRCreationState union so the branches
        // are exhaustive.
        <PRCreationPill prState={prState} hasDiff={!!hasDiff} />
      )}
    </div>
  );
}

function PRCreationPill({
  prState,
  hasDiff,
}: {
  prState: PRCreationState | undefined;
  hasDiff: boolean;
}) {
  if (prState === "queued" || prState === "pushing") {
    return (
      <Badge
        variant="outline"
        className="gap-1 text-xs text-muted-foreground"
        title="The worker is pushing the branch and opening a pull request."
      >
        <Loader2 aria-hidden className="h-3 w-3 animate-spin" />
        Creating PR…
      </Badge>
    );
  }
  if (prState === "failed") {
    return (
      <Badge
        variant="outline"
        className="border-red-500/40 text-xs text-red-700 dark:text-red-300"
        title="PR creation failed. Open the session to retry."
      >
        PR creation failed
      </Badge>
    );
  }
  if (hasDiff) {
    return (
      <Badge variant="outline" className="text-xs text-muted-foreground">
        No PR yet
      </Badge>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// Primary action button — varies by row state
// ---------------------------------------------------------------------------

function PrimaryAction({
  run,
  kind,
  navigateTo,
}: {
  run: AutomationRun;
  kind: FullCardKind;
  navigateTo: Navigator;
}) {
  const sessionId = run.session?.id;
  // Stop card-level navigation from firing when the inner control handles
  // the click — otherwise the user lands on the session page after we
  // already opened the PR in a new tab.
  const stop = (e: React.MouseEvent | React.KeyboardEvent) => e.stopPropagation();

  if (kind === "completed_with_pr" && run.session?.pr) {
    // Real anchor (rather than window.open) so middle-click and "open in
    // new tab" work as users expect, and popup blockers don't intercept
    // the click. Button asChild gives us the same visual treatment.
    return (
      <Button asChild size="sm" variant="outline" className="shrink-0">
        <a
          href={run.session.pr.url}
          target="_blank"
          rel="noopener noreferrer"
          onClick={stop}
          onKeyDown={stop}
        >
          Review PR
          <ArrowUpRight className="ml-1.5 h-3.5 w-3.5" />
        </a>
      </Button>
    );
  }

  if (!sessionId) return null;

  let label = "Open session";
  if (kind === "needs_input") label = "Reply to agent";
  else if (kind === "running") label = "View live session";
  else if (kind === "failed" && run.session?.failure_retry_advised) label = "Retry on session";

  return (
    <Button
      size="sm"
      variant={kind === "needs_input" ? "default" : "outline"}
      className="shrink-0"
      onClick={(e) => {
        stop(e);
        navigateTo(`/sessions/${sessionId}`);
      }}
      onKeyDown={stop}
    >
      {label}
      <ChevronRight className="ml-1 h-3.5 w-3.5" />
    </Button>
  );
}

// ---------------------------------------------------------------------------
// Live duration tick — only mounts while the run is running
// ---------------------------------------------------------------------------

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);
  const elapsedMs = Math.max(0, now - new Date(startedAt).getTime());
  return (
    <span className="text-xs tabular-nums text-muted-foreground">
      · {formatElapsed(elapsedMs)}
    </span>
  );
}

function formatElapsed(ms: number): string {
  const totalSec = Math.floor(ms / 1000);
  if (totalSec < 60) return `${totalSec}s`;
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  if (m < 60) return `${m}m ${s.toString().padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  const remM = m % 60;
  return `${h}h ${remM.toString().padStart(2, "0")}m`;
}

function formatDuration(start: string, end: string): string {
  const ms = new Date(end).getTime() - new Date(start).getTime();
  if (!Number.isFinite(ms) || ms <= 0) return "<1s";
  return formatElapsed(ms);
}

// ---------------------------------------------------------------------------
// Pending — thin row, not clickable (no session yet)
// ---------------------------------------------------------------------------

function PendingRow({ run }: { run: AutomationRun }) {
  return (
    <div className="flex items-center gap-2 rounded-md border border-dashed border-border/60 px-3 py-2 text-xs text-muted-foreground">
      <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
      <span>Pending · queued {formatTimeAgo(run.triggered_at)}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Quiet row — used both standalone and inside a quiet group
// ---------------------------------------------------------------------------

export function QuietRunRow({
  run,
  navigateTo,
}: {
  run: AutomationRun;
  navigateTo: Navigator;
}) {
  const sessionId = run.session?.id;
  const navigate = sessionId ? () => navigateTo(`/sessions/${sessionId}`) : undefined;
  const headline = run.status === "skipped" ? "Skipped" : "No work needed";
  const summary = run.result_summary;

  return (
    <div
      role={navigate ? "button" : undefined}
      tabIndex={navigate ? 0 : undefined}
      onClick={navigate}
      onKeyDown={(e) => {
        if (!navigate) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          navigate();
        }
      }}
      className={cn(
        "flex items-center gap-3 rounded-md border border-transparent px-3 py-1.5 text-xs text-muted-foreground transition-colors",
        navigate && "cursor-pointer hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
      )}
    >
      <Minus aria-hidden className="h-3.5 w-3.5 shrink-0" />
      <span className="font-medium text-foreground/70">{headline}</span>
      <span title={new Date(run.triggered_at).toLocaleString()}>
        {formatTimeAgo(run.triggered_at)}
      </span>
      {summary && (
        <span className="truncate" title={summary}>
          · {summary}
        </span>
      )}
    </div>
  );
}
