"use client";

import { RefObject, useMemo } from "react";
import { Loader2, MoreVertical, Plus, Undo2, AlertTriangle, X } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AGENTS_BY_KEY } from "@/lib/agents";
import type { SessionThread, SessionThreadFileEvent } from "@/lib/types";

// Status helpers — kept in one place so the tab strip and detail panel agree.

function threadStatusLabel(status: string, statusConfig: Record<string, { label: string }>): string {
  return statusConfig[status]?.label ?? status.replace(/_/g, " ");
}

function isActiveStatus(status: string): boolean {
  return status === "pending" || status === "running" || status === "awaiting_input";
}

function shouldShowUnreadDot(thread: SessionThread, viewedThreadIds: ReadonlySet<string>): boolean {
  if (!viewedThreadIds.has(thread.id)) {
    return true;
  }

  return thread.status === "pending" || thread.status === "running";
}

function canArchiveThread(thread: SessionThread, threadCount: number): boolean {
  return threadCount > 1 && !isActiveStatus(thread.status);
}

function addTabButtonClassName(): string {
  return "h-7 w-7 shrink-0 rounded-sm text-muted-foreground opacity-70 transition-opacity hover:text-foreground hover:opacity-100 focus-visible:opacity-100";
}

function formatDeliverySummary(thread: SessionThread): string | null {
  const delivery = thread.inbox_delivery;
  if (!delivery || delivery.state === "idle") {
    return null;
  }
  const stateLabel =
    delivery.state === "dead_letter"
      ? "dead letter"
      : delivery.state === "unknown_delivery"
        ? "unknown"
        : delivery.state.replace(/_/g, " ");
  const parts: string[] = [];
  if (delivery.pending_count > 0) {
    parts.push(`${delivery.pending_count} waiting`);
  }
  if (delivery.delivering_count > 0) {
    parts.push(`${delivery.delivering_count} delivering`);
  }
  if (delivery.delivered_count > 0) {
    parts.push(`${delivery.delivered_count} delivered`);
  }
  if (delivery.unknown_delivery_count > 0) {
    parts.push(`${delivery.unknown_delivery_count} uncertain`);
  }
  if (delivery.dead_letter_count > 0) {
    parts.push(`${delivery.dead_letter_count} failed`);
  }
  return `Delivery ${stateLabel}${parts.length > 0 ? ` · ${parts.join(" · ")}` : ""}`;
}

