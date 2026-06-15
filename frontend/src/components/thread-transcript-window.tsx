"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  ArrowDown,
  ChevronDown,
  ChevronUp,
  Loader2,
  RefreshCw,
  Terminal,
  Wrench,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { LazyMarkdownContent } from "@/components/lazy-markdown-content";
import { HumanInputRequestCard } from "@/components/human-input-request-card";
import { AttachmentGrid } from "@/components/chat-timeline";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  SessionMessage,
  SessionTranscriptEntry,
  SessionTranscriptTurn,
  SessionTranscriptWindowResponse,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// Distance from scroll bottom below which "Jump to latest" is hidden.
const NEAR_BOTTOM_THRESHOLD_PX = 200;

type TranscriptPageParam = {
  position?: "latest" | "around";
  before?: string;
  after?: string;
  anchorEntryId?: string;
  anchorMessageId?: number;
  anchorTurnNumber?: number;
  limitTurns?: number;
};

// --- Position persistence ---

type StoredTranscriptPosition = {
  version: 3;
  sessionId: string;
  threadId: string;
  anchor: { entryId: string; turnNumber: number };
  savedAt: string;
};

function positionKey(orgId: string, userId: string, sessionId: string, threadId: string): string {
  return `session-transcript-position:${orgId}:${userId}:${sessionId}:${threadId}`;
}

function readPosition(
  orgId: string,
  userId: string,
  sessionId: string,
  threadId: string,
): StoredTranscriptPosition | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(positionKey(orgId, userId, sessionId, threadId));
    if (!raw) return null;
    const p = JSON.parse(raw);
    return p?.version === 3 ? (p as StoredTranscriptPosition) : null;
  } catch {
    return null;
  }
}

function savePosition(orgId: string, userId: string, pos: StoredTranscriptPosition): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(positionKey(orgId, userId, pos.sessionId, pos.threadId), JSON.stringify(pos));
  } catch {
    // localStorage may be full or unavailable.
  }
}

function clearPosition(orgId: string, userId: string, sessionId: string, threadId: string): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(positionKey(orgId, userId, sessionId, threadId));
  } catch {
    // ignore
  }
}

// findTopmostEntry returns the entry ID and turn number of the element nearest
// the top of the scroll container, used to save the reading position.
function findTopmostEntry(
  scroller: HTMLDivElement,
): { entryId: string; turnNumber: number } | null {
  const rect = scroller.getBoundingClientRect();
  const midX = rect.left + rect.width / 2;
  // Sample a point just below the top edge of the visible area.
  const sampleY = rect.top + 8;
  const el = document.elementFromPoint(midX, sampleY)?.closest("[data-entry-id]");
  if (!el) return null;
  const entryId = (el as HTMLElement).dataset.entryId;
  const turnEl = el.closest("[data-turn-number]");
  const turnNumber = turnEl != null ? Number((turnEl as HTMLElement).dataset.turnNumber) : 0;
  return entryId ? { entryId, turnNumber } : null;
}

// --- Component ---

interface ThreadTranscriptWindowProps {
  sessionId: string;
  threadId: string;
  /** Org and user IDs for localStorage key scoping. When omitted, position is not persisted. */
  orgId?: string;
  userId?: string;
  /** Force-open around this anchor instead of restoring the saved position. */
  initialAnchorEntryId?: string;
  initialAnchorMessageId?: number;
  initialAnchorTurnNumber?: number;
  optimisticMessages?: SessionMessage[];
  refetchIntervalMs?: number | false;
  className?: string;
}

