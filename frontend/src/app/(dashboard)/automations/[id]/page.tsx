"use client";

import { useMemo, useRef, useState } from "react";
import dynamic from "next/dynamic";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Play, Pause, Loader2 } from "lucide-react";
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
import { MobileBackButton } from "@/components/mobile-back-button";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { BranchPicker } from "@/components/branch-picker";
import { AutomationModelSelect } from "@/components/automation-model-select";
import { api } from "@/lib/api";
import { AUTOMATION_GOAL_MAX_LENGTH, automationGoalLengthState } from "@/lib/automation-validation";
import type { Automation } from "@/lib/types";
import { cn } from "@/lib/utils";
import { RunsTab } from "./runs-tab";
import {
  browserTimezone,
  formatRunAtWithTimezone,
  hourOptions,
  minuteOptions,
  splitRunAt,
} from "../schedule-time";
import { TimezonePicker } from "../timezone-picker";

// Defer recharts (the only dep here that's expensive) into its own chunk.
const AutomationStatsCard = dynamic(
  () => import("./automation-stats-card").then((m) => ({ default: m.AutomationStatsCard })),
  {
    ssr: false,
    loading: () => <div className="h-48 bg-muted/20 animate-pulse rounded-lg" />,
  },
);

// Single source of truth for interval unit values. Kept as a tuple so we can
// derive the union type for state AND runtime-validate incoming Select values
// without an unsafe `as` cast. Adding a unit means updating this tuple only.
const INTERVAL_UNITS = ["hours", "days", "weeks"] as const;
type IntervalUnit = (typeof INTERVAL_UNITS)[number];
const toIntervalUnit = (v: string, fallback: IntervalUnit): IntervalUnit =>
  (INTERVAL_UNITS as readonly string[]).includes(v) ? (v as IntervalUnit) : fallback;

