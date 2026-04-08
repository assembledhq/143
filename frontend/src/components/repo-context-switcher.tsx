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

function StatusDot({ status }: { status: string | null }) {
  if (!status) return null;

  if (status === "running" || status === "pending") {
    return (
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
      </span>
    );
  }

  if (status === "needs_human_guidance" || status === "awaiting_input") {
    return <span className="inline-flex rounded-full h-2 w-2 bg-amber-500" />;
  }

  if (status === "failed" || status === "cancelled") {
    return <span className="inline-flex rounded-full h-2 w-2 bg-red-500" />;
  }

  return null;
}

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
        className="flex items-center gap-1.5 text-[13px] font-medium text-muted-foreground hover:text-foreground transition-colors px-2 py-1 rounded-md hover:bg-sidebar-accent"
        data-testid="repo-context-switcher"
      >
        <span>{label}</span>
        <ChevronDown className="h-3.5 w-3.5 opacity-50" />
        {selectedRepo && <StatusDot status={selectedRepo.latest_session_status} />}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
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
            <StatusDot status={r.latest_session_status} />
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
