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
  // openCodeAvailability maps an OpenCode model id to its route availability.
  // When absent (registry still loading / API failed) OpenCode models render
  // as normal, enabled options with no transport badge.
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
  // selectedModel is always kept visible even when it has no runnable route, so
  // the trigger keeps displaying the current value instead of falling back to
  // the placeholder while the stored selection silently persists.
  selectedModel?: string;
}

export function shouldRenderModelOption(
  model: string,
  isOpenCode: boolean,
  availabilityById?: Map<string, OpenCodeModelAvailability>,
): boolean {
  const availability = isOpenCode ? availabilityById?.get(model) : undefined;
  return !availability || availability.hasRunnableRoute;
}

// isModelOptionVisible decides whether a model renders in the picker. Available
// models always show; an unavailable model shows only when it is the current
// selection (so the trigger stays truthful).
export function isModelOptionVisible(
  model: string,
  isOpenCode: boolean,
  availabilityById?: Map<string, OpenCodeModelAvailability>,
  selectedModel?: string,
): boolean {
  return model === selectedModel || shouldRenderModelOption(model, isOpenCode, availabilityById);
}

// openCodeModelItemDecor computes the per-item transport badge for an OpenCode
// model. Unavailable OpenCode models are filtered before this runs.
function openCodeModelItemDecor(
  model: string,
  isOpenCode: boolean,
  availabilityById?: Map<string, OpenCodeModelAvailability>,
): { disabled: boolean; trailing: string | null } {
  const availability = isOpenCode ? availabilityById?.get(model) : undefined;
  if (!availability) return { disabled: false, trailing: null };
  return {
    disabled: false,
    trailing: availability.transportLabel,
  };
}

function ModelSelectItem({
  model,
  isOpenCode,
  availabilityById,
  selectedModel,
}: {
  model: string;
  isOpenCode: boolean;
  availabilityById?: Map<string, OpenCodeModelAvailability>;
  selectedModel?: string;
}) {
  if (!isModelOptionVisible(model, isOpenCode, availabilityById, selectedModel)) return null;
  const { disabled, trailing } = openCodeModelItemDecor(model, isOpenCode, availabilityById);
  return (
    <SelectItem value={model} disabled={disabled}>
      <span className="flex items-center gap-1.5">
        <span>{modelOptionLabel(model)}</span>
        {trailing ? <span className="text-xs text-muted-foreground">· {trailing}</span> : null}
      </span>
    </SelectItem>
  );
}

// ModelOptionGroups renders the grouped model options for the session/PM model
// pickers. For OpenCode models it adds a "· OpenRouter" transport badge (the
// route that would run given current keys) and hides models with no runnable
// transport, except the current selection which always stays visible.
// Centralized so the three pickers stay consistent.
export function ModelOptionGroups({ modelGroups, openCodeAvailability, selectedModel }: ModelOptionGroupsProps) {
  return (
    <>
      {modelGroups.map((group) => {
        const isOpenCode = group.key === "opencode";
        const models = group.models.filter((model) =>
          isModelOptionVisible(model, isOpenCode, openCodeAvailability, selectedModel),
        );
        if (models.length === 0) return null;
        return (
          <SelectGroup key={group.key}>
            <SelectLabel>{group.label}</SelectLabel>
            {models.map((model) => (
              <ModelSelectItem
                key={model}
                model={model}
                isOpenCode={isOpenCode}
                availabilityById={openCodeAvailability}
                selectedModel={selectedModel}
              />
            ))}
          </SelectGroup>
        );
      })}
    </>
  );
}

// FlatModelOptions renders a single agent's model list (no groups) with the same
// OpenCode transport badge + availability filtering. Used by the in-session
// composer where the picker is scoped to one agent.
export function FlatModelOptions({
  models,
  agentType,
  openCodeAvailability,
  selectedModel,
}: {
  models: readonly string[];
  agentType: string;
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
  selectedModel?: string;
}) {
  const isOpenCode = agentType === "opencode";
  return (
    <>
      {models
        .filter((model) => isModelOptionVisible(model, isOpenCode, openCodeAvailability, selectedModel))
        .map((model) => (
          <ModelSelectItem
            key={model}
            model={model}
            isOpenCode={isOpenCode}
            availabilityById={openCodeAvailability}
            selectedModel={selectedModel}
          />
        ))}
    </>
  );
}
