"use client";

import { RefObject, useMemo } from "react";
import { Loader2, MoreVertical, Plus, Square, GitBranch, Undo2, AlertTriangle } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AGENTS_BY_KEY } from "@/lib/agents";
import type { SessionThread, SessionThreadFileEvent } from "@/lib/types";

// Status helpers — kept in one place so the tab strip and detail panel agree.

function threadStatusLabel(status: string, statusConfig: Record<string, { label: string }>): string {
  return statusConfig[status]?.label ?? status.replace(/_/g, " ");
}

function threadStatusDotClass(status: string): string {
  switch (status) {
    case "running":
      return "bg-primary";
    case "pending":
      return "bg-muted-foreground";
    case "awaiting_input":
      return "bg-amber-500";
    case "failed":
      return "bg-destructive";
    case "completed":
      return "bg-emerald-500";
    case "cancelled":
      return "bg-muted-foreground/60";
    default:
      return "bg-primary";
  }
}

function isActiveStatus(status: string): boolean {
  return status === "pending" || status === "running" || status === "awaiting_input";
}

// Compute per-thread overlap: a path counts as an overlap when at least two
// distinct active threads have touched it. Computed client-side from the
// session-wide file events so the tab strip can render a badge without an
// extra round-trip.
export function computeThreadOverlap(
  threads: SessionThread[],
  events: SessionThreadFileEvent[] | undefined,
): Map<string, string[]> {
  const result = new Map<string, string[]>();
  if (!events || events.length === 0) {
    return result;
  }
  const activeThreadIds = new Set(threads.filter((t) => isActiveStatus(t.status)).map((t) => t.id));
  // path -> set of thread ids that touched it
  const ownersByPath = new Map<string, Set<string>>();
  for (const e of events) {
    if (!e.thread_id) continue;
    if (!activeThreadIds.has(e.thread_id)) continue;
    let owners = ownersByPath.get(e.path);
    if (!owners) {
      owners = new Set<string>();
      ownersByPath.set(e.path, owners);
    }
    owners.add(e.thread_id);
  }
  // For each thread, the overlapping paths are those where its set of owners
  // includes the thread AND at least one other active thread.
  for (const threadId of activeThreadIds) {
    const overlapping: string[] = [];
    for (const [path, owners] of ownersByPath) {
      if (owners.has(threadId) && owners.size >= 2) {
        overlapping.push(path);
      }
    }
    if (overlapping.length > 0) {
      overlapping.sort();
      result.set(threadId, overlapping);
    }
  }
  return result;
}

interface AgentTabStripProps {
  threads: SessionThread[];
  activeThreadId: string | null;
  overlapsByThreadId: Map<string, string[]>;
  statusConfig: Record<string, { label: string }>;
  onActiveThreadChange: (threadId: string) => void;
  onAddTab: () => void;
  addTabPending?: boolean;
  onCancelThread: (threadId: string) => void;
  onForkThread: (threadId: string) => void;
  onRevertThread: (threadId: string) => void;
  cancelPendingThreadId: string | null;
  addTabButtonRef?: RefObject<HTMLButtonElement | null>;
}

