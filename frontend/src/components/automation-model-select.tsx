"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { availableAgentModelGroups, pmUsableResolvedCredentials } from "@/lib/agents";
import { ModelOptionGroups } from "@/components/model-option-groups";
import { useOpenCodeAvailability } from "@/hooks/use-opencode-models";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  CodingCredentialSummary,
  ListResponse,
  OrgSettings,
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
import type { ControlDensity } from "@/components/ui/control-sizing";

const AUTO_MODEL_VALUE = "__auto__";

interface AutomationModelSelectProps {
  value?: string;
  onValueChange: (value: string | undefined) => void;
  id?: string;
  ariaLabel?: string;
  /** Restyles the trigger — used by inline property rows to render it ghosted. */
  triggerClassName?: string;
  density?: ControlDensity;
  /**
   * Models another rank already claims. The ranked fallback list passes the
   * primary model and its sibling rows so the same model cannot be picked
   * twice — a duplicate rank is accepted by the API but retries a model that
   * just failed, which is the one thing a fallback chain exists to avoid. The
   * row's own value is never excluded, so the trigger stays truthful.
   */
  excludeModels?: readonly string[];
  disabled?: boolean;
}

export function AutomationModelSelect({
  value,
  onValueChange,
  id,
  ariaLabel = "Automation model",
  triggerClassName,
  density = "default",
  excludeModels,
  disabled,
}: AutomationModelSelectProps) {
  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const { data: resolvedCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const { data: codexAuthResponse } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
  });
  const { data: orgCodingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("org"),
    queryFn: () => api.codingCredentials.list("org"),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const resolvedCredentials = useMemo(
    () => resolvedCredentialsResponse?.data ?? [],
    [resolvedCredentialsResponse],
  );
  const orgCodingCredentials = useMemo(
    () => orgCodingCredentialsResponse?.data ?? [],
    [orgCodingCredentialsResponse],
  );
  // Automations run server-side without a user id, so only org-scoped
  // credentials count toward availability.
  const automationResolvedCredentials = useMemo(
    () => pmUsableResolvedCredentials(resolvedCredentials),
    [resolvedCredentials],
  );
  const modelGroups = useMemo(
    () =>
      availableAgentModelGroups(
        automationResolvedCredentials,
        codexAuthResponse?.data,
        orgCodingCredentials,
        settings.default_agent_type || "codex",
        { orgAgentConfig: settings.agent_config },
      ),
    [
      automationResolvedCredentials,
      codexAuthResponse?.data,
      orgCodingCredentials,
      settings.default_agent_type,
      settings.agent_config,
    ],
  );
  const selectableGroups = useMemo(() => {
    if (!excludeModels || excludeModels.length === 0) return modelGroups;
    const excluded = new Set(excludeModels.filter((model) => model && model !== value));
    return modelGroups
      .map((group) => ({
        ...group,
        models: group.models.filter((model) => !excluded.has(model)),
      }))
      .filter((group) => group.models.length > 0);
  }, [excludeModels, modelGroups, value]);
  const currentValueAvailable = useMemo(
    () => !value || modelGroups.some((group) => group.models.includes(value)),
    [modelGroups, value],
  );
  const openCodeAvailability = useOpenCodeAvailability(
    orgCodingCredentials,
    settings.opencode_routing?.require_openrouter ?? false,
  );

  return (
    <Select
      value={value ?? AUTO_MODEL_VALUE}
      disabled={disabled}
      onValueChange={(nextValue) =>
        onValueChange(nextValue === AUTO_MODEL_VALUE ? undefined : nextValue)
      }
    >
      <SelectTrigger id={id} aria-label={ariaLabel} density={density} className={triggerClassName}>
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
        <ModelOptionGroups modelGroups={selectableGroups} openCodeAvailability={openCodeAvailability} selectedModel={value} />
      </SelectContent>
    </Select>
  );
}
