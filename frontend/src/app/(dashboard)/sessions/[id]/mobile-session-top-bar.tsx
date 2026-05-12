"use client";

import { useMemo, useState } from "react";
import {
  GitBranch,
  Loader2,
  MoreVertical,
  PanelBottomOpen,
  Pencil,
  Plus,
  Square,
  Undo2,
  X,
} from "lucide-react";

import { AGENTS_BY_KEY } from "@/lib/agents";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MobileBackButton } from "@/components/mobile-back-button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import type { SessionThread } from "@/lib/types";

interface MobileSessionTopBarProps {
  sessionTitle: string;
  detailButtonLabel: string;
  backTo: string;
  threads: SessionThread[];
  activeThreadId: string | null;
  viewedThreadIds: ReadonlySet<string>;
  nonInteractiveThreadIds?: ReadonlySet<string>;
  onOpenDetails: () => void;
  onActiveThreadChange: (threadId: string) => void;
  onAddThread: () => void;
  onRenameSession: () => void;
  onCancelThread: (threadId: string) => void;
  onForkThread: (threadId: string) => void;
  onRevertThread: (threadId: string) => void;
  onArchiveThread: (threadId: string) => void;
  cancelPendingThreadId: string | null;
  archivePendingThreadId: string | null;
}

function threadStatusLabel(status: string): string {
  switch (status) {
    case "pending":
      return "Pending";
    case "running":
      return "Running";
    case "idle":
      return "Idle";
    case "awaiting_input":
      return "Awaiting input";
    case "needs_human_guidance":
      return "Needs guidance";
    case "completed":
      return "Completed";
    case "pr_created":
      return "PR created";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
    case "skipped":
      return "Skipped";
    default:
      return status.replace(/_/g, " ");
  }
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

export function MobileSessionTopBar({
  sessionTitle,
  detailButtonLabel,
  backTo,
  threads,
  activeThreadId,
  viewedThreadIds,
  nonInteractiveThreadIds,
  onOpenDetails,
  onActiveThreadChange,
  onAddThread,
  onRenameSession,
  onCancelThread,
  onForkThread,
  onRevertThread,
  onArchiveThread,
  cancelPendingThreadId,
  archivePendingThreadId,
}: MobileSessionTopBarProps) {
  const [actionsOpen, setActionsOpen] = useState(false);

  const activeThread = useMemo(
    () => threads.find((thread) => thread.id === activeThreadId) ?? null,
    [activeThreadId, threads],
  );
  const canCancel = activeThread ? isActiveStatus(activeThread.status) : false;
  const canRevert = !!activeThread?.diff;
  const isCancellingThis = activeThread ? cancelPendingThreadId === activeThread.id : false;

  return (
    <>
      <div className="sticky top-0 z-20 flex items-center gap-1 border-b border-border bg-background/95 px-2 py-2 backdrop-blur supports-[backdrop-filter]:bg-background/85 md:hidden">
        <MobileBackButton to={backTo} label="Back to sessions" className="h-9 w-9" />
        <p className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
          {sessionTitle}
        </p>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="h-9 w-9 shrink-0"
          aria-label="Open session actions"
          aria-expanded={actionsOpen}
          onClick={() => setActionsOpen(true)}
        >
          <MoreVertical className="h-5 w-5" />
        </Button>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="h-9 w-9 shrink-0"
          aria-label={detailButtonLabel}
          aria-controls="session-detail-sheet"
          onClick={onOpenDetails}
        >
          <PanelBottomOpen className="h-5 w-5" />
        </Button>
      </div>

      <Sheet open={actionsOpen} onOpenChange={setActionsOpen}>
        <SheetContent
          side="bottom"
          className="md:hidden max-h-[85vh] overflow-y-auto rounded-t-2xl px-0 pb-0 pt-4"
        >
          <SheetHeader className="px-4 text-left">
            <SheetTitle>Session actions</SheetTitle>
            <SheetDescription>
              Switch threads and manage this session without taking permanent space away from the conversation.
            </SheetDescription>
          </SheetHeader>

          <div className="mt-4 border-t border-border">
            {threads.length > 1 ? (
              <section className="px-4 py-4">
                <div className="mb-3 flex items-center justify-between gap-2">
                  <div>
                    <p className="text-sm font-medium text-foreground">Threads</p>
                    <p className="text-xs text-muted-foreground">Switch the active conversation lane.</p>
                  </div>
                  <Badge variant="secondary" className="text-xs">
                    {threads.length}
                  </Badge>
                </div>
                <div className="space-y-2">
                  {threads.map((thread) => {
                    const agent = AGENTS_BY_KEY[thread.agent_type];
                    const statusLabel = threadStatusLabel(thread.status);
                    const isActive = thread.id === activeThreadId;
                    const showUnreadDot = shouldShowUnreadDot(thread, viewedThreadIds);
                    const showArchiveButton = canArchiveThread(thread, threads.length);
                    const isNonInteractive = nonInteractiveThreadIds?.has(thread.id) ?? false;
                    const closeLabel = `Close ${thread.label}${thread.label.toLowerCase().endsWith(" tab") ? "" : " tab"}`;
                    return (
                      <div key={thread.id} className="flex items-center gap-2">
                        <Button
                          type="button"
                          variant="ghost"
                          className={cn(
                            "h-auto flex-1 justify-start rounded-xl border px-3 py-3 text-left",
                            isActive ? "border-primary/30 bg-primary/5" : "border-border bg-background",
                          )}
                          aria-label={`Switch to ${thread.label}`}
                          disabled={isNonInteractive}
                          onClick={() => {
                            if (isNonInteractive) return;
                            onActiveThreadChange(thread.id);
                            setActionsOpen(false);
                          }}
                        >
                          <div className="flex w-full items-start gap-3">
                            {showUnreadDot ? (
                              <span
                                className={cn(
                                  "mt-1.5 h-2 w-2 shrink-0 rounded-full bg-primary",
                                  thread.status === "running" && "animate-pulse",
                                )}
                                aria-hidden
                              />
                            ) : (
                              <span className="mt-1.5 h-2 w-2 shrink-0" aria-hidden />
                            )}
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-2">
                                <span className="truncate text-sm font-medium text-foreground">{thread.label}</span>
                                {isActive ? <Badge variant="secondary" className="text-xs">Active</Badge> : null}
                              </div>
                              <p className="truncate text-xs text-muted-foreground">
                                {(agent?.label ?? thread.agent_type)} · {statusLabel}
                              </p>
                            </div>
                          </div>
                        </Button>
                        {showArchiveButton ? (
                          <Button
                            type="button"
                            size="icon"
                            variant="ghost"
                            className="h-10 w-10 rounded-xl border border-border bg-background"
                            aria-label={closeLabel}
                            disabled={archivePendingThreadId === thread.id}
                            onClick={() => {
                              onArchiveThread(thread.id);
                              setActionsOpen(false);
                            }}
                          >
                            {archivePendingThreadId === thread.id ? <Loader2 className="h-4 w-4 animate-spin" /> : <X className="h-4 w-4" />}
                          </Button>
                        ) : null}
                      </div>
                    );
                  })}
                </div>
              </section>
            ) : null}

            <section className="border-t border-border px-4 py-4">
              <p className="mb-3 text-sm font-medium text-foreground">Session</p>
              <div className="space-y-2">
                <Button
                  type="button"
                  variant="ghost"
                  className="h-11 w-full justify-start rounded-xl border border-border bg-background px-3"
                  aria-label="Add agent tab"
                  onClick={() => {
                    onAddThread();
                    setActionsOpen(false);
                  }}
                >
                  <Plus className="mr-2 h-4 w-4" />
                  Add agent tab
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  className="h-11 w-full justify-start rounded-xl border border-border bg-background px-3"
                  aria-label="Rename session"
                  onClick={() => {
                    onRenameSession();
                    setActionsOpen(false);
                  }}
                >
                  <Pencil className="mr-2 h-4 w-4" />
                  Rename session
                </Button>
              </div>
            </section>

            {activeThread ? (
              <section className="border-t border-border px-4 py-4">
                <div className="mb-3">
                  <p className="text-sm font-medium text-foreground">Active thread</p>
                  <p className="text-xs text-muted-foreground">
                    {activeThread.label} · {threadStatusLabel(activeThread.status)}
                  </p>
                </div>
                <div className="space-y-2">
                  <Button
                    type="button"
                    variant="ghost"
                    className="h-11 w-full justify-start rounded-xl border border-border bg-background px-3"
                    aria-label={isCancellingThis ? "Cancelling turn" : "Cancel turn"}
                    disabled={!canCancel || isCancellingThis}
                    onClick={() => {
                      if (!canCancel) return;
                      onCancelThread(activeThread.id);
                      setActionsOpen(false);
                    }}
                  >
                    {isCancellingThis ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Square className="mr-2 h-4 w-4" />}
                    {isCancellingThis ? "Cancelling…" : "Cancel turn"}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    className="h-11 w-full justify-start rounded-xl border border-border bg-background px-3"
                    aria-label="Fork into new session"
                    onClick={() => {
                      onForkThread(activeThread.id);
                      setActionsOpen(false);
                    }}
                  >
                    <GitBranch className="mr-2 h-4 w-4" />
                    Fork into new session
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    className="h-11 w-full justify-start rounded-xl border border-border bg-background px-3"
                    aria-label="Revert this tab's changes"
                    disabled={!canRevert}
                    onClick={() => {
                      if (!canRevert) return;
                      onRevertThread(activeThread.id);
                      setActionsOpen(false);
                    }}
                  >
                    <Undo2 className="mr-2 h-4 w-4" />
                    Revert this tab&apos;s changes
                  </Button>
                </div>
              </section>
            ) : null}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
