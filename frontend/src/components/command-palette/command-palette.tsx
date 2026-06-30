"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useQueryState } from "nuqs";
import { useQuery } from "@tanstack/react-query";
import { GitBranch, Loader2, Play, FolderKanban, Sparkles } from "lucide-react";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { getFilteredActions, type PaletteAction } from "./command-palette-actions";
import { useCommandPaletteSearch } from "./use-command-palette-search";
import { useRecentPaletteItems, type RecentItem } from "./use-recent-palette-items";

interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userRole: string;
  logout: () => void;
}

interface CommandPaletteContentProps {
  userRole: string;
  logout: () => void;
  onClose: () => void;
}

function CommandPaletteContent({
  userRole,
  logout,
  onClose,
}: CommandPaletteContentProps) {
  const router = useRouter();
  const [query, setQuery] = useState("");
  const [repo, setRepo] = useQueryState("repo");
  const { displayItems, addRecent } = useRecentPaletteItems();

  const actions = useMemo(() => getFilteredActions(userRole), [userRole]);
  const canStartManualSession = userRole !== "viewer";
  const { sessions, projects, isLoading } = useCommandPaletteSearch(query, repo);

  const { data: repoSummaries } = useQuery({
    queryKey: queryKeys.repositories.summary,
    queryFn: () => api.repositories.summary(),
    refetchInterval: 10_000,
  });
  const repos = useMemo(() => repoSummaries?.data ?? [], [repoSummaries?.data]);

  const buildHref = useCallback(
    (href: string, preserveRepo = false) => {
      if (!preserveRepo || !repo) {
        return href;
      }

      const url = new URL(href, "http://localhost");
      if (!url.searchParams.has("repo")) {
        url.searchParams.set("repo", repo);
      }
      return `${url.pathname}${url.search}${url.hash}`;
    },
    [repo]
  );

  const navigate = useCallback(
    (href: string, preserveRepo = false) => {
      router.push(buildHref(href, preserveRepo));
      onClose();
    },
    [buildHref, onClose, router]
  );

  const handleActionSelect = useCallback(
    (action: PaletteAction) => {
      if (action.id === "action-logout") {
        onClose();
        logout();
        return;
      }
      if (action.href) {
        if (action.group === "navigation") {
          addRecent({
            type: "navigation",
            id: action.id,
            label: action.label,
            href: action.href,
          });
        }
        navigate(action.href, action.preserveRepo);
      }
    },
    [addRecent, logout, navigate, onClose]
  );

  const handleSessionSelect = useCallback(
    (session: { id: string; title?: string }) => {
      const label = session.title || `Session ${session.id.slice(0, 8)}`;
      addRecent({
        type: "session",
        id: session.id,
        label,
        href: `/sessions/${session.id}`,
      });
      navigate(`/sessions/${session.id}`, true);
    },
    [addRecent, navigate]
  );

  const handleProjectSelect = useCallback(
    (project: { id: string; title: string }) => {
      addRecent({
        type: "project",
        id: project.id,
        label: project.title,
        href: `/projects/${project.id}`,
      });
      navigate(`/projects/${project.id}`, true);
    },
    [addRecent, navigate]
  );

  const handleRepoSelect = useCallback(
    (repoID: string | null) => {
      setRepo(repoID);
      onClose();
    },
    [onClose, setRepo]
  );

  const handleRecentSelect = useCallback(
    (item: RecentItem) => {
      navigate(item.href, true);
    },
    [navigate]
  );

  const handleStartSession = useCallback(() => {
    if (!canStartManualSession) {
      return;
    }
    const params = new URLSearchParams();
    if (query) {
      params.set("prompt", query);
    }
    const qs = params.toString();
    navigate(`/sessions/new${qs ? `?${qs}` : ""}`);
  }, [canStartManualSession, navigate, query]);

  const normalizedQuery = query.trim().toLowerCase();
  const navigationActions = useMemo(() => actions.filter((action) => action.group === "navigation"), [actions]);
  const settingsActions = useMemo(() => actions.filter((action) => action.group === "settings"), [actions]);
  const quickActions = useMemo(() => actions.filter((action) => action.group === "quick-actions"), [actions]);
  const hasQuery = query.length >= 2;
  const hasDynamicResults = sessions.length > 0 || projects.length > 0;
  const hasMatchingStaticItem = useMemo(() => {
    if (normalizedQuery.length === 0) {
      return false;
    }

    const labels = [
      ...actions.map((action) => action.label),
      ...displayItems.map((item) => item.label),
      ...(repos.length >= 2 ? ["All repositories"] : []),
      ...repos.map((repoSummary) => repoSummary.full_name),
    ];
    return labels.some((label) => label.toLowerCase().includes(normalizedQuery));
  }, [actions, displayItems, normalizedQuery, repos]);
  const canStartSessionFromKeyboard =
    canStartManualSession &&
    normalizedQuery.length > 0 &&
    !isLoading &&
    !hasDynamicResults &&
    !hasMatchingStaticItem;

  const handleInputKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLInputElement>) => {
      if (event.key === "Enter" && canStartSessionFromKeyboard) {
        event.preventDefault();
        handleStartSession();
      }
    },
    [canStartSessionFromKeyboard, handleStartSession]
  );

  return (
    <>
      <CommandInput
        placeholder="Type a command or search..."
        value={query}
        onValueChange={setQuery}
        onKeyDown={handleInputKeyDown}
      />
      <CommandList>
        {isLoading && (
          <div className="flex items-center justify-center gap-2 py-3 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            Searching...
          </div>
        )}

        {hasQuery && sessions.length > 0 && (
          <CommandGroup heading="Sessions">
            {sessions.map((session) => (
              <CommandItem
                key={session.id}
                value={`session-${session.id}-${session.title ?? ""}`}
                onSelect={() => handleSessionSelect(session)}
              >
                <Play className="h-4 w-4" />
                <span className="flex-1 truncate">
                  {session.title || `Session ${session.id.slice(0, 8)}`}
                </span>
                <Badge
                  variant="secondary"
                  className="ml-auto px-1.5 py-0 text-xs"
                >
                  {session.status}
                </Badge>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {hasQuery && projects.length > 0 && (
          <CommandGroup heading="Projects">
            {projects.map((project) => (
              <CommandItem
                key={project.id}
                value={`project-${project.id}-${project.title}`}
                onSelect={() => handleProjectSelect(project)}
              >
                <FolderKanban className="h-4 w-4" />
                <span className="flex-1 truncate">{project.title}</span>
                <Badge
                  variant="secondary"
                  className="ml-auto px-1.5 py-0 text-xs"
                >
                  {project.status}
                </Badge>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {hasQuery && hasDynamicResults && <CommandSeparator />}

        {displayItems.length > 0 && (
          <CommandGroup heading="Recent">
            {displayItems.map((item) => (
              <CommandItem
                key={`${item.type}-${item.id}`}
                value={`recent-${item.type}-${item.id}-${item.label}`}
                onSelect={() => handleRecentSelect(item)}
              >
                {item.type === "session" && <Play className="h-4 w-4" />}
                {item.type === "project" && <FolderKanban className="h-4 w-4" />}
                {item.type === "navigation" && <Sparkles className="h-4 w-4" />}
                <span className="truncate">{item.label}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        <CommandGroup heading="Quick Actions">
          {quickActions.map((action) => (
            <CommandItem
              key={action.id}
              value={action.label}
              onSelect={() => handleActionSelect(action)}
            >
              <action.icon className="h-4 w-4" />
              {action.label}
            </CommandItem>
          ))}
        </CommandGroup>

        {repos.length >= 2 && (
          <CommandGroup heading="Switch Repository">
            <CommandItem
              value="All repositories"
              onSelect={() => handleRepoSelect(null)}
            >
              <GitBranch className="h-4 w-4" />
              <span className={!repo ? "font-medium" : ""}>All repositories</span>
            </CommandItem>
            {repos.map((repoSummary) => (
              <CommandItem
                key={repoSummary.repository_id}
                value={`repo-${repoSummary.full_name}`}
                onSelect={() => handleRepoSelect(repoSummary.repository_id)}
              >
                <GitBranch className="h-4 w-4" />
                <span
                  className={`flex-1 truncate ${repo === repoSummary.repository_id ? "font-medium" : ""}`}
                >
                  {repoSummary.full_name}
                </span>
                {repoSummary.active_session_count > 0 && (
                  <span className="rounded-full bg-primary/10 px-1.5 py-0.5 text-xs font-medium tabular-nums text-primary">
                    {repoSummary.active_session_count}
                  </span>
                )}
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        <CommandGroup heading="Navigation">
          {navigationActions.map((action) => (
            <CommandItem
              key={action.id}
              value={action.label}
              onSelect={() => handleActionSelect(action)}
            >
              <action.icon className="h-4 w-4" />
              {action.label}
              {action.shortcut && (
                <kbd className="ml-auto text-xs text-muted-foreground">
                  {action.shortcut}
                </kbd>
              )}
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading="Settings">
          {settingsActions.map((action) => (
            <CommandItem
              key={action.id}
              value={action.label}
              onSelect={() => handleActionSelect(action)}
            >
              <action.icon className="h-4 w-4" />
              {action.label}
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandEmpty>
          {canStartManualSession && query.length > 0 ? (
            <Button
              variant="ghost"
              onClick={handleStartSession}
              className="h-auto w-full justify-center gap-2 px-4 py-2 text-sm text-muted-foreground hover:text-foreground"
            >
              <Sparkles className="h-4 w-4" />
              Start manual session: &ldquo;{query}&rdquo;
            </Button>
          ) : (
            "No results found."
          )}
        </CommandEmpty>
      </CommandList>
    </>
  );
}

export function CommandPalette({
  open,
  onOpenChange,
  userRole,
  logout,
}: CommandPaletteProps) {
  const previousActiveElement = useRef<Element | null>(null);
  const wasOpenRef = useRef(false);

  useEffect(() => {
    if (open && !wasOpenRef.current) {
      previousActiveElement.current = document.activeElement;
      wasOpenRef.current = true;
      return;
    }

    if (!open && wasOpenRef.current) {
      wasOpenRef.current = false;
      requestAnimationFrame(() => {
        const element = previousActiveElement.current;
        if (element instanceof HTMLElement && document.contains(element)) {
          element.focus();
        }
        previousActiveElement.current = null;
      });
    }
  }, [open]);

  const close = useCallback(() => {
    onOpenChange(false);
  }, [onOpenChange]);

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Command Palette"
      description="Search for pages, sessions, projects, and actions"
      showCloseButton={false}
    >
      {open ? (
        <CommandPaletteContent
          userRole={userRole}
          logout={logout}
          onClose={close}
        />
      ) : null}
    </CommandDialog>
  );
}
