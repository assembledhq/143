"use client";

import Link from "next/link";
import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, Loader2 } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import { agentTypeForModel } from "@/lib/agents";
import { AUTOMATION_GOAL_MAX_LENGTH, automationGoalLengthState } from "@/lib/automation-validation";
import { BranchPicker } from "@/components/branch-picker";
import { AutomationModelSelect } from "@/components/automation-model-select";
import { NoReposWarning } from "@/components/no-repos-warning";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { cn } from "@/lib/utils";
import {
  automationTemplates,
  featuredAutomationTemplateIDs,
  getAutomationTemplate,
} from "@/lib/automation-templates";
import {
  getCodingAgentReasoningOptions,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import {
  browserTimezone,
  hourOptions,
  minuteOptions,
} from "../schedule-time";
import { TimezonePicker } from "../timezone-picker";

export default function NewAutomationPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const initialTemplate = getAutomationTemplate(searchParams.get("template") ?? "");

  // Form state
  const [name, setName] = useState(initialTemplate?.name ?? "");
  const [goal, setGoal] = useState(initialTemplate?.goal ?? "");
  const [scope, setScope] = useState("");
  const [selectedRepoId, setSelectedRepoId] = useState("");
  const [intervalValue, setIntervalValue] = useState(initialTemplate?.defaultInterval ?? 1);
  const [intervalUnit, setIntervalUnit] = useState<"hours" | "days" | "weeks">(
    initialTemplate?.defaultUnit ?? "days",
  );
  const [intervalRunHour, setIntervalRunHour] = useState("09");
  const [intervalRunMinute, setIntervalRunMinute] = useState("00");
  // Default to the viewer's detected IANA zone, but let them override via
  // TimezonePicker for the "schedule in a different region" case.
  const [detectedTimezone] = useState<string>(() => browserTimezone());
  const [timezone, setTimezone] = useState<string>(detectedTimezone);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [baseBranchByRepoId, setBaseBranchByRepoId] = useState<Record<string, string>>({});
  const [model, setModel] = useState<string | undefined>(undefined);
  const [identityScope, setIdentityScope] = useState<"org" | "personal">("org");
  const [reasoningEffort, setReasoningEffort] = useState<CodingAgentReasoningEffort>("");
  const [priority, setPriority] = useState(50);
  const [redirecting, setRedirecting] = useState(false);

  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const settings = (settingsResponse?.data?.settings ?? {}) as { default_agent_type?: string };

  // Load repos
  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });
  const repos = reposData?.data ?? [];

  // Fall back to the first repo until the user picks one so the form has a
  // valid default without syncing state inside an effect.
  const repoId = selectedRepoId || repos[0]?.id || "";
  const selectedRepo = repos.find((repo) => repo.id === repoId);
  const selectedBaseBranch = repoId
    ? baseBranchByRepoId[repoId] ?? selectedRepo?.default_branch ?? ""
    : "";
  const defaultAgentType = settings.default_agent_type ?? "codex";
  const effectiveAgentType = model ? agentTypeForModel(model) ?? defaultAgentType : defaultAgentType;
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);

  const applyTemplate = (templateId: string) => {
    const t = getAutomationTemplate(templateId);
    if (!t) return;
    setName(t.name);
    setGoal(t.goal);
    setIntervalValue(t.defaultInterval);
    setIntervalUnit(t.defaultUnit);
  };

  const createMutation = useMutation({
    mutationFn: () =>
      api.automations.create({
        name: name.trim(),
        goal: goal.trim(),
        repository_id: repoId,
        scope: scope.trim() || undefined,
        interval_value: intervalValue,
        interval_unit: intervalUnit,
        interval_run_at: `${intervalRunHour}:${intervalRunMinute}`,
        timezone,
        model,
        identity_scope: identityScope,
        ...(showReasoningSelector && reasoningEffort ? { reasoning_effort: reasoningEffort } : {}),
        base_branch: selectedBaseBranch.trim() || undefined,
        priority,
      }),
    onSuccess: (res) => {
      setRedirecting(true);
      router.push(`/automations/${res.data.id}`);
    },
  });

  if (repos.length === 0 && reposData) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader
            title="New automation"
            description="Recurring agents that run on a schedule for your team."
          />
          <NoReposWarning />
        </div>
      </PageContainer>
    );
  }

  const goalLength = automationGoalLengthState(goal);
  const canSubmit =
    name.trim().length > 0 &&
    goal.trim().length > 0 &&
    !goalLength.isTooLong &&
    repoId.length > 0;

  const featuredTemplates = automationTemplates.filter((template) =>
    featuredAutomationTemplateIDs.includes(template.id),
  );

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="New automation"
          description="Recurring agents that run on a schedule for your team."
        />

        {/* Templates */}
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <Label className="text-xs text-muted-foreground">
              Start from a template
            </Label>
            <Button asChild variant="ghost" size="sm" className="h-7 px-2 text-xs">
              <Link href="/automations/templates">Browse all templates</Link>
            </Button>
          </div>

          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {featuredTemplates.map((t) => {
              const Icon = t.icon;
              return (
                <Card
                  key={t.id}
                  className={cn(
                    "transition-colors",
                    name === t.name && "border-primary bg-primary/5"
                  )}
                >
                  <CardHeader className="space-y-2">
                    <div className="flex items-center gap-2">
                      <div className="rounded-md border border-border bg-muted/50 p-2">
                        <Icon className="h-4 w-4" />
                      </div>
                      <div className="min-w-0">
                        <CardTitle className="text-sm">{t.name}</CardTitle>
                        <CardDescription className="line-clamp-2">
                          {t.summary}
                        </CardDescription>
                      </div>
                    </div>
                  </CardHeader>
                  <CardContent className="space-y-3">
                    <div className="flex flex-wrap gap-2">
                      {t.tags.slice(0, 3).map((tag) => (
                        <span
                          key={tag}
                          className="rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground"
                        >
                          {tag}
                        </span>
                      ))}
                    </div>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => applyTemplate(t.id)}
                    >
                      Use template
                    </Button>
                  </CardContent>
                </Card>
              );
            })}
          </div>
        </div>

        {/* Main form */}
        <div className="space-y-4 rounded-lg border border-border bg-card p-5">
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Find flaky tests"
            />
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
              placeholder="Describe what the automation should do each run..."
              rows={3}
              maxLength={AUTOMATION_GOAL_MAX_LENGTH}
              aria-invalid={goalLength.isTooLong}
            />
            <p className={cn("text-xs", goalLength.isTooLong ? "text-destructive" : "text-muted-foreground")}>
              {goalLength.message ?? `Up to ${AUTOMATION_GOAL_MAX_LENGTH.toLocaleString("en-US")} characters.`}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="scope">
              Scope{" "}
              <span className="text-muted-foreground font-normal">
                (optional)
              </span>
            </Label>
            <Input
              id="scope"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder="e.g. src/payments/, tests/"
            />
          </div>

          <div className="space-y-1.5">
            <Label>Repository</Label>
            <Select value={repoId} onValueChange={setSelectedRepoId}>
              <SelectTrigger>
                <SelectValue placeholder="Select a repository" />
              </SelectTrigger>
              <SelectContent>
                {repos.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>
                    {repo.full_name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <Label>Run as</Label>
            <Select value={identityScope} onValueChange={(value: "org" | "personal") => setIdentityScope(value)}>
              <SelectTrigger aria-label="Run as">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="org">Organization</SelectItem>
                <SelectItem value="personal">Personal</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Choose whether this automation runs with organization credentials and opens PRs as 143-bot, or uses the creator&apos;s coding-agent preferences and GitHub identity.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label id="schedule-label">Schedule</Label>
            <div className="grid gap-3 md:grid-cols-2">
              <div
                className="flex items-center gap-2"
                role="group"
                aria-labelledby="schedule-label"
              >
                <span className="text-sm font-medium leading-none text-muted-foreground">Run every</span>
                <Input
                  id="interval-value"
                  aria-label="Interval value"
                  type="number"
                  min={1}
                  max={365}
                  value={intervalValue}
                  onChange={(e) => {
                    const parsed = parseInt(e.target.value, 10);
                    setIntervalValue(
                      Number.isNaN(parsed) ? 1 : Math.max(1, parsed),
                    );
                  }}
                  className="w-20"
                />
                <Select
                  value={intervalUnit}
                  onValueChange={(v) => {
                    if (v === "hours" || v === "days" || v === "weeks") {
                      setIntervalUnit(v);
                    }
                  }}
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
                <span className="text-sm font-medium leading-none text-muted-foreground">At</span>
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
          </div>

          {/* Advanced settings */}
          <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
            <CollapsibleTrigger className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors py-1">
              <ChevronDown
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  advancedOpen && "rotate-180",
                )}
              />
              Advanced options
            </CollapsibleTrigger>
            <CollapsibleContent className="space-y-4 pt-3">
              <div className="space-y-1.5">
                <Label>Base branch</Label>
                <BranchPicker
                  repositoryId={repoId}
                  value={selectedBaseBranch}
                  defaultBranch={selectedRepo?.default_branch}
                  onValueChange={(branch) =>
                    setBaseBranchByRepoId((prev) => ({ ...prev, [repoId]: branch }))
                  }
                  label="Base branch"
                  disabled={!repoId}
                  buttonClassName="w-full justify-between"
                  contentClassName="w-[var(--radix-popover-trigger-width)]"
                />
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
              {showReasoningSelector ? (
                <div className="space-y-1.5">
                  <Label htmlFor="automation-reasoning">Reasoning</Label>
                  <Select
                    value={reasoningEffort || "__default__"}
                    onValueChange={(value) => setReasoningEffort(value === "__default__" ? "" : toCodingAgentReasoningEffort(value))}
                  >
                    <SelectTrigger id="automation-reasoning" aria-label="Reasoning">
                      <SelectValue placeholder="Default reasoning" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="__default__">Default reasoning</SelectItem>
                      {reasoningOptions.map((option) => (
                        <SelectItem key={option.value} value={option.value}>
                          {option.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              ) : null}
              <div className="space-y-1.5">
                <Label>Priority</Label>
                <Select
                  value={String(priority)}
                  onValueChange={(v) => setPriority(parseInt(v, 10))}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="0">Critical</SelectItem>
                    <SelectItem value="25">High</SelectItem>
                    <SelectItem value="50">Medium</SelectItem>
                    <SelectItem value="75">Low</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CollapsibleContent>
          </Collapsible>

          {/* Submit */}
          <div className="flex items-center gap-3 pt-2">
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!canSubmit || createMutation.isPending || redirecting}
            >
              {createMutation.isPending || redirecting ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Creating...
                </>
              ) : (
                "Create automation"
              )}
            </Button>
            {createMutation.isError && (
              <p className="text-xs text-destructive">
                Failed to create automation. Please try again.
              </p>
            )}
          </div>
        </div>
      </div>
    </PageContainer>
  );
}
