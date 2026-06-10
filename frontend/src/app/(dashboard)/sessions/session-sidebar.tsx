"use client";

import { useMutation, useQuery, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { notify as toast } from "@/lib/notify";
import { Archive, ArchiveRestore, Plus, Search } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter, useSelectedLayoutSegment } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState, type FocusEventHandler, type KeyboardEvent as ReactKeyboardEvent, type MouseEventHandler, type ReactNode } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { PeopleFilter } from "@/components/people-filter";
import { cn, formatTimeAgo, sessionTitle } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import { AnimatedEllipsis } from "@/components/animated-ellipsis";
import { SwipeActionRow } from "@/components/swipe-action-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Kbd } from "@/components/ui/kbd";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { useFilterSuffix, usePeopleFilter } from "@/hooks/use-people-filter";
import { queryKeys } from "@/lib/query-keys";
import { useOptimisticSessions, type OptimisticSession } from "@/contexts/optimistic-sessions";
import { DiffStatsBadge } from "@/components/code-review/diff-stats-badge";
import { SessionLinearBadge as SharedSessionLinearBadge } from "@/components/session-linear-badge";
import { NoReposWarning } from "@/components/no-repos-warning";
import type { ListResponse, SessionCounts, SessionDetail, SessionListItem, SingleResponse, User } from "@/lib/types";
import { prMergedAccent } from "@/lib/pr-status-styles";
import { provisionalSessionDetailFromListItem } from "@/lib/session-detail-cache";
import { hasSessionKeyboardTransientSurface, isSessionKeyboardTextEntryTarget } from "@/hooks/use-session-keyboard-shortcuts";
import {
  workingSet,
  filterToStatusParam,
} from "@/lib/session-status-groups";
import { getCountForTab, renderCount } from "@/lib/session-counts";

// ---------------------------------------------------------------------------
// Status config
// ---------------------------------------------------------------------------

const statusConfig: Record<string, { dot: string; label: string }> = {
  pending: { dot: "bg-muted-foreground/50", label: "Pending" },
  running: { dot: "bg-primary", label: "Running" },
  idle: { dot: "bg-primary", label: "Idle" },
  awaiting_input: { dot: "bg-warning", label: "Awaiting input" },
  needs_human_guidance: { dot: "bg-attention", label: "Needs guidance" },
  completed: { dot: "bg-success", label: "Completed" },
  pr_created: { dot: prMergedAccent.dot, label: "PR created" },
  failed: { dot: "bg-destructive", label: "Failed" },
  cancelled: { dot: "bg-muted-foreground/50", label: "Cancelled" },
  skipped: { dot: "bg-muted-foreground/30", label: "Skipped" },
};

const filterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "archived", label: "Archived" },
];

const newSessionOptionId = "new-session";
const sessionSidebarOptionFrameClass = "flex min-w-0 rounded-xl border border-transparent p-1 transition-all duration-150";
const sessionSidebarLinkSurfaceClass = "relative block min-w-0 flex-1 overflow-hidden rounded-lg border border-border/50 bg-card px-3 py-2.5 shadow-sm transition-all duration-150 md:border-transparent md:bg-transparent md:shadow-none";

function SessionSidebarOptionFrame({
  id,
  ariaLabel,
  ariaSelected,
  className,
  children,
  optionRef,
  onClick,
  onMouseDown,
}: {
  id: string;
  ariaLabel?: string;
  ariaSelected: boolean;
  className?: string;
  children: ReactNode;
  optionRef?: (node: HTMLDivElement | null) => void;
  onClick?: MouseEventHandler<HTMLDivElement>;
  onMouseDown?: MouseEventHandler<HTMLDivElement>;
}) {
  return (
    <div
      ref={optionRef}
      id={id}
      role="option"
      aria-label={ariaLabel}
      aria-selected={ariaSelected}
      data-active={ariaSelected ? "true" : undefined}
      className={cn(sessionSidebarOptionFrameClass, className)}
      onClick={onClick}
      onMouseDown={onMouseDown}
    >
      {children}
    </div>
  );
}

function SessionSidebarRowSurface({
  href,
  ariaCurrent,
  className,
  onClick,
  onFocus,
  onMouseEnter,
  onMouseDown,
  children,
}: {
  href?: string;
  ariaCurrent?: "page";
  className?: string;
  onClick?: MouseEventHandler<HTMLAnchorElement>;
  onFocus?: FocusEventHandler<HTMLAnchorElement>;
  onMouseEnter?: MouseEventHandler<HTMLAnchorElement>;
  onMouseDown?: MouseEventHandler<HTMLAnchorElement>;
  children: ReactNode;
}) {
  const surfaceClassName = cn(sessionSidebarLinkSurfaceClass, className);
  if (!href) {
    return (
      <div
        data-session-row-surface="true"
        className={surfaceClassName}
      >
        {children}
      </div>
    );
  }

  return (
    <Link
      href={href}
      aria-current={ariaCurrent}
      data-session-row-surface="true"
      className={surfaceClassName}
      onClick={onClick}
      onFocus={onFocus}
      onMouseEnter={onMouseEnter}
      onMouseDown={onMouseDown}
    >
      {children}
    </Link>
  );
}

// ---------------------------------------------------------------------------
// Unread indicator logic
// ---------------------------------------------------------------------------