// AgentTabStrip is the user's primary surface for switching between tabs and
// taking per-tab actions (cancel, fork, revert). It renders a compact
// Conductor-style row with status, overlap badge, and an actions menu
// per tab.
//
// Design notes:
// - Single tab degrades into the original "quiet header" look so a session
//   with one agent does not feel project-board-y.
// - The tab dot animates when running so a glance at the strip tells the
//   user which lane is mid-turn.
// - Overlap is rendered with an AlertTriangle so the user notices conflict
//   before reviewing the diff.
export function AgentTabStrip({
  threads,
  activeThreadId,
  overlapsByThreadId,
  statusConfig,
  onActiveThreadChange,
  onAddTab,
  addTabPending = false,
  onCancelThread,
  onForkThread,
  onRevertThread,
  cancelPendingThreadId,
  addTabButtonRef,
}: AgentTabStripProps) {
  const tabs = useMemo(() => threads, [threads]);
  if (tabs.length === 0 || !activeThreadId) {
    return null;
  }
  const activeThread = tabs.find((thread) => thread.id === activeThreadId);
  if (!activeThread) {
    return null;
  }

  if (tabs.length === 1) {
    const agent = AGENTS_BY_KEY[activeThread.agent_type];
    const agentLabel = agent?.label ?? activeThread.agent_type;
    const statusLabel = threadStatusLabel(activeThread.status, statusConfig);
    const overlap = overlapsByThreadId.get(activeThread.id) ?? [];
    const isCancelling =
      activeThread.cancel_requested_at != null && isActiveStatus(activeThread.status);
    const queued = activeThread.pending_message_count ?? 0;
    const needsAttention =
      activeThread.status === "awaiting_input" || activeThread.status === "failed";

    return (
      <TooltipProvider delayDuration={150}>
        <div className="shrink-0 border-b border-border bg-background px-3 py-2">
          <div className="flex min-w-0 items-center gap-2">
            <Tooltip>
              <TooltipTrigger asChild>
                <div
                  tabIndex={0}
                  role="group"
                  aria-label={`${agentLabel} ${statusLabel}`}
                  className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1 py-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  <span
                    className={cn(
                      "h-2 w-2 shrink-0 rounded-full",
                      threadStatusDotClass(activeThread.status),
                      activeThread.status === "running" && !isCancelling && "animate-pulse",
                    )}
                    aria-hidden
                  />
                  <span className="truncate text-sm font-medium text-foreground">{agentLabel}</span>
                  <span className="truncate text-sm text-muted-foreground">{statusLabel}</span>
                  {isCancelling && (
                    <Loader2
                      className="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground"
                      aria-label="Cancelling"
                    />
                  )}
                  {queued > 0 && (
                    <Badge variant="secondary" className="h-4 px-1 text-xs leading-none">
                      {queued}
                    </Badge>
                  )}
                  {needsAttention && (
                    <span
                      className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500"
                      aria-label="Needs attention"
                    />
                  )}
                  {overlap.length > 0 && (
                    <AlertTriangle
                      className="h-3 w-3 shrink-0 text-amber-600 dark:text-amber-400"
                      aria-label={`Overlaps with another tab on ${overlap.length} file${overlap.length === 1 ? "" : "s"}`}
                    />
                  )}
                </div>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="max-w-sm text-xs">
                <div className="space-y-1">
                  <div className="font-medium">
                    {agentLabel}
                    <span className="font-normal text-muted-foreground"> — {activeThread.label}</span>
                  </div>
                  <div className="text-muted-foreground">
                    {statusLabel}
                    {queued > 0 ? ` · ${queued} message${queued === 1 ? "" : "s"} queued` : ""}
                  </div>
                  {overlap.length > 0 && (
                    <div className="pt-1">
                      <div className="font-medium text-amber-700 dark:text-amber-400">
                        Overlap with another tab:
                      </div>
                      <ul className="text-muted-foreground">
                        {overlap.slice(0, 5).map((path) => (
                          <li key={path} className="truncate">
                            {path}
                          </li>
                        ))}
                        {overlap.length > 5 && (
                          <li className="text-muted-foreground/80">…and {overlap.length - 5} more</li>
                        )}
                      </ul>
                    </div>
                  )}
                </div>
              </TooltipContent>
            </Tooltip>

            <ThreadActionsMenu
              threads={tabs}
              activeThreadId={activeThreadId}
              onCancel={onCancelThread}
              onFork={onForkThread}
              onRevert={onRevertThread}
              cancelPendingThreadId={cancelPendingThreadId}
            />
            <Button
              ref={addTabButtonRef}
              type="button"
              size="icon"
              variant="ghost"
              className="h-8 w-8 shrink-0"
              aria-label="Add agent tab"
              title="Add agent tab"
              onClick={onAddTab}
            >
              <Plus className="h-4 w-4" />
            </Button>
          </div>
        </div>
      </TooltipProvider>
    );
  }

  return (
    <TooltipProvider delayDuration={150}>
      <div className="border-b border-border bg-background px-3 py-2 shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <Tabs value={activeThreadId} onValueChange={onActiveThreadChange} className="min-w-0 flex-1">
            <TabsList
              variant="line"
              size="sm"
              aria-label="Agent tabs"
              className={cn(
                "h-auto max-w-full justify-start gap-1 overflow-x-auto overflow-y-hidden border-b-0 bg-transparent p-0",
              )}
            >
              {tabs.map((thread) => {
                const agent = AGENTS_BY_KEY[thread.agent_type];
                const agentLabel = agent?.label ?? thread.agent_type;
                const statusLabel = threadStatusLabel(thread.status, statusConfig);
                const needsAttention = thread.status === "awaiting_input" || thread.status === "failed";
                const overlap = overlapsByThreadId.get(thread.id) ?? [];
                const isCancelling = thread.cancel_requested_at != null && isActiveStatus(thread.status);
                const queued = thread.pending_message_count ?? 0;

                return (
                  <Tooltip key={thread.id}>
                    <TooltipTrigger asChild>
                      <TabsTrigger
                        value={thread.id}
                        className={cn(
                          "h-8 max-w-[15rem] gap-1.5 rounded-md px-2 text-xs data-[state=active]:text-primary",
                          tabs.length === 1 && "data-[state=active]:bg-transparent data-[state=active]:shadow-none",
                        )}
                      >
                        <span
                          className={cn(
                            "h-2 w-2 shrink-0 rounded-full",
                            threadStatusDotClass(thread.status),
                            thread.status === "running" && !isCancelling && "animate-pulse",
                          )}
                          aria-hidden
                        />
                        <span className="truncate">{thread.label}</span>
                        <span className="hidden sm:inline text-muted-foreground">{"— "}{statusLabel.toLowerCase()}</span>
                        {isCancelling && (
                          <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" aria-label="Cancelling" />
                        )}
                        {queued > 0 && (
                          <Badge variant="secondary" className="h-4 px-1 text-xs leading-none">
                            {queued}
                          </Badge>
                        )}
                        {needsAttention && (
                          <span className="h-1.5 w-1.5 rounded-full bg-amber-500" aria-label="Needs attention" />
                        )}
                        {overlap.length > 0 && (
                          <AlertTriangle
                            className="h-3 w-3 shrink-0 text-amber-600 dark:text-amber-400"
                            aria-label={`Overlaps with another tab on ${overlap.length} file${overlap.length === 1 ? "" : "s"}`}
                          />
                        )}
                      </TabsTrigger>
                    </TooltipTrigger>
                    <TooltipContent side="bottom" className="max-w-sm text-xs">
                      <div className="space-y-1">
                        <div className="font-medium">{thread.label} <span className="font-normal text-muted-foreground">— {agentLabel}</span></div>
                        <div className="text-muted-foreground">{statusLabel}{queued > 0 ? ` · ${queued} message${queued === 1 ? "" : "s"} queued` : ""}</div>
                        {overlap.length > 0 && (
                          <div className="pt-1">
                            <div className="font-medium text-amber-700 dark:text-amber-400">Overlap with another tab:</div>
                            <ul className="text-muted-foreground">
                              {overlap.slice(0, 5).map((p) => (
                                <li key={p} className="truncate">{p}</li>
                              ))}
                              {overlap.length > 5 && (
                                <li className="text-muted-foreground/80">…and {overlap.length - 5} more</li>
                              )}
                            </ul>
                          </div>
                        )}
                      </div>
                    </TooltipContent>
                  </Tooltip>
                );
              })}
            </TabsList>
          </Tabs>

          <ThreadActionsMenu
            threads={tabs}
            activeThreadId={activeThreadId}
            onCancel={onCancelThread}
            onFork={onForkThread}
            onRevert={onRevertThread}
            cancelPendingThreadId={cancelPendingThreadId}
          />
          <Button
            ref={addTabButtonRef}
            type="button"
            size="icon"
            variant="ghost"
            className="h-8 w-8 shrink-0"
            aria-label="Add agent tab"
            title="Add agent tab"
            onClick={onAddTab}
            disabled={addTabPending}
          >
            <Plus className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </TooltipProvider>
  );
}