function SettingsTab({ automation }: { automation: Automation }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(automation.name);
  const [goal, setGoal] = useState(automation.goal);
  const [scope, setScope] = useState(automation.scope ?? "");
  const [intervalValue, setIntervalValue] = useState(automation.interval_value ?? 1);
  const [intervalUnit, setIntervalUnit] = useState<IntervalUnit>(
    toIntervalUnit(automation.interval_unit ?? "days", "days"),
  );
  // Form state is seeded from the automation prop on first mount only. The
  // parent polls every 10s and will refetch into a new `automation` object —
  // SettingsTab is keyed on `automation.updated_at` (see AutomationDetailPage
  // below) so a remote change remounts this subtree and reseeds the form.
  const initialRunAt = splitRunAt(automation.interval_run_at ?? "09:00");
  const [intervalRunHour, setIntervalRunHour] = useState(initialRunAt.hour);
  const [intervalRunMinute, setIntervalRunMinute] = useState(initialRunAt.minute);
  const [timezone, setTimezone] = useState<string>(automation.timezone || "UTC");
  // Memoised per mount: Intl.DateTimeFormat() is cheap but there's no reason
  // to re-evaluate it on every render, and stability prevents the
  // TimezonePicker's `detected` prop from changing identity.
  const detectedTimezone = useMemo(() => browserTimezone(), []);
  const [baseBranch, setBaseBranch] = useState(automation.base_branch);
  const [model, setModel] = useState<string | undefined>(automation.model_override);
  const goalLength = automationGoalLengthState(goal);

  const updateMutation = useMutation({
    mutationFn: () =>
      api.automations.update(automation.id, {
        name: name.trim(),
        goal: goal.trim(),
        scope: scope.trim() || undefined,
        interval_value: intervalValue,
        interval_unit: intervalUnit,
        interval_run_at: `${intervalRunHour}:${intervalRunMinute}`,
        timezone,
        model: model ?? "",
        base_branch: baseBranch.trim() || undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["automation", automation.id] });
    },
  });

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card p-5">
      <div className="space-y-1.5">
        <Label htmlFor="name">Name</Label>
        <Input id="name" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="space-y-1.5">
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor="goal">Goal</Label>
          <span
            className={cn(
              "text-xs tabular-nums",
              goalLength.isTooLong ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {goalLength.countText}
          </span>
        </div>
        <Textarea
          id="goal"
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          rows={3}
          maxLength={AUTOMATION_GOAL_MAX_LENGTH}
          aria-invalid={goalLength.isTooLong}
        />
        <p className={cn("text-xs", goalLength.isTooLong ? "text-destructive" : "text-muted-foreground")}>
          {goalLength.message ?? `Up to ${AUTOMATION_GOAL_MAX_LENGTH} characters.`}
        </p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="scope">
          Scope{" "}
          <span className="text-muted-foreground font-normal">(optional)</span>
        </Label>
        <Input id="scope" value={scope} onChange={(e) => setScope(e.target.value)} />
      </div>
      <div className="space-y-1.5">
        <Label id="schedule-label">Schedule</Label>
        <div className="grid gap-3 md:grid-cols-2">
          <div
            className="flex items-center gap-2"
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
          <div className="flex flex-wrap items-start gap-2 sm:items-center">
            <span className="text-sm text-muted-foreground">At</span>
            <Select value={intervalRunHour} onValueChange={setIntervalRunHour}>
              <SelectTrigger className="w-20" aria-label="Run at hour">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {hourOptions.map((h) => (
                  <SelectItem key={h} value={h}>
                    {h}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-sm text-muted-foreground">:</span>
            <Select value={intervalRunMinute} onValueChange={setIntervalRunMinute}>
              <SelectTrigger className="w-20" aria-label="Run at minute">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {minuteOptions.map((m) => (
                  <SelectItem key={m} value={m}>
                    {m}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <TimezonePicker
              value={timezone}
              onChange={setTimezone}
              detected={detectedTimezone}
              className="w-full sm:w-auto"
            />
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          Run time is in {timezone}, selectable in 5-minute increments.
        </p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="automation-model">Model</Label>
        <AutomationModelSelect
          id="automation-model"
          ariaLabel="Model"
          value={model}
          onValueChange={setModel}
        />
      </div>
      <div className="space-y-1.5">
        <Label>Base branch</Label>
        <BranchPicker
          repositoryId={automation.repository_id ?? ""}
          value={baseBranch}
          defaultBranch={automation.base_branch}
          onValueChange={setBaseBranch}
          label="Base branch"
          buttonClassName="w-full justify-between"
          contentClassName="w-[var(--radix-popover-trigger-width)]"
        />
      </div>
      <div className="flex items-center gap-3 pt-2">
        <Button
          onClick={() => updateMutation.mutate()}
          disabled={updateMutation.isPending || goalLength.isTooLong}
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
      <PageContainer size="default">
        <div className="space-y-6">
          <MobileBackButton to="/automations" label="Back to automations" />
          <div className="text-center py-12 text-sm text-muted-foreground">
            Loading...
          </div>
        </div>
      </PageContainer>
    );
  }

  if (!automation) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <MobileBackButton to="/automations" label="Back to automations" />
          <PageHeader
            title="Automation not found"
            description="This automation does not exist or has been deleted."
          />
        </div>
      </PageContainer>
    );
  }

  const scheduleTimezone = automation.timezone || "UTC";
  const schedule = automation.schedule_type === "cron" && automation.cron_expression
    ? `cron: ${automation.cron_expression} (${scheduleTimezone})`
    : `every ${automation.interval_value ?? 1} ${automation.interval_unit ?? "days"}${automation.interval_run_at ? ` at ${formatRunAtWithTimezone(automation.interval_run_at, scheduleTimezone)}` : ""}`;

  const headerDescription = automation.enabled
    ? automation.next_run_at
      ? `${schedule} · Next: ${new Date(automation.next_run_at).toLocaleString()}`
      : schedule
    : `${schedule} · Paused`;

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
    <PageContainer size="default">
      <div className="space-y-6">
        <MobileBackButton to="/automations" label="Back to automations" />
        <PageHeader
          title={automation.name}
          description={headerDescription}
          action={
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
          }
        />

        {headerError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            {headerError}
          </div>
        )}

        <AutomationStatsCard automationId={automationId} />

        <Tabs defaultValue="runs">
          <TabsList>
            <TabsTrigger value="runs">Runs</TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
          <TabsContent value="runs" className="mt-4">
            <RunsTab automationId={automationId} />
          </TabsContent>
          <TabsContent value="settings" className="mt-4">
            {/* Key on updated_at so a polling refetch that captures a remote
                edit remounts SettingsTab, reseeding its useState-from-props
                form fields. Without this, the visible values would drift
                from the server until the user manually reopens the tab. */}
            <SettingsTab key={automation.updated_at} automation={automation} />
          </TabsContent>
        </Tabs>
      </div>
    </PageContainer>
  );
}
