"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ExternalLink,
  GitBranch,
  Loader2,
  MonitorPlay,
  Play,
  RotateCw,
  Search,
  Square,
} from "lucide-react";

import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import { api } from "@/lib/api";
import { pollMs } from "@/lib/poll-intervals";
import { queryKeys } from "@/lib/query-keys";
import type {
  BranchPreviewResponse,
  ListResponse,
  PreviewListMeta,
  Repository,
} from "@/lib/types";
import { safeExternalUrl } from "@/lib/utils";

type PreviewScope = "running" | "resumable" | "recent";

const SECTIONS: {
  scope: PreviewScope;
  title: string;
  empty: string;
  interval: number;
}[] = [
  {
    scope: "running",
    title: "Running",
    empty: "No previews are running.",
    interval: 5000,
  },
  {
    scope: "resumable",
    title: "Ready to resume",
    empty: "No warm previews are ready to resume.",
    interval: 30000,
  },
  {
    scope: "recent",
    title: "Recent",
    empty: "No recent preview activity.",
    interval: 30000,
  },
];

function sourceLabel(preview: BranchPreviewResponse): string {
  if (preview.source_type === "pull_request") {
    const number = preview.source_id?.match(/#(\d+)/)?.[1];
    return number ? `PR #${number}` : "PR";
  }
  if (preview.source_type === "session") return "Session";
  if (preview.source_type === "api") return "API";
  if (preview.source_type === "automation") return "Automation";
  return "Manual";
}

function stoppedReasonLabel(
  reason?: BranchPreviewResponse["stopped_reason"],
): string {
  switch (reason) {
    case "warm_policy":
      return "hibernated by policy";
    case "user":
      return "stopped by you";
    case "expired":
      return "expired";
    case "pr_closed":
      return "PR closed";
    case "drain":
      return "worker drain";
    case "error":
      return "stopped after error";
    default:
      return "stopped";
  }
}

function statusLabel(preview: BranchPreviewResponse): string {
  if (preview.status === "target_created") return "not started";
  return preview.status.replaceAll("_", " ");
}

function relativeTime(value?: string): string {
  if (!value) return "";
  const ms = Date.now() - Date.parse(value);
  if (Number.isNaN(ms)) return "";
  const minutes = Math.max(1, Math.round(ms / 60000));
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

function SectionRows({
  scope,
  previews,
  isLoading,
  canMutate,
  onStop,
  onRestart,
  onStartLatest,
}: {
  scope: PreviewScope;
  previews: BranchPreviewResponse[];
  isLoading: boolean;
  canMutate: boolean;
  onStop: (preview: BranchPreviewResponse) => void;
  onRestart: (preview: BranchPreviewResponse) => void;
  onStartLatest: (preview: BranchPreviewResponse) => void;
}) {
  if (isLoading) {
    return (
      <Card>
        <CardContent className="flex items-center gap-2 py-5 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading previews...
        </CardContent>
      </Card>
    );
  }

  if (previews.length === 0) {
    return (
      <Card>
        <CardContent className="py-5 text-sm text-muted-foreground">
          {SECTIONS.find((section) => section.scope === scope)?.empty}
        </CardContent>
      </Card>
    );
  }

  return (
    <>
      <div className="hidden overflow-hidden rounded-md border border-border md:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Preview</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {previews.map((preview) => (
              <TableRow key={preview.target_id}>
                <TableCell>
                  <Link
                    href={`/previews/${preview.target_id}`}
                    className="font-medium text-foreground hover:underline"
                  >
                    {preview.branch || preview.target_id.slice(0, 8)}
                  </Link>
                  <p className="text-xs text-muted-foreground">
                    {preview.repository_full_name || preview.repository_id} ·{" "}
                    {preview.commit_sha?.slice(0, 8) || "latest"}
                  </p>
                </TableCell>
                <TableCell>
                  {preview.source_url ? (
                    <a
                      href={
                        safeExternalUrl(preview.source_url) ??
                        preview.source_url
                      }
                      className="inline-flex items-center gap-1 text-sm text-foreground hover:underline"
                    >
                      {sourceLabel(preview)}
                      <ExternalLink className="h-3 w-3" />
                    </a>
                  ) : (
                    <span className="text-sm text-foreground">
                      {sourceLabel(preview)}
                    </span>
                  )}
                </TableCell>
                <TableCell>
                  <Badge
                    variant={
                      preview.status === "failed"
                        ? "destructive"
                        : preview.status === "ready"
                          ? "default"
                          : "secondary"
                    }
                  >
                    {statusLabel(preview)}
                  </Badge>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {scope === "resumable" && preview.resume_estimate_seconds
                      ? `resumes in ~${preview.resume_estimate_seconds}s`
                      : scope === "recent"
                        ? stoppedReasonLabel(preview.stopped_reason)
                        : preview.expires_at
                          ? `expires ${relativeTime(preview.expires_at)}`
                          : preview.current_phase || ""}
                  </p>
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-2">
                    {preview.preview_url ? (
                      <Button asChild size="sm">
                        <a
                          href={
                            safeExternalUrl(preview.preview_url) ??
                            preview.preview_url
                          }
                          target="_blank"
                          rel="noreferrer"
                        >
                          <ExternalLink className="h-4 w-4" />
                          Open
                        </a>
                      </Button>
                    ) : null}
                    {canMutate && scope === "running" && preview.preview_id ? (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => onStop(preview)}
                      >
                        <Square className="h-4 w-4" />
                        Stop
                      </Button>
                    ) : null}
                    {canMutate && scope === "resumable" ? (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => onRestart(preview)}
                      >
                        <Play className="h-4 w-4" />
                        Resume
                      </Button>
                    ) : null}
                    {canMutate && scope !== "running" ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => onStartLatest(preview)}
                      >
                        <RotateCw className="h-4 w-4" />
                        Start latest
                      </Button>
                    ) : null}
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="grid gap-3 md:hidden">
        {previews.map((preview) => (
          <Card key={preview.target_id}>
            <CardContent className="space-y-3 py-4">
              <div className="min-w-0">
                <Link
                  href={`/previews/${preview.target_id}`}
                  className="block truncate font-medium text-foreground"
                >
                  {preview.branch || preview.target_id.slice(0, 8)}
                </Link>
                <p className="truncate text-sm text-muted-foreground">
                  {preview.repository_full_name || preview.repository_id} ·{" "}
                  {sourceLabel(preview)}
                </p>
              </div>
              <div className="flex items-center justify-between gap-2">
                <Badge
                  variant={
                    preview.status === "failed"
                      ? "destructive"
                      : preview.status === "ready"
                        ? "default"
                        : "secondary"
                  }
                >
                  {statusLabel(preview)}
                </Badge>
                <span className="text-xs text-muted-foreground">
                  {relativeTime(preview.created_at)}
                </span>
              </div>
              <div className="flex flex-wrap gap-2">
                {preview.preview_url ? (
                  <Button asChild size="sm">
                    <a
                      href={
                        safeExternalUrl(preview.preview_url) ??
                        preview.preview_url
                      }
                      target="_blank"
                      rel="noreferrer"
                    >
                      <ExternalLink className="h-4 w-4" />
                      Open
                    </a>
                  </Button>
                ) : null}
                {canMutate && scope === "running" && preview.preview_id ? (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => onStop(preview)}
                  >
                    <Square className="h-4 w-4" />
                    Stop
                  </Button>
                ) : null}
                {canMutate && scope === "resumable" ? (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => onRestart(preview)}
                  >
                    <Play className="h-4 w-4" />
                    Resume
                  </Button>
                ) : null}
                {canMutate && scope !== "running" ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => onStartLatest(preview)}
                  >
                    <RotateCw className="h-4 w-4" />
                    Start latest
                  </Button>
                ) : null}
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
    </>
  );
}

