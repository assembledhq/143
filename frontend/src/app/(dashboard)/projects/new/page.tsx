"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, Sparkles, PenLine } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { api } from "@/lib/api";
import { AGENT_TYPE_OPTIONS } from "@/lib/model-constants";
import type { OrgSettings, Organization, SingleResponse } from "@/lib/types";

const PRIORITY_OPTIONS = [
  { value: "low", label: "Low", numeric: 75 },
  { value: "medium", label: "Medium", numeric: 50 },
  { value: "high", label: "High", numeric: 25 },
  { value: "critical", label: "Critical", numeric: 0 },
] as const;

type PriorityLevel = (typeof PRIORITY_OPTIONS)[number]["value"];

function numericToPriorityLevel(n: number): PriorityLevel {
  if (n <= 12) return "critical";
  if (n <= 37) return "high";
  if (n <= 62) return "medium";
  return "low";
}

function priorityLevelToNumeric(level: PriorityLevel): number {
  return PRIORITY_OPTIONS.find((o) => o.value === level)!.numeric;
}

type CreationMode = "describe" | "form";

export default function NewProjectPage() {
  const router = useRouter();
  const [mode, setMode] = useState<CreationMode>("describe");

  // AI describe mode state
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
    mutationFn: () => api.projects.aiGenerate({ description: description.trim() }),
    onSuccess: (response) => {
      const gen = response.data;
      setTitle(gen.title);
      setGoal(gen.goal);
      setScope(gen.scope ?? "");
      setCompletionCriteria(gen.completion_criteria ?? "");
      setExecutionMode(gen.execution_mode || "sequential");
      setHasGenerated(true);
      setMode("form");
    },
  });

  const createMutation = useMutation({
    mutationFn: () =>
      api.projects.create({
        title: title.trim(),
        goal: goal.trim(),
        repository_id: repositoryId,
        scope: scope.trim() || undefined,
        completion_criteria: completionCriteria.trim() || undefined,
        execution_mode: executionMode,
        max_concurrent: executionMode === "parallel" ? maxConcurrent : undefined,
        priority: priorityLevelToNumeric(priorityLevel),
        base_branch: baseBranch.trim() || undefined,
        agent_type: agentType || undefined,
        model: selectedModel || undefined,
      }),
    onSuccess: (response) => {
      router.push(`/projects/${response.data.id}`);
    },
  });

  const canSubmit =
    title.trim().length > 0 && goal.trim().length > 0 && repositoryId.length > 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="New Project"
        description="Create a multi-task project for the PM agent to manage."
        action={
          <Button variant="outline" asChild>
            <Link href="/projects">
              <ArrowLeft className="mr-2 h-4 w-4" />
              Back to Projects
            </Link>
          </Button>
        }
      />

      {/* Mode Selector */}
      <div className="flex gap-3">
        <button
          type="button"
          onClick={() => setMode("describe")}
          className={`flex items-center gap-2 rounded-lg border px-4 py-3 text-sm font-medium transition-colors ${
            mode === "describe"
              ? "border-primary bg-primary/5 text-primary"
              : "border-border bg-background text-muted-foreground hover:border-primary/40 hover:text-foreground"
          }`}
        >
          <Sparkles className="h-4 w-4" />
          Describe with AI
        </button>
        <button
          type="button"
          onClick={() => setMode("form")}
          className={`flex items-center gap-2 rounded-lg border px-4 py-3 text-sm font-medium transition-colors ${
            mode === "form"
              ? "border-primary bg-primary/5 text-primary"
              : "border-border bg-background text-muted-foreground hover:border-primary/40 hover:text-foreground"
          }`}
        >
          <PenLine className="h-4 w-4" />
          Fill out manually
        </button>
      </div>

      {/* AI Describe Mode */}
      {mode === "describe" && (
        <Card>
          <CardContent className="space-y-4 pt-6">
            <div className="space-y-2">
              <Label htmlFor="description">Describe your project</Label>
              <Textarea
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Describe what you want to build in plain language. For example: &quot;Add dark mode support across the entire app. It should respect the user's OS preference and also have a manual toggle in settings. Store the preference in localStorage.&quot;"
                rows={6}
              />
              <p className="text-xs text-muted-foreground">
                We&apos;ll use AI to turn your description into a structured project with a title, goal, scope, and completion criteria. You can edit everything before creating.
              </p>
            </div>

            <div className="flex items-center gap-3">
              <Button
                onClick={() => generateMutation.mutate()}
                disabled={description.trim().length === 0 || generateMutation.isPending}
              >
                <Sparkles className="mr-2 h-4 w-4" />
                {generateMutation.isPending ? "Generating..." : "Generate Project"}
              </Button>
              {generateMutation.isError && (
                <p className="text-xs text-destructive">
                  Failed to generate project. Try again or switch to manual mode.
                </p>
              )}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Manual Form Mode */}
      {mode === "form" && (
        <Card>
          <CardContent className="space-y-5 pt-6">
            {hasGenerated && (
              <div className="rounded-md border border-primary/20 bg-primary/5 px-4 py-3 text-sm text-primary">
                Project details generated from your description. Review and edit as needed, then select a repository and create.
              </div>
            )}

            <div className="space-y-2">
              <Label htmlFor="title">Title</Label>
              <Input
                id="title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Project title"
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="goal">Goal</Label>
              <Textarea
                id="goal"
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
                placeholder="What should this project accomplish?"
                rows={3}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="scope">Scope (optional)</Label>
              <Textarea
                id="scope"
                value={scope}
                onChange={(e) => setScope(e.target.value)}
                placeholder="What files/areas are in scope?"
                rows={2}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="completion-criteria">
                Completion Criteria (optional)
              </Label>
              <Textarea
                id="completion-criteria"
                value={completionCriteria}
                onChange={(e) => setCompletionCriteria(e.target.value)}
                placeholder="How do we know the project is done?"
                rows={2}
              />
            </div>

            <div className="space-y-2">
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

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Agent</Label>
                <Select
                  value={agentType}
                  onValueChange={(value) => {
                    setAgentType(value);
                    setSelectedModel("");
                  }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder={`Default (${AGENT_TYPE_OPTIONS.find((a) => a.key === defaultAgentType)?.label ?? defaultAgentType})`} />
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

              <div className="space-y-2">
                <Label>Model</Label>
                <Select value={selectedModel} onValueChange={setSelectedModel}>
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

            <div className="space-y-2">
              <Label>Execution Mode</Label>
              <RadioGroup
                value={executionMode}
                onValueChange={setExecutionMode}
                className="flex gap-4"
              >
                <div className="flex items-center space-x-2">
                  <RadioGroupItem value="sequential" id="exec-sequential" />
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
              <div className="space-y-2">
                <Label htmlFor="max-concurrent">Max Concurrent Tasks</Label>
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

            <div className="space-y-2">
              <Label>Priority</Label>
              <Select
                value={priorityLevel}
                onValueChange={(v) => setPriorityLevel(v as PriorityLevel)}
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

            <div className="space-y-2">
              <Label htmlFor="base-branch">Base Branch</Label>
              <Input
                id="base-branch"
                value={baseBranch}
                onChange={(e) => setBaseBranch(e.target.value)}
                placeholder="main"
              />
            </div>

            <div className="flex items-center gap-3 pt-2">
              <Button
                onClick={() => createMutation.mutate()}
                disabled={!canSubmit || createMutation.isPending}
              >
                {createMutation.isPending ? "Creating..." : "Create Project"}
              </Button>
              {createMutation.isError && (
                <p className="text-xs text-destructive">
                  Failed to create project. Please try again.
                </p>
              )}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
