"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { parseAsString, useQueryState } from "nuqs";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, ArrowUpRight, Clock3, GitPullRequest, Loader2, Play, RotateCcw, Search, SlidersHorizontal } from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AutopilotConfigFooter } from "./autopilot-config-footer";
import { useAutopilotPageData } from "./use-autopilot-page-data";
import { useAnalyze } from "@/hooks/use-analyze";
import { AutopilotSteeringSheet } from "./autopilot-steering-sheet";
import { AutopilotDocumentsSheet } from "./autopilot-documents-sheet";
import { AutopilotProposalCard } from "@/components/autopilot-proposal-card";
import { OpenPreviewButton } from "@/components/preview/open-preview-button";
import { SessionLinearBadge } from "@/components/session-linear-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useAuth } from "@/hooks/use-auth";
import { api } from "@/lib/api";
import type { AutopilotQueueRow, AutopilotRunState } from "@/lib/types";
import { formatDateTime, safeExternalUrl } from "@/lib/utils";

const ALL_VALUE = "all";

const SOURCE_OPTIONS = [
  { value: ALL_VALUE, label: "All sources" },
  { value: "linear", label: "Linear" },
  { value: "sentry", label: "Sentry" },
  { value: "pm_agent", label: "PM agent" },
];

const linearIdentifierPattern = /^[A-Z][A-Z0-9_]{0,9}-[0-9]+$/;
const linearTitleIdentifierPattern = /^([A-Z][A-Z0-9_]{0,9}-[0-9]+):/;

const RUN_STATE_OPTIONS = [
  { value: ALL_VALUE, label: "Any run state" },
  { value: "not_started", label: "Not started" },
  { value: "queued", label: "Queued" },
  { value: "running", label: "Running" },
  { value: "awaiting_input", label: "Awaiting input" },
  { value: "needs_review", label: "Needs review" },
  { value: "pr_open", label: "PR open" },
  { value: "merged", label: "Merged" },
  { value: "failed", label: "Failed" },
];

const AUTOMATION_OPTIONS = [
  { value: ALL_VALUE, label: "Any automation" },
  { value: "autorun_attempted", label: "Autoran" },
  { value: "manual_only", label: "Manual only" },
  { value: "ready_to_run", label: "Ready to run" },
];

const SORT_OPTIONS = [
  { value: "rank", label: "System rank" },
  { value: "impact", label: "Impact" },
  { value: "freshness", label: "Freshness" },
  { value: "run_state", label: "Run state" },
];

const PREVIEW_ORIGIN_TEMPLATE =
  process.env.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE ||
  "http://{id}.preview.localhost:9090";