export function ThreadTranscriptWindow({
  sessionId,
  threadId,
  orgId,
  userId,
  initialAnchorEntryId,
  initialAnchorMessageId,
  initialAnchorTurnNumber,
  optimisticMessages = [],
  refetchIntervalMs = false,
  className,
}: ThreadTranscriptWindowProps) {
  const canPersist = Boolean(orgId && userId);
  const callerHasAnchor =
    initialAnchorEntryId != null ||
    initialAnchorMessageId != null ||
    initialAnchorTurnNumber != null;

  // initialPageParam is computed once at mount — useState lazy initializer avoids
  // reading localStorage on every render.
  const [initialPageParam] = useState<TranscriptPageParam>(() => {
    if (callerHasAnchor) {
      return {
        position: "around",
        anchorEntryId: initialAnchorEntryId,
        anchorMessageId: initialAnchorMessageId,
        anchorTurnNumber: initialAnchorTurnNumber,
      };
    }
    if (canPersist) {
      const saved = readPosition(orgId!, userId!, sessionId, threadId);
      if (saved) {
        return {
          position: "around",
          anchorEntryId: saved.anchor.entryId,
          anchorTurnNumber: saved.anchor.turnNumber,
        };
      }
    }
    return { position: "latest" };
  });

  const hasAnchor = initialPageParam.position === "around";

  // forceLatestKey increments when the user explicitly jumps to latest while
  // newer pages exist. It changes the query key, causing TanStack Query to
  // start a fresh infinite query from position="latest" rather than re-fetching
  // the current anchor-rooted pages.
  const [forceLatestKey, setForceLatestKey] = useState(0);
  const effectiveInitialPageParam: TranscriptPageParam =
    forceLatestKey > 0 ? { position: "latest" } : initialPageParam;
  const effectiveAnchorKey =
    forceLatestKey > 0
      ? `latest:v${forceLatestKey}`
      : hasAnchor
        ? `around:${initialPageParam.anchorEntryId ?? ""}:${initialPageParam.anchorMessageId ?? ""}:${initialPageParam.anchorTurnNumber ?? ""}`
        : "latest";
  const isAnchorMode = effectiveInitialPageParam.position === "around";

  // --- Refs ---
  const scrollRef = useRef<HTMLDivElement>(null);
  const anchorElementRef = useRef<HTMLDivElement | null>(null);
  const initialScrollDone = useRef(false);
  const prevScrollHeightRef = useRef<number | null>(null);
  const prevTurnCountRef = useRef(0);

  // --- Scroll-position state ---
  const [isNearBottom, setIsNearBottom] = useState(!hasAnchor);
  const [olderError, setOlderError] = useState<Error | null>(null);
  const [newerError, setNewerError] = useState<Error | null>(null);

  // --- Infinite query ---
  const {
    data,
    fetchNextPage,
    fetchPreviousPage,
    hasNextPage,
    hasPreviousPage,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isLoading,
    isError,
    refetch,
  } = useInfiniteQuery<
    SessionTranscriptWindowResponse,
    Error,
    { pages: SessionTranscriptWindowResponse[] },
    ReturnType<typeof queryKeys.sessions.threadTranscript>,
    TranscriptPageParam
  >({
    queryKey: queryKeys.sessions.threadTranscript(sessionId, threadId, effectiveAnchorKey),
    queryFn: ({ pageParam }) =>
      api.sessions.getThreadTranscriptWindow(sessionId, threadId, pageParam),
    initialPageParam: effectiveInitialPageParam,
    getNextPageParam: (lastPage) =>
      lastPage.meta.has_newer && lastPage.meta.next_newer_cursor
        ? { after: lastPage.meta.next_newer_cursor }
        : undefined,
    getPreviousPageParam: (firstPage) =>
      firstPage.meta.has_older && firstPage.meta.next_older_cursor
        ? { before: firstPage.meta.next_older_cursor }
        : undefined,
    refetchInterval: refetchIntervalMs,
  });

  const persistedTurns: SessionTranscriptTurn[] = data?.pages.flatMap((p) => p.data) ?? [];
  const allTurns = appendOptimisticMessagesToTurns(persistedTurns, optimisticMessages);
  const resolvedAnchorEntryId = data?.pages[0]?.meta?.anchor_entry_id;
  const anchorFound = data?.pages[0]?.meta?.anchor_found ?? false;
  const virtualizer = useVirtualizer({
    count: allTurns.length,
    getScrollElement: () => scrollRef.current,
    getItemKey: (index) => allTurns[index]?.turn_number ?? index,
    estimateSize: () => 180,
    overscan: 6,
  });

  // Clear stale saved position when the server could not locate the anchor.
  useEffect(() => {
    if (!anchorFound && isAnchorMode && canPersist) {
      clearPosition(orgId!, userId!, sessionId, threadId);
    }
  }, [anchorFound, isAnchorMode, canPersist, orgId, userId, sessionId, threadId]);

  // --- Initial scroll: bottom for latest, anchor for around ---
  useLayoutEffect(() => {
    if (initialScrollDone.current || allTurns.length === 0) return;
    const scroller = scrollRef.current;
    if (!scroller) return;

    if (!isAnchorMode) {
      scroller.scrollTop = scroller.scrollHeight;
      initialScrollDone.current = true;
    } else if (anchorElementRef.current) {
      anchorElementRef.current.scrollIntoView({ block: "center", behavior: "instant" });
      initialScrollDone.current = true;
    }
  }, [allTurns.length, isAnchorMode]);

  // --- Scroll compensation after prepend ---
  // Save scrollHeight before fetching older; restore the offset after new rows mount.
  const handleLoadOlder = useCallback(async () => {
    const scroller = scrollRef.current;
    if (scroller) prevScrollHeightRef.current = scroller.scrollHeight;
    setOlderError(null);
    try {
      await fetchPreviousPage();
    } catch (err) {
      setOlderError(err as Error);
    }
  }, [fetchPreviousPage]);

  useLayoutEffect(() => {
    if (prevScrollHeightRef.current == null) return;
    const scroller = scrollRef.current;
    if (!scroller) return;
    const delta = scroller.scrollHeight - prevScrollHeightRef.current;
    if (delta > 0) scroller.scrollTop += delta;
    prevScrollHeightRef.current = null;
  }, [allTurns.length]);

  // --- Auto-scroll to bottom when near bottom and new turns arrive ---
  useLayoutEffect(() => {
    const curr = allTurns.length;
    const prev = prevTurnCountRef.current;
    prevTurnCountRef.current = curr;
    // Skip the first load (prev === 0); initial scroll handles that position.
    if (prev === 0 || !initialScrollDone.current || !scrollRef.current) return;
    if (curr > prev && isNearBottom) {
      scrollRef.current.scrollTo({ top: scrollRef.current.scrollHeight });
    }
  }, [allTurns.length, isNearBottom]);

  const handleLoadNewer = useCallback(async () => {
    setNewerError(null);
    try {
      await fetchNextPage();
    } catch (err) {
      setNewerError(err as Error);
    }
  }, [fetchNextPage]);

  // --- Scroll tracking for near-bottom detection ---
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
    setIsNearBottom(dist <= NEAR_BOTTOM_THRESHOLD_PX);
  }, []);

  // --- Save position on unmount ---
  useEffect(() => {
    const scroller = scrollRef.current;
    return () => {
      if (!canPersist) return;
      if (!scroller) return;
      const found = findTopmostEntry(scroller);
      if (!found) return;
      savePosition(orgId!, userId!, {
        version: 3,
        sessionId,
        threadId,
        anchor: found,
        savedAt: new Date().toISOString(),
      });
    };
    // Intentionally exclude all deps: this cleanup should only run once, on unmount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // --- Jump to latest ---
  const jumpToLatest = useCallback(() => {
    if (canPersist) clearPosition(orgId!, userId!, sessionId, threadId);
    if (!hasNextPage) {
      scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
    } else {
      // There are unloaded newer pages. Reset the query to position="latest" by
      // changing the query key; refetch() would re-run the same anchor pages.
      setIsNearBottom(true);
      initialScrollDone.current = false;
      setForceLatestKey((k) => k + 1);
    }
  }, [hasNextPage, canPersist, orgId, userId, sessionId, threadId]);

  const showJumpToLatest = hasNextPage || !isNearBottom;

  // --- Error / loading states ---

  if (isLoading) {
    return (
      <div
        className={cn("flex items-center justify-center", className)}
        aria-busy="true"
        aria-label="Loading transcript"
      >
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className={cn("flex flex-col items-center justify-center gap-3 p-6", className)}>
        <p className="text-sm text-destructive">Failed to load transcript.</p>
        <Button variant="outline" size="sm" onClick={() => void refetch()}>
          <RefreshCw className="mr-2 h-3 w-3" />
          Retry
        </Button>
      </div>
    );
  }

  return (
    <div className={cn("relative flex flex-col overflow-hidden", className)}>
      <div
        ref={scrollRef}
        className="flex flex-col flex-1 overflow-y-auto"
        onScroll={handleScroll}
        role="log"
        aria-label="Thread transcript"
        aria-live="off"
      >
        {/* Older page boundary */}
        {hasPreviousPage && (
          <div className="flex flex-col items-center py-2 gap-1">
            {olderError ? (
              <>
                <p className="text-xs text-destructive">Could not load older messages.</p>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={handleLoadOlder}
                  disabled={isFetchingPreviousPage}
                >
                  <RefreshCw className="mr-1 h-3 w-3" />
                  Retry
                </Button>
              </>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                onClick={handleLoadOlder}
                disabled={isFetchingPreviousPage}
              >
                {isFetchingPreviousPage ? (
                  <Loader2 className="mr-2 h-3 w-3 animate-spin" />
                ) : (
                  <ChevronUp className="mr-2 h-3 w-3" />
                )}
                Load older
              </Button>
            )}
          </div>
        )}

        {/* Turns or empty state */}
        {allTurns.length === 0 ? (
          <div className="flex flex-1 items-center justify-center p-8 text-sm text-muted-foreground">
            Send a message to start.
          </div>
        ) : (
          <div
            className="relative px-4 py-2"
            style={{ height: `${virtualizer.getTotalSize()}px` }}
          >
            {virtualizer.getVirtualItems().map((virtualItem) => {
              const turn = allTurns[virtualItem.index];
              if (!turn) return null;
              return (
                <div
                  key={virtualItem.key}
                  ref={virtualizer.measureElement}
                  data-index={virtualItem.index}
                  className="absolute left-4 right-4 top-0"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <TranscriptTurnBlock
                    turn={turn}
                    anchorEntryId={resolvedAnchorEntryId}
                    anchorElementRef={anchorElementRef}
                  />
                </div>
              );
            })}
          </div>
        )}

        {/* Newer page boundary */}
        {hasNextPage && (
          <div className="flex flex-col items-center py-2 gap-1">
            {newerError ? (
              <>
                <p className="text-xs text-destructive">Could not load newer messages.</p>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={handleLoadNewer}
                  disabled={isFetchingNextPage}
                >
                  <RefreshCw className="mr-1 h-3 w-3" />
                  Retry
                </Button>
              </>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                onClick={handleLoadNewer}
                disabled={isFetchingNextPage}
              >
                {isFetchingNextPage ? (
                  <Loader2 className="mr-2 h-3 w-3 animate-spin" />
                ) : (
                  <ChevronDown className="mr-2 h-3 w-3" />
                )}
                Load newer
              </Button>
            )}
          </div>
        )}
      </div>

      {/* Floating jump-to-latest affordance */}
      {showJumpToLatest && (
        <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 pointer-events-auto">
          <Button size="sm" variant="secondary" className="shadow-md" onClick={jumpToLatest}>
            <ArrowDown className="mr-2 h-3 w-3" />
            Jump to latest
          </Button>
        </div>
      )}
    </div>
  );
}

function TranscriptTurnBlock({
  turn,
  anchorEntryId,
  anchorElementRef,
}: {
  turn: SessionTranscriptTurn;
  anchorEntryId: string | undefined;
  anchorElementRef: React.MutableRefObject<HTMLDivElement | null>;
}) {
  return (
    <div
      role="group"
      aria-label={`Turn ${turn.turn_number}`}
      data-turn-number={turn.turn_number}
      className="flex flex-col gap-2 py-3 border-b border-border/40 last:border-0"
    >
      {turn.entries.map((entry) => (
        <TranscriptEntryRow
          key={entry.id}
          entry={entry}
          isAnchor={entry.id === anchorEntryId}
          anchorElementRef={anchorElementRef}
        />
      ))}
    </div>
  );
}

function appendOptimisticMessagesToTurns(
  turns: SessionTranscriptTurn[],
  optimisticMessages: SessionMessage[],
): SessionTranscriptTurn[] {
  if (optimisticMessages.length === 0) return turns;
  const next = turns.map((turn) => ({ ...turn, entries: [...turn.entries] }));
  for (const message of optimisticMessages) {
    const entry: SessionTranscriptEntry = {
      id: `optimistic_msg_${message.id}`,
      kind: "message",
      created_at: message.created_at,
      message_id: message.id > 0 ? message.id : undefined,
      role: message.role,
      content: message.content,
      message,
    };
    const existing = next.find((turn) => turn.turn_number === message.turn_number);
    if (existing) {
      existing.entries.push(entry);
      continue;
    }
    next.push({
      turn_number: message.turn_number,
      started_at: message.created_at,
      entries: [entry],
    });
  }
  return next.sort((a, b) => a.turn_number - b.turn_number);
}

function TranscriptEntryRow({
  entry,
  isAnchor,
  anchorElementRef,
}: {
  entry: SessionTranscriptEntry;
  isAnchor: boolean;
  anchorElementRef: React.MutableRefObject<HTMLDivElement | null>;
}) {
  const setRef = useCallback(
    (el: HTMLDivElement | null) => {
      if (isAnchor && el) anchorElementRef.current = el;
    },
    [isAnchor, anchorElementRef],
  );

  return (
    <div ref={setRef} data-entry-id={entry.id} className={cn(isAnchor && "scroll-mt-4")}>
      <TranscriptEntryContent entry={entry} />
    </div>
  );
}

function TranscriptEntryContent({ entry }: { entry: SessionTranscriptEntry }) {
  switch (entry.kind) {
    case "message":
      return <TranscriptMessageEntry entry={entry} />;
    case "tool_use":
      return <TranscriptToolEntry entry={entry} variant="use" />;
    case "tool_result":
      return <TranscriptToolEntry entry={entry} variant="result" />;
    case "log":
      return <TranscriptLogEntry entry={entry} />;
    case "human_input":
      return entry.human_input ? (
        <HumanInputRequestCard request={entry.human_input} answerable={false} onAnswer={() => {}} />
      ) : null;
    case "milestone":
    case "checkpoint":
      return <TranscriptMarkerEntry entry={entry} />;
    default:
      return null;
  }
}

const BUBBLE_MAX_WIDTH = "max-w-[92%]";

function TranscriptMessageEntry({ entry }: { entry: SessionTranscriptEntry }) {
  const isUser = entry.role === "user";
  const content = entry.content ?? entry.message?.content ?? "";
  const attachments = entry.message?.attachments ?? [];

  if (isUser) {
    return (
      <div className="flex justify-end">
        <div
          className={cn(
            "rounded-2xl bg-primary text-primary-foreground px-4 py-2 text-sm",
            BUBBLE_MAX_WIDTH,
          )}
        >
          {content && <p className="whitespace-pre-wrap break-words">{content}</p>}
          <AttachmentGrid attachments={attachments} />
        </div>
      </div>
    );
  }

  return (
    <div className={cn(BUBBLE_MAX_WIDTH)}>
      {content && <LazyMarkdownContent content={content} className="text-sm" />}
      <AttachmentGrid attachments={attachments} />
    </div>
  );
}

function TranscriptToolEntry({
  entry,
  variant,
}: {
  entry: SessionTranscriptEntry;
  variant: "use" | "result";
}) {
  const label = entry.tool_name ?? (variant === "use" ? "tool call" : "tool result");

  return (
    <div className="flex items-start gap-2 text-xs text-muted-foreground">
      <Wrench className="h-3 w-3 mt-0.5 shrink-0" aria-hidden="true" />
      <div className="min-w-0">
        <span className="font-mono font-medium text-foreground/70">{label}</span>
        {entry.summary && <span className="ml-2">{entry.summary}</span>}
        {entry.content_truncated && (
          <span className="ml-1 text-muted-foreground/60">(truncated)</span>
        )}
      </div>
    </div>
  );
}

function TranscriptLogEntry({ entry }: { entry: SessionTranscriptEntry }) {
  const level = entry.level ?? entry.log?.level ?? "info";
  const content = entry.content ?? entry.log?.message ?? "";

  const levelClass =
    level === "error"
      ? "text-destructive"
      : level === "warn"
        ? "text-yellow-600 dark:text-yellow-400"
        : level === "output"
          ? "text-foreground"
          : "text-muted-foreground";

  return (
    <div className={cn("flex items-start gap-2 text-xs font-mono", levelClass)}>
      <Terminal className="h-3 w-3 mt-0.5 shrink-0" aria-hidden="true" />
      <pre className="whitespace-pre-wrap break-all min-w-0">
        {content}
        {entry.content_truncated && (
          <span className="text-muted-foreground/60"> (truncated)</span>
        )}
      </pre>
    </div>
  );
}

function TranscriptMarkerEntry({ entry }: { entry: SessionTranscriptEntry }) {
  return (
    <div
      role="separator"
      aria-label={entry.kind}
      className="flex items-center gap-2 text-xs text-muted-foreground/60 py-1"
    >
      <div className="h-px flex-1 bg-border/40" aria-hidden="true" />
      <span className="capitalize">{entry.kind}</span>
      <div className="h-px flex-1 bg-border/40" aria-hidden="true" />
    </div>
  );
}
