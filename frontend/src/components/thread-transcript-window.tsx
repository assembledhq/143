"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  ArrowDown,
  ChevronDown,
  ChevronUp,
  Loader2,
  RefreshCw,
  Search,
  Terminal,
  Wrench,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { LazyMarkdownContent } from "@/components/lazy-markdown-content";
import { HumanInputRequestCard } from "@/components/human-input-request-card";
import { AttachmentGrid } from "@/components/chat-timeline";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  SessionMessage,
  SessionLog,
  SessionTranscriptSearchMatch,
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
  liveLogs?: SessionLog[];
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
  liveLogs = [],
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
  const [openAnchor, setOpenAnchor] = useState<TranscriptPageParam | null>(null);

  const hasAnchor = (openAnchor ?? initialPageParam).position === "around";

  // forceLatestKey increments when the user explicitly jumps to latest while
  // newer pages exist. It changes the query key, causing TanStack Query to
  // start a fresh infinite query from position="latest" rather than re-fetching
  // the current anchor-rooted pages.
  const [forceLatestKey, setForceLatestKey] = useState(0);
  const effectiveInitialPageParam: TranscriptPageParam =
    forceLatestKey > 0 ? { position: "latest" } : openAnchor ?? initialPageParam;
  const effectiveAnchorKey =
    forceLatestKey > 0
      ? `latest:v${forceLatestKey}`
      : hasAnchor
        ? `around:${effectiveInitialPageParam.anchorEntryId ?? ""}:${effectiveInitialPageParam.anchorMessageId ?? ""}:${effectiveInitialPageParam.anchorTurnNumber ?? ""}`
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
  const [searchText, setSearchText] = useState("");
  const [submittedSearch, setSubmittedSearch] = useState("");

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

  const persistedTurns = useMemo<SessionTranscriptTurn[]>(
    () => data?.pages.flatMap((p) => p.data) ?? [],
    [data?.pages],
  );
  const persistedEntryIds = useMemo(() => {
    const ids = new Set<string>();
    for (const turn of persistedTurns) {
      for (const entry of turn.entries) ids.add(entry.id);
    }
    return ids;
  }, [persistedTurns]);
  const allTurns = appendLiveLogsToTurns(
    appendOptimisticMessagesToTurns(persistedTurns, optimisticMessages),
    isNearBottom ? liveLogs : [],
    persistedEntryIds,
  );
  const resolvedAnchorEntryId = data?.pages[0]?.meta?.anchor_entry_id;
  const anchorFound = data?.pages[0]?.meta?.anchor_found ?? false;
  const latestAssistantEntryId = data?.pages[0]?.meta?.latest_assistant_entry_id;
  const liveEdgeEntryId = data?.pages[0]?.meta?.live_edge_entry_id;
  const shouldAnnounceLive = isNearBottom && liveEdgeEntryId === latestRenderedEntryId(allTurns);
  const searchQuery = useQuery({
    queryKey: [...queryKeys.sessions.threadTranscript(sessionId, threadId), "search", submittedSearch],
    queryFn: () => api.sessions.searchThreadTranscript(sessionId, threadId, {
      q: submittedSearch,
      limit: 8,
      include: ["messages", "tools", "human_inputs", "system"],
    }),
    enabled: submittedSearch.trim().length > 0,
  });
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
    setOpenAnchor(null);
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

  const jumpToEntry = useCallback((entryId: string, turnNumber?: number) => {
    if (canPersist) clearPosition(orgId!, userId!, sessionId, threadId);
    const loaded = document.querySelector(`[data-entry-id="${CSS.escape(entryId)}"]`);
    if (loaded instanceof HTMLElement) {
      loaded.scrollIntoView({ block: "center", behavior: "smooth" });
      return;
    }
    initialScrollDone.current = false;
    setIsNearBottom(false);
    setForceLatestKey(0);
    setOpenAnchor({
      position: "around",
      anchorEntryId: entryId,
      anchorTurnNumber: turnNumber,
    });
  }, [canPersist, orgId, userId, sessionId, threadId]);

  const jumpToLatestAssistant = useCallback(() => {
    if (!latestAssistantEntryId) return;
    jumpToEntry(latestAssistantEntryId);
  }, [jumpToEntry, latestAssistantEntryId]);

  const handleSearchSubmit = useCallback((event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmittedSearch(searchText.trim());
  }, [searchText]);

  const handleSearchResult = useCallback((match: SessionTranscriptSearchMatch) => {
    jumpToEntry(match.entry_id, match.turn_number);
  }, [jumpToEntry]);

  const handleKeyDown = useCallback((event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.target instanceof HTMLInputElement) return;
    if (event.key !== "j" && event.key !== "k" && event.key !== "ArrowDown" && event.key !== "ArrowUp") {
      return;
    }
    if (allTurns.length === 0) return;
    event.preventDefault();
    const virtualItems = virtualizer.getVirtualItems();
    const firstVisibleIndex = virtualItems[0]?.index ?? 0;
    const delta = event.key === "j" || event.key === "ArrowDown" ? 1 : -1;
    const nextIndex = Math.min(Math.max(firstVisibleIndex + delta, 0), allTurns.length - 1);
    virtualizer.scrollToIndex(nextIndex, { align: "start" });
  }, [allTurns.length, virtualizer]);

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
      <div className="flex shrink-0 items-start gap-2 border-b border-border px-3 py-2">
        <form className="relative flex min-w-0 flex-1" onSubmit={handleSearchSubmit}>
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={searchText}
            onChange={(event) => setSearchText(event.target.value)}
            placeholder="Search transcript"
            className="h-8 pl-8 pr-8 text-xs"
            aria-label="Search transcript"
          />
          {searchText ? (
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="absolute right-0 top-0 h-8 w-8"
              aria-label="Clear transcript search"
              onClick={() => {
                setSearchText("");
                setSubmittedSearch("");
              }}
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          ) : null}
        </form>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 shrink-0"
          disabled={!latestAssistantEntryId}
          onClick={jumpToLatestAssistant}
        >
          Latest response
        </Button>
      </div>
      {submittedSearch ? (
        <div className="shrink-0 border-b border-border bg-muted/20 px-3 py-2">
          {searchQuery.isLoading ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Searching...
            </div>
          ) : searchQuery.isError ? (
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-destructive">Could not search transcript.</p>
              <Button size="sm" variant="ghost" onClick={() => void searchQuery.refetch()}>
                Retry
              </Button>
            </div>
          ) : (searchQuery.data?.data.length ?? 0) === 0 ? (
            <p className="text-xs text-muted-foreground">No matches</p>
          ) : (
            <div className="flex gap-2 overflow-x-auto">
              {searchQuery.data?.data.map((match) => (
                <Button
                  key={match.entry_id}
                  type="button"
                  variant="secondary"
                  size="sm"
                  className="h-auto max-w-80 shrink-0 justify-start px-2 py-1 text-left"
                  onClick={() => handleSearchResult(match)}
                >
                  <span className="min-w-0">
                    <span className="block text-xs text-muted-foreground">Turn {match.turn_number}</span>
                    <span className="block truncate text-xs">{match.snippet}</span>
                  </span>
                </Button>
              ))}
            </div>
          )}
        </div>
      ) : null}
      <div
        ref={scrollRef}
        className="flex flex-col flex-1 overflow-y-auto"
        onScroll={handleScroll}
        onKeyDown={handleKeyDown}
        tabIndex={0}
        role="log"
        aria-label="Thread transcript"
        aria-live={shouldAnnounceLive ? "polite" : "off"}
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
                    sessionId={sessionId}
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
  sessionId,
  anchorEntryId,
  anchorElementRef,
}: {
  turn: SessionTranscriptTurn;
  sessionId: string;
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
          sessionId={sessionId}
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

function appendLiveLogsToTurns(
  turns: SessionTranscriptTurn[],
  liveLogs: SessionLog[],
  persistedEntryIds: Set<string>,
): SessionTranscriptTurn[] {
  if (liveLogs.length === 0) return turns;
  const next = turns.map((turn) => ({ ...turn, entries: [...turn.entries] }));
  for (const log of liveLogs) {
    const kind = transcriptKindForLog(log);
    const id = transcriptLogEntryId(log, kind);
    if (persistedEntryIds.has(id)) continue;
    const entry: SessionTranscriptEntry = {
      id,
      kind,
      created_at: log.created_at,
      log_id: log.id,
      level: log.level,
      content: kind === "log" ? log.message : undefined,
      summary: kind === "tool_use" || kind === "tool_result" ? oneLineClientSummary(log.message) : undefined,
      tool_name: typeof log.metadata?.tool_name === "string"
        ? log.metadata.tool_name
        : typeof log.metadata?.tool === "string"
          ? log.metadata.tool
          : undefined,
      content_truncated: log.message_truncated,
      content_bytes: log.message_bytes,
      content_chars: log.message_chars,
      log,
    };
    const existing = next.find((turn) => turn.turn_number === log.turn_number);
    if (existing) {
      existing.entries.push(entry);
      continue;
    }
    next.push({
      turn_number: log.turn_number,
      started_at: log.created_at,
      entries: [entry],
    });
  }
  return next
    .map((turn) => ({
      ...turn,
      entries: turn.entries.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime()),
    }))
    .sort((a, b) => a.turn_number - b.turn_number);
}

