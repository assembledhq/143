"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, Loader2, Minus, Plus, Settings2, Sparkles } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { agentTypeForModel } from "@/lib/agents";
import { automationGoalLengthState } from "@/lib/automation-validation";
import { useAuth } from "@/hooks/use-auth";
import { BranchPicker } from "@/components/branch-picker";
import { AutomationComposer } from "@/components/automation-composer";
import { AutomationModelSelect } from "@/components/automation-model-select";
import { NoReposWarning } from "@/components/no-repos-warning";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import {
  automationTemplates,
  featuredAutomationTemplateIDs,
  getAutomationTemplate,
  type AutomationTemplate,
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
  const { user, isLoading } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";
  const initialTemplate = getAutomationTemplate(searchParams.get("template") ?? "");
  const goalEditorRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!isLoading && !canManage) {
      router.replace("/automations");
    }
  }, [canManage, isLoading, router]);

  const [name, setName] = useState(initialTemplate?.name ?? "");
  const [goal, setGoal] = useState(initialTemplate?.goal ?? "");
  const [iconValue, setIconValue] = useState("⚙️");
  const [scope, setScope] = useState("");
  const [selectedRepoId, setSelectedRepoId] = useState("");
  const [intervalValue, setIntervalValue] = useState(initialTemplate?.defaultInterval ?? 1);
  const [intervalUnit, setIntervalUnit] = useState<"hours" | "days" | "weeks">(
    initialTemplate?.defaultUnit ?? "days",
  );
  const [intervalRunHour, setIntervalRunHour] = useState("09");
  const [intervalRunMinute, setIntervalRunMinute] = useState("00");
  const [detectedTimezone] = useState<string>(() => browserTimezone());
  const [timezone, setTimezone] = useState<string>(detectedTimezone);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [templateOpen, setTemplateOpen] = useState(false);
  const [baseBranchByRepoId, setBaseBranchByRepoId] = useState<Record<string, string>>({});
  const [model, setModel] = useState<string | undefined>(undefined);
  const [identityScope, setIdentityScope] = useState<"org" | "personal">("org");
  const [prePRReviewLoops, setPrePRReviewLoops] = useState(1);
  const [reasoningEffort, setReasoningEffort] = useState<CodingAgentReasoningEffort>("");
  const [priority, setPriority] = useState(50);
  const [redirecting, setRedirecting] = useState(false);

  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const settings = (settingsResponse?.data?.settings ?? {}) as { default_agent_type?: string };

  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });
  const repos = reposData?.data ?? [];

  const repoId = selectedRepoId || repos[0]?.id || "";
  const selectedRepo = repos.find((repo) => repo.id === repoId);
  const selectedBaseBranch = repoId
    ? baseBranchByRepoId[repoId] ?? selectedRepo?.default_branch ?? ""
    : "";
  const defaultAgentType = settings.default_agent_type ?? "codex";
  const effectiveAgentType = model ? agentTypeForModel(model) ?? defaultAgentType : defaultAgentType;
  const supportsNativeReviewLoop = ["codex", "claude_code", "amp", "pi", "opencode"].includes(effectiveAgentType);
  const effectivePrePRReviewLoops = supportsNativeReviewLoop ? prePRReviewLoops : 0;
  const prePRReviewDescription = supportsNativeReviewLoop
    ? effectivePrePRReviewLoops === 0
      ? "Off"
      : "Runs the coding agent's review/fix loop before opening a PR."
    : "Off for agents without review-loop support.";
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);

  const applyTemplate = (template: AutomationTemplate) => {
    setName(template.name);
    setGoal(template.goal);
    setIntervalValue(template.defaultInterval);
    setIntervalUnit(template.defaultUnit);
    setTemplateOpen(false);
    requestAnimationFrame(() => {
      goalEditorRef.current?.querySelector("textarea")?.focus();
    });
  };

  const createMutation = useMutation({
    mutationFn: () =>
      api.automations.create({
        name: name.trim(),
        goal: goal.trim(),
        icon_type: "emoji",
        icon_value: iconValue,
        repository_id: repoId,
        scope: scope.trim() || undefined,
        interval_value: intervalValue,
        interval_unit: intervalUnit,
        interval_run_at: `${intervalRunHour}:${intervalRunMinute}`,
        timezone,
        model,
        identity_scope: identityScope,
        pre_pr_review_loops: effectivePrePRReviewLoops,
        ...(showReasoningSelector && reasoningEffort ? { reasoning_effort: reasoningEffort } : {}),
        base_branch: selectedBaseBranch.trim() || undefined,
        priority,
      }),
    onSuccess: (res) => {
      setRedirecting(true);
      router.push(`/automations/${res.data.id}`);
    },
  });

  if (!isLoading && !canManage) {
    return null;
  }

  if (repos.length === 0 && reposData) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader
            title="New automation"
            description="Create a recurring agent for this team."
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

  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <PageHeader
          title="New automation"
          description="Create a recurring agent for this team."
        />

        <div className="mx-auto max-w-4xl">
          <AutomationComposer
            name={name}
            onNameChange={setName}
            iconValue={iconValue}
            onIconChange={setIconValue}
            goal={goal}
            onGoalChange={setGoal}
            repositoryId={repoId || undefined}
            branch={selectedBaseBranch || selectedRepo?.default_branch || undefined}
            agentType={effectiveAgentType}
            goalEditorContainerRef={goalEditorRef}
            footerControls={(
              <>
                <Select value={repoId} onValueChange={setSelectedRepoId}>
                  <SelectTrigger className="h-9 w-full sm:w-[210px]" aria-label="Repository">
                    <SelectValue placeholder="Select repo" />
                  </SelectTrigger>
                  <SelectContent>
                    {repos.map((repo) => (
                      <SelectItem key={repo.id} value={repo.id}>
                        {repo.full_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-background px-2 py-1">
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
                      setIntervalValue(Number.isNaN(parsed) ? 1 : Math.max(1, parsed));
                    }}
                    className="h-7 w-16 px-2 text-base sm:text-xs"
                  />
                  <Select
                    value={intervalUnit}
                    onValueChange={(v) => {
                      if (v === "hours" || v === "days" || v === "weeks") {
                        setIntervalUnit(v);
                      }
                    }}
                  >
                    <SelectTrigger className="h-7 w-24 text-base sm:text-xs" aria-label="Interval unit">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="hours">hours</SelectItem>
                      <SelectItem value="days">days</SelectItem>
                      <SelectItem value="weeks">weeks</SelectItem>
                    </SelectContent>
                  </Select>
                  <span className="text-sm font-medium leading-none text-muted-foreground">At</span>
                  <Select value={intervalRunHour} onValueChange={setIntervalRunHour}>
                    <SelectTrigger className="h-7 w-18 text-base sm:text-xs" aria-label="Run at hour">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {hourOptions.map((h) => (
                        <SelectItem key={h} value={h}>{h}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <span className="text-sm text-muted-foreground">:</span>
                  <Select value={intervalRunMinute} onValueChange={setIntervalRunMinute}>
                    <SelectTrigger className="h-7 w-18 text-base sm:text-xs" aria-label="Run at minute">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {minuteOptions.map((m) => (
                        <SelectItem key={m} value={m}>{m}</SelectItem>
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

              </>
            )}
            secondaryControls={(
              <>
                <TemplatePicker open={templateOpen} onOpenChange={setTemplateOpen} onSelect={applyTemplate} />
                <Button asChild variant="ghost" size="sm">
                  <Link href="/automations/templates">Browse all templates</Link>
                </Button>
                <Sheet modal={false} open={advancedOpen} onOpenChange={setAdvancedOpen}>
                  <SheetTrigger asChild>
                    <Button type="button" variant="outline" size="sm">
                      <Settings2 className="mr-2 h-4 w-4" />
                      Advanced options
                    </Button>
                  </SheetTrigger>
                  <SheetContent className="sm:max-w-lg">
                    <SheetHeader>
                      <SheetTitle>Advanced settings</SheetTitle>
                      <SheetDescription>
                        Tune lower-frequency defaults for identity, model, branch, scope, priority, and review.
                      </SheetDescription>
                    </SheetHeader>
                    <div className="mt-6 space-y-5">
                      <div className="space-y-1.5">
                        <Label htmlFor="scope">Scope <span className="font-normal text-muted-foreground">(optional)</span></Label>
                        <Input
                          id="scope"
                          value={scope}
                          onChange={(e) => setScope(e.target.value)}
                          placeholder="e.g. src/payments/, tests/"
                        />
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
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="advanced-model">Model</Label>
                        <AutomationModelSelect
                          id="advanced-model"
                          ariaLabel="Model"
                          value={model}
                          onValueChange={setModel}
                        />
                      </div>
                      {showReasoningSelector ? (
                        <div className="space-y-1.5">
                          <Label>Reasoning</Label>
                          <Select
                            value={reasoningEffort || "__default__"}
                            onValueChange={(value) => setReasoningEffort(value === "__default__" ? "" : toCodingAgentReasoningEffort(value))}
                          >
                            <SelectTrigger aria-label="Reasoning">
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
                        <Label>Priority</Label>
                        <Select value={String(priority)} onValueChange={(v) => setPriority(parseInt(v, 10))}>
                          <SelectTrigger aria-label="Priority">
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
                      <div className="space-y-1.5">
                        <Label htmlFor="pre-pr-review-loops">Pre-PR review</Label>
                        <div className="flex items-center gap-2">
                          <Button
                            type="button"
                            variant="outline"
                            size="icon"
                            aria-label="Decrease review passes"
                            onClick={() => setPrePRReviewLoops((value) => Math.max(0, value - 1))}
                            disabled={!supportsNativeReviewLoop}
                          >
                            <Minus className="h-4 w-4" />
                          </Button>
                          <Input
                            id="pre-pr-review-loops"
                            aria-label="Review passes"
                            type="number"
                            min={0}
                            max={5}
                            value={effectivePrePRReviewLoops}
                            onChange={(e) => {
                              const parsed = parseInt(e.target.value, 10);
                              setPrePRReviewLoops(Number.isNaN(parsed) ? 0 : Math.min(5, Math.max(0, parsed)));
                            }}
                            disabled={!supportsNativeReviewLoop}
                            className="w-20 text-center"
                          />
                          <Button
                            type="button"
                            variant="outline"
                            size="icon"
                            aria-label="Increase review passes"
                            onClick={() => setPrePRReviewLoops((value) => Math.min(5, value + 1))}
                            disabled={!supportsNativeReviewLoop}
                          >
                            <Plus className="h-4 w-4" />
                          </Button>
                        </div>
                        <p className="text-xs text-muted-foreground">{prePRReviewDescription}</p>
                      </div>
                      <div className="flex justify-end gap-2 pt-2">
                        <SheetClose asChild>
                          <Button type="button" variant="outline">Cancel</Button>
                        </SheetClose>
                        <SheetClose asChild>
                          <Button type="button">Apply</Button>
                        </SheetClose>
                      </div>
                    </div>
                  </SheetContent>
                </Sheet>
              </>
            )}
            submitArea={(
              <div className="flex items-center gap-3">
                {createMutation.isError && (
                  <p className="text-xs text-destructive">
                    Failed to create automation. Please try again.
                  </p>
                )}
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
              </div>
            )}
          />
        </div>
      </div>
    </PageContainer>
  );
}

function TemplatePicker({
  open,
  onOpenChange,
  onSelect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (template: AutomationTemplate) => void;
}) {
  const featured = useMemo(
    () => automationTemplates.filter((template) => featuredAutomationTemplateIDs.includes(template.id)),
    [],
  );
  const remaining = useMemo(
    () => automationTemplates.filter((template) => !featuredAutomationTemplateIDs.includes(template.id)),
    [],
  );

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" size="sm">
          <Sparkles className="mr-2 h-4 w-4" />
          Templates
          <ChevronDown className="ml-2 h-3.5 w-3.5" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[min(36rem,calc(100vw-2rem))] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search templates..." />
          <CommandList className="max-h-[420px]">
            <CommandEmpty>No templates found.</CommandEmpty>
            <TemplateGroup heading="Featured" templates={featured} onSelect={onSelect} />
            <TemplateGroup heading="All templates" templates={remaining} onSelect={onSelect} />
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function TemplateGroup({
  heading,
  templates,
  onSelect,
}: {
  heading: string;
  templates: AutomationTemplate[];
  onSelect: (template: AutomationTemplate) => void;
}) {
  return (
    <CommandGroup heading={heading}>
      {templates.map((template) => {
        const Icon = template.icon;
        return (
          <CommandItem
            key={template.id}
            value={`${template.name} ${template.summary} ${template.tags.join(" ")}`}
            onSelect={() => onSelect(template)}
            className="items-start gap-3 py-3"
          >
            <span className="mt-0.5 rounded-md border border-border bg-muted/50 p-1.5">
              <Icon className="h-4 w-4" />
            </span>
            <span className="min-w-0 space-y-1">
              <span className="block text-sm font-medium">{template.name}</span>
              <span className="line-clamp-2 block text-xs text-muted-foreground">{template.summary}</span>
            </span>
          </CommandItem>
        );
      })}
    </CommandGroup>
  );
}
