"use client";

import { useQuery } from "@tanstack/react-query";
import { useQueryState } from "nuqs";
import { ChevronDown, GitBranch } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import { useEffect, useState } from "react";

export function RepoContextSwitcher() {
  const { data: summariesData } = useQuery({
    queryKey: ["repositories", "summary"],
    queryFn: () => api.repositories.summary(),
    refetchInterval: 10_000,
  });

  const [repo, setRepo] = useQueryState("repo");
  const [search, setSearch] = useState("");

  const summaries = summariesData?.data;

  // If selected repo no longer exists in summaries (e.g. disconnected), reset
  useEffect(() => {
    if (repo && summaries && !summaries.find((r) => r.repository_id === repo)) {
      setRepo(null);
    }
  }, [repo, summaries, setRepo]);

  // Don't render for single-repo orgs
  if (!summaries || summaries.length < 2) return null;

  const selectedRepo = summaries.find((r) => r.repository_id === repo);
  const label = selectedRepo
    ? selectedRepo.full_name.split("/").pop()
    : "All repositories";

  const showSearch = summaries.length >= 4;
  const filtered = search
    ? summaries.filter((r) =>
        r.full_name.toLowerCase().includes(search.toLowerCase())
      )
    : summaries;

  return (
    <DropdownMenu onOpenChange={(open) => { if (!open) setSearch(""); }}>
      <DropdownMenuTrigger
        className="flex items-center h-8 w-full gap-2 rounded-md px-2.5 text-xs font-medium transition-colors duration-150 text-surface-nav-muted hover:bg-surface-nav-hover hover:text-surface-nav-foreground"
        data-testid="repo-context-switcher"
      >
        <GitBranch className="h-4 w-4 shrink-0" />
        <span className="truncate flex-1 text-left">{label}</span>
        <ChevronDown className="h-3.5 w-3.5 shrink-0 opacity-40" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-64">
        {showSearch && (
          <div className="px-2 pb-1.5">
            <Input
              type="text"
              placeholder="Search repos..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onClick={(e) => e.stopPropagation()}
            />
          </div>
        )}
        {!search && (
          <>
            <DropdownMenuItem
              onClick={() => setRepo(null)}
              className={cn(!repo && "font-medium")}
            >
              All repositories
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        {filtered.map((r) => (
          <DropdownMenuItem
            key={r.repository_id}
            onClick={() => setRepo(r.repository_id)}
            className={cn(
              "flex items-center gap-2",
              repo === r.repository_id && "font-medium"
            )}
          >
            <GitBranch className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            <span className="truncate flex-1" title={r.full_name}>
              {r.full_name}
            </span>
            {r.active_session_count > 0 && (
              <span className="text-xs rounded-full bg-primary/10 text-primary px-1.5 py-0.5 font-medium tabular-nums">
                {r.active_session_count}
              </span>
            )}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
