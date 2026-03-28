"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  Sparkles,
  Timer,
  Bot,
  ShieldCheck,
  TestTube2,
  Wrench,
  ChevronDown,
  Loader2,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
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
import { AGENT_TYPE_OPTIONS } from "@/lib/model-constants";
import { NoReposWarning } from "@/components/no-repos-warning";
import { cn } from "@/lib/utils";
import type { OrgSettings, Organization, SingleResponse } from "@/lib/types";

const PRIORITY_OPTIONS = [
  { value: "low", label: "Low", numeric: 75 },
  { value: "medium", label: "Medium", numeric: 50 },
  { value: "high", label: "High", numeric: 25 },
  { value: "critical", label: "Critical", numeric: 0 },
] as const;

type PriorityLevel = (typeof PRIORITY_OPTIONS)[number]["value"];

function priorityLevelToNumeric(level: PriorityLevel): number {
  return PRIORITY_OPTIONS.find((o) => o.value === level)!.numeric;
}

interface ScheduledTemplate {
  id: string;
  name: string;
  goal: string;
  description: string;
  scheduleInterval: number;
  scheduleUnit: "hours" | "days" | "weeks";
  icon: React.ComponentType<{ className?: string }>;
}

const SCHEDULED_TEMPLATES: ScheduledTemplate[] = [
  {
    id: "flaky-tests",
    name: "Find flaky tests",
    goal: "Identify flaky tests from recent failures, reproduce nondeterminism, and propose or implement deterministic test fixes with minimal behavior change.",
    description: "Detect nondeterministic tests and make them stable.",
    scheduleInterval: 1,
    scheduleUnit: "days",
    icon: TestTube2,
  },
  {
    id: "security-sweep",
    name: "Security sweep",
    goal: "Review recent changes and open issues to identify concrete security vulnerabilities, then propose high-confidence remediation steps and tests.",
    description: "Scan for exploit paths and prioritize high-risk remediations.",
    scheduleInterval: 7,
    scheduleUnit: "days",
    icon: ShieldCheck,
  },
  {
    id: "codebase-maintenance",
    name: "Codebase maintenance",
    goal: "Identify high-leverage maintenance opportunities that reduce operational risk, improve reliability, or reduce long-term complexity without broad rewrites.",
    description: "Pay down technical debt with targeted, low-risk improvements.",
    scheduleInterval: 3,
    scheduleUnit: "days",
    icon: Wrench,
  },
  {
    id: "linear-triage",
    name: "Triage Linear backlog",
    goal: "Analyze current issue context, prioritize work by impact and urgency, and cluster related items into actionable follow-up tasks.",
    description: "Reprioritize and cluster incoming work from Linear.",
    scheduleInterval: 1,
    scheduleUnit: "days",
    icon: Bot,
  },
];