function transcriptKindForLog(log: SessionLog): SessionTranscriptEntry["kind"] {
  if (log.level === "tool_use") return "tool_use";
  if (log.metadata?.type === "tool_result") return "tool_result";
  return "log";
}

function transcriptLogEntryId(log: SessionLog, kind: SessionTranscriptEntry["kind"]): string {
  if (kind === "tool_use") return `tuse_${log.id}`;
  if (kind === "tool_result") return `tres_${log.id}`;
  return `log_${log.id}`;
}

function latestRenderedEntryId(turns: SessionTranscriptTurn[]): string | undefined {
  const latestTurn = turns.at(-1);
  return latestTurn?.entries.at(-1)?.id;
}

function oneLineClientSummary(message: string): string {
  const firstLine = message.replaceAll("\r\n", "\n").split("\n")[0]?.trim() ?? "";
  return firstLine.length > 160 ? `${firstLine.slice(0, 160)}...` : firstLine;
}

function TranscriptEntryRow({
  entry,
  sessionId,
  isAnchor,
  anchorElementRef,
}: {
  entry: SessionTranscriptEntry;
  sessionId: string;
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
      <TranscriptEntryContent entry={entry} sessionId={sessionId} />
    </div>
  );
}

function TranscriptEntryContent({ entry, sessionId }: { entry: SessionTranscriptEntry; sessionId: string }) {
  switch (entry.kind) {
    case "message":
      return <TranscriptMessageEntry entry={entry} />;
    case "tool_use":
      return <TranscriptToolEntry entry={entry} sessionId={sessionId} variant="use" />;
    case "tool_result":
      return <TranscriptToolEntry entry={entry} sessionId={sessionId} variant="result" />;
    case "log":
      return <TranscriptLogEntry entry={entry} sessionId={sessionId} />;
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
  sessionId,
  variant,
}: {
  entry: SessionTranscriptEntry;
  sessionId: string;
  variant: "use" | "result";
}) {
  const label = entry.tool_name ?? (variant === "use" ? "tool call" : "tool result");
  const expanded = useExpandableLogContent(sessionId, entry);

  return (
    <div className="flex items-start gap-2 text-xs text-muted-foreground">
      <Wrench className="h-3 w-3 mt-0.5 shrink-0" aria-hidden="true" />
      <div className="min-w-0">
        <span className="font-mono font-medium text-foreground/70">{label}</span>
        {entry.summary && <span className="ml-2">{entry.summary}</span>}
        <ExpandableLogToggle expanded={expanded} />
        {expanded.expanded && expanded.content ? (
          <pre className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-muted/30 p-2 text-xs text-foreground">
            {expanded.content}
          </pre>
        ) : null}
      </div>
    </div>
  );
}