/** Returns true if the session has activity the current user hasn't seen yet. */
function isUnread(session: SessionListItem): boolean {
  // Sessions that are actively working are always "live" — show an animated dot instead.
  if (workingSet.has(session.status)) return false;

  const lastActivity = session.last_activity_at;
  if (!lastActivity) return false;

  // Never viewed → unread if there's been any activity.
  if (!session.last_viewed_at) return true;

  return new Date(lastActivity) > new Date(session.last_viewed_at);
}

// ---------------------------------------------------------------------------
// PR status badge for sidebar rows
// ---------------------------------------------------------------------------

function PRStatusBadge({ prSummary }: { prSummary?: SessionListItem["pr_summary"] }) {
  if (!prSummary) return null;

  let dotColor: string;
  let label: string;

  if (prSummary.status === "merged") {
    dotColor = prMergedAccent.dot;
    label = "Merged";
  } else if (prSummary.status === "closed") {
    dotColor = "bg-muted-foreground";
    label = "Closed";
  } else if (prSummary.ci_status === "success") {
    dotColor = "bg-success";
    label = "CI passed";
  } else if (prSummary.ci_status === "failure") {
    dotColor = "bg-destructive";
    label = "CI failed";
  } else {
    // pending / unknown CI status
    dotColor = "bg-muted-foreground/40";
    label = "CI pending";
  }

  return (
    <span className="inline-flex items-center gap-1 shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5" title={label}>
      <span className={cn("inline-flex rounded-full h-1.5 w-1.5", dotColor)} />
      <span className="text-xs text-muted-foreground">PR</span>
    </span>
  );
}

// ---------------------------------------------------------------------------
// Lightweight diff stats badge for sidebar rows
// ---------------------------------------------------------------------------

function SessionDiffBadge({ diffStats }: { diffStats?: { added: number; removed: number; files_changed: number } }) {
  if (!diffStats) return null;
  if (diffStats.added === 0 && diffStats.removed === 0) return null;
  return (
    <span className="inline-flex shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5">
      <DiffStatsBadge added={diffStats.added} removed={diffStats.removed} className="text-xs" />
    </span>
  );
}

function SessionLinearBadge({ session }: { session: SessionListItem }) {
  const linearLabel =
    session.linear_identifier_hint ??
    session.linked_issues?.find((issue) => issue.issue_source === "linear")?.external_id;
  return <SharedSessionLinearBadge label={linearLabel} />;
}

// ---------------------------------------------------------------------------
// Optimistic (unsaved) session row
// ---------------------------------------------------------------------------

function OptimisticSessionRow({ session }: { session: OptimisticSession }) {
  const cfg = statusConfig.pending;
  return (
    <SessionSidebarOptionFrame
      id={`session-sidebar-option-${session.id}`}
      ariaLabel={session.title}
      ariaSelected={false}
      className="mb-0.5"
    >
      <SessionSidebarRowSurface>
        <div className="flex items-start gap-2.5 min-w-0">
          <div className="mt-1.5 shrink-0">
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
            </span>
          </div>
          <div className="min-w-0 flex-1">
            <p className="text-xs font-medium text-foreground truncate leading-snug">
              {session.title}
            </p>
            <div className="flex items-center gap-3 mt-0.5">
              <span className="text-xs text-muted-foreground shrink-0">
                <span>{cfg.label}</span>
                <AnimatedEllipsis />
              </span>
              <span className="text-xs text-muted-foreground/50">just now</span>
            </div>
          </div>
        </div>
      </SessionSidebarRowSurface>
    </SessionSidebarOptionFrame>
  );
}

