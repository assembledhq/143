"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { X } from "lucide-react";
import type {
  Organization,
  OrgSettings,
  RepoSettings,
  RepoPMSettings,
  Repository,
  SingleResponse,
} from "@/lib/types";
import { DEFAULT_PM_MODEL, PM_MODELS_BY_PROVIDER } from "@/lib/model-constants";

interface RepoPMSettingsProps {
  repository: Repository;
}

export function RepoPMSettingsEditor({ repository }: RepoPMSettingsProps) {
  const queryClient = useQueryClient();

  const { data: orgResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const orgSettings = (orgResponse?.data?.settings ?? {}) as OrgSettings;
  const repoSettings = (repository.settings ?? {}) as RepoSettings;
  const hasCustomPM = repoSettings.pm != null;

  const [customized, setCustomized] = useState(hasCustomPM);
  const [pmScheduleHours, setPmScheduleHours] = useState(
    String(repoSettings.pm?.pm_schedule_hours ?? orgSettings.pm_schedule_hours ?? 4)
  );
  const [pmModel, setPmModel] = useState(
    repoSettings.pm?.pm_model ?? orgSettings.pm_model ?? DEFAULT_PM_MODEL
  );
  const [philosophy, setPhilosophy] = useState(
    repoSettings.pm?.product_context?.philosophy ?? orgSettings.product_context?.philosophy ?? ""
  );
  const [direction, setDirection] = useState(
    repoSettings.pm?.product_context?.direction ?? orgSettings.product_context?.direction ?? orgSettings.product_direction ?? ""
  );
  const [focusAreas, setFocusAreas] = useState<string[]>(
    repoSettings.pm?.product_context?.focus_areas ?? orgSettings.product_context?.focus_areas ?? []
  );
  const [avoidAreas, setAvoidAreas] = useState<string[]>(
    repoSettings.pm?.product_context?.avoid_areas ?? orgSettings.product_context?.avoid_areas ?? []
  );
  const [focusInput, setFocusInput] = useState("");
  const [avoidInput, setAvoidInput] = useState("");
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync when repository data changes.
  const [prevRepo, setPrevRepo] = useState<string | undefined>(undefined);
  if (repository.id !== prevRepo) {
    setPrevRepo(repository.id);
    const rs = (repository.settings ?? {}) as RepoSettings;
    const hasPM = rs.pm != null;
    setCustomized(hasPM);
    setPmScheduleHours(String(rs.pm?.pm_schedule_hours ?? orgSettings.pm_schedule_hours ?? 4));
    setPmModel(rs.pm?.pm_model ?? orgSettings.pm_model ?? DEFAULT_PM_MODEL);
    setPhilosophy(rs.pm?.product_context?.philosophy ?? orgSettings.product_context?.philosophy ?? "");
    setDirection(rs.pm?.product_context?.direction ?? orgSettings.product_context?.direction ?? orgSettings.product_direction ?? "");
    setFocusAreas(rs.pm?.product_context?.focus_areas ?? orgSettings.product_context?.focus_areas ?? []);
    setAvoidAreas(rs.pm?.product_context?.avoid_areas ?? orgSettings.product_context?.avoid_areas ?? []);
  }

  const enabledPmModelGroups = useMemo(() => {
    const agentConfig = orgSettings.agent_config ?? {};
    const serverDefaults = agentDefaultsResponse?.data ?? {};
    const defaultAgent = orgSettings.default_agent_type || "codex";

    return Object.entries(PM_MODELS_BY_PROVIDER)
      .filter(([providerKey, { apiKeyVar }]) => {
        const orgKey = agentConfig[providerKey]?.[apiKeyVar];
        const serverKey = (serverDefaults[providerKey] ?? {})[apiKeyVar];
        return Boolean(orgKey) || Boolean(serverKey) || providerKey === defaultAgent;
      })
      .map(([, { label, models }]) => ({ label, models }));
  }, [orgSettings.agent_config, orgSettings.default_agent_type, agentDefaultsResponse?.data]);

  const mutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.repositories.update(repository.id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repository", repository.id] });
      queryClient.invalidateQueries({ queryKey: ["repositories"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: () => {
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const addTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    const trimmed = value.trim();
    if (!trimmed || list.includes(trimmed)) return;
    setList([...list, trimmed]);
  };

  const removeTag = (value: string, list: string[], setList: (v: string[]) => void) => {
    setList(list.filter((item) => item !== value));
  };

  function handleSave() {
    if (!customized) {
      // Reset to org defaults — clear PM overrides.
      mutation.mutate({ settings: {} });
      return;
    }

    const pmSettings: RepoPMSettings = {
      pm_schedule_hours: parseInt(pmScheduleHours, 10),
      pm_model: pmModel,
      product_context: {
        philosophy,
        direction,
        focus_areas: focusAreas,
        avoid_areas: avoidAreas,
      },
    };
    mutation.mutate({ settings: { pm: pmSettings } });
  }

  function handleResetToDefaults() {
    setCustomized(false);
    setPmScheduleHours(String(orgSettings.pm_schedule_hours ?? 4));
    setPmModel(orgSettings.pm_model ?? DEFAULT_PM_MODEL);
    setPhilosophy(orgSettings.product_context?.philosophy ?? "");
    setDirection(orgSettings.product_context?.direction ?? orgSettings.product_direction ?? "");
    setFocusAreas(orgSettings.product_context?.focus_areas ?? []);
    setAvoidAreas(orgSettings.product_context?.avoid_areas ?? []);
  }

  const orgPhilosophy = orgSettings.product_context?.philosophy;
  const orgDirection = orgSettings.product_context?.direction ?? orgSettings.product_direction;

  return (
    <div className="space-y-4">
      {/* Toggle */}
      <Card>
        <CardContent>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium">PM settings</p>
              <p className="text-xs text-muted-foreground">
                {customized
                  ? "Custom PM settings for this repository."
                  : "Using organization defaults (Staff PM)."}
              </p>
            </div>
            <div className="flex gap-2">
              <Button
                variant={customized ? "outline" : "default"}
                size="sm"
                onClick={() => setCustomized(false)}
              >
                Org defaults
              </Button>
              <Button
                variant={customized ? "default" : "outline"}
                size="sm"
                onClick={() => setCustomized(true)}
              >
                Customize
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {customized && (
        <>
          {/* PM Agent */}
          <Card>
            <CardContent>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="repo-pm-schedule">Schedule (hours)</Label>
                  <Input
                    id="repo-pm-schedule"
                    type="number"
                    min={1}
                    max={24}
                    value={pmScheduleHours}
                    onChange={(e) => setPmScheduleHours(e.target.value)}
                    placeholder="4"
                  />
                  <p className="text-xs text-muted-foreground">
                    Org default: every {orgSettings.pm_schedule_hours ?? 4} hours
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="repo-pm-model">PM Model</Label>
                  <Select value={pmModel} onValueChange={setPmModel}>
                    <SelectTrigger id="repo-pm-model" aria-label="PM Model">
                      <SelectValue placeholder="Select a model" />
                    </SelectTrigger>
                    <SelectContent>
                      {enabledPmModelGroups.length === 0 ? (
                        <SelectItem value={DEFAULT_PM_MODEL} disabled>
                          No providers configured
                        </SelectItem>
                      ) : (
                        enabledPmModelGroups.map((group) => (
                          <SelectGroup key={group.label}>
                            <SelectLabel>{group.label}</SelectLabel>
                            {group.models.map((model) => (
                              <SelectItem key={model} value={model}>
                                {model}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        ))
                      )}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    Org default: {orgSettings.pm_model ?? DEFAULT_PM_MODEL}
                  </p>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Product Context */}
          <Card>
            <CardContent>
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="repo-philosophy">Philosophy</Label>
                  <Textarea
                    id="repo-philosophy"
                    rows={3}
                    value={philosophy}
                    onChange={(e) => setPhilosophy(e.target.value)}
                    placeholder="How should the PM think about tradeoffs for this repo?"
                  />
                  {orgPhilosophy && (
                    <p className="text-xs text-muted-foreground">
                      Org default: {orgPhilosophy.length > 60 ? orgPhilosophy.slice(0, 60) + "..." : orgPhilosophy}
                    </p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="repo-direction">Current direction</Label>
                  <Textarea
                    id="repo-direction"
                    rows={2}
                    value={direction}
                    onChange={(e) => setDirection(e.target.value)}
                    placeholder="What is this repo focused on?"
                  />
                  {orgDirection && (
                    <p className="text-xs text-muted-foreground">
                      Org default: {orgDirection.length > 60 ? orgDirection.slice(0, 60) + "..." : orgDirection}
                    </p>
                  )}
                </div>
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-2">
                    <Label htmlFor="repo-focus-areas">Focus areas</Label>
                    <Input
                      id="repo-focus-areas"
                      value={focusInput}
                      onChange={(e) => setFocusInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === ",") {
                          e.preventDefault();
                          addTag(focusInput, focusAreas, setFocusAreas);
                          setFocusInput("");
                        }
                      }}
                      placeholder="Add focus area and press Enter"
                    />
                    <div className="flex flex-wrap gap-2">
                      {focusAreas.map((area) => (
                        <Badge key={area} variant="secondary" className="text-[11px]">
                          {area}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="ml-1 h-4 w-4 p-0"
                            onClick={() => removeTag(area, focusAreas, setFocusAreas)}
                          >
                            <X className="h-3 w-3" />
                          </Button>
                        </Badge>
                      ))}
                    </div>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="repo-avoid-areas">Avoid areas</Label>
                    <Input
                      id="repo-avoid-areas"
                      value={avoidInput}
                      onChange={(e) => setAvoidInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === ",") {
                          e.preventDefault();
                          addTag(avoidInput, avoidAreas, setAvoidAreas);
                          setAvoidInput("");
                        }
                      }}
                      placeholder="Add avoid area and press Enter"
                    />
                    <div className="flex flex-wrap gap-2">
                      {avoidAreas.map((area) => (
                        <Badge key={area} variant="secondary" className="text-[11px]">
                          {area}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="ml-1 h-4 w-4 p-0"
                            onClick={() => removeTag(area, avoidAreas, setAvoidAreas)}
                          >
                            <X className="h-3 w-3" />
                          </Button>
                        </Badge>
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            </CardContent>
          </Card>
        </>
      )}

      <div className="flex items-center justify-end gap-3">
        {saveStatus === "success" && (
          <span className="text-sm text-emerald-600 dark:text-emerald-400">Settings saved.</span>
        )}
        {saveStatus === "error" && (
          <span className="text-sm text-destructive">Failed to save settings.</span>
        )}
        {customized && (
          <Button variant="outline" onClick={handleResetToDefaults}>
            Reset to org defaults
          </Button>
        )}
        <Button onClick={handleSave} disabled={mutation.isPending}>
          {mutation.isPending ? "Saving..." : "Save settings"}
        </Button>
      </div>
    </div>
  );
}