function TranscriptLogEntry({ entry, sessionId }: { entry: SessionTranscriptEntry; sessionId: string }) {
  const level = entry.level ?? entry.log?.level ?? "info";
  const expanded = useExpandableLogContent(sessionId, entry);
  const content = expanded.content ?? entry.content ?? entry.log?.message ?? "";

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
      <div className="min-w-0">
        <pre className="whitespace-pre-wrap break-all">
          {content}
        </pre>
        <ExpandableLogToggle expanded={expanded} />
      </div>
    </div>
  );
}

function useExpandableLogContent(sessionId: string, entry: SessionTranscriptEntry) {
  const [expanded, setExpanded] = useState(false);
  const logId = entry.log_id;
  const detailQuery = useQuery({
    queryKey: logId ? queryKeys.sessions.logDetail(sessionId, logId) : ["session", sessionId, "logs", "none"],
    queryFn: () => api.sessions.getLogDetail(sessionId, logId!),
    enabled: expanded && Boolean(logId) && Boolean(entry.content_truncated),
  });
  return {
    expanded,
    setExpanded,
    canExpand: Boolean(entry.content_truncated && logId),
    isLoading: detailQuery.isFetching,
    isError: detailQuery.isError,
    content: expanded
      ? detailQuery.data?.data.message ?? entry.content ?? entry.log?.message
      : entry.content ?? entry.log?.message,
  };
}

function ExpandableLogToggle({
  expanded,
}: {
  expanded: ReturnType<typeof useExpandableLogContent>;
}) {
  if (!expanded.canExpand) return null;
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="ml-1 h-6 px-1.5 text-xs"
      onClick={() => expanded.setExpanded(!expanded.expanded)}
      aria-expanded={expanded.expanded}
    >
      {expanded.isLoading ? "Loading..." : expanded.expanded ? "Collapse" : "Expand"}
    </Button>
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