export default function NewProjectPage() {
  const router = useRouter();

  // AI description
  const [description, setDescription] = useState("");

  // Form fields
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [scope, setScope] = useState("");
  const [completionCriteria, setCompletionCriteria] = useState("");
  const [repositoryId, setRepositoryId] = useState("");
  const [executionMode, setExecutionMode] = useState("sequential");
  const [maxConcurrent, setMaxConcurrent] = useState(2);
  const [priorityLevel, setPriorityLevel] = useState<PriorityLevel>("medium");
  const [baseBranch, setBaseBranch] = useState("main");
  const [agentType, setAgentType] = useState("");
  const [selectedModel, setSelectedModel] = useState("");
  const [hasGenerated, setHasGenerated] = useState(false);

  // Schedule
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleInterval, setScheduleInterval] = useState(1);
  const [scheduleUnit, setScheduleUnit] = useState<
    "hours" | "days" | "weeks"
  >("days");

  // Advanced section
  const [showAdvanced, setShowAdvanced] = useState(false);

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgentType = settings?.default_agent_type ?? "codex";
  const effectiveAgentType = agentType || defaultAgentType;

  const availableModels = useMemo(() => {
    const agent = AGENT_TYPE_OPTIONS.find((a) => a.key === effectiveAgentType);
    return agent?.models ?? [];
  }, [effectiveAgentType]);

  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const repos = reposData?.data ?? [];

  const generateMutation = useMutation({
    mutationFn: () =>
      api.projects.aiGenerate({ description: description.trim() }),
    onSuccess: (response) => {
      const gen = response.data;
      setTitle(gen.title);
      setGoal(gen.goal);
      setScope(gen.scope ?? "");
      setCompletionCriteria(gen.completion_criteria ?? "");
      setExecutionMode(gen.execution_mode || "sequential");
      setHasGenerated(true);
    },
  });

  const createMutation = useMutation({
    mutationFn: () =>
      api.projects.create({
        title: title.trim(),
        goal: goal.trim(),
        repository_id: repositoryId,
        scope: scope.trim() || undefined,
        completion_criteria: !scheduleEnabled
          ? completionCriteria.trim() || undefined
          : undefined,
        execution_mode: executionMode,
        max_concurrent:
          executionMode === "parallel" ? maxConcurrent : undefined,
        priority: priorityLevelToNumeric(priorityLevel),
        base_branch: baseBranch.trim() || undefined,
        agent_type: agentType || undefined,
        model: selectedModel || undefined,
        schedule_enabled: scheduleEnabled ? true : undefined,
        schedule_interval: scheduleEnabled ? scheduleInterval : undefined,
        schedule_unit: scheduleEnabled ? scheduleUnit : undefined,
      }),
    onSuccess: (response) => {
      router.push(`/projects/${response.data.id}`);
    },
  });

  function applyTemplate(template: ScheduledTemplate) {
    setTitle(template.name);
    setGoal(template.goal);
    setScheduleEnabled(true);
    setScheduleInterval(template.scheduleInterval);
    setScheduleUnit(template.scheduleUnit);
  }

  function handleDescriptionKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (description.trim().length > 0 && !generateMutation.isPending) {
        generateMutation.mutate();
      }
    }
  }

  const canSubmit =
    title.trim().length > 0 &&
    goal.trim().length > 0 &&
    repositoryId.length > 0;

  return (
    <div className="p-6 max-w-2xl mx-auto">
      <div className="space-y-6">
        <div>
          <h1 className="text-xl font-bold text-foreground">New project</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Describe what you want to build and we&apos;ll set it up for you.
          </p>
        </div>

        {/* ── AI Description Input ─────────────────────────────── */}
        <div className="space-y-2">
          <div className="relative">
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              onKeyDown={handleDescriptionKeyDown}
              placeholder='Describe your project in plain language, e.g. "Add dark mode support across the entire app with an OS-preference toggle in settings"'
              rows={3}
              className="pr-24 resize-none"
            />
            <Button
              size="sm"
              variant="ghost"
              onClick={() => generateMutation.mutate()}
              disabled={
                description.trim().length === 0 || generateMutation.isPending
              }
              className="absolute right-2 bottom-2 h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
            >
              {generateMutation.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Sparkles className="h-3.5 w-3.5" />
              )}
              {generateMutation.isPending ? "Generating" : "Generate"}
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground/60">
            {generateMutation.isPending
              ? "Generating project details..."
              : "Press ⌘ Enter to generate, or fill in the form directly below."}
          </p>
          {generateMutation.isError && (
            <p className="text-xs text-destructive">
              Failed to generate. Try again or fill in the form manually.
            </p>
          )}
        </div>

        {/* ── Divider ──────────────────────────────────────────── */}
        <div className="relative">
          <div className="absolute inset-0 flex items-center">
            <span className="w-full border-t" />
          </div>
          <div className="relative flex justify-center text-[11px]">
            <span className="bg-background px-3 text-muted-foreground/50">
              project details
            </span>
          </div>
        </div>

        {/* ── Main Form ────────────────────────────────────────── */}
        <div className="space-y-4">
          {hasGenerated && (
            <div className="rounded-md border border-primary/20 bg-primary/5 px-3 py-2.5 text-[13px] text-primary flex items-center gap-2">
              <Sparkles className="h-3.5 w-3.5 shrink-0" />
              Generated from your description. Review and edit as needed.
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="title">Title</Label>
            <Input
              id="title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Project title"
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="goal">Goal</Label>
            <Textarea
              id="goal"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder={
                scheduleEnabled
                  ? "What should this project do on each scheduled run?"
                  : "What should this project accomplish?"
              }
              rows={3}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="scope">
              Scope{" "}
              <span className="text-muted-foreground font-normal">
                (optional)
              </span>
            </Label>
            <Textarea
              id="scope"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder="What files or areas are in scope?"
              rows={2}
            />
          </div>

          {!scheduleEnabled && (
            <div className="space-y-1.5">
              <Label htmlFor="completion-criteria">
                Completion criteria{" "}
                <span className="text-muted-foreground font-normal">
                  (optional)
                </span>
              </Label>
              <Textarea
                id="completion-criteria"
                value={completionCriteria}
                onChange={(e) => setCompletionCriteria(e.target.value)}
                placeholder="How do we know the project is done?"
                rows={2}
              />
            </div>
          )}

          {repos.length === 0 && <NoReposWarning />}

          <div className="space-y-1.5">
            <Label>Repository</Label>
            <Select value={repositoryId} onValueChange={setRepositoryId}>
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

          {/* ── Schedule toggle ───────────────────────────────── */}
          <div className="rounded-lg border border-border p-3 space-y-3">
            <label className="flex items-center justify-between cursor-pointer">
              <div className="flex items-center gap-2">
                <Timer className="h-4 w-4 text-muted-foreground" />
                <span className="text-[13px] font-medium">
                  Run on a schedule
                </span>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={scheduleEnabled}
                onClick={() => setScheduleEnabled(!scheduleEnabled)}
                className={cn(
                  "relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors",
                  scheduleEnabled ? "bg-primary" : "bg-muted-foreground/25",
                )}
              >
                <span
                  className={cn(
                    "pointer-events-none block h-4 w-4 rounded-full bg-white shadow-sm ring-0 transition-transform",
                    scheduleEnabled ? "translate-x-4" : "translate-x-0",
                  )}
                />
              </button>
            </label>

            {scheduleEnabled && (
              <div className="space-y-3">
                <div className="flex items-center gap-2">
                  <span className="text-sm text-muted-foreground">
                    Run every
                  </span>
                  <Input
                    type="number"
                    min={1}
                    max={365}
                    value={scheduleInterval}
                    onChange={(e) =>
                      setScheduleInterval(Number(e.target.value))
                    }
                    className="w-20 h-8"
                  />
                  <Select
                    value={scheduleUnit}
                    onValueChange={(v) =>
                      setScheduleUnit(v as "hours" | "days" | "weeks")
                    }
                  >
                    <SelectTrigger className="w-28 h-8">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="hours">hour(s)</SelectItem>
                      <SelectItem value="days">day(s)</SelectItem>
                      <SelectItem value="weeks">week(s)</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                {/* Template quick-start pills */}
                <div className="space-y-1.5">
                  <p className="text-[11px] text-muted-foreground/60">
                    Quick start from a template
                  </p>
                  <div className="flex flex-wrap gap-1.5">
                    {SCHEDULED_TEMPLATES.map((template) => (
                      <button
                        key={template.id}
                        type="button"
                        onClick={() => applyTemplate(template)}
                        className="inline-flex items-center gap-1.5 rounded-full border border-border bg-background px-2.5 py-1 text-[11px] text-muted-foreground hover:border-primary/40 hover:text-foreground transition-colors"
                      >
                        <template.icon className="h-3 w-3" />
                        {template.name}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            )}
          </div>

          {/* ── Advanced Settings ─────────────────────────────── */}
          <Collapsible open={showAdvanced} onOpenChange={setShowAdvanced}>
            <CollapsibleTrigger className="flex items-center gap-1.5 text-[13px] text-muted-foreground hover:text-foreground transition-colors py-1">
              <ChevronDown
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  showAdvanced && "rotate-180",
                )}
              />
              Advanced options
            </CollapsibleTrigger>
            <CollapsibleContent className="space-y-4 pt-3">
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-1.5">
                  <Label>Agent</Label>
                  <Select
                    value={agentType}
                    onValueChange={(value) => {
                      setAgentType(value);
                      setSelectedModel("");
                    }}
                  >
                    <SelectTrigger>
                      <SelectValue
                        placeholder={`Default (${AGENT_TYPE_OPTIONS.find((a) => a.key === defaultAgentType)?.label ?? defaultAgentType})`}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {AGENT_TYPE_OPTIONS.map((agent) => (
                        <SelectItem key={agent.key} value={agent.key}>
                          {agent.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div className="space-y-1.5">
                  <Label>Model</Label>
                  <Select
                    value={selectedModel}
                    onValueChange={setSelectedModel}
                  >
                    <SelectTrigger>
                      <SelectValue placeholder="Default model" />
                    </SelectTrigger>
                    <SelectContent>
                      {availableModels.map((model) => (
                        <SelectItem key={model} value={model}>
                          {model}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>

              <div className="space-y-1.5">
                <Label>Execution mode</Label>
                <RadioGroup
                  value={executionMode}
                  onValueChange={setExecutionMode}
                  className="flex gap-4"
                >
                  <div className="flex items-center space-x-2">
                    <RadioGroupItem
                      value="sequential"
                      id="exec-sequential"
                    />
                    <Label htmlFor="exec-sequential" className="font-normal">
                      Sequential
                    </Label>
                  </div>
                  <div className="flex items-center space-x-2">
                    <RadioGroupItem value="parallel" id="exec-parallel" />
                    <Label htmlFor="exec-parallel" className="font-normal">
                      Parallel
                    </Label>
                  </div>
                </RadioGroup>
              </div>

              {executionMode === "parallel" && (
                <div className="space-y-1.5">
                  <Label htmlFor="max-concurrent">Max concurrent tasks</Label>
                  <Input
                    id="max-concurrent"
                    type="number"
                    min={1}
                    max={10}
                    value={maxConcurrent}
                    onChange={(e) => setMaxConcurrent(Number(e.target.value))}
                  />
                </div>
              )}

              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-1.5">
                  <Label>Priority</Label>
                  <Select
                    value={priorityLevel}
                    onValueChange={(v) =>
                      setPriorityLevel(v as PriorityLevel)
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PRIORITY_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {opt.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div className="space-y-1.5">
                  <Label htmlFor="base-branch">Base branch</Label>
                  <Input
                    id="base-branch"
                    value={baseBranch}
                    onChange={(e) => setBaseBranch(e.target.value)}
                    placeholder="main"
                  />
                </div>
              </div>
            </CollapsibleContent>
          </Collapsible>

          {/* ── Submit ────────────────────────────────────────── */}
          <div className="flex items-center gap-3 pt-2">
            <Button
              onClick={() => createMutation.mutate()}
              disabled={!canSubmit || createMutation.isPending}
            >
              {createMutation.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Creating...
                </>
              ) : (
                "Create project"
              )}
            </Button>
            {createMutation.isError && (
              <p className="text-xs text-destructive">
                Failed to create project. Please try again.
              </p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
