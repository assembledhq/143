import { SelectGroup, SelectItem, SelectLabel } from "@/components/ui/select";
import { modelOptionLabel } from "@/lib/agents";
import type { OpenCodeModelAvailability } from "@/hooks/use-opencode-models";

interface ModelGroup {
  key: string;
  label: string;
  models: readonly string[];
}

interface ModelOptionGroupsProps {
  modelGroups: readonly ModelGroup[];
  getOptionValue?: (group: ModelGroup, model: string) => string;
  // openCodeAvailability maps an OpenCode model id to its route availability.
  // When absent (registry still loading / API failed) OpenCode models render
  // as normal, enabled options with no transport badge.
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
}

// openCodeModelItemDecor computes the per-item badge/disabled state for an
// OpenCode model. Returns plain (no badge, enabled) for non-OpenCode models or
// when availability data is absent.
function openCodeModelItemDecor(
  model: string,
  isOpenCode: boolean,
  availabilityById?: Map<string, OpenCodeModelAvailability>,
): { disabled: boolean; trailing: string | null } {
  const availability = isOpenCode ? availabilityById?.get(model) : undefined;
  if (!availability) return { disabled: false, trailing: null };
  return {
    disabled: !availability.hasRunnableRoute,
    trailing: availability.transportLabel ?? (availability.hasRunnableRoute ? null : "add a key"),
  };
}

function ModelSelectItem({
  value,
  model,
  isOpenCode,
  availabilityById,
}: {
  value: string;
  model: string;
  isOpenCode: boolean;
  availabilityById?: Map<string, OpenCodeModelAvailability>;
}) {
  const { disabled, trailing } = openCodeModelItemDecor(model, isOpenCode, availabilityById);
  return (
    <SelectItem value={value} disabled={disabled}>
      <span className="flex items-center gap-1.5">
        <span>{modelOptionLabel(model)}</span>
        {trailing ? <span className="text-xs text-muted-foreground">· {trailing}</span> : null}
      </span>
    </SelectItem>
  );
}

// ModelOptionGroups renders the grouped model options for the session/PM model
// pickers. For OpenCode models it adds a "· OpenRouter" transport badge (the
// route that would run given current keys) and disables models with no runnable
// transport. Centralized so model pickers stay consistent.
export function ModelOptionGroups({ modelGroups, getOptionValue, openCodeAvailability }: ModelOptionGroupsProps) {
  return (
    <>
      {modelGroups.map((group) => (
        <SelectGroup key={group.key}>
          <SelectLabel>{group.label}</SelectLabel>
          {group.models.map((model) => (
            <ModelSelectItem
              key={model}
              value={getOptionValue ? getOptionValue(group, model) : model}
              model={model}
              isOpenCode={group.key === "opencode"}
              availabilityById={openCodeAvailability}
            />
          ))}
        </SelectGroup>
      ))}
    </>
  );
}

// FlatModelOptions renders a single agent's model list (no groups) with the same
// OpenCode transport badge + disabled treatment. Used by the in-session composer
// where the picker is scoped to one agent.
export function FlatModelOptions({
  models,
  agentType,
  openCodeAvailability,
}: {
  models: readonly string[];
  agentType: string;
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
}) {
  const isOpenCode = agentType === "opencode";
  return (
    <>
      {models.map((model) => (
        <ModelSelectItem
          key={model}
          value={model}
          model={model}
          isOpenCode={isOpenCode}
          availabilityById={openCodeAvailability}
        />
      ))}
    </>
  );
}