export default function PreviewsPage() {
  usePageTitle("Previews");
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [query, setQuery] = useState("");
  const [repositoryId, setRepositoryId] = useState("all");
  const canMutate = user?.role !== "viewer";
  const isAdmin = user?.role === "admin";

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const repositoryFilter = repositoryId === "all" ? undefined : repositoryId;

  const runningQuery = useQuery<
    ListResponse<BranchPreviewResponse> & { meta: PreviewListMeta }
  >({
    queryKey: ["previews", "running", repositoryFilter ?? "", query],
    queryFn: () =>
      api.previews.list({
        scope: "running",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }),
    refetchInterval: pollMs(5000),
  });
  const resumableQuery = useQuery<
    ListResponse<BranchPreviewResponse> & { meta: PreviewListMeta }
  >({
    queryKey: ["previews", "resumable", repositoryFilter ?? "", query],
    queryFn: () =>
      api.previews.list({
        scope: "resumable",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }),
    refetchInterval: pollMs(30000),
  });
  const recentQuery = useQuery<
    ListResponse<BranchPreviewResponse> & { meta: PreviewListMeta }
  >({
    queryKey: ["previews", "recent", repositoryFilter ?? "", query],
    queryFn: () =>
      api.previews.list({
        scope: "recent",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }),
    refetchInterval: pollMs(30000),
  });
  const sectionQueries = [runningQuery, resumableQuery, recentQuery];

  const firstMeta = sectionQueries.find((item) => item.data?.meta)?.data?.meta;
  const allEmpty = sectionQueries.every(
    (item) => !item.isLoading && (item.data?.data.length ?? 0) === 0,
  );
  const repositories = useMemo(
    () => repositoriesQuery.data?.data ?? [],
    [repositoriesQuery.data?.data],
  );

  const recentPreviews = useMemo(() => {
    const data = recentQuery.data?.data ?? [];
    return [...data].sort((a, b) => {
      if (a.status === "failed" && b.status !== "failed") return -1;
      if (a.status !== "failed" && b.status === "failed") return 1;
      return 0;
    });
  }, [recentQuery.data?.data]);

  const stopPreview = useMutation({
    mutationFn: (preview: BranchPreviewResponse) =>
      api.previews.stop(preview.preview_id ?? preview.target_id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["previews"] }),
  });
  const restartPreview = useMutation({
    mutationFn: (preview: BranchPreviewResponse) =>
      api.previews.restart(preview.preview_id ?? preview.target_id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["previews"] }),
  });
  const startLatest = useMutation({
    mutationFn: (preview: BranchPreviewResponse) =>
      api.previews.startLatest(preview.preview_id ?? preview.target_id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["previews"] }),
  });

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Previews"
          description="See running previews, resume warm ones, and review recent activity."
          action={
            canMutate ? (
              <Button asChild>
                <Link href="/previews/new">
                  <MonitorPlay className="h-4 w-4" />
                  New preview
                </Link>
              </Button>
            ) : null
          }
        />

        <div className="grid gap-3 md:grid-cols-[1fr_260px]">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search branch, repo, or PR"
              className="pl-9"
            />
          </div>
          <Select value={repositoryId} onValueChange={setRepositoryId}>
            <SelectTrigger aria-label="Repository">
              <SelectValue placeholder="Repository" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All repositories</SelectItem>
              {repositories.map((repo) => (
                <SelectItem key={repo.id} value={repo.id}>
                  {repo.full_name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {allEmpty ? (
          <EmptyState
            icon={MonitorPlay}
            title="No previews yet"
            description="Previews let anyone see a branch or PR running in the browser."
            action={
              canMutate
                ? {
                    label: isAdmin
                      ? "Create your first preview"
                      : "Create preview",
                    href: "/previews/new",
                  }
                : undefined
            }
          />
        ) : (
          <div className="space-y-7">
            {SECTIONS.map((section, index) => {
              const sectionQuery = sectionQueries[index];
              const count =
                firstMeta?.counts?.[section.scope] ??
                sectionQuery.data?.data.length ??
                0;
              return (
                <section
                  key={section.scope}
                  className="space-y-3"
                  aria-labelledby={`previews-${section.scope}`}
                >
                  <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
                    <div className="flex items-center gap-2">
                      {section.scope === "running" ? (
                        <MonitorPlay className="h-4 w-4 text-muted-foreground" />
                      ) : section.scope === "resumable" ? (
                        <Play className="h-4 w-4 text-muted-foreground" />
                      ) : (
                        <GitBranch className="h-4 w-4 text-muted-foreground" />
                      )}
                      <h2
                        id={`previews-${section.scope}`}
                        className="text-sm font-semibold text-foreground"
                      >
                        {section.title} ({count})
                      </h2>
                    </div>
                    {section.scope === "running" && firstMeta?.pool ? (
                      <p className="text-xs text-muted-foreground">
                        Pool:{" "}
                        {firstMeta.pool.user_active +
                          firstMeta.pool.auto_active}{" "}
                        of {firstMeta.pool.user_max + firstMeta.pool.auto_max}{" "}
                        previews
                      </p>
                    ) : section.scope === "resumable" ? (
                      <p className="text-xs text-muted-foreground">
                        warm - resumes in ~30s
                      </p>
                    ) : null}
                  </div>
                  <SectionRows
                    scope={section.scope}
                    previews={section.scope === "recent" ? recentPreviews : (sectionQuery.data?.data ?? [])}
                    isLoading={sectionQuery.isLoading}
                    canMutate={canMutate}
                    onStop={(preview) => stopPreview.mutate(preview)}
                    onRestart={(preview) => restartPreview.mutate(preview)}
                    onStartLatest={(preview) => startLatest.mutate(preview)}
                  />
                </section>
              );
            })}
          </div>
        )}
      </div>
    </PageContainer>
  );
}
