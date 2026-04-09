"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
import { DEFAULT_PM_MODEL, PM_MODELS_BY_PROVIDER } from "@/lib/model-constants";
import { queryKeys } from "@/lib/query-keys";
import type { ListResponse, Organization, OrgSettings, RepoSettings, Repository, SingleResponse } from "@/lib/types";

export default function AutopilotSettingsPage() {
  const queryClient = useQueryClient();
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: agentDefaultsResponse } = useQuery({
    queryKey: queryKeys.settings.agentDefaults,
    queryFn: () => api.settings.getAgentDefaults(),
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
    const serverDefaults = agentDefaultsResponse?.data ?? {};
    const defaultAgent = settings.default_agent_type || "codex";

    return Object.entries(PM_MODELS_BY_PROVIDER)
      .filter(([providerKey, { apiKeyVar }]) => {
        const orgKey = agentConfig[providerKey]?.[apiKeyVar];
        const serverKey = (serverDefaults[providerKey] ?? {})[apiKeyVar];
        return Boolean(orgKey) || Boolean(serverKey) || providerKey === defaultAgent;
      })
      .map(([, { label, models }]) => ({ label, models }));
  }, [agentDefaultsResponse?.data, settings.agent_config, settings.default_agent_type]);

  const [scheduleHoursOverride, setScheduleHoursOverride] = useState<string | null>(null);
  const [pmModelOverride, setPmModelOverride] = useState<string | null>(null);
  const scheduleHours = scheduleHoursOverride ?? String(settings.pm_schedule_hours ?? 4);
  const pmModel = pmModelOverride ?? settings.pm_model ?? DEFAULT_PM_MODEL;

  const mutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
    },
    onError: (error) => {
      console.error("failed to save autopilot settings", error);
    },
  });

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader title="Autopilot settings" description="Configure PM model, cadence, and organization-wide automation defaults." />
      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">PM configuration</h2>
        <Card>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="pm-schedule-hours">Schedule (hours)</Label>
              <Input
                id="pm-schedule-hours"
                type="number"
                min={1}
                max={24}
                value={scheduleHours}
                onChange={(event) => setScheduleHoursOverride(event.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="pm-model">PM model</Label>
              <Select value={pmModel} onValueChange={setPmModelOverride}>
                <SelectTrigger id="pm-model" aria-label="PM model">
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
            </div>
            <div className="flex justify-end">
              <Button
                onClick={() => mutation.mutate({
                  settings: {
                    pm_schedule_hours: parseInt(scheduleHours, 10),
                    pm_model: pmModel,
                  },
                })}
                disabled={mutation.isPending}
              >
                {mutation.isPending ? "Saving..." : "Save settings"}
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Repository overrides</h2>
        <Card>
          <CardContent className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Individual repositories can override Autopilot settings from their repository settings page.
            </p>
            {reposWithCustomPM.length === 0 ? (
              <p className="text-sm text-muted-foreground">No repository overrides yet.</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {reposWithCustomPM.map((repository) => (
                  <Badge key={repository.id} variant="secondary">
                    {repository.full_name}
                  </Badge>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </section>
      </div>
    </PageContainer>
  );
}
