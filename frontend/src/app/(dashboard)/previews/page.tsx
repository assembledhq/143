"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import {
  AlertTriangle,
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
import { CreatePreviewDialog } from "@/components/preview/create-preview-dialog";
import { PreviewStatusBadge } from "@/components/preview/preview-status-badge";
import {
  ResponsiveResourceList,
  type ResponsiveResourceListColumn,
} from "@/components/responsive-resource-list";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useLiveHealth } from "@/components/live-event-provider";
import { useDocumentVisible } from "@/hooks/use-document-visible";
import { useLiveQueryRegistration } from "@/hooks/use-live-query-registration";
import { liveRefreshInterval } from "@/lib/live-refresh-policy";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import { api } from "@/lib/api";
import { formatPreviewStatus } from "@/lib/preview-types";
import { queryKeys } from "@/lib/query-keys";
import type {
  ListResponse,
  PreviewCurrentResponse,
  PreviewListMeta,
  Repository,
} from "@/lib/types";
import { safeExternalUrl } from "@/lib/utils";

type PreviewScope = "running" | "attention" | "resumable" | "recent";

const RESTART_LATEST_LABEL = "Start latest";
const RESTART_LATEST_TOOLTIP = "Start a new preview from the latest source state";

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
    scope: "attention",
    title: "Needs attention",
    empty: "No previews need attention.",
    interval: 30000,
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

