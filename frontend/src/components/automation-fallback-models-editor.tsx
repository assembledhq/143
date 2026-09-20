"use client";

import { useState, type ReactNode } from "react";
import { ChevronDown, ChevronUp, Plus, Trash2 } from "lucide-react";

import { AutomationModelSelect } from "@/components/automation-model-select";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { agentDisplayLabel, agentTypeForModel, modelOptionLabel } from "@/lib/agents";
import {
  getCodingAgentReasoningOptions,
  toCodingAgentReasoningEffort,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type { AutomationFallbackModels } from "@/lib/types";

// Mirrors models.MaxAutomationFallbackModels — ranks stored beyond the primary,
// so a run attempts at most five models. Kept in lockstep the same way
// MAX_REVIEWER_MODELS mirrors its own Go constant; the API rejects a longer
// list, this only stops the UI from offering one.
export const MAX_AUTOMATION_FALLBACK_MODELS = 4;

const INHERIT_REASONING_VALUE = "__default__";

interface FallbackRank {
  model: string;
  agentType: string;
  reasoningEffort: CodingAgentReasoningEffort;
}

// The agent a rank runs on: the stored override, else the one the model name
// implies, else the automation's primary agent. Mirrors
// models.AutomationFallbackModels.AgentTypeAt so the row labels agree with what
// the worker will actually dispatch.
function rankAgentType(
  value: AutomationFallbackModels | undefined,
  index: number,
  model: string,
  primaryAgentType: string,
): string {
  const explicit = value?.agent_types?.[index]?.trim();
  if (explicit) return explicit;
  return agentTypeForModel(model) ?? primaryAgentType;
}

/**
 * What an inherited (empty) rank actually runs at, phrased for the row.
 *
 * An empty entry means "inherit the primary's effort", but the backend only
 * honours that when this rank's own agent has that level —
 * models.AutomationFallbackModels.ReasoningEffortAt drops an inherited level
 * the rank's agent cannot run, and the rank falls back to that agent's default.
 * Labelling both cases "Same as preferred" would claim a Codex rank runs at the
 * primary's "max", which it never will.
 */
function rankInheritReasoningLabel(
  agentType: string,
  primaryReasoningEffort: CodingAgentReasoningEffort,
): string {
  // Nothing to inherit: both the primary and this rank run at whatever their
  // own agent defaults to, so "same as preferred" is not a false claim.
  if (!primaryReasoningEffort) return "Same as preferred";
  return getCodingAgentReasoningOptions(agentType).some(
    (option) => option.value === primaryReasoningEffort,
  )
    ? "Same as preferred"
    : "Agent default";
}

/**
 * Re-resolves one rank's reasoning effort against the agent that rank runs on.
 *
 * Only an EXPLICIT level is validated against the rank's agent server-side, and
 * an explicit level that agent does not have is a 400 — so it is cleared here.
 * An empty entry is never rejected: the backend drops an inherited level the
 * rank's agent cannot run and lets the rank use its own agent's default. So a
 * rank that inherits stays inheriting, whatever the primary runs at.
 *
 * Substituting a concrete level for an unrunnable inherited one (this used to
 * return "high") both invented a setting the user never chose and made the
 * inherit menu item unselectable: picking it re-emitted the chain already
 * stored, useAutosave's deepEqual guard skipped the save, and the controlled
 * select snapped back with no explanation.
 *
 * Every handler below runs each row through this before emitting, which is why
 * the three arrays are always rewritten together: a row that keeps a stale
 * effort (or an array that drifts out of length) is a 400 on the next save.
 */
export function normalizeRankReasoningEffort(
  agentType: string,
  effort: CodingAgentReasoningEffort,
): CodingAgentReasoningEffort {
  const options = getCodingAgentReasoningOptions(agentType);
  // The agent takes no reasoning level at all; anything stored here is noise.
  if (options.length === 0) return "";
  if (effort && options.some((option) => option.value === effort)) return effort;
  return "";
}

/**
 * Rebuilds the parallel arrays for a whole chain, dropping ranks with no model.
 *
 * Exported because the detail page has to run the same reconciliation inside the
 * patch that changes the primary model: the new primary has to be dropped from
 * the chain in one body, or the save lands a chain the API already considers
 * invalid (a fallback that duplicates the primary).
 */
export function buildAutomationFallbackModels(
  ranks: readonly FallbackRank[],
): AutomationFallbackModels {
  const kept = ranks.filter((rank) => rank.model.trim() !== "");
  if (kept.length === 0) return {};
  const reasoningEfforts = kept.map((rank) =>
    normalizeRankReasoningEffort(rank.agentType, rank.reasoningEffort),
  );
  return {
    models: kept.map((rank) => rank.model.trim()),
    agent_types: kept.map((rank) => rank.agentType.trim()),
    // Omitted entirely when every rank inherits — the same canonical encoding
    // the Go side normalizes to, so a no-op edit doesn't show up as a change.
    ...(reasoningEfforts.some(Boolean) ? { reasoning_efforts: reasoningEfforts } : {}),
  };
}

/** Reads the stored chain back into rows, resolving each rank's agent. */
export function automationFallbackRanks(
  value: AutomationFallbackModels | undefined,
  primaryAgentType: string,
): FallbackRank[] {
  return (value?.models ?? []).map((model, index) => ({
    model,
    agentType: rankAgentType(value, index, model, primaryAgentType),
    reasoningEffort: value?.reasoning_efforts?.[index] ?? "",
  }));
}

function RankAction({
  label,
  disabled,
  disabledReason,
  onClick,
  children,
}: {
  label: string;
  disabled: boolean;
  disabledReason: string;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <DisabledTooltip disabled={disabled} content={disabledReason}>
      <Button
        type="button"
        variant="outline"
        size="icon-sm"
        aria-label={label}
        disabled={disabled}
        onClick={onClick}
      >
        {children}
      </Button>
    </DisabledTooltip>
  );
}

export function AutomationFallbackModelsEditor({
  value,
  primaryModel,
  primaryAgentType,
  primaryReasoningEffort = "",
  onChange,
  disabled = false,
}: {
  value?: AutomationFallbackModels;
  primaryModel?: string;
  primaryAgentType: string;
  /**
   * The primary's reasoning level. Not cosmetic: an empty rank inherits it, and
   * whether this rank's agent actually has that level decides what the inherit
   * option can honestly be called.
   */
  primaryReasoningEffort?: CodingAgentReasoningEffort;
  onChange: (value: AutomationFallbackModels) => void;
  disabled?: boolean;
}) {
  const ranks = automationFallbackRanks(value, primaryAgentType);
  // A rank with no model yet is rejected by the API, and these editors save on
  // every change — so "Add fallback model" opens a row locally and only commits
  // once a model is chosen, instead of firing a save that is certain to 400.
  const [draftOpen, setDraftOpen] = useState(false);
  const rowCount = ranks.length + (draftOpen ? 1 : 0);
  const atCap = rowCount >= MAX_AUTOMATION_FALLBACK_MODELS;

  function commit(next: readonly FallbackRank[]) {
    onChange(buildAutomationFallbackModels(next));
  }

  function setRankModel(index: number, model: string | undefined) {
    // Clearing a committed rank back to "Auto" has no meaning for a fallback —
    // a rank with no model is exactly what removing it expresses.
    if (!model) {
      removeRank(index);
      return;
    }
    const agentType = agentTypeForModel(model) ?? primaryAgentType;
    commit(
      ranks.map((rank, i) =>
        i === index ? { model, agentType, reasoningEffort: rank.reasoningEffort } : rank,
      ),
    );
  }

  function setRankReasoningEffort(index: number, effort: CodingAgentReasoningEffort) {
    commit(ranks.map((rank, i) => (i === index ? { ...rank, reasoningEffort: effort } : rank)));
  }

  function moveRank(index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target < 0 || target >= ranks.length) return;
    const next = [...ranks];
    [next[index], next[target]] = [next[target], next[index]];
    commit(next);
  }

  function removeRank(index: number) {
    commit(ranks.filter((_, i) => i !== index));
  }

  function addDraftRank(model: string) {
    const agentType = agentTypeForModel(model) ?? primaryAgentType;
    setDraftOpen(false);
    commit([...ranks, { model, agentType, reasoningEffort: "" }]);
  }

  // Every rank the chain already claims, so no two rows pick the same model and
  // no fallback duplicates the primary.
  const claimedModels = [primaryModel ?? "", ...ranks.map((rank) => rank.model)].filter(Boolean);

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label className="text-xs font-medium text-muted-foreground">Fallback models</Label>
        <DisabledTooltip
          disabled={disabled || atCap}
          content={
            disabled
              ? "You do not have permission to edit this automation."
              : `You can rank up to ${MAX_AUTOMATION_FALLBACK_MODELS} fallback models.`
          }
        >
          <Button
            type="button"
            variant="outline"
            size="xs"
            disabled={disabled || atCap}
            onClick={() => setDraftOpen(true)}
          >
            <Plus className="mr-1 h-3 w-3" />
            Add fallback model
          </Button>
        </DisabledTooltip>
      </div>
      <p className="text-xs text-muted-foreground">
        Tried in order when a rank has no usable credential or fails because the model is
        overloaded. Each retry starts a new session.
      </p>

      <div className="divide-y divide-border rounded-md border border-border">
        <div className="space-y-1 px-2.5 py-2">
          <span className="text-xs font-medium text-muted-foreground">1. Preferred model</span>
          <p className="text-xs text-foreground">
            {primaryModel ? modelOptionLabel(primaryModel) : "Auto"}
            <span className="text-muted-foreground">
              {" · "}
              {agentDisplayLabel(primaryAgentType)}
            </span>
          </p>
        </div>

        {ranks.map((rank, index) => {
          const reasoningOptions = getCodingAgentReasoningOptions(rank.agentType);
          const inheritLabel = rankInheritReasoningLabel(
            rank.agentType,
            primaryReasoningEffort,
          );
          return (
            // Keyed by model alone, never by position: models are unique within
            // a chain (excludeModels enforces it), and mixing the index in made
            // every key from the edit point onward change on a reorder or a
            // removal. React then unmounted and remounted the rows instead of
            // moving them, destroying the very button the user had just
            // activated and dropping keyboard focus to <body>.
            <div key={rank.model} className="space-y-1.5 px-2.5 py-2">
              <span className="text-xs font-medium text-muted-foreground">
                {index + 2}. Fallback model
              </span>
              <div className="flex items-center gap-1.5">
                <div className="min-w-0 flex-1">
                  <AutomationModelSelect
                    ariaLabel={`Rank ${index + 2} fallback model`}
                    value={rank.model}
                    density="dense"
                    disabled={disabled}
                    excludeModels={claimedModels}
                    onValueChange={(model) => setRankModel(index, model)}
                  />
                </div>
                <RankAction
                  label={`Move rank ${index + 2} up`}
                  disabled={disabled || index === 0}
                  disabledReason={
                    disabled
                      ? "You do not have permission to edit this automation."
                      : "This model is already first."
                  }
                  onClick={() => moveRank(index, -1)}
                >
                  <ChevronUp className="h-3.5 w-3.5" />
                </RankAction>
                <RankAction
                  label={`Move rank ${index + 2} down`}
                  disabled={disabled || index === ranks.length - 1}
                  disabledReason={
                    disabled
                      ? "You do not have permission to edit this automation."
                      : "This model is already last."
                  }
                  onClick={() => moveRank(index, 1)}
                >
                  <ChevronDown className="h-3.5 w-3.5" />
                </RankAction>
                <RankAction
                  label={`Remove rank ${index + 2}`}
                  disabled={disabled}
                  disabledReason="You do not have permission to edit this automation."
                  onClick={() => removeRank(index)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </RankAction>
              </div>
              {reasoningOptions.length > 0 ? (
                <Select
                  value={rank.reasoningEffort || INHERIT_REASONING_VALUE}
                  disabled={disabled}
                  onValueChange={(next) =>
                    setRankReasoningEffort(
                      index,
                      next === INHERIT_REASONING_VALUE ? "" : toCodingAgentReasoningEffort(next),
                    )
                  }
                >
                  <SelectTrigger
                    aria-label={`Rank ${index + 2} reasoning level`}
                    density="dense"
                  >
                    <SelectValue placeholder={inheritLabel} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={INHERIT_REASONING_VALUE}>{inheritLabel}</SelectItem>
                    {reasoningOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : null}
            </div>
          );
        })}

        {draftOpen ? (
          <div className="space-y-1.5 px-2.5 py-2">
            <span className="text-xs font-medium text-muted-foreground">
              {ranks.length + 2}. Fallback model
            </span>
            <div className="flex items-center gap-1.5">
              <div className="min-w-0 flex-1">
                <AutomationModelSelect
                  ariaLabel={`Rank ${ranks.length + 2} fallback model`}
                  density="dense"
                  disabled={disabled}
                  excludeModels={claimedModels}
                  onValueChange={(model) => {
                    if (model) addDraftRank(model);
                  }}
                />
              </div>
              <RankAction
                label={`Remove rank ${ranks.length + 2}`}
                disabled={disabled}
                disabledReason="You do not have permission to edit this automation."
                onClick={() => setDraftOpen(false)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </RankAction>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** Read-only chain for members who cannot edit the automation. */
export function AutomationFallbackModelsSummary({
  value,
  primaryModel,
  primaryAgentType,
}: {
  value?: AutomationFallbackModels;
  primaryModel?: string;
  primaryAgentType: string;
}) {
  const ranks = automationFallbackRanks(value, primaryAgentType);

  return (
    <div className="space-y-1.5">
      <span className="text-xs font-medium text-muted-foreground">Fallback models</span>
      <p className="text-xs text-foreground">
        1. {primaryModel ? modelOptionLabel(primaryModel) : "Auto"}
        <span className="text-muted-foreground">
          {" · "}
          {agentDisplayLabel(primaryAgentType)}
        </span>
      </p>
      {ranks.length === 0 ? (
        <p className="text-xs text-muted-foreground">No fallback models.</p>
      ) : (
        ranks.map((rank, index) => (
          <p key={rank.model} className="text-xs text-foreground">
            {index + 2}. {modelOptionLabel(rank.model)}
            <span className="text-muted-foreground">
              {" · "}
              {agentDisplayLabel(rank.agentType)}
              {rank.reasoningEffort ? ` · ${rank.reasoningEffort}` : ""}
            </span>
          </p>
        ))
      )}
    </div>
  );
}
