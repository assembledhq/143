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

const runStatusConfig: Record<AutomationRunStatus, { icon: React.ComponentType<{ className?: string }>; label: string; color: string }> = {
  pending: { icon: Clock, label: "Pending", color: "text-muted-foreground" },
  running: { icon: RefreshCw, label: "Running", color: "text-blue-500" },
  completed: { icon: CheckCircle2, label: "Completed", color: "text-green-500" },
  completed_noop: { icon: Minus, label: "No-op", color: "text-muted-foreground" },
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
        isNoop ? "border-border/50 opacity-70" : "border-border bg-background"
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

  const { data, isLoading } = useQuery({
    queryKey: ["automation-runs", automationId],
    queryFn: () => api.automations.listRuns(automationId, { limit: 25 }),
    refetchInterval: 10000,
  });

  const firstPage = data?.data ?? [];
  const firstPageCursor = data?.meta?.next_cursor || undefined;

  // Until "Load more" is clicked, the next-page cursor tracks the freshest
  // first-page poll. Once extra pages exist, the cursor reflects the last
  // mutation's response so polls don't rewind pagination.
  const cursor = extraPages.length === 0 ? firstPageCursor : loadMoreCursor;

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
  const [intervalUnit, setIntervalUnit] = useState(automation.interval_unit ?? "days");
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
        <Label>Schedule</Label>
        <div className="flex items-center gap-2 mt-1">
          <span className="text-sm text-muted-foreground">Run every</span>
          <Input
            type="number"
            min={1}
            max={365}
            value={intervalValue}
            onChange={(e) => setIntervalValue(parseInt(e.target.value) || 1)}
            className="w-20"
          />
          <Select value={intervalUnit} onValueChange={(v) => setIntervalUnit(v as "hours" | "days" | "weeks")}>
            <SelectTrigger className="w-28">
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
      <Button
        onClick={() => updateMutation.mutate()}
        disabled={updateMutation.isPending}
      >
        {updateMutation.isPending && <Loader2 className="h-4 w-4 mr-2 animate-spin" />}
        Save changes
      </Button>
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
              <Pause className="h-3.5 w-3.5 mr-1.5" />
              Pause
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              onClick={() => resumeMutation.mutate()}
              disabled={resumeMutation.isPending}
            >
              <Play className="h-3.5 w-3.5 mr-1.5" />
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
