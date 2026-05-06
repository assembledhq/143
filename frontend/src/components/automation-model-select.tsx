"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { availableAgentModelGroups, pmUsableResolvedCredentials } from "@/lib/agents";
import { api } from "@/lib/api";
import type {
  CodingAuth,
  CodingCredentialSummary,
  ListResponse,
  OrgSettings,
  ResolvedCredential,
  UserCredentialSummary,
} from "@/lib/types";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const AUTO_MODEL_VALUE = "__auto__";

interface AutomationModelSelectProps {
  value?: string;
  onValueChange: (value: string | undefined) => void;
  id?: string;
  ariaLabel?: string;
}

export function AutomationModelSelect({
  value,
  onValueChange,
  id,
  ariaLabel = "Automation model",
}: AutomationModelSelectProps) {
  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const { data: resolvedCredsResponse } = useQuery<ListResponse<ResolvedCredential>>({
    queryKey: ["resolved-credentials"],
    queryFn: () => api.userCredentials.listResolved(),
  });
  const { data: teamDefaultsResponse } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["team-default-credentials"],
    queryFn: () => api.userCredentials.listTeamDefaults(),
  });
  const { data: codexAuthResponse } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
  });
  const { data: codingAuthsResponse } = useQuery<ListResponse<CodingAuth>>({
    queryKey: ["coding-auths"],
    queryFn: () => api.codingAuths.list(),
  });
  const { data: orgCodingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: ["coding-credentials", "org"],
    queryFn: () => api.codingCredentials.list("org"),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const resolvedCredentials = useMemo(
    () => resolvedCredsResponse?.data ?? [],
    [resolvedCredsResponse],
  );
  const teamDefaultCredentials = useMemo(
    () => teamDefaultsResponse?.data ?? [],
    [teamDefaultsResponse],
  );
  const codingAuths = useMemo(
    () => codingAuthsResponse?.data ?? [],
    [codingAuthsResponse],
  );
  const orgCodingCredentials = useMemo(
    () => orgCodingCredentialsResponse?.data ?? [],
    [orgCodingCredentialsResponse],
  );
  const automationResolvedCredentials = useMemo(
    () => pmUsableResolvedCredentials(resolvedCredentials, teamDefaultCredentials),
    [resolvedCredentials, teamDefaultCredentials],
  );
  const automationCodingAuthAvailability = useMemo(
    () => [...codingAuths, ...orgCodingCredentials],
    [codingAuths, orgCodingCredentials],
  );
  const modelGroups = useMemo(
    () =>
      availableAgentModelGroups(
        automationResolvedCredentials,
        codexAuthResponse?.data,
        automationCodingAuthAvailability,
        settings.default_agent_type || "codex",
        { orgAgentConfig: settings.agent_config },
      ),
    [
      automationResolvedCredentials,
      codexAuthResponse?.data,
      automationCodingAuthAvailability,
      settings.default_agent_type,
      settings.agent_config,
    ],
  );
  const currentValueAvailable = useMemo(
    () => !value || modelGroups.some((group) => group.models.includes(value)),
    [modelGroups, value],
  );

  return (
    <Select
      value={value ?? AUTO_MODEL_VALUE}
      onValueChange={(nextValue) =>
        onValueChange(nextValue === AUTO_MODEL_VALUE ? undefined : nextValue)
      }
    >
      <SelectTrigger id={id} aria-label={ariaLabel}>
        <SelectValue placeholder="Auto" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={AUTO_MODEL_VALUE}>Auto</SelectItem>
        {value && !currentValueAvailable ? (
          <SelectGroup>
            <SelectLabel>Current selection</SelectLabel>
            <SelectItem value={value}>{value}</SelectItem>
          </SelectGroup>
        ) : null}
        {modelGroups.map((group) => (
          <SelectGroup key={group.key}>
            <SelectLabel>{group.label}</SelectLabel>
            {group.models.map((model) => (
              <SelectItem key={model} value={model}>
                {model}
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
      </SelectContent>
    </Select>
  );
}
