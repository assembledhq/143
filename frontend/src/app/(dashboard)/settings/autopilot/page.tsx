"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useAuth } from "@/hooks/use-auth";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { RepoPMSettingsEditor } from "@/components/repo-pm-settings";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { useAutosave } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { availableAgentModelGroups, pmUsableResolvedCredentials } from "@/lib/agents";
import { DEFAULT_PM_MODEL } from "@/lib/model-constants";
import { queryKeys } from "@/lib/query-keys";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import {
  MIN_CONCURRENT_RUNS,
  MAX_CONCURRENT_RUNS,
  PM_SCHEDULE_MIN_HOURS,
  PM_SCHEDULE_MAX_HOURS,
  clampNumber,
} from "@/lib/settings-constants";
import type { CodingCredentialSummary, ListResponse, Organization, OrgSettings, RepoSettings, Repository, SingleResponse } from "@/lib/types";

export default function AutopilotSettingsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
    enabled: isAdmin,
  });
  const { data: repositoriesResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
    enabled: isAdmin,
  });
  const { data: resolvedCredsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
    enabled: isAdmin,
  });
  const { data: codexAuthResponse } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
    enabled: isAdmin,
  });
  const { data: orgCodingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("org"),
    queryFn: () => api.codingCredentials.list("org"),
    enabled: isAdmin,
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const repositories = repositoriesResponse?.data ?? [];
  const [selectedRepositoryID, setSelectedRepositoryID] = useState("");
  const firstRepositoryID = repositories[0]?.id ?? "";
  const effectiveSelectedRepositoryID =
    repositories.some((repository) => repository.id === selectedRepositoryID)
      ? selectedRepositoryID
      : firstRepositoryID;
  const selectedRepositoryFromList = repositories.find(
    (repository) => repository.id === effectiveSelectedRepositoryID,
  );
  const { data: selectedRepositoryResponse } = useQuery<SingleResponse<Repository>>({
    queryKey: ["repository", effectiveSelectedRepositoryID],
    queryFn: () => api.repositories.get(effectiveSelectedRepositoryID),
    enabled: isAdmin && effectiveSelectedRepositoryID !== "",
    initialData: selectedRepositoryFromList
      ? { data: selectedRepositoryFromList }
      : undefined,
    // Prevent a mount-time background refetch from overwriting the optimistic
    // update applied when the user clicks "Customize". The cache is refreshed
    // explicitly after each autosave mutation via query invalidation.
    staleTime: Infinity,
  });
  const selectedRepository = selectedRepositoryResponse?.data ?? selectedRepositoryFromList;
  const resolvedCredentials = useMemo(
    () => resolvedCredsResponse?.data ?? [],
    [resolvedCredsResponse],
  );
  const orgCodingCredentials = useMemo(
    () => orgCodingCredentialsResponse?.data ?? [],
    [orgCodingCredentialsResponse],
  );
  const codexAuthStatus = codexAuthResponse?.data;
  const pmResolvedCredentials = useMemo(
    () => pmUsableResolvedCredentials(resolvedCredentials),
    [resolvedCredentials],
  );

  const pmModelGroups = useMemo(() => {
    return availableAgentModelGroups(
      pmResolvedCredentials,
      codexAuthStatus,
      orgCodingCredentials,
      settings.default_agent_type || "codex",
      { orgAgentConfig: settings.agent_config },
    );
  }, [pmResolvedCredentials, codexAuthStatus, orgCodingCredentials, settings.default_agent_type, settings.agent_config]);

  const scheduleHoursServer = settings.pm_schedule_hours ?? 24;
  const pmModel = settings.pm_model ?? DEFAULT_PM_MODEL;
  const autonomyLevel = settings.autonomy_level ?? "auto_simple";
  const executionAggressiveness = String(settings.execution_aggressiveness ?? 2);
  const maxConcurrentRunsServer = settings.max_concurrent_runs ?? 3;

  const autosave = useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: (payload) => api.settings.update(payload),
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
  });

  const scheduleHoursField = useAutosaveNumericField({
    serverValue: scheduleHoursServer,
    autosave,
    toPatch: (v) => ({ settings: { pm_schedule_hours: v } }),
    clamp: (v) => clampNumber(v, PM_SCHEDULE_MIN_HOURS, PM_SCHEDULE_MAX_HOURS),
  });
  const maxConcurrentField = useAutosaveNumericField({
    serverValue: maxConcurrentRunsServer,
    autosave,
    toPatch: (v) => ({ settings: { max_concurrent_runs: v } }),
    clamp: (v) => clampNumber(v, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS),
  });

  if (!isAdmin) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader
            title="Autopilot"
            description="Configure PM model, cadence, and organization-wide automation defaults."
          />
          <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
            Only admins can manage Autopilot settings.
          </div>
        </div>
      </PageContainer>
    );
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Autopilot"
          description="Configure PM model, cadence, and organization-wide automation defaults."
          action={<AutosaveIndicator status={autosave.status} />}
        />
        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">PM configuration</h2>
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="pm-schedule-hours">Schedule (hours)</Label>
                <Input
                  id="pm-schedule-hours"
                  type="number"
                  min={PM_SCHEDULE_MIN_HOURS}
                  max={PM_SCHEDULE_MAX_HOURS}
                  value={scheduleHoursField.value}
                  onChange={scheduleHoursField.onChange}
                  onBlur={scheduleHoursField.onBlur}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="pm-model">PM model</Label>
                <Select
                  value={pmModel}
                  onValueChange={(value) =>
                    autosave.save({ settings: { pm_model: value } })
                  }
                >
                  <SelectTrigger id="pm-model" aria-label="PM model">
                    <SelectValue placeholder="Select a model" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT_PM_MODEL}>
                      Auto ({DEFAULT_PM_MODEL})
                    </SelectItem>
                    {pmModelGroups.map((group) => {
                      const models = group.models.filter((m) => m !== DEFAULT_PM_MODEL);
                      if (models.length === 0) return null;
                      return (
                        <SelectGroup key={group.key}>
                          <SelectLabel>{group.label}</SelectLabel>
                          {models.map((model) => (
                            <SelectItem key={model} value={model}>
                              {model}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      );
                    })}
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>
        </section>

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Execution</h2>
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label>Autonomy level</Label>
                <p className="text-xs text-muted-foreground">
                  Controls how much autonomy the autopilot has when executing tasks.
                </p>
                <RadioGroup
                  value={autonomyLevel}
                  onValueChange={(value) =>
                    autosave.save({
                      settings: {
                        autonomy_level: value as "manual" | "auto_simple" | "auto_all",
                      },
                    })
                  }
                  className="gap-4"
                >
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="manual" id="autonomy-manual" aria-label="Suggest" className="mt-0.5" />
                    <Label htmlFor="autonomy-manual" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Suggest</span>
                        <p className="text-xs text-muted-foreground">Autopilot recommends, you decide.</p>
                      </div>
                    </Label>
                  </div>
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="auto_simple" id="autonomy-auto-simple" aria-label="Act on low-risk" className="mt-0.5" />
                    <Label htmlFor="autonomy-auto-simple" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Act on low-risk</span>
                        <p className="text-xs text-muted-foreground">Auto-create sessions for bounded work.</p>
                      </div>
                    </Label>
                  </div>
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="auto_all" id="autonomy-auto-all" aria-label="Operate broadly" className="mt-0.5" />
                    <Label htmlFor="autonomy-auto-all" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Operate broadly</span>
                        <p className="text-xs text-muted-foreground">Autopilot runs automatically on eligible work.</p>
                      </div>
                    </Label>
                  </div>
                </RadioGroup>
              </div>
              <div className="space-y-2">
                <Label>Execution aggressiveness</Label>
                <p className="text-xs text-muted-foreground">
                  Controls how broadly Autopilot edits once it decides to act.
                </p>
                <RadioGroup
                  value={executionAggressiveness}
                  onValueChange={(value) =>
                    autosave.save({
                      settings: { execution_aggressiveness: parseInt(value, 10) },
                    })
                  }
                  className="gap-4"
                >
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="1" id="execution-aggressiveness-1" aria-label="Conservative" className="mt-0.5" />
                    <Label htmlFor="execution-aggressiveness-1" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Conservative</span>
                        <p className="text-xs text-muted-foreground">Prefer smaller, lower-risk edits.</p>
                      </div>
                    </Label>
                  </div>
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="2" id="execution-aggressiveness-2" aria-label="Moderate" className="mt-0.5" />
                    <Label htmlFor="execution-aggressiveness-2" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Moderate</span>
                        <p className="text-xs text-muted-foreground">Balance scope and caution.</p>
                      </div>
                    </Label>
                  </div>
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="3" id="execution-aggressiveness-3" aria-label="Aggressive" className="mt-0.5" />
                    <Label htmlFor="execution-aggressiveness-3" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Aggressive</span>
                        <p className="text-xs text-muted-foreground">Allow broader changes when they stay coherent.</p>
                      </div>
                    </Label>
                  </div>
                  <div className="flex items-start gap-3">
                    <RadioGroupItem value="4" id="execution-aggressiveness-4" aria-label="Maximum" className="mt-0.5" />
                    <Label htmlFor="execution-aggressiveness-4" className="items-start gap-0">
                      <div>
                        <span className="text-xs font-medium">Maximum</span>
                        <p className="text-xs text-muted-foreground">Optimize for throughput over caution.</p>
                      </div>
                    </Label>
                  </div>
                </RadioGroup>
              </div>
              <div className="space-y-2">
                <Label htmlFor="max-concurrent-runs">Max concurrent runs</Label>
                <p className="text-xs text-muted-foreground">
                  Maximum number of sessions the autopilot can run at the same time.
                </p>
                <Input
                  id="max-concurrent-runs"
                  type="number"
                  min={MIN_CONCURRENT_RUNS}
                  max={MAX_CONCURRENT_RUNS}
                  value={maxConcurrentField.value}
                  onChange={maxConcurrentField.onChange}
                  onBlur={maxConcurrentField.onBlur}
                />
              </div>
            </CardContent>
          </Card>
        </section>

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Repository overrides</h2>
          <p className="text-xs text-muted-foreground">
            Edit repository-specific Autopilot behavior here, or keep a repository on organization defaults.
          </p>
          {repositories.length === 0 ? (
            <p className="text-xs text-muted-foreground italic">No repositories connected yet.</p>
          ) : (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="autopilot-repository-override">Repository</Label>
                <Select
                  value={effectiveSelectedRepositoryID}
                  onValueChange={setSelectedRepositoryID}
                >
                  <SelectTrigger id="autopilot-repository-override" aria-label="Repository">
                    <SelectValue placeholder="Select a repository" />
                  </SelectTrigger>
                  <SelectContent>
                    {repositories.map((repository) => (
                      <SelectItem key={repository.id} value={repository.id}>
                        {repository.full_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {selectedRepository && (
                <RepoPMSettingsEditor
                  key={selectedRepository.id}
                  repository={selectedRepository}
                />
              )}
            </div>
          )}
        </section>
      </div>
    </PageContainer>
  );
}
