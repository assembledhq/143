"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
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
import { DEFAULT_PM_MODEL, PM_MODELS_BY_PROVIDER } from "@/lib/model-constants";
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
import type { ListResponse, Organization, OrgSettings, RepoSettings, Repository, SingleResponse } from "@/lib/types";

export default function AutopilotSettingsPage() {
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: repositoriesResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const repositories = repositoriesResponse?.data ?? [];
  const reposWithCustomPM = repositories.filter((repository) => {
    const repoSettings = (repository.settings ?? {}) as RepoSettings;
    return repoSettings.pm != null;
  });

  const enabledPmModelGroups = useMemo(() => {
    const agentConfig = settings.agent_config ?? {};
    const defaultAgent = settings.default_agent_type || "codex";

    return Object.entries(PM_MODELS_BY_PROVIDER)
      .filter(([providerKey, { apiKeyVar }]) => {
        const orgKey = agentConfig[providerKey]?.[apiKeyVar];
        return Boolean(orgKey) || providerKey === defaultAgent;
      })
      .map(([, { label, models }]) => ({ label, models }));
  }, [settings.agent_config, settings.default_agent_type]);

  const scheduleHoursServer = settings.pm_schedule_hours ?? 4;
  const pmModel = settings.pm_model ?? DEFAULT_PM_MODEL;
  const autonomyLevel = settings.autonomy_level ?? "auto_simple";
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

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Autopilot settings"
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
                    {enabledPmModelGroups.map((group) => {
                      const models = group.models.filter((m) => m !== DEFAULT_PM_MODEL);
                      if (models.length === 0) return null;
                      return (
                        <SelectGroup key={group.label}>
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
                >
                  <label className="flex items-center gap-3 rounded-lg border p-3">
                    <RadioGroupItem value="manual" aria-label="Suggest" />
                    <div>
                      <span className="text-xs font-medium">Suggest</span>
                      <p className="text-xs text-muted-foreground">Autopilot recommends, you decide.</p>
                    </div>
                  </label>
                  <label className="flex items-center gap-3 rounded-lg border p-3">
                    <RadioGroupItem value="auto_simple" aria-label="Act on low-risk" />
                    <div>
                      <span className="text-xs font-medium">Act on low-risk</span>
                      <p className="text-xs text-muted-foreground">Auto-create sessions for bounded work.</p>
                    </div>
                  </label>
                  <label className="flex items-center gap-3 rounded-lg border p-3">
                    <RadioGroupItem value="auto_all" aria-label="Operate broadly" />
                    <div>
                      <span className="text-xs font-medium">Operate broadly</span>
                      <p className="text-xs text-muted-foreground">Autopilot runs automatically on eligible work.</p>
                    </div>
                  </label>
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
            Individual repositories can override Autopilot settings from their repository settings page.
          </p>
          {reposWithCustomPM.length === 0 ? (
            <p className="text-xs text-muted-foreground italic">No repository overrides yet.</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {reposWithCustomPM.map((repository) => (
                <Badge key={repository.id} variant="secondary">
                  {repository.full_name}
                </Badge>
              ))}
            </div>
          )}
        </section>
      </div>
    </PageContainer>
  );
}