function formatThreadProvenance(thread: SessionThread): string | null {
  if (thread.created_by_source === "agent_tool") {
    return "Created by agent via 143-tools";
  }
  if (thread.created_by_source === "system") {
    return "Created by system";
  }
  return null;
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
  viewedThreadIds: ReadonlySet<string>;
  nonInteractiveThreadIds?: ReadonlySet<string>;
  overlapsByThreadId: Map<string, string[]>;
  statusConfig: Record<string, { label: string }>;
  onActiveThreadChange: (threadId: string) => void;
  onAddTab: () => void;
  addTabPending?: boolean;
  onRevertThread: (threadId: string) => void;
  onArchiveThread: (threadId: string) => void;
  archivePendingThreadId: string | null;
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
  viewedThreadIds,
  nonInteractiveThreadIds,
  overlapsByThreadId,
  statusConfig,
  onActiveThreadChange,
  onAddTab,
  addTabPending = false,
  onRevertThread,
  onArchiveThread,
  archivePendingThreadId,
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
    const deliverySummary = formatDeliverySummary(activeThread);
    const provenance = formatThreadProvenance(activeThread);
    const needsAttention =
      activeThread.status === "awaiting_input" || activeThread.status === "failed";
    const showUnreadDot = shouldShowUnreadDot(activeThread, viewedThreadIds);

    return (
      <TooltipProvider delayDuration={150}>
        <div className="shrink-0 border-b border-border bg-background px-3 py-2">
          <div className="flex min-w-0 items-center gap-2 min-h-9">
            <div className="flex min-w-0 flex-1 items-center">
              <Tooltip>
                <TooltipTrigger asChild>
                  <div
                    tabIndex={0}
                    role="group"
                    aria-label={`${agentLabel} ${statusLabel}`}
                    className="inline-flex max-w-full min-w-0 items-center gap-2 rounded-md px-1 py-1 outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  >
                    {showUnreadDot ? (
                      <span
                        className={cn(
                          "h-2 w-2 shrink-0 rounded-full bg-primary",
                          activeThread.status === "running" && !isCancelling && "animate-pulse",
                        )}
                        aria-hidden
                      />
                    ) : (
                      <span className="h-2 w-2 shrink-0" aria-hidden />
                    )}
                    <span className="truncate text-xs font-medium text-foreground">{activeThread.label}</span>
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
                    {provenance && <div className="text-muted-foreground">{provenance}</div>}
                    {deliverySummary && <div className="text-muted-foreground">{deliverySummary}</div>}
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
            </div>

            <ThreadActionsMenu
              threads={tabs}
              activeThreadId={activeThreadId}
              onRevert={onRevertThread}
            />
            <Button
              ref={addTabButtonRef}
              type="button"
              size="icon"
              variant="ghost"
              className={addTabButtonClassName()}
              aria-label="Add agent tab"
              title="Add agent tab (t)"
              onClick={onAddTab}
            >
              <Plus className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      </TooltipProvider>
    );
  }

  return (
    <TooltipProvider delayDuration={150}>
      <div className="border-b border-border bg-background px-3 py-2 shrink-0">
        <div className="flex items-center gap-2 min-w-0 min-h-9">
          <Tabs value={activeThreadId} onValueChange={onActiveThreadChange} className="min-w-0 flex-1">
            <div className="overflow-x-auto overflow-y-hidden pb-1 min-w-0">
              <TabsList
                variant="line"
                size="sm"
                aria-label="Agent tabs"
                className={cn(
                  "h-auto max-w-full justify-start gap-1 border-b-0 bg-transparent px-0",
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
                  const deliverySummary = formatDeliverySummary(thread);
                  const provenance = formatThreadProvenance(thread);
                  const showUnreadDot = shouldShowUnreadDot(thread, viewedThreadIds);
                  const showArchiveButton = canArchiveThread(thread, tabs.length);
                  const isNonInteractive = nonInteractiveThreadIds?.has(thread.id) ?? false;
                  const closeLabel = `Close ${thread.label}${thread.label.toLowerCase().endsWith(" tab") ? "" : " tab"}`;

                  return (
                    <div key={thread.id} className="group relative flex shrink-0 items-center">
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span className="inline-flex">
                            <TabsTrigger
                              value={thread.id}
                              disabled={isNonInteractive}
                              className={cn(
                                "h-7 max-w-[15rem] gap-1.5 rounded-md px-2 text-xs data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none",
                                showArchiveButton && "pr-8",
                                tabs.length === 1 && "data-[state=active]:bg-transparent data-[state=active]:shadow-none",
                                isNonInteractive && "cursor-default opacity-60",
                              )}
                            >
                              {showUnreadDot ? (
                                <span
                                  className={cn(
                                    "h-2 w-2 shrink-0 rounded-full bg-primary",
                                    thread.status === "running" && !isCancelling && "animate-pulse",
                                  )}
                                  aria-hidden
                                />
                              ) : (
                                <span className="h-2 w-2 shrink-0" aria-hidden />
                              )}
                              <span className="truncate">{thread.label}</span>
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
                          </span>
                        </TooltipTrigger>
                        <TooltipContent side="bottom" className="max-w-sm text-xs">
                          <div className="space-y-1">
                            <div className="font-medium">{thread.label} <span className="font-normal text-muted-foreground">- {agentLabel}</span></div>
                            <div className="text-muted-foreground">{statusLabel}{queued > 0 ? ` · ${queued} message${queued === 1 ? "" : "s"} queued` : ""}</div>
                            {provenance && <div className="text-muted-foreground">{provenance}</div>}
                            {deliverySummary && <div className="text-muted-foreground">{deliverySummary}</div>}
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
                      {showArchiveButton ? (
                        <Button
                          type="button"
                          size="icon"
                          variant="ghost"
                          className="absolute right-1 top-1/2 h-6 w-6 -translate-y-1/2 rounded-sm opacity-70 hover:opacity-100 focus-visible:opacity-100"
                          aria-label={closeLabel}
                          title={closeLabel}
                          disabled={archivePendingThreadId === thread.id}
                          onClick={(event) => {
                            event.stopPropagation();
                            onArchiveThread(thread.id);
                          }}
                        >
                          {archivePendingThreadId === thread.id ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <X className="h-3.5 w-3.5" />}
                        </Button>
                      ) : null}
                    </div>
                  );
                })}
              </TabsList>
            </div>
          </Tabs>

          <ThreadActionsMenu
            threads={tabs}
            activeThreadId={activeThreadId}
            onRevert={onRevertThread}
          />
          <Button
            ref={addTabButtonRef}
            type="button"
            size="icon"
            variant="ghost"
            className={addTabButtonClassName()}
            aria-label="Add agent tab"
            title="Add agent tab (t)"
            onClick={onAddTab}
            disabled={addTabPending}
          >
            <Plus className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
    </TooltipProvider>
  );
}

interface ThreadActionsMenuProps {
  threads: SessionThread[];
  activeThreadId: string;
  onRevert: (threadId: string) => void;
}

function ThreadActionsMenu({ threads, activeThreadId, onRevert }: ThreadActionsMenuProps) {
  const active = threads.find((t) => t.id === activeThreadId);
  if (!active) return null;
  const canRevert = !!active.diff && active.diff.length > 0;

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
