"use client";

import { useMemo } from "react";
import { AutomationActionsConfig, actionConfigError } from "@/components/automation-actions-config";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ShieldAlert } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { AgentCapabilityDefinition, AgentCapabilityGrant, AgentCapabilityID } from "@/lib/types";

export function capabilityAccessFor(definition: AgentCapabilityDefinition) {
  return definition.max_access_level;
}

// Capabilities that ship enabled by default for the org session-default policy
// when an admin has not configured one yet. These are the broadly-useful
// capabilities (code/PR/test context, integration issue context, read-only
// production diagnostics, Slack status notifications, automation management,
// plus branch & PR publishing).
// Keep in sync with recommendedDefaultGrants in
// internal/services/agentcapabilities/service.go.
export const RECOMMENDED_DEFAULT_CAPABILITY_IDS: readonly AgentCapabilityID[] = [
  "repo_context",
  "pr_history",
  "review_feedback",
  "ci_history",
  "issue_sources",
  "production_diagnostics",
  "slack_notifications",
  "automation_management",
  "publishing",
];

// recommendedDefaultGrants seeds the catalog with the default-enabled set above.
// Use this (instead of normalizeCapabilityGrants) when there is no stored policy
// so the UI reflects sensible defaults rather than everything switched off.
export function recommendedDefaultGrants(catalog: AgentCapabilityDefinition[]): AgentCapabilityGrant[] {
  const enabled = new Set<AgentCapabilityID>(RECOMMENDED_DEFAULT_CAPABILITY_IDS);
  return catalog.map((definition) => ({
    capability_id: definition.id,
    access_level: capabilityAccessFor(definition),
    enabled: enabled.has(definition.id),
    config: {},
  }));
}

export function normalizeCapabilityGrants(
  catalog: AgentCapabilityDefinition[],
  grants: AgentCapabilityGrant[],
): AgentCapabilityGrant[] {
  const byID = new Map(grants.map((grant) => [grant.capability_id, grant]));
  return catalog.map((definition) => ({
    capability_id: definition.id,
    access_level: byID.get(definition.id)?.access_level ?? capabilityAccessFor(definition),
    enabled: byID.get(definition.id)?.enabled ?? false,
    config: byID.get(definition.id)?.config ?? {},
  }));
}

export function capabilitySummary(catalog: AgentCapabilityDefinition[], grants: AgentCapabilityGrant[]) {
  const names = grants
    .filter((grant) => grant.enabled)
    .map((grant) => catalog.find((definition) => definition.id === grant.capability_id)?.display_name ?? grant.capability_id);
  if (names.length === 0) return "Use defaults";
  if (names.length <= 3) return names.join(", ");
  return `${names.slice(0, 3).join(", ")} +${names.length - 3}`;
}

export function AutomationCapabilitiesEditor({
  catalog,
  grants,
  onChange,
  disabled = false,
  allowActions = false,
  canConfigureActions = false,
}: {
  catalog: AgentCapabilityDefinition[];
  grants: AgentCapabilityGrant[];
  onChange: (grants: AgentCapabilityGrant[]) => void;
  disabled?: boolean;
  allowActions?: boolean;
  canConfigureActions?: boolean;
}) {
  const groups = useMemo(() => {
    const byCategory = new Map<string, AgentCapabilityDefinition[]>();
    for (const definition of catalog) {
      if (definition.id === "automation_actions" && !allowActions && !grants.some((grant) => grant.capability_id === definition.id && grant.enabled)) continue;
      const current = byCategory.get(definition.category) ?? [];
      current.push(definition);
      byCategory.set(definition.category, current);
    }
    return [...byCategory.entries()];
  }, [catalog, allowActions, grants]);

  const policyNeedsAdmin = !canConfigureActions && grants.some((grant) => grant.capability_id === "automation_actions" && grant.enabled);

  const grantByID = useMemo(() => new Map(grants.map((grant) => [grant.capability_id, grant])), [grants]);

  function setEnabled(definition: AgentCapabilityDefinition, enabled: boolean) {
    if (enabled && definition.risk === "high") {
      const confirmed = window.confirm(`${definition.display_name} is high-risk. Changes apply to future runs only.`);
      if (!confirmed) return;
    }
    onChange(grants.map((grant) => (
      grant.capability_id === definition.id
        ? { ...grant, enabled, access_level: capabilityAccessFor(definition) }
        : grant
    )));
  }

  return (
    <div className="space-y-4">
      {policyNeedsAdmin ? <p className="text-xs text-muted-foreground">An admin must edit capabilities while resumable automation actions are enabled. You can still disable resumable automation actions.</p> : null}
      {groups.map(([category, definitions]) => (
        <div key={category} className="space-y-2">
          <div className="text-xs font-medium uppercase text-muted-foreground">{category}</div>
          <div className="divide-y divide-border rounded-md border border-border">
            {definitions.map((definition) => {
              const grant = grantByID.get(definition.id);
              const unavailable = definition.availability?.available === false;
              const actions = definition.id === "automation_actions";
              const cannotEnableActions = actions && !grant?.enabled && (!allowActions || !canConfigureActions || !!actionConfigError(grant?.config ?? {}));
              const switchDisabled = disabled || (unavailable && !(actions && grant?.enabled)) || cannotEnableActions || (policyNeedsAdmin && !actions);
              return (
                <div key={definition.id} className="flex items-start justify-between gap-3 px-3 py-3">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Label htmlFor={`capability-${definition.id}`} className="text-sm font-medium">
                        {definition.display_name}
                      </Label>
                      {definition.risk === "high" ? (
                        <Badge variant="outline" className="gap-1 border-warning/40 text-warning">
                          <ShieldAlert className="h-3 w-3" />
                          High risk
                        </Badge>
                      ) : null}
                      {unavailable ? <Badge variant="secondary">Unavailable</Badge> : null}
                    </div>
                    <p className="text-sm text-muted-foreground">{definition.description}</p>
                    {actions ? (
                      <AutomationActionsConfig config={grant?.config ?? {}} disabled={disabled || !allowActions || !canConfigureActions}
                        onSave={(config) => onChange(grants.map((item) => item.capability_id === definition.id ? { ...item, config } : item))} />
                    ) : null}
                    {unavailable && definition.availability?.reason ? (
                      <p className="text-xs text-muted-foreground">{definition.availability.reason}</p>
                    ) : null}
                  </div>
                  <DisabledTooltip disabled={switchDisabled} content={cannotEnableActions ? "An organization admin must configure the selected actions before enabling automation actions." : "You need permission and an available integration to change this capability."}>
                    <Switch
                      id={`capability-${definition.id}`}
                      checked={grant?.enabled ?? false}
                      disabled={switchDisabled}
                      onCheckedChange={(checked) => setEnabled(definition, checked)}
                      aria-label={definition.display_name}
                    />
                  </DisabledTooltip>
                </div>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}
