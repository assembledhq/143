"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { isAgentConnected } from "@/components/autopilot/autopilot-helpers";
import type { OrgSettings, Integration, Repository, ListResponse, Organization, SingleResponse } from "@/lib/types";

export function useSetupStatus() {
  const { data: settingsResponse, isLoading: settingsLoading } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse, isLoading: agentDefaultsLoading } = useQuery({
    queryKey: queryKeys.settings.agentDefaults,
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const { data: codexAuthStatusResponse, isLoading: codexAuthLoading } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });

  const { data: integrationsResponse, isLoading: integrationsLoading } = useQuery<ListResponse<Integration>>({
    queryKey: queryKeys.integrations.all,
    queryFn: () => api.integrations.list(),
  });

  const { data: repositoriesResponse, isLoading: repositoriesLoading } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const rawSettings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgent = rawSettings?.default_agent_type ?? "codex";
  const agentConfig = rawSettings?.agent_config ?? {};
  const agentDefaults = agentDefaultsResponse?.data ?? {};

  const agentConnected = isAgentConnected(defaultAgent, agentConfig, agentDefaults, codexAuthStatusResponse?.data);

  const integrations = integrationsResponse?.data ?? [];
  const repositories = repositoriesResponse?.data ?? [];
  const githubReady = integrations.some((i) => i.provider === "github" && i.status === "active")
    && repositories.length > 0;

  const isLoading = settingsLoading || agentDefaultsLoading || codexAuthLoading || integrationsLoading || repositoriesLoading;
  const isSetupComplete = agentConnected && githubReady;

  return {
    isLoading,
    isSetupComplete,
    agentConnected,
    githubReady,
  };
}
