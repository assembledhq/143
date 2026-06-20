"use client";

import { useMemo } from "react";
import { ShieldAlert } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { AgentCapabilityDefinition, AgentCapabilityGrant, AgentCapabilityID } from "@/lib/types";

export function capabilityAccessFor(definition: AgentCapabilityDefinition) {
  return definition.max_access_level;
}

// Capabilities that ship enabled by default for the org session-default policy
// when an admin has not configured one yet. These are the broadly-useful,
// commonly-needed capabilities (code/PR/test context plus branch & PR
// publishing); higher-risk write and production-data capabilities stay opt-in.
// Keep in sync with recommendedDefaultGrants in
// internal/services/agentcapabilities/service.go.
export const RECOMMENDED_DEFAULT_CAPABILITY_IDS: readonly AgentCapabilityID[] = [
  "repo_context",
  "pr_history",
  "session_history",
  "review_feedback",
  "ci_history",
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
}: {
  catalog: AgentCapabilityDefinition[];
  grants: AgentCapabilityGrant[];
  onChange: (grants: AgentCapabilityGrant[]) => void;
  disabled?: boolean;
}) {
  const groups = useMemo(() => {
    const byCategory = new Map<string, AgentCapabilityDefinition[]>();
    for (const definition of catalog) {
      const current = byCategory.get(definition.category) ?? [];
      current.push(definition);
      byCategory.set(definition.category, current);
    }
    return [...byCategory.entries()];
  }, [catalog]);

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
      {groups.map(([category, definitions]) => (
        <div key={category} className="space-y-2">
          <div className="text-xs font-medium uppercase text-muted-foreground">{category}</div>
          <div className="divide-y divide-border rounded-md border border-border">
            {definitions.map((definition) => {
              const grant = grantByID.get(definition.id);
              const unavailable = definition.availability?.available === false;
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
                    {unavailable && definition.availability?.reason ? (
                      <p className="text-xs text-muted-foreground">{definition.availability.reason}</p>
                    ) : null}
                  </div>
                  <Switch
                    id={`capability-${definition.id}`}
                    checked={grant?.enabled ?? false}
                    disabled={disabled || unavailable}
                    onCheckedChange={(checked) => setEnabled(definition, checked)}
                    aria-label={definition.display_name}
                  />
                </div>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}
