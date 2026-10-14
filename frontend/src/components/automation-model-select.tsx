"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  agentTypeForModel,
  availableAgentModelGroups,
  pmUsableResolvedCredentials,
} from "@/lib/agents";
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

// Pi and OpenCode publish several identical provider/model ids, so a model id
// alone can't say which agent the user picked it under. Each option carries its
// group's agent so the choice survives the round trip.
function selectionValue(agentType: string, model: string): string {
  return `${agentType}::${model}`;
}

function parseSelectionValue(value: string): AutomationModelSelection {
  const [agentType, ...modelParts] = value.split("::");
  return { agentType: agentType || undefined, model: modelParts.join("::") };
}

export interface AutomationModelSelection {
  model: string;
  /** The agent group the model was picked from; undefined only when unknown. */
  agentType: string | undefined;
}

interface AutomationModelSelectProps {
  value?: string;
  /**
   * The agent the current value runs on. Needed to mark the right option
   * selected when more than one agent lists the same model id; falls back to
   * the agent that owns the model.
   */
  agentType?: string;
  /** Called with `undefined` when the user picks Auto. */
  onValueChange: (selection: AutomationModelSelection | undefined) => void;
  id?: string;
  ariaLabel?: string;
  /** Restyles the trigger — used by inline property rows to render it ghosted. */
  triggerClassName?: string;
  density?: ControlDensity;
  /**
   * Selections another rank already claims. The ranked fallback list passes the
   * primary and its sibling rows so the same choice cannot be picked twice — a
   * duplicate rank is accepted by the API but retries a model that just
   * failed, which is the one thing a fallback chain exists to avoid. Matched
   * per agent, as the worker compares ranks: the same model id under Pi and
   * under OpenCode is a different route. An entry without an agent matches the
   * model in every group. The row's own value is never excluded, so the
   * trigger stays truthful.
   */
  excludeSelections?: readonly AutomationModelSelection[];
  disabled?: boolean;
}

export function AutomationModelSelect({
  value,
  agentType,
  onValueChange,
  id,
  ariaLabel = "Automation model",
  triggerClassName,
  density = "default",
  excludeSelections,
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
  const currentAgentType = value
    ? (agentType ?? agentTypeForModel(value) ?? "")
    : "";
  const selectableGroups = useMemo(() => {
    if (!excludeSelections || excludeSelections.length === 0) return modelGroups;
    const excluded = (groupAgentType: string, model: string) =>
      !(model === value && groupAgentType === currentAgentType)
      && excludeSelections.some(
        (selection) =>
          selection.model === model
          && (selection.agentType === undefined
            || selection.agentType === groupAgentType),
      );
    return modelGroups
      .map((group) => ({
        ...group,
        models: group.models.filter((model) => !excluded(group.key, model)),
      }))
      .filter((group) => group.models.length > 0);
  }, [currentAgentType, excludeSelections, modelGroups, value]);
  // Matched against the agent's own group: the same id listed under another
  // agent is a different choice, and selecting it would move the automation.
  const currentValueAvailable = useMemo(
    () =>
      !value
      || modelGroups.some(
        (group) => group.key === currentAgentType && group.models.includes(value),
      ),
    [currentAgentType, modelGroups, value],
  );
  const openCodeAvailability = useOpenCodeAvailability(
    orgCodingCredentials,
    settings.opencode_routing?.require_openrouter ?? false,
  );

  return (
    <Select
      value={value ? selectionValue(currentAgentType, value) : AUTO_MODEL_VALUE}
      disabled={disabled}
      onValueChange={(nextValue) =>
        onValueChange(
          nextValue === AUTO_MODEL_VALUE
            ? undefined
            : parseSelectionValue(nextValue),
        )
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
            <SelectItem value={selectionValue(currentAgentType, value)}>
              {value}
            </SelectItem>
          </SelectGroup>
        ) : null}
        <ModelOptionGroups
          modelGroups={selectableGroups}
          getOptionValue={(group, model) => selectionValue(group.key, model)}
          openCodeAvailability={openCodeAvailability}
          // Only keeps an OpenCode option with no runnable route visible, so
          // it only applies when the current value is that OpenCode option.
          selectedModel={currentAgentType === "opencode" ? value : undefined}
        />
      </SelectContent>
    </Select>
  );
}