interface ThreadActionsMenuProps {
  threads: SessionThread[];
  activeThreadId: string;
  onCancel: (threadId: string) => void;
  onFork: (threadId: string) => void;
  onRevert: (threadId: string) => void;
  cancelPendingThreadId: string | null;
}

function ThreadActionsMenu({ threads, activeThreadId, onCancel, onFork, onRevert, cancelPendingThreadId }: ThreadActionsMenuProps) {
  const active = threads.find((t) => t.id === activeThreadId);
  if (!active) return null;
  const canCancel = isActiveStatus(active.status);
  const canRevert = !!active.diff && active.diff.length > 0;
  const isCancellingThis = cancelPendingThreadId === active.id;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="h-8 w-8 shrink-0"
          aria-label="Tab actions"
          title="Tab actions"
        >
          <MoreVertical className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuItem
          onSelect={() => canCancel && onCancel(active.id)}
          disabled={!canCancel || isCancellingThis}
        >
          {isCancellingThis ? <Loader2 className="h-4 w-4 animate-spin" /> : <Square className="h-4 w-4" />}
          <span>{isCancellingThis ? "Cancelling…" : "Cancel turn"}</span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => onFork(active.id)}>
          <GitBranch className="h-4 w-4" />
          <span>Fork into new session</span>
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={() => canRevert && onRevert(active.id)}
          disabled={!canRevert}
          title={canRevert ? undefined : "This tab has no recorded diff yet."}
        >
          <Undo2 className="h-4 w-4" />
          <span>Revert this tab&apos;s changes</span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