export function AutopilotPageContent() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [showDirectionEditor, setShowDirectionEditor] = useState(false);
  const [showDocumentsEditor, setShowDocumentsEditor] = useState(false);
  const [selectedIssue, setSelectedIssue] = useState<AutopilotQueueRow | null>(null);
  const [sessionNotes, setSessionNotes] = useState("");
  const [source, setSource] = useQueryState("source", parseAsString.withDefault(ALL_VALUE));
  const [runState, setRunState] = useQueryState("run_state", parseAsString.withDefault(ALL_VALUE));
  const [automation, setAutomation] = useQueryState("automation", parseAsString.withDefault(ALL_VALUE));
  const [sort, setSort] = useQueryState("sort", parseAsString.withDefault("rank"));
  const [search, setSearch] = useQueryState("q", parseAsString.withDefault(""));
  const {
    isLoading,
    isSetupComplete,
    pmStatus,
    settings,
    viewModel,
    queue,
    queueLoading,
    hasNextQueuePage,
    fetchNextQueuePage,
    isFetchingNextQueuePage,
  } = useAutopilotPageData({
    source: source === ALL_VALUE ? null : source,
    run_state: runState === ALL_VALUE ? null : runState,
    automation: automation === ALL_VALUE ? null : automation,
    sort,
    q: search || null,
  });
  const { handleAnalyze, isAnalyzing, isPending } = useAnalyze(pmStatus.is_running);
  const isAdmin = user?.role === "admin";
  const canMutate = user?.role !== "viewer";

  useEffect(() => {
    if (!isLoading && !isSetupComplete) {
      router.replace("/onboarding");
    }
  }, [isLoading, isSetupComplete, router]);

  const startRunMutation = useMutation({
    mutationFn: ({ issueId, message, force }: { issueId: string; message: string; force?: boolean }) =>
      api.issues.triggerFix(issueId, {
        autonomy_level: "semi",
        token_mode: "low",
        message: message.trim() || undefined,
        force: force || undefined,
      }),
    onSuccess: () => {
      setSelectedIssue(null);
      setSessionNotes("");
      void queryClient.invalidateQueries({ queryKey: ["autopilot", "queue"] });
    },
  });

  if (isLoading || !isSetupComplete) {
    return (
      <PageContainer size="wide">
        <p className="text-sm text-muted-foreground">Loading Autopilot...</p>
      </PageContainer>
    );
  }

  const rows = queue?.data ?? [];
  const summary = queue?.meta.summary;
  const statusLine = buildStatusLine(viewModel.statusLine, summary);

  return (
    <PageContainer size="wide">
      <TooltipProvider>
      <div className="space-y-6">
        <PageHeader
          title="Autopilot"
          subtitle={statusLine}
          action={isAdmin ? (
            <Button onClick={handleAnalyze} disabled={isAnalyzing || isPending}>
              {isAnalyzing || isPending ? "Running..." : "Run analysis"}
            </Button>
          ) : undefined}
        />

        <SummaryStrip summary={summary} />

        <QueueFilters
          source={source}
          runState={runState}
          automation={automation}
          sort={sort}
          search={search}
          onSourceChange={setSource}
          onRunStateChange={setRunState}
          onAutomationChange={setAutomation}
          onSortChange={setSort}
          onSearchChange={setSearch}
        />

        <QueueTable
          rows={rows}
          loading={queueLoading}
          hasNextPage={hasNextQueuePage}
          loadingNextPage={isFetchingNextQueuePage}
          onLoadMore={() => void fetchNextQueuePage()}
          onStartRun={setSelectedIssue}
          canOverrideBlocked={isAdmin}
          canMutate={canMutate}
        />

        <AutopilotProposalCard />

        <AutopilotConfigFooter
          directionSummary={viewModel.directionSummary}
          focusAreas={viewModel.focusAreas}
          documentsSummary={viewModel.documentsSummary}
          weightsSummary={viewModel.weightsSummary}
          canEdit={canMutate}
          onEditDirection={() => setShowDirectionEditor(true)}
          onManageDocuments={() => setShowDocumentsEditor(true)}
          onOpenSettings={() => router.push("/settings/autopilot")}
        />

        <AutopilotSteeringSheet
          open={showDirectionEditor}
          onOpenChange={setShowDirectionEditor}
          settings={settings}
        />
        <AutopilotDocumentsSheet
          open={showDocumentsEditor}
          onOpenChange={setShowDocumentsEditor}
        />
        <StartRunSheet
          row={selectedIssue}
          pending={startRunMutation.isPending}
          error={startRunMutation.error instanceof Error ? startRunMutation.error.message : null}
          notes={sessionNotes}
          onNotesChange={setSessionNotes}
          onOpenChange={(open) => {
            if (!open) {
              setSelectedIssue(null);
              setSessionNotes("");
            }
          }}
          onConfirm={() => {
            if (selectedIssue) {
              const isOverride = selectedIssue.available_action === "blocked" || selectedIssue.available_action === "retry";
              startRunMutation.mutate({ issueId: selectedIssue.id, message: sessionNotes, force: isOverride || undefined });
            }
          }}
        />
      </div>
      </TooltipProvider>
    </PageContainer>
  );
}

