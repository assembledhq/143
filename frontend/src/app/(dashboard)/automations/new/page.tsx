"use client";

import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, Loader2 } from "lucide-react";
import { useRouter } from "next/navigation";
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
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import { NoReposWarning } from "@/components/no-repos-warning";
import { cn } from "@/lib/utils";
import { automationTemplates } from "@/lib/automation-templates";

export default function NewAutomationPage() {
  const router = useRouter();

  // Form state
  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [scope, setScope] = useState("");
  const [selectedRepoId, setSelectedRepoId] = useState("");
  const [intervalValue, setIntervalValue] = useState(1);
  const [intervalUnit, setIntervalUnit] = useState<"hours" | "days" | "weeks">("days");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [baseBranch, setBaseBranch] = useState("main");
  const [priority, setPriority] = useState(50);

  // Load repos
  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });
  const repos = reposData?.data ?? [];

  // Fall back to the first repo until the user picks one so the form has a
  // valid default without syncing state inside an effect.
  const repoId = selectedRepoId || repos[0]?.id || "";

  const applyTemplate = (templateId: string) => {
    const t = automationTemplates.find((t) => t.id === templateId);
    if (!t) return;
    setName(t.name);
    setGoal(t.goal);
    setIntervalValue(t.defaultInterval);
    setIntervalUnit(t.defaultUnit);
  };

  const createMutation = useMutation({
    mutationFn: () =>
      api.automations.create({
        name,
        goal,
        repository_id: repoId || undefined,
        scope: scope || undefined,
        interval_value: intervalValue,
        interval_unit: intervalUnit,
        base_branch: baseBranch,
        priority,
      }),
    onSuccess: (res) => {
      router.push(`/automations/${res.data.id}`);
    },
  });

  if (repos.length === 0 && reposData) {
    return (
      <div className="max-w-2xl mx-auto px-6 py-8">
        <h1 className="text-lg font-semibold mb-4">New Automation</h1>
        <NoReposWarning />
      </div>
    );
  }

  return (
    <div className="max-w-2xl mx-auto px-6 py-8">
      <h1 className="text-lg font-semibold text-foreground mb-6">New Automation</h1>

      {/* Templates */}
      <div className="mb-6">
        <Label className="text-xs text-muted-foreground mb-2 block">
          Start from a template
        </Label>
        <div className="flex flex-wrap gap-2">
          {automationTemplates.map((t) => {
            const Icon = t.icon;
            return (
              <Button
                key={t.id}
                type="button"
                variant="outline"
                size="sm"
                onClick={() => applyTemplate(t.id)}
                className={cn(
                  "rounded-full h-7 px-3 text-xs",
                  name === t.name && "border-primary bg-primary/5"
                )}
              >
                <Icon className="h-3.5 w-3.5 mr-1.5" />
                {t.name}
              </Button>
            );
          })}
        </div>
      </div>

      <div className="space-y-4">
        {/* Name */}
        <div>
          <Label htmlFor="name">Name</Label>
          <Input
            id="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Find flaky tests"
          />
        </div>

        {/* Goal */}
        <div>
          <Label htmlFor="goal">Goal</Label>
          <Textarea
            id="goal"
            value={goal}
            onChange={(e) => setGoal(e.target.value)}
            placeholder="Describe what the automation should do each run..."
            rows={3}
          />
        </div>

        {/* Scope (optional) */}
        <div>
          <Label htmlFor="scope">
            Scope <span className="text-muted-foreground font-normal">(optional)</span>
          </Label>
          <Input
            id="scope"
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            placeholder="e.g. src/payments/, tests/"
          />
        </div>

        {/* Repository */}
        <div>
          <Label>Repository</Label>
          <Select value={repoId} onValueChange={setSelectedRepoId}>
            <SelectTrigger>
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
        </div>

        {/* Schedule */}
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
            <Select value={intervalUnit} onValueChange={(v) => setIntervalUnit(v as typeof intervalUnit)}>
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

        {/* Advanced settings */}
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <CollapsibleTrigger className="flex items-center gap-2 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors py-2">
            <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", advancedOpen && "rotate-180")} />
            Advanced
          </CollapsibleTrigger>
          <CollapsibleContent className="space-y-4 pt-2">
            <div>
              <Label htmlFor="baseBranch">Base branch</Label>
              <Input
                id="baseBranch"
                value={baseBranch}
                onChange={(e) => setBaseBranch(e.target.value)}
                placeholder="main"
              />
            </div>
            <div>
              <Label>Priority</Label>
              <Select value={String(priority)} onValueChange={(v) => setPriority(parseInt(v))}>
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
        <Button
          onClick={() => createMutation.mutate()}
          disabled={!name || !goal || createMutation.isPending}
          className="w-full"
        >
          {createMutation.isPending && <Loader2 className="h-4 w-4 mr-2 animate-spin" />}
          Create automation
        </Button>

        {createMutation.isError && (
          <p className="text-sm text-destructive">
            Failed to create automation. Please try again.
          </p>
        )}
      </div>
    </div>
  );
}