function sourceLabel(preview: PreviewCurrentResponse): string {
  if (preview.group_kind === "pull_request" || preview.source_type === "pull_request") {
    const sourcePRNumber = preview.source_id?.match(/#(\d+)/)?.[1];
    const prNumber = preview.pull_request_number ?? sourcePRNumber;
    return prNumber ? `PR #${prNumber}` : "PR";
  }
  if (preview.source_type === "session") return "Session";
  if (preview.source_type === "api") return "API";
  if (preview.source_type === "automation") return "Automation";
  return "Manual";
}

function previewNeedsAttention(preview: PreviewCurrentResponse): boolean {
  return (
    preview.status === "failed" ||
    preview.status === "unavailable" ||
    preview.status === "blocked" ||
    preview.status === "capacity_blocked" ||
    preview.status === "config_invalid" ||
    preview.status === "outdated" ||
    preview.freshness === "outdated" ||
    preview.freshness === "unknown" ||
    preview.latest_commit_sha === ""
  );
}

function sortAttentionFirst(
  previews: PreviewCurrentResponse[],
): PreviewCurrentResponse[] {
  return [...previews].sort((a, b) => {
    const attentionDelta =
      Number(previewNeedsAttention(b)) - Number(previewNeedsAttention(a));
    if (attentionDelta !== 0) return attentionDelta;
    if (a.status === "failed" && b.status !== "failed") return -1;
    if (a.status !== "failed" && b.status === "failed") return 1;
    return 0;
  });
}

function stoppedReasonLabel(
  reason?: PreviewCurrentResponse["stopped_reason"],
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

function statusLabel(preview: PreviewCurrentResponse): string {
  if (preview.freshness === "outdated" && preview.status === "ready") return "Out of date";
  if (preview.freshness === "unknown") return "Needs attention";
  if (preview.status === "target_created" || preview.status === "none") return "Not started";
  return formatPreviewStatus(preview.status);
}

function statusBadgeVariant(
  preview: PreviewCurrentResponse,
): "default" | "secondary" | "destructive" {
  if (preview.status === "failed" || preview.freshness === "outdated" || preview.freshness === "unknown") {
    return "destructive";
  }
  return preview.status === "ready" ? "default" : "secondary";
}

function capitalizeStatusDetail(detail: string): string {
  if (!detail) return "";
  return detail.charAt(0).toUpperCase() + detail.slice(1);
}

function statusDetail(preview: PreviewCurrentResponse, scope: PreviewScope): string {
  if (preview.freshness === "outdated") {
    return capitalizeStatusDetail(
      `running ${preview.running_commit_sha?.slice(0, 8) || "unknown"}, branch is ${preview.latest_commit_sha?.slice(0, 8) || "unknown"}`,
    );
  }
  if (scope === "resumable" && preview.resume_estimate_seconds) {
    return capitalizeStatusDetail(`resumes in ~${preview.resume_estimate_seconds}s`);
  }
  if (scope === "recent" || scope === "attention") {
    return capitalizeStatusDetail(stoppedReasonLabel(preview.stopped_reason));
  }
  if (preview.expires_at) return capitalizeStatusDetail(`expires ${expiresIn(preview.expires_at)}`);
  return preview.current_phase ? formatPreviewStatus(preview.current_phase) : "";
}

function previewDisplayName(preview: PreviewCurrentResponse): string {
  if (preview.group_kind === "pull_request" || preview.source_type === "pull_request") {
    const sourcePRNumber = preview.source_id?.match(/#(\d+)/)?.[1];
    const prNumber = preview.pull_request_number ?? sourcePRNumber;
    if (prNumber && preview.branch) return `PR #${prNumber} - ${preview.branch}`;
    if (prNumber) return `PR #${prNumber}`;
  }
  return preview.branch || preview.preview_group_id.slice(0, 8);
}

function previewDetailHref(preview: PreviewCurrentResponse): string {
  return `/previews/${preview.current_target_id ?? preview.preview_group_id}`;
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

// Formats a timestamp that may be in the past or future as "in 29m" / "5m ago".
function expiresIn(value?: string): string {
  if (!value) return "";
  const ms = Date.parse(value) - Date.now();
  if (Number.isNaN(ms)) return "";
  const future = ms > 0;
  const minutes = Math.max(1, Math.round(Math.abs(ms) / 60000));
  let amount: string;
  if (minutes < 60) amount = `${minutes}m`;
  else {
    const hours = Math.round(minutes / 60);
    amount = hours < 48 ? `${hours}h` : `${Math.round(hours / 24)}d`;
  }
  return future ? `in ${amount}` : `${amount} ago`;
}

function PreviewPrimaryCell({
  preview,
}: {
  preview: PreviewCurrentResponse;
}) {
  return (
    <Link href={previewDetailHref(preview)} className="block min-w-0 space-y-1">
      <span className="block truncate text-sm font-medium text-foreground hover:underline">
        {previewDisplayName(preview)}
      </span>
      <span className="block text-xs text-muted-foreground">
        {preview.repository_full_name || preview.repository_id} ·{" "}
        {preview.pinned ? "Pinned · " : ""}
        {(preview.running_commit_sha || preview.latest_commit_sha)?.slice(0, 8) || "latest"}
      </span>
    </Link>
  );
}

function PreviewSourceCell({ preview }: { preview: PreviewCurrentResponse }) {
  const sourceHref = safeExternalUrl(preview.source_url);

  if (!sourceHref) {
    return <span className="text-sm text-foreground">{sourceLabel(preview)}</span>;
  }

  return (
    <a
      href={sourceHref}
      className="inline-flex items-center gap-1 text-sm text-foreground hover:underline"
    >
      {sourceLabel(preview)}
      <ExternalLink className="h-3 w-3" />
    </a>
  );
}

function PreviewStatusCell({
  preview,
  scope,
}: {
  preview: PreviewCurrentResponse;
  scope: PreviewScope;
}) {
  return (
    <div className="space-y-1">
      <PreviewStatusBadge
        status={preview.status}
        label={statusLabel(preview)}
        variant={statusBadgeVariant(preview)}
      />
      <p className="text-xs text-muted-foreground">{statusDetail(preview, scope)}</p>
    </div>
  );
}

type PreviewActionsProps = {
  preview: PreviewCurrentResponse;
  scope: PreviewScope;
  canMutate: boolean;
  onStop: (preview: PreviewCurrentResponse) => void;
  onRestart: (preview: PreviewCurrentResponse) => void;
  onStartLatest: (preview: PreviewCurrentResponse) => void;
  isRestartPending: (preview: PreviewCurrentResponse) => boolean;
  isStartLatestPending: (preview: PreviewCurrentResponse) => boolean;
};

function PreviewActions({
  preview,
  scope,
  canMutate,
  onStop,
  onRestart,
  onStartLatest,
  isRestartPending,
  isStartLatestPending,
}: PreviewActionsProps) {
  const previewHref = safeExternalUrl(preview.preview_url);

  return (
    <div className="flex flex-wrap items-center justify-end gap-1.5 whitespace-nowrap">
      {previewHref ? (
        <Button
          asChild
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-muted-foreground hover:text-foreground"
        >
          <a
            href={previewHref}
            target="_blank"
            rel="noreferrer"
          >
            <ExternalLink className="h-3.5 w-3.5" />
            Open
          </a>
        </Button>
      ) : null}
      {canMutate && scope === "running" && preview.current_preview_id ? (
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-muted-foreground hover:text-foreground"
          onClick={() => onStop(preview)}
        >
          <Square className="h-3.5 w-3.5" />
          Stop
        </Button>
      ) : null}
      {canMutate && scope === "running" && previewNeedsAttention(preview) ? (
        <RestartLatestButton
          loading={isStartLatestPending(preview)}
          onClick={() => onStartLatest(preview)}
        />
      ) : null}
      {canMutate && scope === "resumable" ? (
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-muted-foreground hover:text-foreground"
          loading={isRestartPending(preview)}
          onClick={() => onRestart(preview)}
        >
          <Play className="h-3.5 w-3.5" />
          Resume
        </Button>
      ) : null}
      {canMutate && scope !== "running" ? (
        <RestartLatestButton
          loading={isStartLatestPending(preview)}
          onClick={() => onStartLatest(preview)}
        />
      ) : null}
    </div>
  );
}

function PreviewMobileRow(props: PreviewActionsProps) {
  const { preview } = props;

  return (
    <div className="space-y-3 p-4">
      <div className="min-w-0">
        <Link
          href={previewDetailHref(preview)}
          className="block truncate font-medium text-foreground"
        >
          {previewDisplayName(preview)}
        </Link>
        <p className="truncate text-sm text-muted-foreground">
          {preview.repository_full_name || preview.repository_id} ·{" "}
          {sourceLabel(preview)}
        </p>
      </div>
      <div className="flex items-center justify-between gap-2">
        <PreviewStatusBadge
          status={preview.status}
          label={statusLabel(preview)}
          variant={statusBadgeVariant(preview)}
        />
        <span className="text-xs text-muted-foreground">
          {relativeTime(preview.created_at)}
        </span>
      </div>
      <PreviewActions {...props} />
    </div>
  );
}

function previewColumns(
  props: Omit<PreviewActionsProps, "preview">,
): ResponsiveResourceListColumn<PreviewCurrentResponse>[] {
  return [
    {
      id: "preview",
      header: "Preview",
      className: "w-[42%]",
      cellClassName: "min-w-0",
      render: (preview) => <PreviewPrimaryCell preview={preview} />,
    },
    {
      id: "source",
      header: "Source",
      className: "w-[16%]",
      render: (preview) => <PreviewSourceCell preview={preview} />,
    },
    {
      id: "status",
      header: "Status",
      className: "w-[22%]",
      render: (preview) => <PreviewStatusCell preview={preview} scope={props.scope} />,
    },
    {
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      className: "w-[20%] min-w-[13rem] text-right",
      cellClassName: "text-right align-middle",
      render: (preview) => <PreviewActions preview={preview} {...props} />,
    },
  ];
}

function SectionRows({
  scope,
  previews,
  isLoading,
  isError,
  onRetry,
  canMutate,
  onStop,
  onRestart,
  onStartLatest,
  isRestartPending,
  isStartLatestPending,
}: {
  scope: PreviewScope;
  previews: PreviewCurrentResponse[];
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  canMutate: boolean;
  onStop: (preview: PreviewCurrentResponse) => void;
  onRestart: (preview: PreviewCurrentResponse) => void;
  onStartLatest: (preview: PreviewCurrentResponse) => void;
  isRestartPending: (preview: PreviewCurrentResponse) => boolean;
  isStartLatestPending: (preview: PreviewCurrentResponse) => boolean;
}) {
  if (isError) {
    return (
      <Card>
        <CardContent className="flex items-center justify-between gap-2 py-5 text-sm">
          <span className="text-destructive">Failed to load previews.</span>
          <Button size="sm" variant="outline" onClick={onRetry}>
            <RotateCw className="h-4 w-4" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

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

  const actionProps = {
    scope,
    canMutate,
    onStop,
    onRestart,
    onStartLatest,
    isRestartPending,
    isStartLatestPending,
  };

  return (
    <ResponsiveResourceList
      ariaLabel={`${SECTIONS.find((section) => section.scope === scope)?.title ?? "Preview"} previews`}
      items={previews}
      getItemKey={(preview) => preview.preview_group_id}
      columns={previewColumns(actionProps)}
      emptyState={SECTIONS.find((section) => section.scope === scope)?.empty}
      renderMobileItem={(preview) => (
        <PreviewMobileRow preview={preview} {...actionProps} />
      )}
      className="rounded-md"
    />
  );
}

function RestartLatestButton({
  loading,
  onClick,
}: {
  loading: boolean;
  onClick: () => void;
}) {
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            size="sm"
            variant="ghost"
            aria-label={RESTART_LATEST_TOOLTIP}
            className="h-7 px-2 text-muted-foreground hover:text-foreground"
            loading={loading}
            onClick={onClick}
          >
            <RotateCw className="h-3.5 w-3.5" />
            {RESTART_LATEST_LABEL}
          </Button>
        </TooltipTrigger>
        <TooltipContent>{RESTART_LATEST_TOOLTIP}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

export default function PreviewsPage() {
	const liveHealth = useLiveHealth();
  const documentVisible = useDocumentVisible();
  usePageTitle("Previews");
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [query, setQuery] = useState("");
  const [repositoryId, setRepositoryId] = useQueryState("repo", parseAsString.withDefault("all"));
  const [branchParam] = useQueryState("branch", parseAsString);
  const [createOpen, setCreateOpen] = useState(false);
  const canMutate = user?.role !== "viewer";
  const isAdmin = user?.role === "admin";

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const repositoryFilter = repositoryId === "all" ? undefined : repositoryId;
  const runningKey = ["previews", "running", repositoryFilter ?? "", query] as const;
  const resumableKey = ["previews", "resumable", repositoryFilter ?? "", query] as const;
  const attentionKey = ["previews", "attention", repositoryFilter ?? "", query] as const;
  const recentKey = ["previews", "recent", repositoryFilter ?? "", query] as const;
  const livePollMs = liveRefreshInterval(runningKey, "list", liveHealth, documentVisible);
  useLiveQueryRegistration({ queryKey: runningKey, families: ["preview.list"], priority: "critical", visible: documentVisible });
  useLiveQueryRegistration({ queryKey: attentionKey, families: ["preview.list"], priority: "critical", visible: documentVisible });
  useLiveQueryRegistration({ queryKey: resumableKey, families: ["preview.list"], priority: "secondary", visible: documentVisible });
  useLiveQueryRegistration({ queryKey: recentKey, families: ["preview.list"], priority: "secondary", visible: documentVisible });

  const runningQuery = useQuery<
    ListResponse<PreviewCurrentResponse> & { meta: PreviewListMeta }
  >({
    queryKey: runningKey,
    queryFn: ({ signal }) =>
      api.previews.current.list({
        scope: "running",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }, { signal }),
    refetchInterval: livePollMs,
    placeholderData: (previous) => previous,
  });
  const resumableQuery = useQuery<
    ListResponse<PreviewCurrentResponse> & { meta: PreviewListMeta }
  >({
    queryKey: resumableKey,
    queryFn: ({ signal }) =>
      api.previews.current.list({
        scope: "resumable",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }, { signal }),
    refetchInterval: livePollMs,
    placeholderData: (previous) => previous,
  });
  const attentionQuery = useQuery<
    ListResponse<PreviewCurrentResponse> & { meta: PreviewListMeta }
  >({
    queryKey: attentionKey,
    queryFn: ({ signal }) =>
      api.previews.current.list({
        scope: "attention",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }, { signal }),
    refetchInterval: livePollMs,
    placeholderData: (previous) => previous,
  });
  const recentQuery = useQuery<
    ListResponse<PreviewCurrentResponse> & { meta: PreviewListMeta }
  >({
    queryKey: recentKey,
    queryFn: ({ signal }) =>
      api.previews.current.list({
        scope: "recent",
        repository_id: repositoryFilter,
        q: query.trim(),
        limit: 50,
      }, { signal }),
    refetchInterval: livePollMs,
    placeholderData: (previous) => previous,
  });
  const allSectionQueries = [runningQuery, attentionQuery, resumableQuery, recentQuery];
  const visibleSectionQueries = [runningQuery, attentionQuery, resumableQuery, recentQuery];

  const firstMeta = allSectionQueries.find((item) => item.data?.meta)?.data?.meta;
  // A query that has only ever errored holds no data, and React Query resets
  // no-data queries to pending (clearing the error) on every interval refetch.
  // isError alone would therefore blink off for the duration of each poll —
  // treat "errored and still no data" as a stable failed state instead.
  // Sections that already hold rows keep showing them through refetch
  // failures: stale-but-real previews beat an error card, and the poll loop
  // refreshes them as soon as the backend recovers.
  const sectionFailed = (query: (typeof allSectionQueries)[number]) =>
    query.data === undefined && (query.isError || query.errorUpdateCount > 0);
  const previewSectionsSettled = allSectionQueries.every(
    (item) => item.data !== undefined || sectionFailed(item),
  );
  // Only successfully settled, genuinely empty sections count toward the
  // page-level empty state; loading or failed sections must not flip the page
  // to "No previews yet".
  const allEmpty = allSectionQueries.every(
    (item) => item.data !== undefined && item.data.data.length === 0,
  );
  const repositories = useMemo(
    () => repositoriesQuery.data?.data ?? [],
    [repositoriesQuery.data?.data],
  );

  const attentionPreviews = useMemo(
    () => sortAttentionFirst(attentionQuery.data?.data ?? []),
    [attentionQuery.data?.data],
  );
  const attentionPreviewIds = useMemo(
    () => new Set(attentionPreviews.map((preview) => preview.preview_group_id)),
    [attentionPreviews],
  );
  const runningPreviews = useMemo(
    () => sortAttentionFirst(runningQuery.data?.data ?? []),
    [runningQuery.data?.data],
  );
  const resumablePreviews = useMemo(
    () => sortAttentionFirst(resumableQuery.data?.data ?? []),
    [resumableQuery.data?.data],
  );
  const recentPreviews = useMemo(
    () =>
      (recentQuery.data?.data ?? []).filter(
        (preview) =>
          !previewNeedsAttention(preview) &&
          !attentionPreviewIds.has(preview.preview_group_id),
      ),
    [attentionPreviewIds, recentQuery.data?.data],
  );
  const previewsByScope: Record<PreviewScope, PreviewCurrentResponse[]> = {
    running: runningPreviews,
    attention: attentionPreviews,
    resumable: resumablePreviews,
    recent: recentPreviews,
  };

  const stopPreview = useMutation({
    mutationFn: (preview: PreviewCurrentResponse) =>
      api.previews.current.stop(preview.preview_group_id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["previews"] }),
  });
  const [pendingRestartIds, setPendingRestartIds] = useState<Set<string>>(new Set());
  const restartPreview = useMutation({
    mutationFn: (preview: PreviewCurrentResponse) =>
      api.previews.current.restart(preview.preview_group_id),
    onMutate: (preview) => {
      setPendingRestartIds((prev) => new Set([...prev, preview.preview_group_id]));
    },
    onSettled: (_data, _error, preview) => {
      setPendingRestartIds((prev) => {
        const next = new Set(prev);
        next.delete(preview.preview_group_id);
        return next;
      });
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["previews"] }),
  });

  const [pendingStartLatestIds, setPendingStartLatestIds] = useState<Set<string>>(new Set());
  const startLatest = useMutation({
    mutationFn: (preview: PreviewCurrentResponse) =>
      api.previews.current.startLatest(preview.preview_group_id),
    onMutate: (preview) => {
      setPendingStartLatestIds((prev) => new Set([...prev, preview.preview_group_id]));
    },
    onSettled: (_data, _error, preview) => {
      setPendingStartLatestIds((prev) => {
        const next = new Set(prev);
        next.delete(preview.preview_group_id);
        return next;
      });
    },
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
              <Button type="button" onClick={() => setCreateOpen(true)}>
                <MonitorPlay className="h-4 w-4" />
                New preview
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

        {!previewSectionsSettled ? (
          <div className="min-h-48" aria-hidden="true" />
        ) : allEmpty ? (
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
                    onClick: () => setCreateOpen(true),
                  }
                : undefined
            }
          />
        ) : (
          <div className="space-y-7">
            {SECTIONS.map((section, index) => {
              const sectionQuery = visibleSectionQueries[index];
              const sectionPreviews = previewsByScope[section.scope];
              const count = sectionPreviews.length;
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
                      ) : section.scope === "attention" ? (
                        <AlertTriangle className="h-4 w-4 text-muted-foreground" />
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
                    previews={
                      sectionPreviews
                    }
                    isLoading={
                      sectionQuery.isLoading && !sectionFailed(sectionQuery)
                    }
                    isError={sectionFailed(sectionQuery)}
                    onRetry={() => sectionQuery.refetch()}
                    canMutate={canMutate}
                    onStop={(preview) => stopPreview.mutate(preview)}
                    onRestart={(preview) => restartPreview.mutate(preview)}
                    onStartLatest={(preview) => startLatest.mutate(preview)}
                    isRestartPending={(preview) =>
                      pendingRestartIds.has(preview.preview_group_id)
                    }
                    isStartLatestPending={(preview) =>
                      pendingStartLatestIds.has(preview.preview_group_id)
                    }
                  />
                </section>
              );
            })}
          </div>
        )}
        {canMutate ? (
          <CreatePreviewDialog
            open={createOpen}
            onOpenChange={setCreateOpen}
            initialRepositoryId={repositoryFilter}
            initialBranch={branchParam ?? undefined}
            onCreated={() => {
              void queryClient.invalidateQueries({ queryKey: ["previews"] });
            }}
          />
        ) : null}
      </div>
    </PageContainer>
  );
}
