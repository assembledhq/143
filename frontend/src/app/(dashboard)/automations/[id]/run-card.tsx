"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle,
  ArrowUpRight,
  CheckCircle2,
  ChevronRight,
  Loader2,
  MessageCircleWarning,
  Minus,
  RefreshCw,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { cn, formatTimeAgo } from "@/lib/utils";
import type { AutomationRun, AutomationRunStatus, PRCreationState } from "@/lib/types";

export const QUIET_RUN_STATUSES: ReadonlyArray<AutomationRunStatus> = [
  "completed_noop",
  "skipped",
];

export function isQuietRun(run: AutomationRun): boolean {
  return QUIET_RUN_STATUSES.includes(run.status);
}

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

type Navigator = (path: string) => void;

export function RunCard({ run }: RunCardProps) {
  const router = useRouter();
  const navigateTo = (path: string) => router.push(path);

  const kind = classifyRun(run);
  if (kind === "quiet") return <QuietRunRow run={run} navigateTo={navigateTo} />;
  if (kind === "pending") return <PendingRow run={run} />;
  return <FullCard run={run} kind={kind} navigateTo={navigateTo} />;
}

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

  const ariaLabel = navigate
    ? `Open session for ${headlineFor(kind).label.toLowerCase()} run from ${formatTimeAgo(run.triggered_at)}`
    : undefined;

  return (
    <Card
      role={navigate ? "button" : undefined}
      tabIndex={navigate ? 0 : undefined}
      aria-label={ariaLabel}
      onClick={navigate}
      onKeyDown={handleKeyDown}
      className={cn(
        "group overflow-hidden border transition-all",
        navigate && "cursor-pointer hover:border-primary/30 hover:bg-accent/35 hover:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        cardSurfaceClass(kind),
      )}
    >
      <CardContent className="p-0">
        <div
          data-testid="run-card-layout"
          className="flex flex-col gap-3 px-4 py-3.5 sm:flex-row sm:items-start sm:justify-between sm:px-5"
        >
          <div className="min-w-0 flex-1 space-y-2">
            <Header run={run} kind={kind} />
            <Body run={run} kind={kind} />
            <MetadataRail run={run} kind={kind} />
          </div>
          <div className="flex w-full items-center justify-between gap-2 sm:w-auto sm:justify-start">
            <PrimaryAction run={run} kind={kind} navigateTo={navigateTo} />
            {navigate && (
              <ChevronRight
                aria-hidden
                className="h-4 w-4 shrink-0 translate-x-0 text-muted-foreground/50 opacity-0 transition-all group-hover:translate-x-0.5 group-hover:opacity-100"
              />
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function cardSurfaceClass(kind: FullCardKind): string {
  switch (kind) {
    case "failed":
      return "border-destructive/30 bg-destructive/5";
    case "needs_input":
      return "border-warning/40 bg-warning/5";
    case "running":
      return "border-info/30 bg-info/5";
    default:
      return "border-border/70 bg-card";
  }
}

function Header({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  const headline = headlineFor(kind);
  const Icon = headline.icon;
  const title = primaryLine(run, kind);

  return (
    <div className="flex flex-col gap-1.5 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0 space-y-1.5">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Icon
            aria-hidden
            className={cn("h-4 w-4 shrink-0", headline.iconClass, kind === "running" && "animate-spin")}
          />
          <span className={cn("text-sm font-semibold", headline.labelClass)}>{headline.label}</span>
          {run.session?.failure_category && (kind === "failed" || kind === "needs_input") && (
            <Badge
              variant={kind === "failed" ? "destructive" : "outline"}
              className={cn(
                "text-xs uppercase tracking-[0.18em]",
                kind === "needs_input" && "border-warning/40 text-warning",
              )}
            >
              {run.session.failure_category.replaceAll("_", " ")}
            </Badge>
          )}
        </div>
        {title && (
          <p className="line-clamp-2 text-sm font-medium leading-5 text-foreground" title={title}>
            {title}
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-1.5 text-xs tabular-nums text-muted-foreground sm:pl-4">
        <span title={new Date(run.triggered_at).toLocaleString()}>{formatTimeAgo(run.triggered_at)}</span>
        {kind === "running" && <LiveDuration startedAt={run.triggered_at} />}
        {kind !== "running" && run.completed_at && (
          <span>· {formatDuration(run.triggered_at, run.completed_at)}</span>
        )}
      </div>
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
        iconClass: "text-info",
        labelClass: "text-info",
      };
    case "needs_input":
      return {
        icon: MessageCircleWarning,
        label: "Needs your input",
        iconClass: "text-warning",
        labelClass: "text-warning",
      };
    case "completed_with_pr":
    case "completed_no_pr":
      return {
        icon: CheckCircle2,
        label: "Completed",
        iconClass: "text-success",
        labelClass: "text-success",
      };
    case "failed":
      return {
        icon: AlertTriangle,
        label: "Failed",
        iconClass: "text-destructive",
        labelClass: "text-destructive",
      };
  }
}

function Body({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  const subline = secondaryLine(run, kind);
  if (!subline) return null;

  return (
    <p
      className={cn(
        "line-clamp-2 text-sm leading-5",
        kind === "failed" ? "text-destructive/90" : "text-muted-foreground",
      )}
      title={subline}
    >
      {subline}
    </p>
  );
}

function primaryLine(run: AutomationRun, kind: FullCardKind): string | null {
  if (kind === "needs_input") {
    return run.session?.title || run.result_summary || "Agent paused, waiting for your guidance.";
  }
  if (kind === "failed") {
    return run.session?.title || run.result_summary || "Run failed.";
  }
  if (kind === "running") {
    return run.session?.title || "Working on it…";
  }
  return run.session?.title || run.result_summary || null;
}

function secondaryLine(run: AutomationRun, kind: FullCardKind): string | null {
  if (kind === "failed" || kind === "needs_input") {
    if (run.session?.failure_explanation && run.session.failure_explanation !== run.session?.title) {
      return run.session.failure_explanation;
    }
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

function MetadataRail({ run, kind }: { run: AutomationRun; kind: FullCardKind }) {
  const items = metadataItems(run, kind);
  if (items.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
      {items.map((item, index) => (
        <span key={`${item}-${index}`} className="flex items-center gap-2">
          {index > 0 && <span aria-hidden className="text-muted-foreground/40">·</span>}
          <span>{item}</span>
        </span>
      ))}
    </div>
  );
}

function metadataItems(run: AutomationRun, kind: FullCardKind): string[] {
  const items: string[] = [];
  items.push(automationRunTriggerLabel(run.triggered_by));
  if (run.session?.id) items.push("Linked session");
  if (run.capability_snapshot?.length) {
    items.push(`${run.capability_snapshot.length} capabilities`);
  }
  if (run.session?.pr) items.push(`PR #${run.session.pr.number}`);

  const filesChanged = run.session?.diff_stats?.files_changed;
  if (typeof filesChanged === "number" && filesChanged > 0) {
    items.push(`${filesChanged} file${filesChanged === 1 ? "" : "s"} changed`);
  } else {
    const diff = run.session?.diff_stats;
    const hasDiff = diff && (diff.added > 0 || diff.removed > 0);
    if (hasDiff && kind === "completed_no_pr") items.push("Code changes ready");
  }

  if (!run.session?.pr) {
    const diff = run.session?.diff_stats;
    const hasDiff = !!diff && (diff.added > 0 || diff.removed > 0);
    const prLabel = prStateLabel(run.session?.pr_creation_state, hasDiff);
    if (prLabel) items.push(prLabel);
  }

  return items;
}

function prStateLabel(prState: PRCreationState | undefined, hasDiff: boolean): string | null {
  if (prState === "queued" || prState === "pushing") return "Creating PR…";
  if (prState === "failed") return "PR creation failed";
  if (hasDiff) return "No PR yet";
  return null;
}

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
  const stop = (e: React.MouseEvent | React.KeyboardEvent) => e.stopPropagation();

  if (kind === "completed_with_pr" && run.session?.pr) {
    return (
      <Button asChild size="sm" variant="outline" className="w-full shrink-0 sm:w-auto">
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
  else if (kind === "completed_no_pr") label = "Session";

  return (
    <Button
      size="sm"
      variant={kind === "needs_input" ? "default" : "outline"}
      className={cn(
        "w-full shrink-0 sm:w-auto",
        kind === "completed_no_pr" && "h-7 w-auto px-2 text-muted-foreground hover:text-foreground",
      )}
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

function PendingRow({ run }: { run: AutomationRun }) {
  const triggerLabel = automationRunTriggerLabel(run.triggered_by);
  return (
    <Card className="border-dashed border-border/70 bg-muted/10">
      <CardContent className="flex items-center justify-between gap-3 px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
          <div className="min-w-0">
            <p className="text-sm font-medium text-foreground">Pending</p>
            <p className="text-xs text-muted-foreground">{triggerLabel} · waiting to start</p>
          </div>
        </div>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
          {formatTimeAgo(run.triggered_at)}
        </span>
      </CardContent>
    </Card>
  );
}

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
    <Card
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
        "group border-border/70 bg-muted/10 transition-colors",
        navigate && "cursor-pointer hover:border-border/90 hover:bg-muted/20 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
      )}
    >
      <CardContent className="px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <Minus aria-hidden className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <p className="truncate text-sm font-medium text-foreground/80">{headline}</p>
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
              <span>{automationRunTriggerLabel(run.triggered_by)}</span>
              {run.session?.id && (
                <span className="flex items-center gap-2">
                  <span aria-hidden className="text-muted-foreground/40">·</span>
                  <span>Linked session</span>
                </span>
              )}
            </div>
            {summary && <p className="mt-1 truncate text-xs text-muted-foreground">{summary}</p>}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <span
              className="text-xs tabular-nums text-muted-foreground"
              title={new Date(run.triggered_at).toLocaleString()}
            >
              {formatTimeAgo(run.triggered_at)}
            </span>
            {navigate && (
              <ChevronRight
                aria-hidden
                className="h-3.5 w-3.5 text-muted-foreground/50 opacity-0 transition-opacity group-hover:opacity-100"
              />
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function automationRunTriggerLabel(triggeredBy: AutomationRun["triggered_by"]): string {
  switch (triggeredBy) {
    case "manual":
      return "Manual run";
    case "github":
      return "GitHub event";
    case "schedule":
    default:
      return "Scheduled run";
  }
}
