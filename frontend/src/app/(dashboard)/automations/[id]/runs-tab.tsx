"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import type { AutomationRun } from "@/lib/types";
import { cn, formatTimeAgo } from "@/lib/utils";

import { QuietRunRow, RunCard } from "./run-card";
import { groupRuns, type RunGroup } from "./run-grouping";

const PAGE_SIZE = 25;
const POLL_MS = 10_000;

export function RunsTab({ automationId }: { automationId: string }) {
  // Pages are stored as a list of result pages so the polling refetch only
  // replaces page 0 (latest runs) and any pages loaded via "Load more"
  // persist across refetches. Using setState inside `select` would reset
  // pagination on every poll tick.
  const [extraPages, setExtraPages] = useState<AutomationRun[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>(undefined);
  // Tracks user-driven toggle state for quiet groups, keyed by groupKey
  // (the streak's oldest run id, stable across polling). We deliberately
  // store explicit (key → open) instead of "set of open keys" so we can
  // distinguish "user hasn't touched this group" from "user collapsed it"
  // and let the auto-expand fall-through only apply to the former.
  const [userToggles, setUserToggles] = useState<Record<string, boolean>>({});

  // Pause polling once the user paginates. If polling kept running while
  // extra pages were loaded, any new run arriving at the top would shift
  // the window and make the stored loadMoreCursor point into the middle of
  // a now-different result set — producing skipped or duplicated runs on
  // the next "Load more". Users who want fresh first-page runs can reload
  // or re-navigate to the tab.
  const isPaginated = extraPages.length > 0;

  const { data, isLoading } = useQuery({
    queryKey: ["automation-runs", automationId],
    queryFn: () => api.automations.listRuns(automationId, { limit: PAGE_SIZE }),
    refetchInterval: isPaginated ? false : POLL_MS,
  });

  const firstPageCursor = data?.meta?.next_cursor || undefined;
  const cursor = isPaginated ? loadMoreCursor : firstPageCursor;

  const loadMoreMutation = useMutation({
    mutationFn: () => api.automations.listRuns(automationId, { limit: PAGE_SIZE, cursor }),
    onSuccess: (res) => {
      // Skip empty pages so a "Load more" near the end of history doesn't
      // permanently pause polling — extraPages.length stays 0 if the
      // server returned no rows, and the standard 10s refetch resumes.
      if (res.data && res.data.length > 0) {
        setExtraPages((prev) => [...prev, res.data]);
      }
      setLoadMoreCursor(res.meta?.next_cursor || undefined);
    },
  });

  const allRuns = useMemo(() => {
    const firstPage = data?.data ?? [];
    return [firstPage, ...extraPages].flat();
  }, [data, extraPages]);
  const groups = useMemo(() => groupRuns(allRuns), [allRuns]);
  const hasMore = !!cursor;

  // Auto-expand rule: if the entire visible list is quiet (a brand-new
  // automation that has only no-op'd, or a slow stretch), default the
  // topmost quiet group open so the page doesn't read as empty. Computed
  // during render — no effect, no setState — so polling refetches don't
  // need to re-run it. Once the user clicks any group's toggle we honor
  // their explicit choice via userToggles.
  const autoExpandedKey = useMemo(() => {
    if (groups.length === 0) return null;
    const onlyQuiet = groups.every((g) => g.kind === "quiet");
    if (!onlyQuiet) return null;
    const topQuiet = groups.find(
      (g): g is Extract<RunGroup, { kind: "quiet" }> => g.kind === "quiet",
    );
    return topQuiet?.groupKey ?? null;
  }, [groups]);

  const isGroupOpen = (groupKey: string): boolean => {
    if (groupKey in userToggles) return userToggles[groupKey];
    return groupKey === autoExpandedKey;
  };
  const setGroupOpen = (groupKey: string, open: boolean) => {
    setUserToggles((prev) => ({ ...prev, [groupKey]: open }));
  };

  const router = useRouter();
  const navigateTo = (path: string) => router.push(path);

  if (isLoading) return <RunsSkeleton />;
  if (allRuns.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-border/70 bg-muted/15 px-5 py-12 text-center">
        <p className="text-sm font-medium text-foreground">No runs yet</p>
        <p className="mt-1 text-sm text-muted-foreground">
          The first run will appear here after the scheduled time, or when you click <span className="font-medium">Run now</span>.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {groups.map((group, idx) => {
        if (group.kind === "single") {
          return <RunCard key={group.run.id} run={group.run} />;
        }
        return (
          <QuietGroup
            key={`${group.groupKey}-${idx}`}
            group={group}
            open={isGroupOpen(group.groupKey)}
            onOpenChange={(open) => setGroupOpen(group.groupKey, open)}
            navigateTo={navigateTo}
          />
        );
      })}

      {loadMoreMutation.isError && (
        <p className="text-center text-xs text-destructive">
          Failed to load more runs. Please try again.
        </p>
      )}
      {hasMore && (
        <Button
          variant="ghost"
          size="sm"
          className="w-full rounded-xl border border-dashed border-border/70 bg-muted/10 text-muted-foreground hover:bg-muted/20 hover:text-foreground"
          onClick={() => loadMoreMutation.mutate()}
          disabled={loadMoreMutation.isPending}
        >
          {loadMoreMutation.isPending ? "Loading…" : "Load more"}
        </Button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Quiet group — collapsible bar that hides ≥2 consecutive low-signal runs
// ---------------------------------------------------------------------------

interface QuietGroupProps {
  group: Extract<RunGroup, { kind: "quiet" }>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  navigateTo: (path: string) => void;
}

function QuietGroup({ group, open, onOpenChange, navigateTo }: QuietGroupProps) {
  const newest = group.runs[0];
  const oldest = group.runs[group.runs.length - 1];
  const summary = `${group.runs.length} quiet runs`;
  const span = `last one ${formatTimeAgo(newest.triggered_at)}`;

  return (
    <Collapsible open={open} onOpenChange={onOpenChange} className="rounded-xl border border-border/70 bg-muted/10">
      <CollapsibleTrigger
        className={cn(
          "group flex w-full items-center justify-between gap-3 rounded-xl px-4 py-3 text-xs text-muted-foreground transition-colors",
          "hover:bg-muted/25 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
        )}
        title={`${group.runs.length} runs from ${new Date(oldest.triggered_at).toLocaleString()} to ${new Date(newest.triggered_at).toLocaleString()}`}
      >
        <span className="flex items-center gap-2">
          <ChevronRight
            aria-hidden
            className={cn(
              "h-3.5 w-3.5 shrink-0 transition-transform",
              open && "rotate-90",
            )}
          />
          <span className="font-medium text-foreground/80">{summary}</span>
          <span>· {span}</span>
        </span>
        <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground/70">
          {open ? "Hide" : "Show"}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="space-y-2 px-3 pb-3 pt-1">
          {group.runs.map((run) => (
            <QuietRunRow key={run.id} run={run} navigateTo={navigateTo} />
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

// ---------------------------------------------------------------------------
// Loading skeleton — alternates tall card and thin row to hint at the
// status-aware row layout users will see once data lands.
// ---------------------------------------------------------------------------

function RunsSkeleton() {
  return (
    <div className="space-y-3" aria-busy aria-live="polite" aria-label="Loading runs">
      <SkeletonCard tall />
      <SkeletonRow />
      <SkeletonCard />
      <SkeletonRow />
    </div>
  );
}

function SkeletonCard({ tall = false }: { tall?: boolean }) {
  return (
    <div
      className={cn(
        "rounded-xl border border-border/60 bg-muted/20 p-4",
        tall ? "h-[104px]" : "h-[76px]",
        "animate-pulse",
      )}
    />
  );
}

function SkeletonRow() {
  return <div className="h-12 animate-pulse rounded-xl border border-border/50 bg-muted/20" />;
}
