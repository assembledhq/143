"use client";

import { useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  RefreshCw,
  Play,
  Pause,
  AlertTriangle,
  CheckCircle2,
  Clock,
  Minus,
  Loader2,
} from "lucide-react";
import { useParams, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { Automation, AutomationRun, AutomationRunStatus } from "@/lib/types";

// Single source of truth for interval unit values. Kept as a tuple so we can
// derive the union type for state AND runtime-validate incoming Select values
// without an unsafe `as` cast. Adding a unit means updating this tuple only.
const INTERVAL_UNITS = ["hours", "days", "weeks"] as const;
type IntervalUnit = (typeof INTERVAL_UNITS)[number];
const toIntervalUnit = (v: string, fallback: IntervalUnit): IntervalUnit =>
  (INTERVAL_UNITS as readonly string[]).includes(v) ? (v as IntervalUnit) : fallback;

const runStatusConfig: Record<AutomationRunStatus, { icon: React.ComponentType<{ className?: string }>; label: string; color: string }> = {
  pending: { icon: Clock, label: "Pending", color: "text-muted-foreground" },
  running: { icon: RefreshCw, label: "Running", color: "text-blue-500" },
  completed: { icon: CheckCircle2, label: "Completed", color: "text-green-500" },
  completed_noop: { icon: Minus, label: "Not executed", color: "text-amber-600 dark:text-amber-500" },
  failed: { icon: AlertTriangle, label: "Failed", color: "text-red-500" },
  skipped: { icon: Minus, label: "Skipped", color: "text-muted-foreground" },
};

function RunCard({ run }: { run: AutomationRun }) {
  const cfg = runStatusConfig[run.status] || runStatusConfig.pending;
  const Icon = cfg.icon;
  const isFailed = run.status === "failed";
  const isNoop = run.status === "completed_noop";

  return (
    <div
      className={cn(
        "rounded-lg border p-4",
        isFailed ? "border-red-200 bg-red-50/50 dark:border-red-900/30 dark:bg-red-950/20" :
        isNoop ? "border-amber-200 bg-amber-50/50 dark:border-amber-900/30 dark:bg-amber-950/20" : "border-border bg-background"
      )}
    >
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <Icon className={cn("h-4 w-4", cfg.color)} />
          <span className="text-sm font-medium">{cfg.label}</span>
          <span className="text-xs text-muted-foreground">
            {new Date(run.triggered_at).toLocaleString()}
          </span>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          {run.triggered_by === "manual" && (
            <span className="rounded-full bg-muted px-2 py-0.5 text-xs">Manual</span>
          )}
          {run.completed_at && (
            <span>
              {Math.round(
                (new Date(run.completed_at).getTime() - new Date(run.triggered_at).getTime()) / 1000
              )}s
            </span>
          )}
        </div>
      </div>
      {run.result_summary && (
        <p className="text-sm text-muted-foreground mt-1">{run.result_summary}</p>
      )}
    </div>
  );
}

function RunsTab({ automationId }: { automationId: string }) {
  // Pages are stored as a list of result pages so the polling refetch only
  // replaces page 0 (latest runs) and any pages loaded via "Load more" persist
  // across refetches. Using setState inside `select` would reset pagination on
  // every poll tick.
  const [extraPages, setExtraPages] = useState<AutomationRun[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>(undefined);

  // Pause polling once the user paginates. If polling kept running while
  // extra pages were loaded, any new run arriving at the top would shift the
  // window and make the stored loadMoreCursor point into the middle of a
  // now-different result set — producing skipped or duplicated runs on the
  // next "Load more". Users who want fresh first-page runs can reload or
  // re-navigate to the tab.
  const isPaginated = extraPages.length > 0;

  const { data, isLoading } = useQuery({
    queryKey: ["automation-runs", automationId],
    queryFn: () => api.automations.listRuns(automationId, { limit: 25 }),
    refetchInterval: isPaginated ? false : 10000,
  });

  const firstPage = data?.data ?? [];
  const firstPageCursor = data?.meta?.next_cursor || undefined;

  // Before the user paginates, the cursor tracks the freshest first-page poll.
  // Once extra pages exist, polling is disabled (see above) and the cursor
  // comes from the most recent Load-more response.
  const cursor = isPaginated ? loadMoreCursor : firstPageCursor;

  const loadMoreMutation = useMutation({
    mutationFn: () => api.automations.listRuns(automationId, { limit: 25, cursor }),
    onSuccess: (res) => {
      setExtraPages((prev) => [...prev, res.data ?? []]);
      setLoadMoreCursor(res.meta?.next_cursor || undefined);
    },
  });

  const allRuns = [firstPage, ...extraPages].flat();
  const hasMore = !!cursor;

  return (
    <div className="space-y-3">
      {isLoading && (
        <div className="text-center py-8 text-sm text-muted-foreground">
          Loading runs...
        </div>
      )}
      {!isLoading && allRuns.length === 0 && (
        <div className="text-center py-8 text-sm text-muted-foreground">
          No runs yet. The first run will appear after the scheduled time.
        </div>
      )}
      {allRuns.map((run) => (
        <RunCard key={run.id} run={run} />
      ))}
      {loadMoreMutation.isError && (
        <p className="text-center text-xs text-destructive">
          Failed to load more runs. Please try again.
        </p>
      )}
      {hasMore && (
        <Button
          variant="ghost"
          size="sm"
          className="w-full"
          onClick={() => loadMoreMutation.mutate()}
          disabled={loadMoreMutation.isPending}
        >
          {loadMoreMutation.isPending ? "Loading..." : "Load more"}
        </Button>
      )}
    </div>
  );
}

function SettingsTab({ automation }: { automation: Automation }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(automation.name);
  const [goal, setGoal] = useState(automation.goal);
  const [scope, setScope] = useState(automation.scope ?? "");
  const [intervalValue, setIntervalValue] = useState(automation.interval_value ?? 1);
  const [intervalUnit, setIntervalUnit] = useState<IntervalUnit>(
    toIntervalUnit(automation.interval_unit ?? "days", "days"),
  );
  const [baseBranch, setBaseBranch] = useState(automation.base_branch);

  const updateMutation = useMutation({
    mutationFn: () =>
      api.automations.update(automation.id, {
        name,
        goal,
        scope: scope || undefined,
        interval_value: intervalValue,
        interval_unit: intervalUnit,
        base_branch: baseBranch,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["automation", automation.id] });
    },
  });

  return (
    <div className="space-y-4">
      <div>
        <Label htmlFor="name">Name</Label>
        <Input id="name" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div>
        <Label htmlFor="goal">Goal</Label>
        <Textarea id="goal" value={goal} onChange={(e) => setGoal(e.target.value)} rows={3} />
      </div>
      <div>
        <Label htmlFor="scope">Scope</Label>
        <Input id="scope" value={scope} onChange={(e) => setScope(e.target.value)} />
      </div>
      <div>
        <Label id="schedule-label">Schedule</Label>
        <div
          className="flex items-center gap-2 mt-1"
          role="group"
          aria-labelledby="schedule-label"
        >
          <span className="text-sm text-muted-foreground">Run every</span>
          <Input
            id="interval-value"
            aria-label="Interval value"
            type="number"
            min={1}
            max={365}
            value={intervalValue}
            onChange={(e) => {
              const parsed = parseInt(e.target.value, 10);
              setIntervalValue(Number.isNaN(parsed) ? 1 : Math.max(1, parsed));
            }}
            className="w-20"
          />
          <Select
            value={intervalUnit}
            onValueChange={(v) => setIntervalUnit(toIntervalUnit(v, intervalUnit))}
          >
            <SelectTrigger className="w-28" aria-label="Interval unit">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="hours">hours</SelectItem>
              <SelectItem value="days">days</SelectItem>
              <SelectItem value="weeks">weeks</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div>
        <Label htmlFor="baseBranch">Base branch</Label>
        <Input id="baseBranch" value={baseBranch} onChange={(e) => setBaseBranch(e.target.value)} />
      </div>
      <div className="flex items-center gap-3">
        <Button
          onClick={() => updateMutation.mutate()}
          disabled={updateMutation.isPending}
        >
          {updateMutation.isPending && <Loader2 className="h-4 w-4 mr-2 animate-spin" />}
          Save changes
        </Button>
        {updateMutation.isError && (
          <p className="text-xs text-destructive">Failed to save changes.</p>
        )}
        {updateMutation.isSuccess && !updateMutation.isPending && (
          <p className="text-xs text-muted-foreground">Saved.</p>
        )}
      </div>
    </div>
  );
}

export default function AutomationDetailPage() {
  const params = useParams();
  const router = useRouter();
  const queryClient = useQueryClient();
  const automationId = params?.id as string;

  const { data, isLoading } = useQuery({
    queryKey: ["automation", automationId],
    queryFn: () => api.automations.get(automationId),
    refetchInterval: 10000,
  });

  const automation = data?.data;

  const pauseMutation = useMutation({
    mutationFn: () => api.automations.pause(automationId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automation", automationId] }),
  });

  const resumeMutation = useMutation({
    mutationFn: () => api.automations.resume(automationId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automation", automationId] }),
  });

  // runNowInFlight guards against rapid double-clicks that can slip through
  // `disabled={runNowMutation.isPending}`: React updates `isPending` on its
  // next render tick, so two clicks in the same tick both see `isPending=false`
  // and both fire mutate(). A synchronous ref flipped inside the click handler
  // closes that window without waiting for a render.
  const runNowInFlight = useRef(false);
  const runNowMutation = useMutation({
    mutationFn: () => api.automations.runNow(automationId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["automation-runs", automationId] }),
    onSettled: () => {
      runNowInFlight.current = false;
    },
  });
  const handleRunNow = () => {
    if (runNowInFlight.current || runNowMutation.isPending) return;
    runNowInFlight.current = true;
    runNowMutation.mutate();
  };

  const deleteMutation = useMutation({
    mutationFn: () => api.automations.del(automationId),
    onSuccess: () => router.push("/automations"),
  });

  if (isLoading) {
    return (
      <div className="max-w-4xl mx-auto px-6 py-8 text-center text-sm text-muted-foreground">
        Loading...
      </div>
    );
  }

  if (!automation) {
    return (
      <div className="max-w-4xl mx-auto px-6 py-8 text-center text-sm text-muted-foreground">
        Automation not found.
      </div>
    );
  }

  const schedule = automation.schedule_type === "cron" && automation.cron_expression
    ? `cron: ${automation.cron_expression}`
    : `every ${automation.interval_value ?? 1} ${automation.interval_unit ?? "days"}`;

  // Surface the most recent failure across the header mutations. These are
  // user-initiated actions (pause/resume/run now/delete) so silent failure is
  // worse than a potentially stale banner — the user needs to know the click
  // did not take effect before deciding whether to retry.
  const headerError =
    pauseMutation.isError ? "Failed to pause automation." :
    resumeMutation.isError ? "Failed to resume automation." :
    runNowMutation.isError ? "Failed to trigger run." :
    deleteMutation.isError ? "Failed to delete automation." :
    null;

  return (
    <div className="max-w-4xl mx-auto px-6 py-8">
      {/* Header */}
      <div className="flex items-start justify-between mb-6">
        <div>
          <div className="flex items-center gap-2.5 mb-1">
            <RefreshCw className={cn("h-5 w-5", automation.enabled ? "text-blue-500" : "text-muted-foreground")} />
            <h1 className="text-lg font-semibold text-foreground">{automation.name}</h1>
          </div>
          <div className="flex items-center gap-3 text-sm text-muted-foreground">
            <span>{schedule}</span>
            {automation.next_run_at && automation.enabled && (
              <>
                <span>&middot;</span>
                <span>Next: {new Date(automation.next_run_at).toLocaleString()}</span>
              </>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          {automation.enabled ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => pauseMutation.mutate()}
              disabled={pauseMutation.isPending}
            >
              {pauseMutation.isPending ? (
                <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
              ) : (
                <Pause className="h-3.5 w-3.5 mr-1.5" />
              )}
              Pause
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              onClick={() => resumeMutation.mutate()}
              disabled={resumeMutation.isPending}
            >
              {resumeMutation.isPending ? (
                <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
              ) : (
                <Play className="h-3.5 w-3.5 mr-1.5" />
              )}
              Resume
            </Button>
          )}
          <Button
            size="sm"
            onClick={handleRunNow}
            disabled={runNowMutation.isPending}
          >
            {runNowMutation.isPending ? (
              <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
            ) : (
              <Play className="h-3.5 w-3.5 mr-1.5" />
            )}
            Run now
          </Button>
        </div>
      </div>

      {headerError && (
        <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
          {headerError}
        </div>
      )}

      {/* Tabs */}
      <Tabs defaultValue="runs">
        <TabsList>
          <TabsTrigger value="runs">Runs</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>
        <TabsContent value="runs" className="mt-4">
          <RunsTab automationId={automationId} />
        </TabsContent>
        <TabsContent value="settings" className="mt-4">
          <SettingsTab automation={automation} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
