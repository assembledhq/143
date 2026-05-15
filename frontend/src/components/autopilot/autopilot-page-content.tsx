"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { parseAsString, useQueryState } from "nuqs";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, ArrowUpRight, CheckCircle2, Clock3, GitPullRequest, Play, RotateCcw, Search, SlidersHorizontal } from "lucide-react";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { AutopilotConfigFooter } from "./autopilot-config-footer";
import { useAutopilotPageData } from "./use-autopilot-page-data";
import { useAnalyze } from "@/hooks/use-analyze";
import { AutopilotSteeringSheet } from "./autopilot-steering-sheet";
import { AutopilotDocumentsSheet } from "./autopilot-documents-sheet";
import { AutopilotProposalCard } from "@/components/autopilot-proposal-card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { AutopilotQueueRow, AutopilotRunState } from "@/lib/types";

const ALL_VALUE = "all";

const SOURCE_OPTIONS = [
  { value: ALL_VALUE, label: "All sources" },
  { value: "linear", label: "Linear" },
  { value: "sentry", label: "Sentry" },
  { value: "manual", label: "Internal" },
  { value: "pm_agent", label: "PM agent" },
];

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

export function AutopilotPageContent() {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [showDirectionEditor, setShowDirectionEditor] = useState(false);
  const [showDocumentsEditor, setShowDocumentsEditor] = useState(false);
  const [selectedIssue, setSelectedIssue] = useState<AutopilotQueueRow | null>(null);
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

  useEffect(() => {
    if (!isLoading && !isSetupComplete) {
      router.replace("/onboarding");
    }
  }, [isLoading, isSetupComplete, router]);

  const startRunMutation = useMutation({
    mutationFn: (issueId: string) => api.issues.triggerFix(issueId, { autonomy_level: "semi", token_mode: "low" }),
    onSuccess: () => {
      setSelectedIssue(null);
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
  const topIssue = rows.find((row) => row.id === summary?.top_issue_id) ?? rows[0];
  const statusLine = buildStatusLine(viewModel.statusLine, summary);

  return (
    <PageContainer size="wide">
      <TooltipProvider>
      <div className="space-y-6">
        <PageHeader
          title="Autopilot"
          subtitle={statusLine}
          action={
            <Button onClick={handleAnalyze} disabled={isAnalyzing || isPending}>
              {isAnalyzing || isPending ? "Running..." : "Run analysis"}
            </Button>
          }
        />

        <SummaryStrip topIssue={topIssue} summary={summary} />

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
        />

        <AutopilotProposalCard />

        <AutopilotConfigFooter
          directionSummary={viewModel.directionSummary}
          focusAreas={viewModel.focusAreas}
          documentsSummary={viewModel.documentsSummary}
          weightsSummary={viewModel.weightsSummary}
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
          onOpenChange={(open) => {
            if (!open) setSelectedIssue(null);
          }}
          onConfirm={() => {
            if (selectedIssue) startRunMutation.mutate(selectedIssue.id);
          }}
        />
      </div>
      </TooltipProvider>
    </PageContainer>
  );
}

function SummaryStrip({ topIssue, summary }: { topIssue?: AutopilotQueueRow; summary?: { autorunnable_count: number; needs_review_count: number; open_pr_count: number; active_run_count: number; ranked_issue_count: number } }) {
  const cards = [
    {
      label: "Top opportunity",
      value: topIssue ? topIssue.title : "None",
      detail: topIssue ? `${topIssue.low_hanging_fruit.label} · ${topIssue.source.key}` : "No ranked issues right now",
      icon: CheckCircle2,
    },
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
      label: "PRs open",
      value: String(summary?.open_pr_count ?? 0),
      detail: `${summary?.active_run_count ?? 0} active runs`,
      icon: GitPullRequest,
    },
  ];

  return (
    <div className="grid gap-3 md:grid-cols-4">
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
}: {
  rows: AutopilotQueueRow[];
  loading: boolean;
  hasNextPage: boolean;
  loadingNextPage: boolean;
  onLoadMore: () => void;
  onStartRun: (row: AutopilotQueueRow) => void;
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-14">Rank</TableHead>
              <TableHead>Issue</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Customer impact</TableHead>
              <TableHead>Ease</TableHead>
              <TableHead>Low-hanging fruit</TableHead>
              <TableHead>Run state</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.id}>
                <TableCell className="font-medium text-muted-foreground">#{row.rank}</TableCell>
                <TableCell className="min-w-[260px] whitespace-normal">
                  <div className="font-medium text-foreground">{row.title}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span>{row.repo?.name ?? "No repo"}</span>
                    <span>{row.issue_status}</span>
                    {row.low_hanging_fruit.cluster_size > 1 && <span>{row.low_hanging_fruit.cluster_size} related</span>}
                  </div>
                </TableCell>
                <TableCell><SourceBadge row={row} /></TableCell>
                <TableCell>{row.customer_impact.label} · {row.customer_impact.count}</TableCell>
                <TableCell>{row.implementation_ease}</TableCell>
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
                <TableCell className="text-right"><RowAction row={row} onStartRun={onStartRun} /></TableCell>
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

function SourceBadge({ row }: { row: AutopilotQueueRow }) {
  return <Badge variant="outline" className="capitalize">{sourceLabel(row.source.type)} · {row.source.key}</Badge>;
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

function RowAction({ row, onStartRun }: { row: AutopilotQueueRow; onStartRun: (row: AutopilotQueueRow) => void }) {
  if (row.available_action === "start_run") {
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

function StartRunSheet({ row, pending, error, onOpenChange, onConfirm }: { row: AutopilotQueueRow | null; pending: boolean; error: string | null; onOpenChange: (open: boolean) => void; onConfirm: () => void }) {
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
            {row.action_disabled_reason && <p className="text-sm text-destructive">{row.action_disabled_reason}</p>}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button className="w-full" onClick={onConfirm} disabled={pending || Boolean(row.action_disabled_reason)}>
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

function formatShortTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "recently";
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(date);
}