function CurrentSessionContextRow({
  session,
  href,
  ariaSelected,
  optionRef,
}: {
  session: SessionDetail;
  href: string;
  ariaSelected: boolean;
  optionRef?: (node: HTMLDivElement | null) => void;
}) {
  const cfg = statusConfig[session.status] || statusConfig.pending;
  const isWorkingSession = workingSet.has(session.status);
  const ts = session.completed_at || session.started_at || session.created_at;
  const title = sessionTitle(session);

  return (
    <SessionSidebarOptionFrame
      id={`session-sidebar-option-${session.id}`}
      ariaLabel={title}
      ariaSelected={ariaSelected}
      optionRef={optionRef}
      className="border-primary/25 bg-card shadow-sm ring-1 ring-primary/10"
    >
      <SessionSidebarRowSurface
        href={href}
        ariaCurrent="page"
        className="border-transparent bg-primary/5 shadow-none ring-0 md:border-transparent md:bg-primary/5 md:shadow-none"
      >
        <span
          aria-hidden="true"
          className="absolute inset-y-2 left-1 w-0.5 rounded-full bg-primary transition-colors duration-150"
        />
        <div className="flex items-start gap-2.5 min-w-0">
          <div className="mt-1.5 shrink-0">
            {isWorkingSession ? (
              <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />
            ) : (
              <span className="inline-flex rounded-full h-2 w-2 bg-primary/55" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-start justify-between gap-2">
              <p className="text-xs font-medium text-foreground truncate leading-snug">
                {title}
              </p>
              <span className="shrink-0 rounded-md border border-primary/20 bg-card/70 px-1.5 py-0.5 text-xs font-medium text-primary">
                Current
              </span>
            </div>
            <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
              <span className="text-xs text-muted-foreground shrink-0">
                <span>{cfg.label}</span>
                {isWorkingSession && <AnimatedEllipsis />}
              </span>
              <span className="text-xs text-muted-foreground/50 shrink-0">
                {formatTimeAgo(ts)}
              </span>
              <span className="text-xs text-muted-foreground/70 shrink-0">
                Not in this list
              </span>
            </div>
          </div>
        </div>
      </SessionSidebarRowSurface>
    </SessionSidebarOptionFrame>
  );
}

// ---------------------------------------------------------------------------
// Sidebar component
// ---------------------------------------------------------------------------

export function SessionSidebar() {
  const router = useRouter();
  const pathname = usePathname();
  const selectedSegment = useSelectedLayoutSegment();
  const queryClient = useQueryClient();
  const {
    mode,
    selectedUserIDs,
    scopedUserIDs,
    serializedPeopleParam,
    currentUser,
    isResolved,
    setPeopleFilter,
  } = usePeopleFilter();
  const canListTeamMembers = currentUser?.role === "admin" || currentUser?.role === "member";
  const selectedId = selectedSegment && selectedSegment !== "new" ? selectedSegment : undefined;
  const [searchParam, setSearchParam] = useQueryState("search", parseAsString);
  const [search, setSearch] = useState(searchParam ?? "");
  const searchInputRef = useRef<HTMLInputElement>(null);
  const listContainerRef = useRef<HTMLDivElement>(null);
  const optionRefs = useRef(new Map<string, HTMLDivElement>());
  const [activeSessionFocus, setActiveSessionFocus] = useState<{ id: string; pathname: string } | null>(null);
  const searchRef = useRef(search);
  const skipNextSearchParamWriteRef = useRef(false);
  // Debounce the search query so rapid typing doesn't fire a request per
  // keystroke. useDeferredValue only lowers render priority — it does not
  // throttle network calls.
  const [debouncedSearch, setDebouncedSearch] = useState("");
  useEffect(() => {
    const handle = setTimeout(() => setDebouncedSearch(search.trim()), 200);
    return () => clearTimeout(handle);
  }, [search]);
  useEffect(() => {
    searchRef.current = search;
  }, [search]);
  useEffect(() => {
    const nextSearch = searchParam ?? "";
    if (searchRef.current === nextSearch) {
      return;
    }
    // Sync the local input from the URL when navigation/back/forward changes
    // the search param outside this component.
    // When that external navigation cleared/changed the param, the URL is the
    // source of truth. Skip the next write-back so we do not restore stale
    // input text into the URL and thrash the router state.
    skipNextSearchParamWriteRef.current = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSearch(nextSearch);
  }, [searchParam]);
  useEffect(() => {
    const currentParam = searchParam ?? "";
    if (search === currentParam) {
      skipNextSearchParamWriteRef.current = false;
      return;
    }
    if (skipNextSearchParamWriteRef.current) {
      skipNextSearchParamWriteRef.current = false;
      return;
    }
    void setSearchParam(search || null);
  }, [search, searchParam, setSearchParam]);

  const [activeFilter, setActiveFilter] = useQueryState("status", parseAsString);
  const [repo] = useQueryState("repo");
  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: canListTeamMembers,
  });
  const members = useMemo<User[]>(() => membersData?.data ?? [], [membersData?.data]);

  const { optimisticSessions, removeOptimisticSession } = useOptimisticSessions();

  const currentFilter = activeFilter ?? "all";
  const isArchivedView = currentFilter === "archived";
  const statusParam = filterToStatusParam(currentFilter);

  // Pagination state. Once the user clicks "Show more" we stop polling entirely
  // (page 0 included) — refreshing page 0 while extra pages are held locally
  // would invalidate the cursor that produced them. Pages re-hydrate on scope
  // change via the scopeKey reset below. See automations/[id]/page.tsx for the
  // same pattern.
  const [extraPages, setExtraPages] = useState<SessionListItem[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>(undefined);
  const isPaginated = extraPages.length > 0;

  const trimmedSearch = debouncedSearch;

  // Pause list polling while the pointer is over the list. A poll response can
  // reorder rows mid-click, which either swallows the click (mousedown/mouseup
  // land on different nodes) or moves a different session under the cursor.
  const [isListHovered, setIsListHovered] = useState(false);

  // Reset pagination when the effective query scope changes. Adjusting state
  // during render (rather than in an effect) avoids cascading renders — see
  // https://react.dev/reference/react/useState#storing-information-from-previous-renders.
  const scopeKey = `${repo ?? ""}|${serializedPeopleParam ?? "mine"}|${currentFilter}|${trimmedSearch}`;
  const [prevScopeKey, setPrevScopeKey] = useState(scopeKey);
  if (prevScopeKey !== scopeKey) {
    setPrevScopeKey(scopeKey);
    setExtraPages([]);
    setLoadMoreCursor(undefined);
  }
  const listParams = useMemo(
    () => ({
      limit: 50,
      repository_id: repo ?? undefined,
      triggered_by_user_ids: scopedUserIDs,
      search: trimmedSearch || undefined,
      ...(isArchivedView ? { only_archived: true } : {}),
      ...(!isArchivedView && statusParam ? { status: statusParam } : {}),
    }),
    [repo, scopedUserIDs, trimmedSearch, isArchivedView, statusParam],
  );

  const { data: listData, isLoading } = useQuery({
    queryKey: [...queryKeys.sessions.list(repo), "filtered", currentFilter, serializedPeopleParam, trimmedSearch],
    queryFn: () => api.sessions.list(listParams),
    enabled: isResolved,
    refetchInterval: isPaginated || isListHovered ? false : 10000,
  });

  // Tab badge counts. Search-independent so tabs reflect the scope totals, not
  // the current search result size.
  const { data: countsData } = useQuery({
    queryKey: queryKeys.sessions.counts(repo, serializedPeopleParam),
    queryFn: () =>
      api.sessions.counts({
        repository_id: repo ?? undefined,
        triggered_by_user_ids: scopedUserIDs,
      }),
    enabled: isResolved,
    refetchInterval: 10000,
  });

  const firstPage = useMemo(() => listData?.data ?? [], [listData?.data]);
  const firstPageCursor = listData?.meta?.next_cursor || undefined;
  const cursor = isPaginated ? loadMoreCursor : firstPageCursor;
  const hasMore = !!cursor;

  const loadMoreMutation = useMutation({
    mutationFn: () => api.sessions.list({ ...listParams, cursor }),
    onSuccess: (res) => {
      setExtraPages((prev) => [...prev, res.data ?? []]);
      setLoadMoreCursor(res.meta?.next_cursor || undefined);
    },
  });

  const invalidateSessions = () => {
    queryClient.invalidateQueries({ queryKey: queryKeys.sessions.all });
  };

  const removeSessionFromCachedLists = (sessionId: string) => {
    queryClient.setQueriesData<ListResponse<SessionListItem>>(
      { queryKey: queryKeys.sessions.all },
      (current) => {
        if (!current || !Array.isArray(current.data)) {
          return current;
        }
        const nextData = current.data.filter((session) => session.id !== sessionId);
        if (nextData.length === current.data.length) {
          return current;
        }
        return { ...current, data: nextData };
      },
    );
  };

  const removeSessionFromExtraPages = (sessionId: string) => {
    setExtraPages((pages) =>
      pages.map((page) => page.filter((session) => session.id !== sessionId)),
    );
  };

  const updateCappedCount = (current: number, delta: number, cap: number) => {
    if (current >= cap) {
      return cap;
    }
    return Math.max(0, current + delta);
  };

  const updateCachedSessionCounts = (session: SessionListItem, direction: "archive" | "unarchive") => {
    queryClient.setQueryData<SingleResponse<SessionCounts>>(
      queryKeys.sessions.counts(repo, serializedPeopleParam),
      (current) => {
        if (!current?.data) return current;
        const activeDelta = workingSet.has(session.status) ? 1 : 0;
        const archivedDelta = direction === "archive" ? 1 : -1;
        const visibleDelta = direction === "archive" ? -1 : 1;
        const activeVisibleDelta = direction === "archive" ? -activeDelta : activeDelta;
        return {
          ...current,
          data: {
            ...current.data,
            all: updateCappedCount(current.data.all, visibleDelta, current.data.cap),
            active: updateCappedCount(current.data.active, activeVisibleDelta, current.data.cap),
            archived: updateCappedCount(current.data.archived, archivedDelta, current.data.cap),
          },
        };
      },
    );
  };

  type ArchiveMutationContext = {
    lists: Array<[QueryKey, ListResponse<SessionListItem> | undefined]>;
    counts: Array<[QueryKey, SingleResponse<SessionCounts> | undefined]>;
    extraPages: SessionListItem[][];
  };

  const snapshotSessionCaches = (): ArchiveMutationContext => ({
    lists: queryClient.getQueriesData<ListResponse<SessionListItem>>({ queryKey: queryKeys.sessions.all })
      .filter(([, current]) => Array.isArray(current?.data)),
    counts: queryClient.getQueriesData<SingleResponse<SessionCounts>>({ queryKey: queryKeys.sessions.counts(repo, serializedPeopleParam) })
      .filter(([, value]) => value !== undefined),
    extraPages,
  });

  const restoreSessionCaches = (snapshot?: ArchiveMutationContext) => {
    if (!snapshot) return;
    for (const [key, value] of snapshot.lists) {
      queryClient.setQueryData(key, value);
    }
    for (const [key, value] of snapshot.counts) {
      if (value !== undefined) {
        queryClient.setQueryData(key, value);
      }
    }
    setExtraPages(snapshot.extraPages);
  };

  const archiveMutation = useMutation({
    mutationFn: (session: SessionListItem) => api.sessions.archive(session.id),
    onMutate: async (session) => {
      await queryClient.cancelQueries({ queryKey: queryKeys.sessions.all });
      const snapshot = snapshotSessionCaches();
      removeSessionFromCachedLists(session.id);
      removeSessionFromExtraPages(session.id);
      updateCachedSessionCounts(session, "archive");
      return snapshot;
    },
    onSuccess: () => {
      invalidateSessions();
    },
    onError: (_error, _session, snapshot) => {
      restoreSessionCaches(snapshot);
      invalidateSessions();
      toast.error("Failed to archive session");
    },
  });

  const unarchiveMutation = useMutation({
    mutationFn: (session: SessionListItem) => api.sessions.unarchive(session.id),
    onMutate: async (session) => {
      await queryClient.cancelQueries({ queryKey: queryKeys.sessions.all });
      const snapshot = snapshotSessionCaches();
      removeSessionFromCachedLists(session.id);
      removeSessionFromExtraPages(session.id);
      updateCachedSessionCounts(session, "unarchive");
      return snapshot;
    },
    onSuccess: () => {
      invalidateSessions();
    },
    onError: (_error, _session, snapshot) => {
      restoreSessionCaches(snapshot);
      invalidateSessions();
      toast.error("Failed to unarchive session");
    },
  });

  const displayedSessions = useMemo(() => {
    const merged = [firstPage, ...extraPages].flat();
    const q = search.trim().toLowerCase();
    if (!q) return merged;
    return merged.filter((s) => sessionTitle(s).toLowerCase().includes(q));
  }, [firstPage, extraPages, search]);

  const isNewSession = pathname === "/sessions/new";
  const selectedSessionIsDisplayed = !!selectedId && displayedSessions.some((session) => session.id === selectedId);
  const shouldShowCurrentSessionContextRow = !!selectedId && !isNewSession && !selectedSessionIsDisplayed;
  const { data: currentSessionData } = useQuery({
    queryKey: queryKeys.sessions.detail(selectedId ?? ""),
    queryFn: () => api.sessions.get(selectedId!),
    enabled: isResolved && shouldShowCurrentSessionContextRow,
    retry: false,
  });
  const currentSessionContext = shouldShowCurrentSessionContextRow ? currentSessionData?.data : undefined;
  const activeSessionId = activeSessionFocus?.id ?? null;
  const hasNavigatedFromNewSessionDraft = isNewSession && activeSessionFocus?.pathname === pathname;

  const currentActiveSessionId = useMemo(() => {
    if (displayedSessions.length === 0) {
      return null;
    }
    if (isNewSession && !hasNavigatedFromNewSessionDraft) {
      return null;
    }
    if (activeSessionId && displayedSessions.some((session) => session.id === activeSessionId)) {
      return activeSessionId;
    }
    if (selectedId) {
      if (!selectedSessionIsDisplayed) {
        return null;
      }
      return selectedId;
    }
    return displayedSessions[0]?.id ?? null;
  }, [activeSessionId, displayedSessions, hasNavigatedFromNewSessionDraft, isNewSession, selectedId, selectedSessionIsDisplayed]);
  // Hide optimistic rows whose real session is already in the list — prevents
  // the double-render flash between "optimistic added" and "server refetch
  // lands". Resolved-but-not-yet-visible rows stay until the real row arrives.
  const realIds = useMemo(() => new Set(displayedSessions.map((s) => s.id)), [displayedSessions]);
  const visibleOptimistic = useMemo(
    () => optimisticSessions.filter((os) => !(os.resolvedId && realIds.has(os.resolvedId))),
    [optimisticSessions, realIds],
  );

  // Garbage-collect resolved optimistic rows once we've observed their real
  // counterpart in the list. Done in an effect so state updates happen after
  // render. This also handles the failure case: if the real session later
  // changes status (e.g. to "failed"), it's still in the list, so the
  // optimistic stays hidden and gets cleaned up here.
  useEffect(() => {
    for (const os of optimisticSessions) {
      if (os.resolvedId && realIds.has(os.resolvedId)) {
        removeOptimisticSession(os.id);
      }
    }
  }, [optimisticSessions, realIds, removeOptimisticSession]);

  const counts = countsData?.data;

  // Carry the sidebar's filters into detail-page links so opening a session
  // doesn't reset the scope back to "Mine".
  const filterSuffix = useFilterSuffix(serializedPeopleParam, activeFilter, repo, search || null);

  const showDefaultEmptyState =
    currentFilter === "all" && !trimmedSearch && (!counts || counts.all === 0);
  const activeOptionId = currentSessionContext && !currentActiveSessionId
    ? currentSessionContext.id
    : isNewSession && !currentActiveSessionId
    ? newSessionOptionId
    : currentActiveSessionId;

  const focusSearch = useCallback(() => {
    const input = searchInputRef.current;
    if (!input) return;
    input.focus();
    input.select();
  }, []);

  const focusList = useCallback(() => {
    listContainerRef.current?.focus({ preventScroll: true });
  }, []);

  const setActiveByIndex = useCallback((index: number) => {
    if (displayedSessions.length === 0) return;
    const boundedIndex = Math.min(Math.max(index, 0), displayedSessions.length - 1);
    const next = displayedSessions[boundedIndex];
    if (!next) return;
    setActiveSessionFocus({ id: next.id, pathname });
    focusList();
    requestAnimationFrame(() => {
      optionRefs.current.get(next.id)?.scrollIntoView({ block: "nearest" });
    });
  }, [displayedSessions, focusList, pathname]);

  const moveActiveSession = useCallback((delta: number) => {
    if (displayedSessions.length === 0) return;
    if (isNewSession && !hasNavigatedFromNewSessionDraft) {
      setActiveByIndex(delta >= 0 ? 0 : displayedSessions.length - 1);
      return;
    }
    // Context row acts as a virtual slot above the saved-session list.
    if (currentSessionContext && !currentActiveSessionId) {
      if (delta >= 0) setActiveByIndex(0);
      // Upward movement from context row: nothing above it, stay put.
      return;
    }
    const currentIndex = currentActiveSessionId
      ? displayedSessions.findIndex((session) => session.id === currentActiveSessionId)
      : -1;
    const baseIndex = currentIndex >= 0 ? currentIndex : 0;
    const nextIndex = baseIndex + delta;
    // Upward movement past the first saved session when context row exists:
    // return keyboard focus to the context row.
    if (currentSessionContext && nextIndex < 0) {
      setActiveSessionFocus(null);
      focusList();
      requestAnimationFrame(() => {
        optionRefs.current.get(currentSessionContext.id)?.scrollIntoView({ block: "nearest" });
      });
      return;
    }
    setActiveByIndex(nextIndex);
  }, [currentActiveSessionId, currentSessionContext, displayedSessions, focusList, hasNavigatedFromNewSessionDraft, isNewSession, setActiveByIndex]);

  const activeSession = useMemo(
    () => displayedSessions.find((session) => session.id === currentActiveSessionId) ?? null,
    [currentActiveSessionId, displayedSessions],
  );

  const seedSessionDetailCache = useCallback((session: SessionListItem) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(
      queryKeys.sessions.detail(session.id),
      (current) => current ?? provisionalSessionDetailFromListItem(session),
    );
  }, [queryClient]);

  useEffect(() => {
    for (const session of displayedSessions) {
      seedSessionDetailCache(session);
    }
  }, [displayedSessions, seedSessionDetailCache]);

  const navigateToSession = useCallback((session: SessionListItem, href: string) => {
    seedSessionDetailCache(session);
    router.push(href);
  }, [router, seedSessionDetailCache]);

  const handleSessionLinkClick = useCallback((session: SessionListItem) => {
    seedSessionDetailCache(session);
  }, [seedSessionDetailCache]);

  const openActiveSession = useCallback(() => {
    if (!activeSession) return;
    const sessionHref = `/sessions/${activeSession.id}${filterSuffix}`;
    navigateToSession(activeSession, sessionHref);
  }, [activeSession, filterSuffix, navigateToSession]);

  const prefetchRoute = useCallback((href: string) => {
    router.prefetch(href);
  }, [router]);

  const toggleArchiveActiveSession = useCallback(() => {
    if (!activeSession) return;
    if (activeSession.archived_at) {
      unarchiveMutation.mutate(activeSession);
      return;
    }
    archiveMutation.mutate(activeSession);
  }, [activeSession, archiveMutation, unarchiveMutation]);

  const handleListKeyDown = useCallback((event: ReactKeyboardEvent<HTMLDivElement>) => {
    switch (event.key) {
      case "ArrowDown":
      case "j":
        event.preventDefault();
        moveActiveSession(1);
        break;
      case "ArrowUp":
      case "k":
        event.preventDefault();
        moveActiveSession(-1);
        break;
      case "Home":
        event.preventDefault();
        setActiveByIndex(0);
        break;
      case "End":
        event.preventDefault();
        setActiveByIndex(displayedSessions.length - 1);
        break;
      case "PageDown":
        event.preventDefault();
        moveActiveSession(8);
        if (hasMore && displayedSessions.length > 0) {
          const currentIndex = currentActiveSessionId
            ? displayedSessions.findIndex((session) => session.id === currentActiveSessionId)
            : 0;
          if (currentIndex >= displayedSessions.length - 8) {
            loadMoreMutation.mutate();
          }
        }
        break;
      case "PageUp":
        event.preventDefault();
        moveActiveSession(-8);
        break;
      case "Enter":
        event.preventDefault();
        openActiveSession();
        break;
      case "A":
        // Shift+A archives — `a` alone is too easy to fire accidentally on
        // the highlighted row.
        if (!event.shiftKey) return;
        event.preventDefault();
        toggleArchiveActiveSession();
        break;
    }
  }, [currentActiveSessionId, displayedSessions, hasMore, loadMoreMutation, moveActiveSession, openActiveSession, setActiveByIndex, toggleArchiveActiveSession]);

  useEffect(() => {
    function handleDocumentKeyDown(event: KeyboardEvent) {
      if (isSessionKeyboardTextEntryTarget(event.target) || hasSessionKeyboardTransientSurface()) {
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      // The list container has its own keydown handler for j/k/Enter/a; skip
      // those here to avoid the React-delegated handler and this document
      // listener both firing on the same keystroke.
      const list = listContainerRef.current;
      if (list && event.target instanceof Node && list.contains(event.target)) {
        return;
      }
      if (event.key === "j") {
        event.preventDefault();
        moveActiveSession(1);
      } else if (event.key === "k") {
        event.preventDefault();
        moveActiveSession(-1);
      } else if (event.key === "/") {
        event.preventDefault();
        focusSearch();
      } else if (event.key === "n") {
        event.preventDefault();
        router.push(`/sessions/new${filterSuffix}`);
      }
    }

    document.addEventListener("keydown", handleDocumentKeyDown);
    return () => document.removeEventListener("keydown", handleDocumentKeyDown);
  }, [filterSuffix, focusSearch, moveActiveSession, router]);

  return (
    <div className="w-full h-full border-r border-border bg-panel flex flex-col">
      {/* Header */}
      <div className="px-4 pt-3 pb-3 space-y-3">

        {/* Row 1: Search + Owner toggle */}
        <div className="flex items-center gap-2">
          <div className="relative flex-1 min-w-0">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/50" />
            <Input
              ref={searchInputRef}
              placeholder="Search sessions..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setSearch("");
                  e.currentTarget.blur();
                }
              }}
              className="h-8 pl-8 pr-8 text-xs"
            />
            <Kbd className="absolute right-2 top-1/2 hidden -translate-y-1/2 md:inline-flex">/</Kbd>
          </div>
          <PeopleFilter
            mode={mode}
            selectedUserIDs={selectedUserIDs}
            members={members}
            currentUser={currentUser}
            onFilterChange={setPeopleFilter}
            className="shrink-0"
          />
        </div>

        {/* New session button */}
        <Link
          href={`/sessions/new${filterSuffix}`}
          onMouseEnter={() => prefetchRoute(`/sessions/new${filterSuffix}`)}
          onFocus={() => prefetchRoute(`/sessions/new${filterSuffix}`)}
          className="relative flex items-center justify-center gap-2 w-full h-9 rounded-md bg-primary bg-[image:var(--gradient-primary)] text-xs font-medium text-white shadow-sm hover:bg-[image:var(--gradient-primary-hover)] hover:shadow-[var(--glow-primary-sm)] transition-all"
        >
          <Plus className="h-4 w-4" />
          New session
          <Kbd variant="primary" className="absolute right-2 top-1/2 hidden -translate-y-1/2 md:inline-flex">N</Kbd>
        </Link>

        {/* Filter tabs */}
        <Tabs
          value={currentFilter}
          onValueChange={(v) => setActiveFilter(v === "all" ? null : v)}
          className="gap-0"
        >
          <div className="overflow-x-auto overflow-y-hidden pb-1">
            <TabsList variant="line" size="sm">
              {filterTabs.map((tab) => {
                const count = getCountForTab(tab.value, counts);
                const label = renderCount(count, counts);
                // Active uses the existing attention-grabbing pill; All/Archived get a
                // muted inline number so totals are visible without being loud.
                // Zero buckets render nothing — a "0" badge is noise.
                const isActivePill = tab.value === "active" && count !== undefined && count > 0;
                const isMutedNumber = !isActivePill && label !== undefined && count !== undefined && count > 0;
                return (
                  <TabsTrigger key={tab.value} value={tab.value}>
                    {tab.label}
                    {isActivePill && (
                      <span className="rounded-full text-white text-xs leading-none px-1.5 py-0.5 bg-primary">{label}</span>
                    )}
                    {isMutedNumber && (
                      <span className="text-xs leading-none text-muted-foreground/60 tabular-nums">{label}</span>
                    )}
                  </TabsTrigger>
                );
              })}
            </TabsList>
          </div>
        </Tabs>
      </div>

      {/* Session list */}
      <div
        ref={listContainerRef}
        role="listbox"
        tabIndex={0}
        aria-label="Sessions"
        aria-activedescendant={
          activeOptionId ? `session-sidebar-option-${activeOptionId}` : undefined
        }
        className="flex-1 overflow-y-auto px-2 pt-1 pb-2 outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
        onKeyDown={handleListKeyDown}
        onPointerEnter={() => setIsListHovered(true)}
        onPointerLeave={() => setIsListHovered(false)}
      >
        {/* Ghost "New session" entry when creating */}
        {isNewSession && (
          <div className="mb-2 border-b border-border/60 pb-2">
            <SessionSidebarOptionFrame
              id={`session-sidebar-option-${newSessionOptionId}`}
              ariaLabel="New session draft"
              ariaSelected={!currentActiveSessionId}
              className={!currentActiveSessionId ? "border-primary/25 bg-card shadow-sm ring-1 ring-primary/10" : undefined}
            >
              <SessionSidebarRowSurface
                href={`/sessions/new${filterSuffix}`}
                ariaCurrent="page"
                className={
                  !currentActiveSessionId
                    ? "border-transparent bg-primary/5 shadow-none ring-0 md:border-transparent md:bg-primary/5 md:shadow-none"
                    : "hover:border-border/60 hover:bg-card md:hover:border-transparent md:hover:bg-muted/50"
                }
              >
                <div className="flex items-center gap-2.5 min-w-0">
                  <span
                    className={cn(
                      "inline-flex rounded-full h-2 w-2 shrink-0",
                      !currentActiveSessionId ? "bg-primary/55" : "border border-muted-foreground/30",
                    )}
                  />
                  <p
                    className={cn(
                      "text-xs font-medium",
                      !currentActiveSessionId ? "text-foreground" : "text-muted-foreground",
                    )}
                  >
                    New session
                  </p>
                </div>
              </SessionSidebarRowSurface>
            </SessionSidebarOptionFrame>
          </div>
        )}

        {currentSessionContext && (
          <div className="mb-2 border-b border-border/60 pb-2">
            <CurrentSessionContextRow
              session={currentSessionContext}
              href={`/sessions/${currentSessionContext.id}${filterSuffix}`}
              ariaSelected={!currentActiveSessionId}
              optionRef={(node) => {
                if (node) {
                  optionRefs.current.set(currentSessionContext.id, node);
                } else {
                  optionRefs.current.delete(currentSessionContext.id);
                }
              }}
            />
          </div>
        )}

        {(currentFilter === "all" || currentFilter === "active") &&
          visibleOptimistic.map((os) => (
            <OptimisticSessionRow key={os.id} session={os} />
          ))}

        {(!isResolved || isLoading) && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            Loading...
          </div>
        )}

        {isResolved && !isLoading && displayedSessions.length === 0 && showDefaultEmptyState && (
          <div className="space-y-3 px-2 py-3">
            <NoReposWarning showDisconnectedState compact />
            <div className="px-2 py-5 text-center text-xs text-muted-foreground">
              No sessions yet
            </div>
          </div>
        )}

        {isResolved && !isLoading && displayedSessions.length === 0 && !showDefaultEmptyState && (
          <div className="px-2 py-8 text-center text-xs text-muted-foreground">
            No sessions match this filter.
          </div>
        )}

        {displayedSessions.map((session) => {
          const isSelected = selectedId === session.id;
          const cfg = statusConfig[session.status] || statusConfig.pending;
          const isWorkingSession = workingSet.has(session.status);
          const hasUnread = isUnread(session);
          const ts = session.completed_at || session.started_at || session.created_at;
          const isArchived = !!session.archived_at;
          const title = sessionTitle(session);
          const sessionHref = `/sessions/${session.id}${filterSuffix}`;

          return (
            <SwipeActionRow
              key={session.id}
              className="mb-0.5"
              actionLabel={isArchived ? "Unarchive session" : "Archive session"}
              actionText={isArchived ? "Restore" : "Archive"}
              desktopActionVisibility="hover"
              actionIcon={isArchived ? <ArchiveRestore className="h-4 w-4" /> : <Archive className="h-4 w-4" />}
              onAction={() => {
                if (isArchived) {
                  return unarchiveMutation.mutateAsync(session);
                } else {
                  return archiveMutation.mutateAsync(session);
                }
              }}
            >
              <SessionSidebarOptionFrame
                id={`session-sidebar-option-${session.id}`}
                ariaSelected={currentActiveSessionId === session.id}
                optionRef={(node) => {
                  if (node) {
                    optionRefs.current.set(session.id, node);
                  } else {
                    optionRefs.current.delete(session.id);
                  }
                }}
                onMouseDown={() => seedSessionDetailCache(session)}
                className={cn(
                  "cursor-pointer",
                  currentActiveSessionId === session.id && !isSelected && "border-border/70 bg-muted/40 ring-1 ring-ring/20",
                  isSelected && "border-primary/25 bg-card shadow-sm ring-1 ring-primary/10",
                )}
                onClick={(event) => {
                  // Clicks on the frame padding (outside the inner link surface)
                  // should still open the session — a dead zone here reads as
                  // "click did nothing".
                  if (event.defaultPrevented || event.target !== event.currentTarget) {
                    return;
                  }
                  navigateToSession(session, sessionHref);
                }}
              >
                <SessionSidebarRowSurface
                  href={sessionHref}
                  ariaCurrent={isSelected ? "page" : undefined}
                  onClick={() => handleSessionLinkClick(session)}
                  onMouseDown={() => seedSessionDetailCache(session)}
                  onMouseEnter={() => prefetchRoute(sessionHref)}
                  onFocus={() => prefetchRoute(sessionHref)}
                  className={
                    isSelected
                      ? "border-transparent bg-primary/5 shadow-none ring-0 md:border-transparent md:bg-primary/5 md:shadow-none"
                      : "hover:border-border/60 hover:bg-card md:hover:border-transparent md:hover:bg-muted/50"
                  }
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      "absolute inset-y-2 left-1 w-0.5 rounded-full bg-primary/0 transition-colors duration-150",
                      isSelected && "bg-primary",
                    )}
                  />
                  <div className="flex items-start gap-2.5 min-w-0">
                    <div className="mt-1.5 shrink-0">
                      {isWorkingSession ? (
                        <StatusDot animate color="bg-primary" pingColor="bg-primary/60" />
                      ) : hasUnread ? (
                        <StatusDot color="bg-primary" />
                      ) : (
                        <span className="inline-flex rounded-full h-2 w-2" />
                      )}
                    </div>

                    <div className="min-w-0 flex-1">
                      <div className="flex items-start justify-between gap-2">
                        <p className={cn(
                          "text-xs font-medium truncate leading-snug",
                          hasUnread || isWorkingSession ? "text-foreground" : "text-muted-foreground"
                        )}>
                          {title}
                        </p>
                      </div>
                      <div className="mt-0.5 flex min-w-0 items-center gap-2">
                        <div
                          data-testid={`session-row-meta-scroll-${session.id}`}
                          className="min-w-0 flex-1 overflow-x-auto overflow-y-hidden scrollbar-hide"
                        >
                          <div className="flex min-w-max items-center gap-1.5 pr-1">
                            <span className="text-xs text-muted-foreground shrink-0">
                              <span>{cfg.label}</span>
                              {isWorkingSession && <AnimatedEllipsis />}
                            </span>
                            {session.pm_plan_id && !session.triggered_by_user_id && (
                              <span className="inline-flex items-center rounded-full bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary shrink-0">
                                PM
                              </span>
                            )}
                            <SessionLinearBadge session={session} />
                            <span className="text-xs text-muted-foreground/50 shrink-0">
                              {formatTimeAgo(ts)}
                            </span>
                            <PRStatusBadge prSummary={session.pr_summary} />
                            <SessionDiffBadge diffStats={session.diff_stats} />
                          </div>
                        </div>
                      </div>
                      {session.status === "failed" && (session.failure_explanation || session.error) && (
                        <p className="text-xs text-destructive/70 truncate mt-0.5">
                          {session.failure_explanation || session.error}
                        </p>
                      )}
                    </div>
                  </div>
                </SessionSidebarRowSurface>
              </SessionSidebarOptionFrame>
            </SwipeActionRow>
          );
        })}

        {loadMoreMutation.isError && (
          <p className="px-2 py-2 text-center text-xs text-destructive/80">
            Failed to load more. Try again.
          </p>
        )}

        {hasMore && (
          <Button
            variant="ghost"
            size="sm"
            className="mx-2 mt-1 mb-2 h-7 w-[calc(100%-1rem)] text-xs text-muted-foreground hover:text-foreground"
            onClick={() => loadMoreMutation.mutate()}
            disabled={loadMoreMutation.isPending}
          >
            {loadMoreMutation.isPending ? "Loading…" : "Show more"}
          </Button>
        )}
      </div>
    </div>
  );
}