function SummaryStrip({ summary }: { summary?: { autorunnable_count: number; needs_review_count: number; open_pr_count: number; active_run_count: number; ranked_issue_count: number } }) {
  const cards = [
    {
      label: "Auto-runnable now",
      value: String(summary?.autorunnable_count ?? 0),
      detail: "eligible without active runs",
      icon: Play,
    },
    {
      label: "Needs review",
      value: String(summary?.needs_review_count ?? 0),
      detail: "blocked on operator input",
      icon: AlertCircle,
    },
    {
      label: "Connected work",
      value: String((summary?.active_run_count ?? 0) + (summary?.open_pr_count ?? 0)),
      detail: `${summary?.active_run_count ?? 0} active runs · ${summary?.open_pr_count ?? 0} PRs`,
      icon: GitPullRequest,
    },
  ];

  return (
    <div className="grid gap-3 md:grid-cols-3">
      {cards.map((card) => {
        const Icon = card.icon;
        return (
          <Card key={card.label} className="border-border/70">
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="text-xs font-medium text-muted-foreground">{card.label}</CardTitle>
              <Icon className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="truncate text-lg font-semibold text-foreground">{card.value}</div>
              <p className="mt-1 truncate text-xs text-muted-foreground">{card.detail}</p>
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

function QueueFilters(props: {
  source: string;
  runState: string;
  automation: string;
  sort: string;
  search: string;
  onSourceChange: (value: string) => void;
  onRunStateChange: (value: string) => void;
  onAutomationChange: (value: string) => void;
  onSortChange: (value: string) => void;
  onSearchChange: (value: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2 border-y border-border/70 py-3 lg:flex-row lg:items-center">
      <div className="relative min-w-0 flex-1">
        <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
        <Input
          value={props.search}
          onChange={(event) => props.onSearchChange(event.target.value)}
          placeholder="Search issues"
          className="h-9 pl-8 text-sm"
        />
      </div>
      <Select value={props.source} onValueChange={props.onSourceChange}>
        <SelectTrigger className="h-9 lg:w-[150px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {SOURCE_OPTIONS.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
      <Select value={props.runState} onValueChange={props.onRunStateChange}>
        <SelectTrigger className="h-9 lg:w-[170px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {RUN_STATE_OPTIONS.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
      <Select value={props.automation} onValueChange={props.onAutomationChange}>
        <SelectTrigger className="h-9 lg:w-[160px]">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {AUTOMATION_OPTIONS.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
      <Select value={props.sort} onValueChange={props.onSortChange}>
        <SelectTrigger className="h-9 lg:w-[150px]">
          <SlidersHorizontal className="mr-2 h-3.5 w-3.5" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {SORT_OPTIONS.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
    </div>
  );
}

function QueueTable({
  rows,
  loading,
  hasNextPage,
  loadingNextPage,
  onLoadMore,
  onStartRun,
  canOverrideBlocked,
  canMutate,
}: {
  rows: AutopilotQueueRow[];
  loading: boolean;
  hasNextPage: boolean;
  loadingNextPage: boolean;
  onLoadMore: () => void;
  onStartRun: (row: AutopilotQueueRow) => void;
  canOverrideBlocked: boolean;
  canMutate: boolean;
}) {
  if (loading) {
    return <Card><CardContent className="py-8 text-sm text-muted-foreground">Loading ranked issues...</CardContent></Card>;
  }
  if (rows.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center">
          <h2 className="text-sm font-semibold text-foreground">No ranked issues right now</h2>
          <p className="mt-2 text-sm text-muted-foreground">Run analysis or clear filters to refresh the queue.</p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-3">
      <Card className="overflow-hidden border-border/70">
        <Table className="w-full min-w-[64rem] table-auto">
          <TableHeader>
            <TableRow>
              <TableHead className="w-14">Rank</TableHead>
              <TableHead className="w-[34%]">Issue</TableHead>
              <TableHead className="w-24">Source</TableHead>
              <TableHead className="w-32">Customer impact</TableHead>
              <TableHead className="w-24">Ease</TableHead>
              <TableHead className="w-28">Priority fit</TableHead>
              <TableHead className="w-36">Run state</TableHead>
              <TableHead className="w-28 text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.id}>
                <TableCell className="font-medium text-muted-foreground">#{row.rank}</TableCell>
                <TableCell className="whitespace-normal">
                  <IssueTitle row={row} />
                  <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span>{row.repo?.name ?? "No repo"}</span>
                    <span>{row.issue_status}</span>
                    {row.low_hanging_fruit.cluster_size > 1 && <span>{row.low_hanging_fruit.cluster_size} related</span>}
                  </div>
                </TableCell>
                <TableCell><SourceBadge row={row} /></TableCell>
                <TableCell>
                  <MetricBadge label={row.customer_impact.label} detail={String(row.customer_impact.count)} />
                </TableCell>
                <TableCell>
                  <MetricBadge label={row.implementation_ease} />
                </TableCell>
                <TableCell>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Badge variant={fruitBadgeVariant(row.low_hanging_fruit.label)}>{row.low_hanging_fruit.label}</Badge>
                    </TooltipTrigger>
                    <TooltipContent>
                      <div className="max-w-56 text-xs">{row.low_hanging_fruit.reasons.join(", ") || "No ranking details yet"}</div>
                    </TooltipContent>
                  </Tooltip>
                </TableCell>
                <TableCell><RunState row={row} /></TableCell>
                <TableCell className="text-right"><RowAction row={row} onStartRun={onStartRun} canOverrideBlocked={canOverrideBlocked} canMutate={canMutate} /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
      {hasNextPage && (
        <div className="flex justify-center">
          <Button variant="outline" onClick={onLoadMore} disabled={loadingNextPage}>
            {loadingNextPage ? "Loading..." : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

function IssueTitle({ row }: { row: AutopilotQueueRow }) {
  const issueUrl = safeExternalUrl(row.issue_url);
  if (!issueUrl) {
    return <div className="font-medium text-foreground">{row.title}</div>;
  }

  return (
    <a
      href={issueUrl}
      target="_blank"
      rel="noreferrer"
      className="inline-flex min-w-0 items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
    >
      <span>{row.title}</span>
      <ArrowUpRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
    </a>
  );
}

function SourceBadge({ row }: { row: AutopilotQueueRow }) {
  if (row.source.type === "linear") {
    return <SessionLinearBadge label={linearIdentifierForRow(row)} />;
  }

  const sourceText = sourceDisplayText(row);
  return <Badge variant="outline" className="capitalize">{sourceText}</Badge>;
}

function MetricBadge({ label, detail }: { label: string; detail?: string }) {
  return (
    <Badge variant={metricBadgeVariant(label)}>
      <span>{label}</span>
      {detail ? <span className="text-muted-foreground">{detail}</span> : null}
    </Badge>
  );
}

function RunState({ row }: { row: AutopilotQueueRow }) {
  const label = runStateLabel(row.display_run_state);
  const autoran = row.latest_agent_run?.trigger_mode === "auto";
  return (
    <div className="space-y-1">
      <Badge variant={runStateVariant(row.display_run_state)}>{label}</Badge>
      {autoran && row.latest_agent_run?.started_at && (
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <Clock3 className="h-3 w-3" />
          Autoran {formatShortTime(row.latest_agent_run.started_at)}
        </div>
      )}
    </div>
  );
}

function RowAction({ row, onStartRun, canOverrideBlocked, canMutate }: { row: AutopilotQueueRow; onStartRun: (row: AutopilotQueueRow) => void; canOverrideBlocked: boolean; canMutate: boolean }) {
  if (hasPreviewAction(row)) {
    return <PreviewRowAction row={row} canMutate={canMutate} />;
  }

  if (canMutate && canStartSession(row, canOverrideBlocked)) {
    return <Button size="sm" onClick={() => onStartRun(row)}><Play className="h-3.5 w-3.5" />Start run</Button>;
  }
  if ((row.available_action === "view_run" || row.available_action === "review") && row.latest_session) {
    return <Button size="sm" variant={row.available_action === "review" ? "default" : "outline"} asChild><Link href={`/sessions/${row.latest_session.id}`}>{row.available_action === "review" ? "Review" : "View run"}</Link></Button>;
  }
  if (row.available_action === "open_pr" && row.latest_pr) {
    return <Button size="sm" variant="outline" asChild><a href={row.latest_pr.url} target="_blank" rel="noreferrer"><ArrowUpRight className="h-3.5 w-3.5" />Open PR</a></Button>;
  }
  if (row.available_action === "retry" && row.latest_session) {
    return <Button size="sm" variant="outline" asChild><Link href={`/sessions/${row.latest_session.id}`}><RotateCcw className="h-3.5 w-3.5" />Retry</Link></Button>;
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button size="sm" variant="outline" disabled>Blocked</Button>
      </TooltipTrigger>
      <TooltipContent>{row.action_disabled_reason ?? "This issue cannot be started yet."}</TooltipContent>
    </Tooltip>
  );
}

function PreviewRowAction({ row, canMutate }: { row: AutopilotQueueRow; canMutate: boolean }) {
  const queryClient = useQueryClient();
  const preview = row.latest_preview;
  const refreshQueue = () => {
    void queryClient.invalidateQueries({ queryKey: ["autopilot", "queue"] });
  };
  const startLatest = useMutation({
    mutationFn: () => api.previews.startLatest(preview?.target_id ?? ""),
    onSuccess: refreshQueue,
    onError: (error) => {
      console.error("Failed to start latest preview", error);
    },
  });
  const retry = useMutation({
    mutationFn: () => preview?.preview_id
      ? api.previews.restart(preview.preview_id, { start_latest: true })
      : api.previews.startLatest(preview?.target_id ?? ""),
    onSuccess: refreshQueue,
    onError: (error) => {
      console.error("Failed to retry preview", error);
    },
  });

  if (!preview) {
    return null;
  }

  const previewUrl = previewURLForID(preview.preview_id);
  const canOpen = Boolean(preview.preview_id && previewUrl);
  const isCurrent = !preview.new_commits_available;
  const isOpenable = preview.status === "ready" || preview.status === "partially_ready" || preview.status === "unhealthy";

  if (preview.new_commits_available) {
    return (
      <div className="flex justify-end gap-2">
        {canOpen && isOpenable ? (
          <OpenPreviewButton
            previewId={preview.preview_id}
            previewUrl={previewUrl}
            label="Open stale preview"
            variant="outline"
            size="sm"
          />
        ) : null}
        {canMutate && (
          <Button size="sm" onClick={() => startLatest.mutate()} disabled={startLatest.isPending}>
            <Play className="h-3.5 w-3.5" />
            {startLatest.isPending ? "Updating..." : "Update to latest"}
          </Button>
        )}
      </div>
    );
  }

  if (isCurrent && isOpenable && canOpen) {
    return (
      <OpenPreviewButton
        previewId={preview.preview_id}
        previewUrl={previewUrl}
        label="Open preview"
        size="sm"
      />
    );
  }

  if (!canMutate) {
    return null;
  }

  if (preview.status === "failed" || preview.status === "unavailable") {
    return (
      <Button size="sm" onClick={() => retry.mutate()} disabled={retry.isPending}>
        <RotateCcw className="h-3.5 w-3.5" />
        {retry.isPending ? "Retrying..." : "Retry preview"}
      </Button>
    );
  }

  if (preview.status === "stopped" || preview.status === "expired" || preview.status === "target_created") {
    return (
      <Button size="sm" onClick={() => startLatest.mutate()} disabled={startLatest.isPending}>
        <Play className="h-3.5 w-3.5" />
        {startLatest.isPending ? "Starting..." : "Start preview"}
      </Button>
    );
  }

  if (preview.status === "starting") {
    return (
      <Button size="sm" disabled>
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        Starting...
      </Button>
    );
  }

  return null;
}

function hasPreviewAction(row: AutopilotQueueRow) {
  return Boolean(row.available_action === "open_pr" && row.latest_preview && row.latest_pr?.status === "open");
}

function previewURLForID(previewID?: string) {
  if (!previewID) return undefined;
  return PREVIEW_ORIGIN_TEMPLATE.replace("{id}", previewID);
}

function canStartSession(row: AutopilotQueueRow, canOverrideBlocked: boolean) {
  if (row.available_action === "start_run") return true;
  if (!canOverrideBlocked || !row.repo) return false;
  return row.available_action === "blocked" || row.available_action === "retry";
}

function StartRunSheet({
  row,
  pending,
  error,
  notes,
  onNotesChange,
  onOpenChange,
  onConfirm,
}: {
  row: AutopilotQueueRow | null;
  pending: boolean;
  error: string | null;
  notes: string;
  onNotesChange: (value: string) => void;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  return (
    <Sheet open={Boolean(row)} onOpenChange={onOpenChange}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>Start run</SheetTitle>
          <SheetDescription>Confirm the issue context before Autopilot creates a linked session.</SheetDescription>
        </SheetHeader>
        {row && (
          <div className="mt-6 space-y-5">
            <div>
              <div className="text-sm font-medium text-foreground">{row.title}</div>
              <div className="mt-1 text-xs text-muted-foreground">{row.source.key} · {row.repo?.name ?? "No repository selected"}</div>
            </div>
            <div className="grid gap-3 text-sm">
              <InfoRow label="Agent" value="Organization default" />
              <InfoRow label="Autonomy" value="Semi" />
              <InfoRow label="Token mode" value="Low" />
              <InfoRow label="Ranking" value={`${row.low_hanging_fruit.label}: ${row.low_hanging_fruit.reasons.join(", ") || "no details"}`} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="autopilot-session-notes">Session notes</Label>
              <Textarea
                id="autopilot-session-notes"
                value={notes}
                onChange={(event) => onNotesChange(event.target.value)}
                placeholder="Add extra instructions for this run"
                className="min-h-28 text-sm"
              />
            </div>
            {row.action_disabled_reason && (
              <p className={`text-sm ${row.repo ? "text-muted-foreground" : "text-destructive"}`}>
                {row.action_disabled_reason}
              </p>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button className="w-full" onClick={onConfirm} disabled={pending || !row.repo}>
              {pending ? "Starting..." : "Create session"}
            </Button>
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}

function InfoRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border/60 pb-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right text-foreground">{value}</span>
    </div>
  );
}

function buildStatusLine(fallback: string, summary?: { ranked_issue_count: number; active_run_count: number; analyzed_at?: string }) {
  if (!summary) return fallback;
  const analyzed = summary.analyzed_at ? `analyzed ${formatShortTime(summary.analyzed_at)}` : "analysis pending";
  return `Operate · ${analyzed} · ${summary.ranked_issue_count} issues ranked · ${summary.active_run_count} runs active`;
}

function runStateLabel(state: AutopilotRunState) {
  return ({
    not_started: "Not started",
    queued: "Queued",
    running: "Running",
    awaiting_input: "Awaiting input",
    needs_review: "Needs review",
    pr_open: "PR open",
    merged: "Merged",
    failed: "Failed",
    skipped: "Skipped",
  } satisfies Record<AutopilotRunState, string>)[state];
}

function sourceLabel(source: string) {
  if (source === "pm_agent") return "PM";
  if (source === "manual") return "Internal";
  return source;
}

function sourceDisplayText(row: AutopilotQueueRow) {
  if (row.source.type === "linear") return linearIdentifierForRow(row);
  if (row.source.type === "manual") return "Internal";
  return row.source.key ? `${sourceLabel(row.source.type)} · ${shortSourceKey(row.source.key)}` : sourceLabel(row.source.type);
}

function linearIdentifierForRow(row: AutopilotQueueRow) {
  const key = row.source.key.trim();
  if (linearIdentifierPattern.test(key)) return key;
  return row.title.match(linearTitleIdentifierPattern)?.[1] ?? "Linear";
}

function shortSourceKey(key: string) {
  if (key.length <= 18) return key;
  return key.slice(0, 12);
}

function runStateVariant(state: AutopilotRunState): "default" | "secondary" | "outline" | "destructive" | "success" {
  if (state === "running" || state === "queued") return "default";
  if (state === "awaiting_input" || state === "needs_review") return "secondary";
  if (state === "pr_open" || state === "merged") return "success";
  if (state === "failed") return "destructive";
  return "outline";
}

function fruitBadgeVariant(label: string): "default" | "secondary" | "outline" | "success" {
  if (label === "Very high") return "default";
  if (label === "High") return "success";
  if (label === "Medium") return "secondary";
  return "outline";
}

function metricBadgeVariant(label: string): "default" | "secondary" | "outline" | "success" {
  if (label === "High") return "success";
  if (label === "Medium") return "secondary";
  return "outline";
}

function formatShortTime(value: string) {
  return formatDateTime(value, { fallback: "recently" });
}
