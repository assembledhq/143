"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { isAgentAvailable } from "@/lib/agents";
import type { CodingCredentialSummary, OrgSettings, Integration, Repository, ListResponse, Organization, SingleResponse } from "@/lib/types";

export function useSetupStatus() {
  const { data: settingsResponse, isLoading: settingsLoading } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const { data: codexAuthStatusResponse, isLoading: codexAuthLoading } = useQuery({
    queryKey: [...queryKeys.codexAuth.status, "personal"],
    queryFn: () => api.codexAuth.status(undefined, "personal"),
  });
  const { data: resolvedCodingCredentialsResponse, isLoading: resolvedCodingCredentialsLoading } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
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
  const resolvedCodingCredentials = resolvedCodingCredentialsResponse?.data ?? [];

  const agentConnected = isAgentAvailable(
    defaultAgent,
    [],
    codexAuthStatusResponse?.data,
    resolvedCodingCredentials,
  );

  const integrations = integrationsResponse?.data ?? [];
  const repositories = repositoriesResponse?.data ?? [];
  const githubReady = integrations.some((i) => i.provider === "github" && i.status === "active")
    && repositories.length > 0;

  const isLoading = settingsLoading || codexAuthLoading || resolvedCodingCredentialsLoading || integrationsLoading || repositoriesLoading;
  const isSetupComplete = agentConnected && githubReady;

  return {
    isLoading,
    isSetupComplete,
    agentConnected,
    githubReady,
  };
}
